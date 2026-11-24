package monshi

// The storage contract (DESIGN §8). This file is the seam milestone 2
// reserved and milestone 4 lands: an exported interface a host implements
// to make case state durable, the library's own in-memory implementation
// of it, and the adapter that runs the engine's operations over any
// implementation. The engine's side of the seam is store.go's
// `caseBackend`; what crosses this file is state as *bytes*, because
// DESIGN §5 makes the exact Go representation an implementation detail,
// and an implementation detail a host can see is the thing to write
// down: codec.go defines what the bytes mean, and a host stores them
// without interpreting them.
//
// What a host implements is the atomicity rule, not a transaction
// technology. §8's `Commit` is one compare-and-swap step — revision
// check, entry append, snapshot swap, committer's rebase — and DESIGN §9's
// guarantee that an operation either transitions or has no persistent
// effect reads back through it: a backend that can fail halfway is a
// backend that owes the engine a rollback, not a receipt to interpret.
// What the engine guarantees back is the shape of the bytes and the
// meaning of the errors: a store never sees a `CaseError`, and a host
// never sees the engine's refusals re-worded by the adapter.
//
// The in-memory default does NOT route through this file.
// `NewRuntimeWithStorage(nil)` is `NewRuntime`, and the default path
// keeps handing states around as structs, which is what lets RUNTIME §8's
// operation rows keep measuring the engine and not an encoding of it. The
// bytes are only what crosses a boundary the engine cannot see through.

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"
)

// CaseRecord is one case as a store holds it: the ID the store allocated,
// the definition the case was created under (the whole of the engine's
// migration surface — §3's reload rule needs a name, not a version), the
// committed revision, and the encoded snapshot. `State` bytes are
// produced and read only by this package; a host stores, compares and
// returns them without interpreting them.
type CaseRecord struct {
	ID         CaseID
	Definition string
	Revision   uint64
	State      []byte
}

// WorkingRecord is one actor's uncommitted copy as a store holds it: the
// committed revision the draft descends from (§6.5's merge input), the
// draft's own revision (the number §6.2's guard presents and the store
// advances), and the encoded candidate.
type WorkingRecord struct {
	Base     uint64
	Revision uint64
	State    []byte
}

// HistoryEntry is one encoded commit record. Entries are dense by
// revision — index 0 produced revision 1, and a case at revision N holds
// N entries — which is the contract §5.11's cursor arithmetic reads:
// a `History` page is a slice of the trail, and a continuation token is
// an integer, because a revision *is* the count of commits.
type HistoryEntry []byte

// Transaction is one atomic commit: who acted, the committed revision
// the operation observed (the compare half of the compare-and-swap), the
// history entry the append writes, and the record the swap installs. The
// engine numbers both sides — `Entry` already says which revision it
// produced and `Head.Revision` is that number — because a store that
// could disagree about which commit it appended would split the trail
// from the snapshot it claims to be the fold of. `Head.Definition`
// restates the stored record's; the bytes were written under it, and a
// host that validates has the fact at hand.
type Transaction struct {
	Actor    UserID
	Expected uint64
	Entry    HistoryEntry
	Head     CaseRecord
}

// Receipt answers what a committed transaction left behind: the revision
// that landed — which is `Head.Revision` for a store that honoured the
// contract, carried back so a host that remapped nothing can be checked
// rather than trusted — and the committer's own working-copy revision
// after the transaction's rebase rule, 0 when the actor holds no copy.
type Receipt struct {
	Revision uint64
	Draft    uint64
}

