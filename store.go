package monshi

// The store seam (DESIGN §5, §8). Case data lives here and nowhere else:
// the registry holds compiled definitions, a handle holds an ID and an
// actor, and every mutable byte of a case is behind this file.
//
// Milestone 2's implementation is in-memory and process-lifetime, which is
// what DESIGN §10 asks of this milestone ("in-memory state"). The *shape*
// is milestone 4's: `create`, `load`, `working` and the revision-guarded
// writes are the methods a persistent backend implements, and the guards
// they enforce are what §6.4's stale-revision refusals and invariant 5
// depend on. What milestone 4 adds is the atomic commit transaction that
// appends history and replaces a snapshot in one step (DESIGN §8): a new
// method here, not a changed one, and not a second place the guards live.
//
// A `context.Context` reaches these methods with milestone 4 too. The seam
// DESIGN §8 defines takes one and the exported operations already carry
// the caller's, so the plumbing there is one parameter per method; an
// in-memory store has nothing to cancel, and threading a context through
// six methods that do no I/O would only invite the question of which of
// them was supposed to honour it.
//
// One discipline makes reads safe without locking a caller out of a writer:
// the store never mutates data it has handed out. A commit builds the next
// state and *replaces* the field holding it, so a snapshot an operation
// read stays consistent while another operation commits. That is the same
// rule as invariant 3's immutable history, stated about the present rather
// than the past, and it is why a `state` copy is a safe thing to hand out.

import "sync"

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
// rather than resolving. `op` names the refusal after the act that asked,
// because Register and Replace report the same collision differently.
func (g *registry) add(op string, d *Definition, replace bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.defs[d.ID()]; dup && !replace {
		return refused(op, InvalidOp, 0,
			"definition "+quote(d.ID())+" is already registered")
	}
	g.defs[d.ID()] = d
	return nil
}

// caseStore holds every case's data, of the three kinds DESIGN §5 names.
// Milestone 2 has two of them — the materialized committed state and the
// per-user working copies. History arrives with milestone 4's transaction:
// it is append-only and derived from these same states, so nothing here
// changes shape when it does.
type caseStore struct {
	mu    sync.RWMutex
	next  CaseID
	cases map[CaseID]*storedCase
}

func newCaseStore() *caseStore {
	return &caseStore{cases: map[CaseID]*storedCase{}}
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
}

// committedState is one materialized committed snapshot: the case's values
// and instance rows, and the stage it is at. Reviews, signatures and
// verification state join this shape in milestone 5; the fields below it do
// not move, which is why a host-facing `Committed` can be added later
// without breaking anything that reads it.
type committedState struct {
	s     state
	stage stageID
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
func (s *caseStore) load(op string, id CaseID) (storedCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, err := s.check(op, id)
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
// The three methods that follow take `op` for the reason `check` gives: a
// working copy is read and written by whichever operation the host called,
// and the store cannot know which one it was.
func (s *caseStore) working(op string, id CaseID, user UserID) (
	workingState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(op, id)
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
		return w.clone(), nil
	}
	w := &workingState{base: rec.revision, s: rec.commit.s.clone()}
	rec.work[user] = w
	return w.clone(), nil
}

// saveWorking replaces an actor's working copy if it is still at expected.
// A copy that does not exist is not created here: `working` creates it, and
// writing over a copy nobody opened is a caller bug, refused as one rather
// than guessed at.
func (s *caseStore) saveWorking(op string, id CaseID, user UserID,
	expected uint64, next workingState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(op, id)
	if err != nil {
		return err
	}
	cur, have := rec.work[user]
	if !have {
		return refused(op, InvalidOp, id,
			"this actor has no working copy to write over")
	}
	if cur.rev != expected {
		return refused(op, StaleRevision, id,
			"the working copy is at revision "+dec(cur.rev)+", not "+dec(expected))
	}
	stored := next
	stored.s = next.s.clone()
	rec.work[user] = &stored
	return nil
}

// commitState replaces a case's committed data if the case is still at
// fromRevision, and reports the revision it now carries. Milestone 5's
// Submit and Transition call this; it lands now so the guard is a property
// of the store rather than of an operation that has not, and milestone 4
// wraps it and the history append in one transaction without changing what
// it checks.
func (s *caseStore) commitState(op string, id CaseID, fromRevision uint64,
	next committedState) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(op, id)
	if err != nil {
		return 0, err
	}
	if rec.revision != fromRevision {
		return 0, refused(op, StaleRevision, id,
			"the case is at revision "+dec(rec.revision)+", not "+dec(fromRevision))
	}
	rec.commit = next
	rec.revision++
	return rec.revision, nil
}

// at finds a case, and `check` is the same lookup as an error. Both assume
// the caller holds the lock: `load` reads under the read lock, the writers
// under the write lock, and a mutation of a record's fields never reaches
// data another goroutine is reading because it replaces a value instead of
// writing through a pointer to it.
//
// `check`'s `op` names the refusal after the act that asked, as
// `registry.add`'s does. The store itself has no business being in a host's
// log line: a host called `Update` and refused a commit, and "load" would
// send whoever reads that line looking at the wrong half of the package.
func (s *caseStore) at(id CaseID) (*storedCase, bool) {
	rec, ok := s.cases[id]
	return rec, ok
}

func (s *caseStore) check(op string, id CaseID) (*storedCase, error) {
	rec, ok := s.at(id)
	if !ok {
		return nil, refused(op, NotFound, id, "no such case")
	}
	return rec, nil
}
