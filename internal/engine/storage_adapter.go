package engine

import (
	"context"
	"errors"
	"time"

	"github.com/monshihq/monshi/internal/program"
)

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
// Anything else a host returns becomes STORAGE_FAILURE with the host's error as
// Cause.
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
	if errors.Is(err, errLayout) {
		return r.caused(SchemaMismatch, err.Error(), err)
	}
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
func (b *hostBackend) imageOf(r refuser, def string) (*program.Program, error) {
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
	img *program.Program) (committedState, uint64, error) {
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
// `newState`'s shape (codec.go).
func (b *hostBackend) readWorking(r refuser, img *program.Program,
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
	r refuser) (caseHead, error) {
	cr, err := b.st.Load(ctx, r.id)
	if err != nil {
		return caseHead{}, b.failed(r, err)
	}
	img, err := b.imageOf(r, cr.Definition)
	if err != nil {
		return caseHead{}, err
	}
	cs, err := decodeCommitted(img, cr.State)
	if err != nil {
		return caseHead{}, b.failed(r, err)
	}
	// The record the operations read carries no drafts and no trail:
	// those are what `draft`, `history` and the commit transaction are
	// for, and a durable store should not pay for either to answer a
	// question about the head.
	return caseHead{def: cr.Definition, revision: cr.Revision,
		commit: cs}, nil
}

func (b *hostBackend) working(ctx context.Context, r refuser,
	user UserID, img *program.Program) (workingState, error) {
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
	w, err = b.st.InitDraft(ctx, r.id, user, WorkingRecord{
		Base:  rev,
		State: encodeWorking(out),
	})
	if err != nil {
		var stale *Stale
		if errors.As(err, &stale) {
			return workingState{}, r.now(StaleRevision, "the case advanced during draft initialization")
		}
		return workingState{}, b.failed(r, err)
	}
	return b.readWorking(r, img, w)
}

func (b *hostBackend) draft(ctx context.Context, r refuser,
	user UserID, img *program.Program) (workingState, bool, error) {
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
	user UserID, img *program.Program) (state, uint64, error) {
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
	// The read first, for both guards: the revision the caller presented is
	// checked against the copy the store holds, and the lineage `Rebase` asks
	// to move needs the copy's own base and the case's revision to be bounded.
	// The store's Save re-checks the guard atomically; this one is what makes
	// the refusal a sentence instead of a race, and the defect guard needs both
	// numbers before anything is encoded.
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
		// Discarding revision zero of a missing draft is already a success. A
		// positive revision names a copy the caller could not have held.
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

// commit is the whole of the atomic step seen from the engine:
// the effects run on the caller's snapshot copy (the in-memory store's
// order — guard, install, effects — survives as guard-in-the-store,
// effects-before-the-encode, because a refused transaction shows its
// effects to nobody), the entry is built at the revision the swap
// installs, and the host answers with the receipt.
func (b *hostBackend) commit(ctx context.Context, r refuser, img *program.Program,
	want uint64, next committedState, by committedBy) (committed, error) {
	head := next
	entry, erased := prepareCommit(img, &head, want+1, b.stamp(), by)
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
// image the case lives on, and the compatibility is what makes their
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
// behind the exported contract: every operation encodes the
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