// Storage is DESIGN §8's contract, refined at the two points §8 allows:
// `Create` takes the record because the initial state — defaults
// materialized, initial stage — is the engine's to build and only the ID
// is the host's to allocate; and §8's find-or-init `Working` splits into
// `Draft`, which only finds, so that a read never grows the store
// (§5.7's rule that a view allocates no per-user state survives
// persistence), and an engine-side init that arrives as an ordinary
// `Save` with expected 0 over no copy.
//
// Every method takes the caller's context, and a backend that can block
// must honour its cancellation. The context is a *request* context, not
// a transaction handle: nothing in this contract composes across calls,
// and each method is one atomic step on its own.
type Storage interface {
	// Create stores `rec` under an ID the backend allocates — never
	// derived from content (§3.1) — and answers it. The engine passes
	// `Revision` 0, `ID` unset, and `State` as defined by codec.go.
	Create(ctx context.Context, rec CaseRecord) (CaseID, error)

	// Load reads one case. An unknown ID answers ErrNoSuchCase; no
	// zero record is ever an acceptable answer, because revision 0 at
	// the initial stage is a real state a case can be in.
	Load(ctx context.Context, id CaseID) (CaseRecord, error)

	// Draft reads one actor's copy and reports whether it exists. It
	// creates nothing: an absent copy answers the zero value with
	// false, and the engine derives what one would hold from `Load`.
	Draft(ctx context.Context, id CaseID, user UserID) (
		WorkingRecord, bool, error)

	// Save installs one actor's copy under the guard. If no copy
	// exists, `expected` must be 0 and the record lands as given:
	// that is the engine's init, and a nonzero `expected` over no
	// copy is ErrNoDraft. If a copy exists, its revision must equal
	// `expected` — else *Stale with what the store holds — and the
	// revision that lands is `expected`+1: the data is the caller's
	// and the numbering is the store's, the same split §3.1 makes of
	// a case ID.
	Save(ctx context.Context, id CaseID, user UserID, expected uint64,
		w WorkingRecord) error

	// Discard removes one actor's copy under the same guard. An
	// absent copy answers ErrNoDraft, which the engine reads as
	// §6.5's rule, not a failure: discarding nothing a caller
	// presented 0 for is a success.
	Discard(ctx context.Context, id CaseID, user UserID,
		expected uint64) error

	// Commit is the contract's one atomic step: the head revision
	// must equal `tx.Expected` — else *Stale with the revision the
	// store holds — and then the swap, the append and this rule
	// happen together: if a copy of `tx.Actor` exists whose `Base`
	// equals `tx.Expected`, its `Base` becomes `tx.Head.Revision`
	// (§6.3's rebase of the committer's draft, and the only reason
	// the store rather than the engine holds this rule: no caller
	// sees the drafts of others).
	Commit(ctx context.Context, id CaseID, tx Transaction) (
		Receipt, error)

	// History reads a page of the trail: entries from revision
	// `cursor` forward — 0 meaning the beginning, because revision 0
	// has no entry — and at most `limit` of them, all of them when
	// limit is 0. The density stated on HistoryEntry is what makes
	// this a slice and not a search.
	History(ctx context.Context, id CaseID, cursor uint64,
		limit int) ([]HistoryEntry, error)
}

// The refusals a backend answers with. They arrive as data rather than
// prose because the engine's §9 categories carry an operation name and a
// case ID a host cannot know from the outside: the adapter translates
// each sentinel into the same sentence the in-memory store refuses with,
// so which backend ran an operation is not something its caller can
// observe in an error.
var (
	// ErrNoSuchCase says the ID was never issued (or is gone).
	ErrNoSuchCase = errors.New("monshi: no such case")

	// ErrNoDraft says the actor holds no working copy. It is a fact
	// about absence, not a refusal: only `Draft` reports it as a
	// bool, and the methods that take `expected` answer it when a
	// guarded write or delete finds nothing to guard.
	ErrNoDraft = errors.New("monshi: no working copy")
)

// Stale is a revision-guard refusal: the backend holds `Have` where the
// caller presented something else. It travels as a value so `errors.As`
// can read the number back, and the engine folds it into the same
// STALE_REVISION sentence its own guards use.
type Stale struct{ Have uint64 }

func (s *Stale) Error() string {
	return "monshi: case is at revision " + strconv.FormatUint(s.Have, 10)
}

// BinaryStore is the separate binary abstraction DESIGN §8 sketches by
// name: content the case record does not carry, reached by the opaque
// `BinaryRef` a field holds (§5.2's retention rule belongs to whoever
// stores what it names). The engine neither reads through it nor
// resolves anything inside a reference — a `BinaryRef` is bytes the
// definition's author's content produced, and what they mean is a host
// and service concern — so this interface is offered for hosts to
// implement and compose, with no wiring into case operations.
type BinaryStore interface {
	Put(ctx context.Context, r io.Reader) (BinaryRef, error)
	Get(ctx context.Context, ref BinaryRef) (io.ReadCloser, error)
}

