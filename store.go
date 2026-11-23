package monshi

// The store seam (DESIGN §5, §8). Case data lives here and nowhere else:
// the registry holds compiled definitions, a handle holds an ID and an
// actor, and every mutable byte of a case is behind this file.
//
// Milestone 2's implementation is in-memory and process-lifetime, which is
// what DESIGN §10 asks of this milestone ("in-memory state"). The *shape*
// is milestone 4's: `create`, `load`, `working` and the revision-guarded
// commit are the methods a persistent backend implements, and the guards
// they enforce are what §6.4's stale-revision refusals and invariant 5
// depend on. What milestone 4 changes is durability: `commit`'s three writes
// become one transaction against a store that can fail halfway, and a
// `context.Context` joins the signature so a stalled write can be cancelled
// (DESIGN §8). The transaction is that one method's implementation, not a
// second place the guards live.
//
// A context reaches these methods with milestone 4 too. The seam DESIGN §8
// defines takes one and the exported operations already carry the caller's,
// so the plumbing there is one parameter per method; an in-memory store has
// nothing to cancel, and threading a context through six methods that do no
// I/O would only invite the question of which of them was supposed to
// honour it.
//
// One discipline makes reads safe without locking a caller out of a writer:
// the store never mutates data it has handed out. A commit builds the next
// state and *replaces* the field holding it, so a snapshot an operation
// read stays consistent while another operation commits. That is the same
// rule as invariant 3's immutable history, stated about the present rather
// than the past, and it is why a `state` copy is a safe thing to hand out.

import (
	"sync"
	"time"
)

// registry is the definition registry (DESIGN §3). Its contents change only
// through an explicit act on Runtime, so the value a handle carries — a
// pointer to it — is immutable even while the map under it is not. A
// Definition never changes after Load; "reloading" replaces the entry.
type registry struct {
	mu   sync.RWMutex
	defs map[string]*Definition
}

func newRegistry() *registry { return &registry{defs: map[string]*Definition{}} }

func (g *registry) get(id string) (*Definition, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	d, ok := g.defs[id]
	return d, ok
}

// add installs a definition, refusing a duplicate unless the caller asked
// to replace. Duplicate IDs are rejected because `Create` selects by ID:
// two definitions under one name would make a case's record ambiguous about
// which one governs its data, and that is a host mistake worth stopping
// rather than resolving. `refuse` names the refusal after the act that asked,
// because Register and Replace report the same collision differently.
func (g *registry) add(refuse refuser, d *Definition, replace bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.defs[d.ID()]; dup && !replace {
		return refuse.now(InvalidOp,
			"definition "+quote(d.ID())+" is already registered")
	}
	g.defs[d.ID()] = d
	return nil
}

// caseStore holds every case's data, of the three kinds DESIGN §5 names.
// Milestone 2 has all three: the materialized committed state, the per-user
// working copies, and the append-only history. What milestone 4 adds is the
// transaction that makes the history append, the snapshot swap and the
// submitter's rebase one atomic durable step — a new method around the same
// three fields, not a different shape for them.
type caseStore struct {
	mu    sync.RWMutex
	next  CaseID
	cases map[CaseID]*storedCase

	// now is the clock a commit stamps. It is a field rather than a call to
	// `time.Now` because a commit's timestamp is *data* — DESIGN §5.2's rule
	// that replay is deterministic means the value has to be recorded, not
	// recomputed, and a test that asserts on a history entry needs to be able
	// to say when it happened. A store built any other way falls back to the
	// system clock.
	now func() time.Time
}

func newCaseStore() *caseStore {
	return &caseStore{cases: map[CaseID]*storedCase{}, now: time.Now}
}

// stamp is the clock's answer for one commit.
func (s *caseStore) stamp() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// storedCase is one case as the store holds it. `load` hands out a *copy*
// of this struct, never a pointer into the map, so a reader sees one
// consistent definition/revision/stage triple. The aliasing that copy
// leaves inside the state is safe because of the replace-never-mutate rule
// above.
type storedCase struct {
	// def is the definition ID this case's data was built under. It never
	// changes: a case's schema is fixed at creation, and DESIGN §3's
	// reloaded definition is a new *entry* under that same name.
	def string

	// revision is the case-wide committed revision — the count of commits
	// appended — so a new case is at 0 and creating one is not a commit
	// (DESIGN §5, invariant 2).
	revision uint64

	commit committedState

	// work is the per-actor working copy, keyed by identity and not by
	// handle or session, which is invariant 4 expressed as a map key.
	work map[UserID]*workingState

	// hist is the commit history: ordered, immutable, one entry per
	// successful committing operation (DESIGN §5's first kind of state, and
	// invariant 2's count). It is dense by revision — the entry at index 0 is
	// the commit that produced revision 1 — because a revision *is* the count
	// of commits, so a cursor into history and a revision number are the same
	// thing. That is what makes §6.5's rebase and the audit read of "what
	// changed between revisions 4 and 7" a slice rather than a search, and it
	// is why creating a case appends nothing: revision 0 has no entry.
	hist []commitRecord
}

