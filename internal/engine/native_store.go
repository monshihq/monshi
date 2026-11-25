package engine

import (
	"context"
	"sync"
	"time"

	"github.com/monshihq/monshi/internal/program"
)

// caseStore is the codec-free backend used by NewRuntime.
type caseStore struct {
	mu    sync.RWMutex
	next  CaseID
	cases map[CaseID]*nativeCase

	// now is the clock a commit stamps. It is a field rather than a call to
	// `time.Now` because a commit's timestamp is *data* — DESIGN §5.2's rule
	// that replay is deterministic means the value has to be recorded, not
	// recomputed, and a test that asserts on a history entry needs to be able
	// to say when it happened. A store built any other way falls back to the
	// system clock.
	now func() time.Time
}

func newCaseStore() *caseStore {
	return &caseStore{cases: map[CaseID]*nativeCase{}, now: time.Now}
}

// stamp is the clock's answer for one commit.
func (s *caseStore) stamp() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// nativeCase owns a snapshot, the actors' drafts, and the dense history log.
// The mutex in caseStore guards writes to all three.
type nativeCase struct {
	caseHead
	work map[UserID]*workingState
	hist []commitRecord
}

// create allocates a case of one definition at revision 0, at the initial
// stage, with its declared defaults materialized — creating a case stores
// an empty case, not an undefaults one (DESIGN §5, RUNTIME §5.1) — and with
// no history. IDs are issued here and never derived from content (§3.1).
func (s *caseStore) create(_ context.Context, _ refuser,
	d *Definition) (CaseID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.cases[s.next] = &nativeCase{
		caseHead: caseHead{def: d.ID(), commit: initialState(d.img)},
		work:     map[UserID]*workingState{},
	}
	return s.next, nil
}

// load returns a copy of one case's record. An unknown ID is NOT_FOUND:
// the zero record is never an acceptable answer, because revision 0 at the
// initial stage is a real state a case can be in.
func (s *caseStore) load(_ context.Context, refuse refuser) (
	caseHead, error,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, err := s.check(refuse)
	if err != nil {
		return caseHead{}, err
	}
	return rec.caseHead, nil
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
func (s *caseStore) working(_ context.Context, refuse refuser, user UserID,
	img *program.Program) (workingState, error) {
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
// into stored data. On the exported path (DESIGN §8, storage.go) the same
// promise costs no clone and no alias at all: every read decodes fresh bytes,
// so the snapshot is by construction the reader's own.
func (s *caseStore) draft(_ context.Context, refuse refuser, user UserID,
	img *program.Program) (workingState, bool, error) {
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
func (s *caseStore) viewed(ctx context.Context, refuse refuser, user UserID,
	img *program.Program) (state, uint64, error) {
	w, _, err := s.draft(ctx, refuse, user, img)
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
func (s *caseStore) saveWorking(_ context.Context, refuse refuser,
	user UserID, expected uint64, next workingState,
	onto uint64) (uint64, error) {
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
func (s *caseStore) discard(_ context.Context, refuse refuser, user UserID,
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

// commit publishes the head and history under one revision check. Only a
// committer draft based on the replaced revision can advance its lineage;
// other drafts retain their base and must rebase explicitly.
func (s *caseStore) commit(_ context.Context, refuse refuser, img *program.Program,
	want uint64, next committedState,
	by committedBy) (committed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.check(refuse)
	if err != nil {
		return committed{}, err
	}
	if rec.revision != want {
		return committed{}, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+dec(want))
	}
	rec.commit = next
	entry, erased := prepareCommit(img, &rec.commit, want+1, s.stamp(), by)
	rec.revision = entry.revision
	rec.hist = append(rec.hist, entry)
	copyRev := uint64(0)
	if w, have := rec.work[by.by]; have {
		if w.base == want {
			rebased := *w
			rebased.base = rec.revision
			rec.work[by.by] = &rebased
		}
		copyRev = w.rev
	}
	return committed{rec: entry, post: rec.commit, copy: copyRev,
		erased: erased}, nil
}

// window is the part of a case's history a reader asked for: the entries from
// revision `from` forward, at most `limit` of them, and all of them when limit
// is 0. It is a slice of the stored array and not a copy, because of the fact
// §5.8 states about the read it needs: the trail is *dense* — the entry at index
// 0 is the commit that produced revision 1, since a revision is the count of
// commits and creating a case appends nothing — so a cursor into history and a
// revision number are the same integer, and the answer is arithmetic rather
// than a search.
//
// Handing out the stored array is safe for the reason `load` handing out a
// copied struct is: an operation reads entries and renders values of its own,
// and the store's rule is replace-never-mutate rather than never-alias.
// `History` is that rule's exported case — every entry it answers with is a
// `Commit` this package built — and the seam's `history` is its paging
// form: both read, neither writes, and no caller of either is handed a
// copy to keep the store safe from.
func (rec *nativeCase) window(from uint64, limit int) []commitRecord {
	return historyPage(rec.hist, from, limit)
}

// history is the page a reader asked for: `window`'s slice of the stored
// array, which the operation renders and `Rebase` splits without either
// one copying — the trail's entries are immutable (invariant 3) and a later
// commit appends without touching what was handed out, so aliasing is safe
// for the same reason `load` handing out a copied struct is. A cursor of 0
// or 1 and a limit of 0 is the whole trail, which is what the derivation
// and the tests assert against.
func (s *caseStore) history(_ context.Context, refuse refuser,
	cursor uint64, limit int) ([]commitRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, err := s.check(refuse)
	if err != nil {
		return nil, err
	}
	return rec.window(cursor, limit), nil
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
func (s *caseStore) at(id CaseID) (*nativeCase, bool) {
	rec, ok := s.cases[id]
	return rec, ok
}

func (s *caseStore) check(refuse refuser) (*nativeCase, error) {
	rec, ok := s.at(refuse.id)
	if !ok {
		return nil, refuse.now(NotFound, "no such case")
	}
	return rec, nil
}