// NewMemStorage returns the library's own Storage: the same three kinds
// of state DESIGN §5 names, held in maps, guarded exactly as store.go's
// in-memory seam guards them. It is the conformance implementation the
// contract's tests run against first — a host comparing its backend
// against this one gets the same refusals — and a service that wants
// process-lifetime durability behind the exported verbs without a
// database can use it directly.
func NewMemStorage() *MemStorage {
	return &MemStorage{cases: map[CaseID]*memCase{}}
}

// MemStorage is NewMemStorage's implementation type. Its fields are
// private: what a host sees of it is the Storage interface, and nothing
// about how a map is keyed is part of the contract.
type MemStorage struct {
	mu    sync.RWMutex
	next  CaseID
	cases map[CaseID]*memCase
}

// memCase is one record and the two lists that grow beside it. The
// bytes alias what Create and Commit stored: this package's encoders
// never reuse a buffer, so handing them out again copies nothing.
type memCase struct {
	rec  CaseRecord
	work map[UserID]WorkingRecord
	hist []HistoryEntry
}

func (s *MemStorage) Create(_ context.Context, rec CaseRecord) (CaseID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	rec.ID = s.next
	s.cases[s.next] = &memCase{rec: rec, work: map[UserID]WorkingRecord{}}
	return s.next, nil
}

func (s *MemStorage) Load(_ context.Context, id CaseID) (CaseRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.cases[id]
	if !ok {
		return CaseRecord{}, ErrNoSuchCase
	}
	return rec.rec, nil
}

func (s *MemStorage) Draft(_ context.Context, id CaseID,
	user UserID) (WorkingRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.cases[id]
	if !ok {
		return WorkingRecord{}, false, ErrNoSuchCase
	}
	w, have := rec.work[user]
	return w, have, nil
}

func (s *MemStorage) Save(_ context.Context, id CaseID, user UserID,
	expected uint64, w WorkingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cases[id]
	if !ok {
		return ErrNoSuchCase
	}
	cur, have := rec.work[user]
	switch {
	case !have:
		if expected != 0 {
			return ErrNoDraft
		}
		rec.work[user] = w
		return nil
	case cur.Revision != expected:
		return &Stale{Have: cur.Revision}
	}
	w.Revision = expected + 1
	rec.work[user] = w
	return nil
}

func (s *MemStorage) Discard(_ context.Context, id CaseID, user UserID,
	expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cases[id]
	if !ok {
		return ErrNoSuchCase
	}
	cur, have := rec.work[user]
	if !have {
		return ErrNoDraft
	}
	if cur.Revision != expected {
		return &Stale{Have: cur.Revision}
	}
	delete(rec.work, user)
	return nil
}

func (s *MemStorage) Commit(_ context.Context, id CaseID,
	tx Transaction) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cases[id]
	if !ok {
		return Receipt{}, ErrNoSuchCase
	}
	if rec.rec.Revision != tx.Expected {
		return Receipt{}, &Stale{Have: rec.rec.Revision}
	}
	rec.rec.State = tx.Head.State
	rec.rec.Revision = tx.Head.Revision
	rec.hist = append(rec.hist, tx.Entry)
	out := Receipt{Revision: tx.Head.Revision}
	if w, have := rec.work[tx.Actor]; have {
		if w.Base == tx.Expected {
			w.Base = tx.Head.Revision
			rec.work[tx.Actor] = w
		}
		out.Draft = w.Revision
	}
	return out, nil
}

func (s *MemStorage) History(_ context.Context, id CaseID, cursor uint64,
	limit int) ([]HistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.cases[id]
	if !ok {
		return nil, ErrNoSuchCase
	}
	if cursor < 1 {
		cursor = 1
	}
	lo := int(cursor) - 1
	if lo >= len(rec.hist) {
		return nil, nil
	}
	page := rec.hist[lo:]
	if limit > 0 && limit < len(page) {
		page = page[:limit]
	}
	out := make([]HistoryEntry, len(page))
	copy(out, page)
	return out, nil
}

var _ Storage = (*MemStorage)(nil)

// stampNow is every backend's fallback clock, the same default
// caseStore.newCaseStore gives its in-memory seam.
func stampNow() time.Time { return time.Now() }