// commitOp names which operation produced a commit. DESIGN §5's `Commit`
// carries an `Operation` because a history entry says more than "the data
// moved": a review decision, a signature and a content change are different
// acts on the same case, and §5.1 lists all three among the semantic
// committed changes. `opSubmit` and `opTransition` were declared before the
// operations that use them were interesting; `opReview` is the first entry
// whose act is not about content at all, and `opSign` joins it when `Sign`
// lands.
// A stored history that never had to be reshaped to hold them is the point of
// writing the word early rather than in milestone 5.
type commitOp uint8

const (
	opSubmit commitOp = iota + 1
	opTransition
	opReview
)

// commitChange is one entry of a forward diff (DESIGN §5.1): which
// declaration moved, at which instance, and what it holds now.
//
// There is no old value, and that is the rule rather than an omission. A
// commit that recorded both would be a *patch* with a reversible half, and
// DESIGN §5's model is a linear sequence of forward diffs replayed onto the
// empty case — one direction, with the materialized snapshot answering what
// the case looks like today. What §6.5's rebase needs from the past is not
// the old value but the *set* a collection's rows formed at the revision a
// draft descended from, and no forward diff can state that: a count says how
// many rows a commit left and not which ones, so a removed ID cannot be
// recovered from what follows it. `ids` is the one place an entry carries
// more than the changed fact — the committed list, in creation order, spelled
// by the same stable IDs §5.1 addresses rows with — and RUNTIME §5.8 states
// why the alternative is a reconstructed snapshot this engine does not keep.
//
// The two content kinds are the two things §5.1 counts as semantic content
// changes. `chgValue` says one field's value is now what `v` holds — where an
// unset `v` is §5.1's *removal* effect, the `Empty` of a field, and so is a
// change rather than the absence of one. `chgRows` says a collection's
// instance set is now the `ids` list, whose length `v.i` repeats and whose
// emptiness `v.set` answers: an instance appearing or vanishing is a change to
// the declaration's rows rather than to a value, which is the same distinction
// §5.3's fanout draws between a write and an `Add`. `ids` is empty for the
// other kind, and a value payload is zero for this one.
//
// `chgReview` is the third kind and the first that is not content: one
// verifiable unit's effective decision, in `v.i`, with its comment in `v.s`.
// It carries no `by` and no `at` because the entry already has both —
// `commitRecord`'s `by` and `revision` are the reviewer and the version they
// decided on, which is DESIGN §4.2's "historical decisions remain in history"
// stated without duplicating a fact that can drift. `path` is always empty:
// F4 reviews a `verifiable multiple` as a whole and not per member, so a
// decision is about a declaration and never about a row.
//
// Being non-content is load-bearing twice, and both readers filter by kind
// rather than by shape. §5.6's reset mapping must skip it — a decision that
// reset its own unit would unmake itself — and §5.8's value pass must skip it
// too, because a `verifiable` *field* is a cell slot, and a merge that read
// `chgReview` as a base value would answer a question about content with an
// approval.
//
// `path` is empty for a value the base row holds. For a member of a
// collection it names the instances outermost first, in the stable IDs §5.1
// addresses instances by — which is what makes the entry replayable and,
// later, mergeable: "the third holder's since changed" is not a fact a
// renumbered row can state.
type commitChange struct {
	kind uint8
	slot nodeID
	path instPath
	v    slotValue
	ids  []instanceID // chgRows: the committed set, in creation order
}

const (
	chgValue  uint8 = iota + 1 // one field's value, at one instance path
	chgRows                    // one collection's instance count
	chgReview                  // one verifiable unit's decision: not content
)

