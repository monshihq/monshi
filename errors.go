package monshi

import (
	"github.com/monshihq/monshi/internal/engine"
	"github.com/monshihq/monshi/internal/program"
)

// Error describes source diagnostics or compiler defects that prevented loading a definition.
type Error = program.Error

// Category identifies the reason a case operation was refused.
type Category = engine.Category

const (
	NotFound       = engine.NotFound
	AccessDenied   = engine.AccessDenied
	InvalidOp      = engine.InvalidOp
	SchemaMismatch = engine.SchemaMismatch
	StaleRevision  = engine.StaleRevision
	Conflict       = engine.Conflict
	HookFailure    = engine.HookFailure
	StorageFailure = engine.StorageFailure
)

// CaseError describes a refused operation, its case, and the underlying cause, if any.
type CaseError = engine.CaseError

// Failed reports whether err is a refusal with the given category.
func Failed(err error, want Category) bool { return engine.Failed(err, want) }

// Code identifies a definition diagnostic.
type Code = program.Code

// Problem reports a diagnostic in definition source, including its position.
type Problem = program.Problem

// FieldProblem describes a validation problem at a case field path.
type FieldProblem = engine.FieldProblem