// hostBackend is the adapter: it runs the engine's `caseBackend` calls
// against a host's `Storage`, encoding on the way out and decoding on
// the way in. The translation work that matters is in the errors. A
// host's refusal arrives as one of three values this package defines —
// ErrNoSuchCase, ErrNoDraft, *Stale — and leaves as the *exact* sentence
// store.go's in-memory methods refuse with, built through the calling
// operation's refuser: same category, same operation name, same numbers.
// Which backend ran an operation is thus not a fact its caller can
// observe, and a conformance suite written against one implementation
// reads the same on any other.
//
// Anything else a host returns becomes STORAGE_FAILURE with the host's
// error as Cause — the one category DESIGN §9 reserves for an explanation
// that belongs to somebody else — and a refusal whose bytes this package
// itself wrote (a corrupt or foreign `State`) lands in the same
// category, because from the caller's side a store that cannot answer is
// a store that failed.
type hostBackend struct {
	st   Storage
	defs *registry
	now  func() time.Time
}

func (b *hostBackend) stamp() time.Time {
	if b.now == nil {
		return stampNow()
	}
	return b.now()
}

// failed is the translation every call runs its error through first:
// the case-level sentinel, then everything that is not a guard's
// refusal, which is the host's own failure to report.
func (b *hostBackend) failed(r refuser, err error) error {
	if errors.Is(err, ErrNoSuchCase) {
		return r.now(NotFound, "no such case")
	}
	return &CaseError{Op: r.op, Reason: StorageFailure, Case: r.id,
		Detail: "the case store refused the call", Cause: err}
}

// imageOf resolves a record's definition the way `look` does, and
// refuses with its sentence: the adapter needs the image to decode by
// before any caller of `load` gets the chance to, and a definition that
// is not registered must be as unopenable through a host's store as it
// is through the in-memory one.
func (b *hostBackend) imageOf(r refuser, def string) (*image, error) {
	d, ok := b.defs.get(def)
	if !ok {
		return nil, r.now(SchemaMismatch, "definition "+quote(def)+
			" governs this case and is not registered")
	}
	return d.img, nil
}

// head reads and decodes the committed snapshot, for the calls that
// answer a missing draft from it — `working` and `draft`, which mirror
// the in-memory store's rule that an actor with no copy is answered
// with the state their copy would have descended from.
func (b *hostBackend) head(ctx context.Context, r refuser,
	img *image) (committedState, uint64, error) {
	cr, err := b.st.Load(ctx, r.id)
	if err != nil {
		return committedState{}, 0, b.failed(r, err)
	}
	cs, err := decodeCommitted(img, cr.State)
	if err != nil {
		return committedState{}, 0, b.failed(r, err)
	}
	return cs, cr.Revision, nil
}

// readWorking decodes one draft. A decoded state arrives built through
// `newState`'s shape (codec.go), so the reinterpret is the same no-op
// it is on the in-memory path for a matching image — and for a
// reloaded one, the same re-pointing §3.9 allows.
func (b *hostBackend) readWorking(r refuser, img *image,
	w WorkingRecord) (workingState, error) {
	s, err := decodeWorking(img, w.State)
	if err != nil {
		return workingState{}, b.failed(r, err)
	}
	s.base, s.rev = w.Base, w.Revision
	s.s.reinterpret(img)
	return s, nil
}

func (b *hostBackend) create(ctx context.Context, r refuser,
	d *Definition) (CaseID, error) {
	id, err := b.st.Create(ctx, CaseRecord{Definition: d.ID(),
		State: encodeCommitted(initialState(d.img))})
	if err != nil {
		return 0, b.failed(r, err)
	}
	return id, nil
}

func (b *hostBackend) load(ctx context.Context,
	r refuser) (storedCase, error) {
	cr, err := b.st.Load(ctx, r.id)
	if err != nil {
		return storedCase{}, b.failed(r, err)
	}
	img, err := b.imageOf(r, cr.Definition)
	if err != nil {
		return storedCase{}, err
	}
	cs, err := decodeCommitted(img, cr.State)
	if err != nil {
		return storedCase{}, b.failed(r, err)
	}
	// The record the operations read carries no drafts and no trail:
	// those are what `draft`, `history` and the commit transaction are
	// for, and a durable store should not pay for either to answer a
	// question about the head.
	return storedCase{def: cr.Definition, revision: cr.Revision,
		commit: cs}, nil
}

