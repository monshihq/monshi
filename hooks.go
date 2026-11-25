package monshi

import (
	"context"

	"github.com/monshihq/monshi/internal/engine"
)

// CaseContext exposes Case, Definition, Actor, Stage, Revision, Working and
// Roles, plus visibility-filtered Value, Review and Signature lookups.
// During Update it reads the operation's draft: later edits in the batch can
// change what a retained context sees. Use it synchronously in the callback;
// a retained context may be read again once the enclosing operation returns.
// OnCommit receives the pinned committed snapshot.
type CaseContext = engine.CaseContext

// ValidateFunc validates or normalizes a custom type's value.
type ValidateFunc func(ctx context.Context, at CaseContext, path string, v Value) (Value, error)

// OptionsFunc supplies the allowed choices for an options field.
type OptionsFunc func(ctx context.Context, at CaseContext, path string) ([]string, error)

// OnCommitFunc observes a successful commit and its pinned case snapshot.
type OnCommitFunc func(ctx context.Context, at CaseContext, com Commit)
