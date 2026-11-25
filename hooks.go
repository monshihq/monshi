package monshi

import (
	"context"

	"github.com/monshihq/monshi/internal/engine"
)

// CaseContext is the read-only case snapshot supplied to hooks.
type CaseContext = engine.CaseContext

// ValidateFunc validates or normalizes a custom type's value.
type ValidateFunc func(ctx context.Context, at CaseContext, path string, v Value) (Value, error)

// OptionsFunc supplies the allowed choices for an options field.
type OptionsFunc func(ctx context.Context, at CaseContext, path string) ([]string, error)

// OnCommitFunc observes a successful commit and its pinned case snapshot.
type OnCommitFunc func(ctx context.Context, at CaseContext, com Commit)