func (b *hostBackend) working(ctx context.Context, r refuser,
	user UserID, img *image) (workingState, error) {
	w, have, err := b.st.Draft(ctx, r.id, user)
	if err != nil {
		return workingState{}, b.failed(r, err)
	}
	if have {
		return b.readWorking(r, img, w)
	}
	cs, rev, err := b.head(ctx, r, img)
	if err != nil {
		return workingState{}, err
	}
	out := workingState{base: rev, s: cs.s.clone()}
	out.s.reinterpret(img)
	if err := b.st.Save(ctx, r.id, user, 0, WorkingRecord{
		Base:  rev,
		State: encodeWorking(out),
	}); err != nil {
		return workingState{}, b.saved(r, 0, err)
	}
	return out, nil
}

func (b *hostBackend) draft(ctx context.Context, r refuser,
	user UserID, img *image) (workingState, bool, error) {
	w, have, err := b.st.Draft(ctx, r.id, user)
	if err != nil {
		return workingState{}, false, b.failed(r, err)
	}
	if have {
		out, err := b.readWorking(r, img, w)
		return out, true, err
	}
	cs, rev, err := b.head(ctx, r, img)
	if err != nil {
		return workingState{}, false, err
	}
	out := workingState{base: rev, s: cs.s.clone()}
	out.s.reinterpret(img)
	return out, false, nil
}

func (b *hostBackend) viewed(ctx context.Context, r refuser,
	user UserID, img *image) (state, uint64, error) {
	w, _, err := b.draft(ctx, r, user, img)
	return w.s, w.rev, err
}

// saved translates a guarded write: the absent-copy sentence `saveWorking`
// refuses with, the same stale-copy sentence its guard does, and the
// store failure behind everything else.
func (b *hostBackend) saved(r refuser, expected uint64, err error) error {
	var stale *Stale
	switch {
	case errors.Is(err, ErrNoSuchCase):
		return r.now(NotFound, "no such case")
	case errors.Is(err, ErrNoDraft):
		return r.now(InvalidOp,
			"this actor has no working copy to write over")
	case errors.As(err, &stale):
		return r.staleCopy(stale.Have, expected)
	}
	return b.failed(r, err)
}

func (b *hostBackend) saveWorking(ctx context.Context, r refuser,
	user UserID, expected uint64, next workingState, onto uint64) (
	uint64, error) {
	// The read first, for both guards: the revision the caller
	// presented is checked against the copy the store holds, and the
	// lineage `Rebase` asks to move needs the copy's own base and the
	// case's revision to be bounded. The store's Save re-checks the
	// guard atomically; this one is what makes the refusal a sentence
	// instead of a race, and the defect guard (§6.5, and the same
	// SchemaMismatch the in-memory path gives) needs both numbers
	// before anything is encoded.
	w0, have, err := b.st.Draft(ctx, r.id, user)
	if err != nil {
		return 0, b.failed(r, err)
	}
	if !have {
		return 0, r.now(InvalidOp,
			"this actor has no working copy to write over")
	}
	if w0.Revision != expected {
		return 0, r.staleCopy(w0.Revision, expected)
	}
	base := w0.Base
	if onto != 0 {
		cr, err := b.st.Load(ctx, r.id)
		if err != nil {
			return 0, b.failed(r, err)
		}
		if onto < base || onto > cr.Revision {
			return 0, r.now(SchemaMismatch, "a rebase would move the copy's"+
				" lineage to revision "+dec(onto)+", which is not between "+
				dec(base)+" and the case's "+dec(cr.Revision))
		}
		base = onto
	}
	landed := expected + 1
	rec := workingState{base: base, rev: landed, s: next.s}
	if err := b.st.Save(ctx, r.id, user, expected, WorkingRecord{
		Base: base, Revision: landed, State: encodeWorking(rec),
	}); err != nil {
		return 0, b.saved(r, expected, err)
	}
	return landed, nil
}