// commitRecord is one entry in a case's history: the revision it produced,
// who and what produced it, and the content it committed.
//
// The stage is recorded per commit rather than left to the current snapshot
// because §5.1 counts a stage change among the semantic committed changes:
// replaying history onto an empty case has to land the case where it is, so
// "revision 7 was at review" must be knowable from the entry. The two
// operations that write an entry use the field in opposite directions, which
// is what makes it load-bearing rather than decorative: a Submit moves the
// content and repeats the stage (DESIGN §4.4 keeps the stage where a
// transition put it), and a Transition moves the stage and carries no content
// at all. Either way the entry says what it changed by saying where the case
// stood and what it wrote, and `diff_test.go` and
// `transition_view_test.go` check the two halves of that from opposite
// sides.
//
// No separate list of the binary content a commit pinned (§5.2's retention
// rule) because there is nothing to list: a binary value's immutable `
// BinaryRef` *is* the diff entry's payload, so the references a commit made
// live in `changes` and are as durable as the entry that holds them.
type commitRecord struct {
	revision uint64
	at       time.Time
	by       UserID
	op       commitOp
	stage    stageID
	changes  []commitChange
}

// committedState is one materialized committed snapshot: the case's values
// and instance rows, the stage it is at, and the decisions reviewers have
// made about that content (DESIGN §4.2). A host-facing `Committed` can be
// added over this shape later without breaking a reader, because the state
// and the stage keep their positions as the struct grows.
//
// `reviews` is dense by slot and consulted only at a slot that declares
// `verifiable` — the governing unit F3 already resolved at lowering — so a
// case nobody has reviewed holds a nil table and pays nothing for the
// feature.
//
// It sits *beside* `s` and not inside it, and that placement is the whole
// design decision. A working copy is a clone of the data snapshot, so a
// review state stored as data would be handed to every draft, compared by
// §5.8's merge and grafted by a submit — three things no decision is a
// statement about, since a reviewer approves what the case *holds* and not
// what somebody is typing. Kept beside it, `Update` cannot write one (a
// signature field is refused for the same structural reason), §6.3's "other
// forms' unsubmitted working edits remain intact" needs no exception for
// reviews, and a rebase has nothing to reconcile.
//
// The replace-never-mutate rule stated above is what makes handing this slice
// to a caller safe: a commit builds a whole new table with `reviewTable` and
// never writes an entry of the stored one, exactly as `Submit` builds a new
// state instead of grafting the stored one.
type committedState struct {
	s     state
	stage stageID

	reviews []reviewState
}

// stageName renders the snapshot's stage as its definition spells it.
func (cs committedState) stageName() string { return cs.s.img.stageName(cs.stage) }

// workingState is one actor's uncommitted copy: the committed revision it
// diverged from, its own revision as the write guard, and the candidate
// data. DESIGN §6.5's rebase compares `base` against the case's committed
// revision; §6.2's refusal compares `rev` against the one an edit presented.
type workingState struct {
	base uint64
	rev  uint64
	s    state
}

// clone hands out a private copy of a working copy. Two of the store's
// guarantees rest on it: an abandoned candidate cannot leak into the next
// session, and a saved candidate cannot be edited behind the store's back.
// The cost is one row copy per operation, which is the cost §5.1's frame
// already pays — a working copy *is* a frame's candidate half, so this is
// the same bytes moved once, not twice.
func (w *workingState) clone() workingState {
	return workingState{base: w.base, rev: w.rev, s: w.s.clone()}
}

// create allocates a case of one definition at revision 0, at the initial
// stage, with its declared defaults materialized — creating a case stores
// an empty case, not an undefaults one (DESIGN §5, RUNTIME §5.1) — and with
// no history. IDs are issued here and never derived from content (§3.1).
func (s *caseStore) create(d *Definition) CaseID {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := newState(d.img)
	st.applyDefaults()
	s.next++
	s.cases[s.next] = &storedCase{
		def:    d.ID(),
		commit: committedState{s: st, stage: d.img.initial},
		work:   map[UserID]*workingState{},
	}
	return s.next
}

// load returns a copy of one case's record. An unknown ID is NOT_FOUND:
// the zero record is never an acceptable answer, because revision 0 at the
// initial stage is a real state a case can be in.
func (s *caseStore) load(refuse refuser) (storedCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, err := s.check(refuse)
	if err != nil {
		return storedCase{}, err
	}
	return *rec, nil
}

