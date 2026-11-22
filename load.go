package monshi

// The host-facing seam (RUNTIME §1). This is the only exported entry the
// runtime offers a definition author today: source in, an immutable
// definition handle out. Everything below it — the image, the lowering
// passes, the condition closures — stays unexported, because a host has
// no reason to touch it and every reason not to (§9's last non-goal).

import (
	"fmt"
	"io"

	"github.com/monshihq/monshi/schema"
)

// Definition is a compiled, immutable case definition: the runtime image
// lowering built from source, plus the front end's non-fatal diagnostics.
//
// A Definition is safe for concurrent use by all goroutines and actors:
// nothing below it is mutated after Load returns (§7.4), and case data
// never lives here. Hosts keep definitions in their own registry keyed by
// ID; Create selects one by that ID when the case operations land
// (DESIGN §3).
type Definition struct {
	img      *image
	warnings []schema.Problem
}

// ID returns the definition identifier the host supplied. It is what
// Create will select this definition by, and what problems about it name.
func (d *Definition) ID() string { return d.img.id }

// Warnings returns the front end's non-fatal diagnostics for this source
// (SYNTAX §5.8): redundancies and dead declarations the definition is
// allowed to have. They do not affect behavior — §4's table says what
// each one becomes in the image — but they are what an author of this
// source wants to see, so they are kept. The result is a copy.
func (d *Definition) Warnings() []schema.Problem {
	return append([]schema.Problem(nil), d.warnings...)
}

// Load compiles definition source and lowers it into a Definition
// (DESIGN §3, RUNTIME §1). Core reads its source through the reader it is
// given and needs no filesystem: the host decides where a definition came
// from and what ID names it.
//
// Load runs the front end once. If the source does not compile, the
// returned error is an *Error carrying Schema.Errors and nothing is
// lowered. Otherwise the definition is lowered, and a §4 invariant that
// failed to hold comes back as an *Error with Mismatch true.
//
// Load is not a hot-reload mechanism: calling it again with the same ID
// produces an independent definition, and replacing a live one is the
// host's explicit decision (§9).
func Load(id string, src io.Reader) (*Definition, error) {
	if id == "" {
		return nil, fmt.Errorf("monshi: Load needs a definition id to name it by")
	}
	s, err := schema.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("monshi: definition %q: %w", id, err)
	}
	if len(s.Errors) != 0 {
		return nil, &Error{ID: id, Problems: s.Errors}
	}
	img, err := lower(id, s)
	if err != nil {
		return nil, err
	}
	return &Definition{img: img, warnings: s.Warnings}, nil
}