func (b *hostBackend) discard(ctx context.Context, r refuser,
	user UserID, expected uint64) error {
	err := b.st.Discard(ctx, r.id, user, expected)
	if err == nil {
		return nil
	}
	var stale *Stale
	switch {
	case errors.Is(err, ErrNoSuchCase):
		return r.now(NotFound, "no such case")
	case errors.Is(err, ErrNoDraft):
		// §6.5's rule, stated here rather than by the host because it
		// is an engine rule: a caller who presented 0 asked for the
		// state they are already in, and one who presented a number
		// for a copy that does not exist is the caller bug the
		// in-memory store refuses in these words.
		if expected == 0 {
			return nil
		}
		return r.now(InvalidOp, "this actor has no working copy, so"+
			" revision "+dec(expected)+" was never a number they held")
	case errors.As(err, &stale):
		return r.staleCopy(stale.Have, expected)
	}
	return b.failed(r, err)
}

// commit is the whole of DESIGN §8's atomic step seen from the engine:
// the effects run on the caller's snapshot copy (the in-memory store's
// order — guard, install, effects — survives as guard-in-the-store,
// effects-before-the-encode, because a refused transaction shows its
// effects to nobody), the entry is built at the revision the swap
// installs, and the host answers with the receipt.
func (b *hostBackend) commit(ctx context.Context, r refuser, img *image,
	want uint64, next committedState, by committedBy) (committed, error) {
	head := next
	erased := applyCommitEffects(img, &head, by.changes, commitEffects)
	entry := commitRecord{revision: want + 1, at: b.stamp(), by: by.by,
		op: by.op, stage: next.stage, changes: by.changes}
	// Head restates the case's definition because CaseRecord is one
	// shape and a host that validates the pair on a swap should be
	// able to; the read that gets it costs a durable store one extra
	// fetch, and honesty about which schema the bytes were written
	// under is worth one fetch.
	cr, err := b.st.Load(ctx, r.id)
	if err != nil {
		return committed{}, b.failed(r, err)
	}
	tx := Transaction{Actor: by.by, Expected: want,
		Entry: encodeRecord(img, entry),
		Head: CaseRecord{ID: r.id, Definition: cr.Definition,
			Revision: want + 1, State: encodeCommitted(head)}}
	rc, err := b.st.Commit(ctx, r.id, tx)
	if err != nil {
		var stale *Stale
		switch {
		case errors.Is(err, ErrNoSuchCase):
			return committed{}, r.now(NotFound, "no such case")
		case errors.As(err, &stale):
			return committed{}, r.now(StaleRevision,
				"the case is at revision "+dec(stale.Have)+", not "+
					dec(want))
		}
		return committed{}, b.failed(r, err)
	}
	return committed{rec: entry, post: head, copy: rc.Draft,
		erased: erased}, nil
}

// history reads one page and decodes it. The committed record is read
// for its definition name alone: trail entries are written under the
// image the case lives on, and §3.9's compatibility is what makes their
// slot ordinals mean anything on the way back.
func (b *hostBackend) history(ctx context.Context, r refuser,
	cursor uint64, limit int) ([]commitRecord, error) {
	cr, err := b.st.Load(ctx, r.id)
	if err != nil {
		return nil, b.failed(r, err)
	}
	img, err := b.imageOf(r, cr.Definition)
	if err != nil {
		return nil, err
	}
	ents, err := b.st.History(ctx, r.id, cursor, limit)
	if err != nil {
		return nil, b.failed(r, err)
	}
	out := make([]commitRecord, 0, len(ents))
	for _, e := range ents {
		rec, err := decodeRecord(img, e)
		if err != nil {
			return nil, b.failed(r, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

var _ caseBackend = (*hostBackend)(nil)

// NewRuntimeWithStorage assembles a runtime whose case state lives
// behind the exported contract (DESIGN §8): every operation encodes the
// state it changes and reads back what a host stored, so the same
// program runs against the in-memory storage above, a database, or a
// service's own backend without the operations knowing. A nil argument
// is NewRuntime's default path — the codec-free one, which is what
// keeps this package's own cost measurements about the engine — and the
// registries and hooks are unchanged: definitions and bindings stay
// process-local no matter where the cases go.
func NewRuntimeWithStorage(s Storage) *Runtime {
	r := NewRuntime()
	if s != nil {
		r.w.cases = &hostBackend{st: s, defs: r.w.defs}
	}
	return r
}