// working returns an actor's working copy, making it from the committed
// state on first access (DESIGN §5: a working state is created when the
// user opens the case). Every session of one actor gets the same copy —
// invariant 4 — because the copy is keyed by identity.
//
// The three methods that follow take a `refuser` for the reason `check`
// gives: a working copy is read and written by whichever operation the host
// called, and the store cannot know which one it was. The case it is asked
// about comes from the same binding, because a name and an ID that were
// allowed to disagree would be a store refusal about the wrong case.
//
// `img` is the image the caller's definition just resolved to, and the copy
// is handed out interpreted through it. The caller has proved the two images
// agree on the ordinal space — `look` refuses before any caller of this
// reaches it — so this is DESIGN §3's reinterpretation applied to a working
// state rather than a committed one, and it is a field of a copy: nothing
// here writes the record, which is the part of invariant 8 that matters to
// a case nobody has re-read yet.
func (s *caseStore) working(refuse refuser, user UserID,
	img *image) (workingState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return workingState{}, err
	}
	if w, have := rec.work[user]; have {
		// The caller gets a candidate, not a window into the store: the
		// state is cloned on the way out for the same reason §5.1's frame is
		// built from a clone. An edit an operation abandons must not be
		// visible to the next session of the same actor, and an operation
		// that reads a working copy to *check* something (Submit's
		// completeness pass, §5.4) must not be able to write through what it
		// read.
		out := w.clone()
		out.s.reinterpret(img)
		return out, nil
	}
	w := &workingState{base: rec.revision, s: rec.commit.s.clone()}
	w.s.reinterpret(img)
	rec.work[user] = w
	return w.clone(), nil
}

// draft returns an actor's working copy with its *lineage* — the committed
// revision it descends from, which is the whole question §6.5's rebase asks —
// and says whether a copy exists at all.
//
// It is `viewed`'s more exact sibling, and both answer without creating what
// they report. The distinction is not tidiness. `working` creates a copy on
// first use, because an edit is about to write into it; a method that
// allocated per-user state for every request would let reads grow the case,
// which is DESIGN §9's "no persistent effect" for an operation that changes
// nothing — and a host that renders a form for ten thousand viewers, or polls
// for synchronization, would pay ten thousand rows for nothing.
//
// So an actor with no draft is answered from the committed snapshot, which is
// what their draft would have been cloned from: `base` is the case's own
// revision and `rev` is 0, the same number §6.2 tells that actor to pass as
// `base` when they do write, because the copy they then get is a fresh one.
// The two answers agree because both follow the same rule: no copy exists
// until an edit.
//
// The state is cloned anyway. That is the store's clone-on-the-way-out rule
// applied without exception, and the exception is the dangerous half: a walk
// that "only reads" today is one memoization away from writing through a window
// into stored data. Milestone 4 replaces this with the read transaction DESIGN
// §8 describes, which hands back a snapshot that cannot be written at all.
func (s *caseStore) draft(refuse refuser, user UserID,
	img *image) (workingState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return workingState{}, false, err
	}
	if w, have := rec.work[user]; have {
		out := w.clone()
		out.s.reinterpret(img)
		return out, true, nil
	}
	out := rec.commit.s.clone()
	out.reinterpret(img)
	return workingState{base: rec.revision, s: out}, false, nil
}

// viewed is `draft` asked by an operation that renders values and never needs
// to know what they descend from. It exists because §5.7's walk wants exactly
// two things, and a caller handed the third would be tempted to use it.
func (s *caseStore) viewed(refuse refuser, user UserID,
	img *image) (state, uint64, error) {
	w, _, err := s.draft(refuse, user, img)
	return w.s, w.rev, err
}

// saveWorking replaces an actor's working copy if it is still at expected,
// and reports the revision it now carries. A copy that does not exist is not
// created here: `working` creates it, and writing over a copy nobody opened
// is a caller bug, refused as one rather than guessed at.
//
// What the caller does *not* get to choose is the revision. `next` arrives
// with the fields the store set when it handed the copy out, and a save that
// trusted them would let one session publish a revision number another
// session had already taken — so the guard reads the stored copy, and the
// stored copy's own rev is what lands, advanced by one. The data is the
// caller's; the numbering is the store's, the same division §3.1 makes of a
// case ID.
//
// `onto` is the one exception, and it is narrow: an operation that leaves the
// base alone passes 0, and `Rebase` passes the committed revision it has just
// folded in — a rebase's whole purpose is to move a draft to a revision the
// case already reached, which §5.4's submit guard reads and `commit` cannot
// help with because that step only advances the committer's own lineage. The
// value is checked against the case rather than trusted, because a lineage a
// caller could set anywhere would be a lineage that skips a commit and lies
// about what the draft descends from.
func (s *caseStore) saveWorking(refuse refuser, user UserID, expected uint64,
	next workingState, onto uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return 0, err
	}
	cur, have := rec.work[user]
	if !have {
		return 0, refuse.now(InvalidOp,
			"this actor has no working copy to write over")
	}
	if cur.rev != expected {
		return 0, refuse.staleCopy(cur.rev, expected)
	}
	if onto != 0 && (onto < cur.base || onto > rec.revision) {
		// A defect and not a request: no caller of this package can reach it
		// by asking for a number, because §5.8 hands over the revision it
		// just read from this record under the same lock. §10's rule against
		// laundering a runtime bug into a refusal is why this is not reported
		// as a stale copy.
		return 0, refuse.now(SchemaMismatch, "a rebase would move the copy's"+
			" lineage to revision "+dec(onto)+", which is not between "+
			dec(cur.base)+" and the case's "+dec(rec.revision))
	}
	stored := next
	stored.s = next.s.clone()
	stored.base, stored.rev = cur.base, expected+1
	if onto != 0 {
		stored.base = onto
	}
	rec.work[user] = &stored
	return stored.rev, nil
}

