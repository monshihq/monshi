package monshi

import (
	"context"
	"io"

	"github.com/monshihq/monshi/internal/engine"
)

// CaseRecord is the durable committed head of a case. State is opaque to the host.
type CaseRecord = engine.CaseRecord

// WorkingRecord holds an actor's draft bytes and its working and base revisions.
type WorkingRecord = engine.WorkingRecord

// HistoryEntry is one opaque encoded commit record.
type HistoryEntry = engine.HistoryEntry

// Transaction atomically replaces the expected head, appends history and advances eligible draft lineage.
type Transaction = engine.Transaction

// Receipt reports the committed revision and the actor's working revision.
type Receipt = engine.Receipt

// Storage persists opaque case data. Each method is individually atomic and
// must honor context cancellation. An unknown case returns ErrNoSuchCase.
type Storage interface {
	// Create allocates a new ID and stores the supplied initial head.
	Create(ctx context.Context, rec CaseRecord) (CaseID, error)
	Load(ctx context.Context, id CaseID) (CaseRecord, error)

	// Draft reads without creating a working copy; false means none exists.
	Draft(ctx context.Context, id CaseID, user UserID) (WorkingRecord, bool, error)
	// Save creates a missing draft as given if expected is zero, otherwise
	// returning ErrNoDraft. For an existing draft the revision must match
	// expected; store the new content at expected+1 or return *Stale.
	Save(ctx context.Context, id CaseID, user UserID, expected uint64, w WorkingRecord) error
	// Discard removes the matching draft, returning ErrNoDraft or *Stale
	// if it is missing or has moved.
	Discard(ctx context.Context, id CaseID, user UserID, expected uint64) error

	// Commit returns *Stale unless the head revision equals tx.Expected.
	// Atomically replace the head, append the entry, and advance the base of
	// tx.Actor's draft to the new revision if it descended from tx.Expected.
	Commit(ctx context.Context, id CaseID, tx Transaction) (Receipt, error)
	// History reads oldest first, starting at cursor (zero means the
	// beginning). A zero limit means all remaining entries.
	History(ctx context.Context, id CaseID, cursor uint64, limit int) ([]HistoryEntry, error)
}

// ErrNoSuchCase is returned by Storage for an unknown case.
var ErrNoSuchCase = engine.ErrNoSuchCase

// ErrNoDraft is returned by Storage when a required working copy is absent.
var ErrNoDraft = engine.ErrNoDraft

// Stale reports the current revision when a storage write loses its revision check.
type Stale = engine.Stale

// BinaryStore is the host-side interface for retained binary content.
type BinaryStore interface {
	Put(ctx context.Context, r io.Reader) (BinaryRef, error)
	Get(ctx context.Context, ref BinaryRef) (io.ReadCloser, error)
}

// NewMemStorage creates an in-memory implementation of the byte storage contract.
func NewMemStorage() *MemStorage { return engine.NewMemStorage() }

// MemStorage is a concurrent in-memory Storage implementation.
type MemStorage = engine.MemStorage

// NewRuntimeWithStorage creates a runtime backed by s. A nil store selects the native in-memory backend.
func NewRuntimeWithStorage(s Storage) *Runtime { return (*Runtime)(engine.NewRuntimeWithStorage(s)) }
