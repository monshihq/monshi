package engine

import (
	"context"

	"github.com/monshihq/monshi/internal/program"
)

// caseBackend serves typed snapshots to the engine. Reads return stable committed
// state or private draft copies. Writes check revisions atomically; errors name
// the calling operation via its refuser.
type caseBackend interface {
	create(ctx context.Context, refuse refuser, d *Definition) (CaseID, error)
	load(ctx context.Context, refuse refuser) (caseHead, error)
	working(ctx context.Context, refuse refuser, user UserID,
		img *program.Program) (workingState, error)
	draft(ctx context.Context, refuse refuser, user UserID,
		img *program.Program) (workingState, bool, error)
	viewed(ctx context.Context, refuse refuser, user UserID,
		img *program.Program) (state, uint64, error)
	saveWorking(ctx context.Context, refuse refuser, user UserID,
		expected uint64, next workingState, onto uint64) (uint64, error)
	discard(ctx context.Context, refuse refuser, user UserID,
		expected uint64) error
	commit(ctx context.Context, refuse refuser, img *program.Program, want uint64,
		next committedState, by committedBy) (committed, error)
	history(ctx context.Context, refuse refuser, cursor uint64,
		limit int) ([]commitRecord, error)
}