// discard removes an actor's working copy, guarded by the working revision
// the caller observed. The guard is not ceremony: §6.5's reason for it is that
// another session of the same actor may have written the copy since, and the
// thing this call destroys is unsaved work that nobody else has a copy of.
// So a caller presenting 0 for a copy that has moved hears the same
// STALE_REVISION `Update` and `Submit` give for it — one sentence, three
// places, and the caller learns the newest edits are somebody else's to
// resolve rather than being deleted quietly.
//
// A copy that does not exist is a success, not a NOT_FOUND: §6.5 says a new
// copy starts from the latest committed state on next access, which is exactly
// the state an actor with no copy is already in, so the caller's wish — "drop
// my draft" — is true whether or not there was one to drop. What is *not*
// true of that actor is a belief that they held edits, which is why 0 is the
// only expected number a missing copy answers, and a copy the store has never
// heard of is refused as the caller bug it is rather than confirmed as a
// discard.
func (s *caseStore) discard(refuse refuser, user UserID,
	expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return err
	}
	cur, have := rec.work[user]
	if !have {
		if expected != 0 {
			return refuse.now(InvalidOp, "this actor has no working copy, so"+
				" revision "+dec(expected)+" was never a number they held")
		}
		return nil
	}
	if cur.rev != expected {
		return refuse.staleCopy(cur.rev, expected)
	}
	delete(rec.work, user)
	return nil
}

// staleCopy is the one sentence DESIGN §6.2's guard needs, stated twice
// because the check is made twice: once when an operation reads the copy it
// is about to write, so a caller whose view is out of date hears about it
// before anything is resolved or checked against permissions, and once under
// the store's lock, because a second session of the same actor can have saved
// in between. Both say the same thing, which is the point: the race is not a
// different error from the stale case, only a rarer one. It is a method
// rather than a sentence in two places so that "the same thing" is true by
// construction, and it carries the operation's name from whichever of the two
// checks reached it.
func (r refuser) staleCopy(cur, want uint64) error {
	return r.now(StaleRevision,
		"the working copy is at revision "+dec(cur)+", not "+dec(want))
}

// committedBy is what a committing operation tells the store about itself and
// the content it is publishing: who acted, which operation, and the forward
// diff that says what the commit changed. The store supplies the timestamp and
// the revision (§3.1's split, applied to history: the caller's words are data,
// the numbering is the store's).
type committedBy struct {
	by      UserID
	op      commitOp
	changes []commitChange
}

