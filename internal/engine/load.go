package engine

import "github.com/monshihq/monshi/internal/program"

// Definition pairs an immutable program with its source warnings.
type Definition struct {
	img      *program.Program
	warnings []program.Problem
}

// ID returns the definition identifier the host supplied. It is what
// Create will select this definition by, and what problems about it name.
func (d *Definition) ID() string { return d.img.ID() }

// Warnings returns the front end's non-fatal diagnostics for this source
// (SYNTAX §5.8): redundancies and dead declarations the definition is
// allowed to have. They do not affect behavior — §4's table says what
// each one becomes in the image — but they are what an author of this
// source wants to see, so they are kept. The result is a copy.
func (d *Definition) Warnings() []program.Problem {
	return append([]program.Problem(nil), d.warnings...)
}

func NewDefinition(img *program.Program, warnings []program.Problem) *Definition {
	return &Definition{img: img, warnings: warnings}
}
