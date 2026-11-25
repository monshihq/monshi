package engine

import (
	"context"
	"sync"
)

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