// commit replaces a case's committed snapshot, appends the commit that says
// why, and rebases the submitting actor's working copy onto the revision the
// commit produced — the three writes DESIGN §6.3 requires to be consistent,
// which in this store is one hold of the lock.
//
// `want` is the committed revision the caller observed; anything else is
// refused rather than merged, which is §6.3's "if state changes concurrently,
// reject rather than apply a stale prepared diff" and invariant 5's reason for
// existing. The check is made here and not in the operation because this is
// the only place the stored revision is both read and written, and a guard an
// operation performed outside the lock it does not hold would be a check that
// another commit could pass.
//
// `img` is the case's own definition, and this method needs it for exactly one
// reason: §4.2's reset of the review table is part of committing the snapshot,
// and the store keys cases by definition *name* rather than holding the image
// the data was built under (DESIGN §3 leaves that pairing to the handle).
// `by.changes` is the diff the same call is committing, which is what the reset
// reads.
//
// The history append and the snapshot swap are the two halves of one act, and
// the order inside the lock is worth stating because it is what makes
// `revision` mean "the count of commits": the snapshot moves, the revision
// moves with it, and only then does the entry get the number the two now
// agree on. A reader of the record sees one consistent triple because every
// field it reads is a value the record owns, not a pointer it can follow into
// a state another goroutine is writing.
//
// The rebase touches only the committer's copy, and only its `base`: the
// content stays, because §6.3 is explicit that other forms' unsubmitted edits
// survive one form's submit. Leaving `rev` where it was is what lets the same
// client keep editing with the working revision it already holds, and what
// keeps another session of that actor from getting a free pass at revision 0.
//
// It also requires that the copy actually descended from the revision this
// commit replaces, which is the difference between `Submit` and every
// committed-state operation that followed it. A submit cannot arrive without
// that already true — its own guard compared the copy's `base` with the case's
// revision before anything was grafted — so for a submit the test passes by
// construction and the rebase is §6.3's rule stated plainly. A `Transition`
// asks nothing of its author's draft: it moves the case, not the caller's
// copy, and the caller presents the *case's* number. So an actor whose draft
// was left behind two revisions ago can move the stage from a current
// snapshot, and an unconditional rebase would then relabel that draft as
// descending from the state it never saw — which is not a convenience but a
// lost-update bug one call later, because his next submit would pass the very
// guard §6.5 exists to make refuse. Lineage is a fact about history, and a
// commit may only extend a copy's lineage when it was made against the state
// that copy stands on.
func (s *caseStore) commit(refuse refuser, img *image, want uint64,
	next committedState, by committedBy) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return 0, err
	}
	if rec.revision != want {
		return 0, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+dec(want))
	}
	// DESIGN §4.2's reset, applied here and not by the caller, which is the
	// difference between a rule and a convention: an operation that forgot to
	// reset its own units would ship a case whose APPROVED mark outlived the
	// content it approved, and no read could tell that happened. `img` is a
	// parameter for this line and nothing else — the store keys cases by
	// definition *name*, and a case's image is the caller's to know.
	//
	// It runs after the revision guard and before either write, so a rejected
	// commit leaves the stored table untouched and an accepted one can never
	// carry a stale mark. What the reset costs is one array read per diff
	// entry (§5.6), which is why it is paid on every commit rather than
	// skipped when the case looks unaffected.
	next.reviews = resetVerifs(img, next.reviews, by.changes)
	rec.commit = next
	rec.revision++
	rec.hist = append(rec.hist, commitRecord{
		revision: rec.revision,
		at:       s.stamp(),
		by:       by.by,
		op:       by.op,
		stage:    next.stage,
		changes:  by.changes,
	})
	if w, have := rec.work[by.by]; have && w.base == want {
		rebased := *w
		rebased.base = rec.revision
		rec.work[by.by] = &rebased
	}
	return rec.revision, nil
}

// history is one case's commits, in the order they landed. It hands out a
// copy of the entry list, never the record's own slice, for the reason
// `load` gives: a reader must not be able to write through a pointer into
// stored data. DESIGN §5's "ordered, immutable commits" is a claim about
// what a reader can do as much as about what the store keeps, and this is
// the only way in that is not the commit path itself.
//
// Today it has no caller outside the tests: a host reads history through the
// audit view and the rebase, and both are later slices (DESIGN §6.5). It
// exists now because the history is the store's third kind of state, and
// because milestone 4's transaction needs the same read to replay from.
func (s *caseStore) history(refuse refuser) ([]commitRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, err := s.check(refuse)
	if err != nil {
		return nil, err
	}
	if len(rec.hist) == 0 {
		return nil, nil
	}
	out := make([]commitRecord, len(rec.hist))
	copy(out, rec.hist)
	return out, nil
}

// at finds a case, and `check` is the same lookup as an error. Both assume
// the caller holds the lock: `load` reads under the read lock, the writers
// under the write lock, and a mutation of a record's fields never reaches
// data another goroutine is reading because it replaces a value instead of
// writing through a pointer to it.
//
// `check`'s `refuse` names the refusal after the act that asked, as
// `registry.add`'s does. The store itself has no business being in a host's
// log line: a host called `Update` and refused a commit, and "load" would
// send whoever reads that line looking at the wrong half of the package.
func (s *caseStore) at(id CaseID) (*storedCase, bool) {
	rec, ok := s.cases[id]
	return rec, ok
}

func (s *caseStore) check(refuse refuser) (*storedCase, error) {
	rec, ok := s.at(refuse.id)
	if !ok {
		return nil, refuse.now(NotFound, "no such case")
	}
	return rec, nil
}
