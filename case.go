package monshi

import (
	"context"

	"github.com/monshihq/monshi/internal/engine"
)

// Case is a handle for one case and actor. It does not cache case data.
type Case engine.Case

// Submit publishes one form from the actor’s working copy at the given working revision.
func (c Case) Submit(ctx context.Context, form string,
	working uint64) (CommitResult, error) {
	return engine.Case(c).Submit(ctx, form, working)
}

// Update applies an edit batch atomically to the actor’s working copy at revision base.
func (c Case) Update(ctx context.Context, base uint64, edits ...Edit) (
	ChangeResult, error) {
	return engine.Case(c).Update(ctx, base, edits...)
}

// Rebase merges changes since the draft’s base into the actor’s working copy.
func (c Case) Rebase(ctx context.Context) (RebaseResult, error) { return engine.Case(c).Rebase(ctx) }

// Discard drops the actor’s working copy if its revision matches expected.
func (c Case) Discard(ctx context.Context, expected uint64) error {
	return engine.Case(c).Discard(ctx, expected)
}

// Review records a decision about committed content at the observed revision.
func (c Case) Review(ctx context.Context, target string, act ReviewAction,
	observed uint64, comment string) (ReviewResult, error) {
	return engine.Case(c).Review(ctx, target, act, observed, comment)
}

// ID identifies this case.
func (c Case) ID() CaseID { return engine.Case(c).ID() }

// Actor returns the identity and primary role bound to the handle.
func (c Case) Actor() Actor { return engine.Case(c).Actor() }

// Definition returns the registered definition and whether it exists.
func (c Case) Definition() (*Definition, bool) {
	d, ok := engine.Case(c).Definition()
	return (*Definition)(d), ok
}

// Stage returns the current committed stage name.
func (c Case) Stage(ctx context.Context) (string, error) { return engine.Case(c).Stage(ctx) }

// Revision returns the case-wide committed revision.
func (c Case) Revision(ctx context.Context) (uint64, error) { return engine.Case(c).Revision(ctx) }

// Transition moves the case along a declared edge at the observed committed revision.
func (c Case) Transition(ctx context.Context, dest string,
	observed uint64) (TransitionResult, error) {
	return engine.Case(c).Transition(ctx, dest, observed)
}

// View reads a consistent, permission-filtered snapshot of the requested paths.
func (c Case) View(ctx context.Context, paths ...string) (ViewResult, error) {
	return engine.Case(c).View(ctx, paths...)
}

// History returns a page of committed changes visible to the actor.
func (c Case) History(ctx context.Context, cursor uint64,
	limit int) (HistoryResult, error) {
	return engine.Case(c).History(ctx, cursor, limit)
}

// Sign records an attestation at the observed committed revision.
func (c Case) Sign(ctx context.Context, target string, observed uint64,
	evidence string) (SignResult, error) {
	return engine.Case(c).Sign(ctx, target, observed, evidence)
}
