package engine

import (
	"context"
	"errors"
	"strconv"
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

// Storage is the byte-oriented interface consumed by the adapter. The public
// contract, including revision and atomicity requirements, is monshi.Storage.
type Storage interface {
	Create(ctx context.Context, rec CaseRecord) (CaseID, error)
	Load(ctx context.Context, id CaseID) (CaseRecord, error)
	Draft(ctx context.Context, id CaseID, user UserID) (
		WorkingRecord, bool, error)
	Save(ctx context.Context, id CaseID, user UserID, expected uint64,
		w WorkingRecord) error
	Discard(ctx context.Context, id CaseID, user UserID,
		expected uint64) error
	Commit(ctx context.Context, id CaseID, tx Transaction) (
		Receipt, error)
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
