package monshi

import (
	"fmt"
	"io"

	"github.com/monshihq/monshi/internal/engine"
	"github.com/monshihq/monshi/internal/program"
	"github.com/monshihq/monshi/schema"
)

// Definition is an immutable compiled definition, safe for concurrent use.
type Definition engine.Definition

// ID returns the identifier supplied to Load.
func (d *Definition) ID() string { return (*engine.Definition)(d).ID() }

// Warnings returns a copy of the non-fatal source diagnostics.
func (d *Definition) Warnings() []Problem { return (*engine.Definition)(d).Warnings() }

// Load parses and compiles source without registering it. Use errors.As to inspect an *Error.
func Load(id string, src io.Reader) (*Definition, error) {
	if id == "" {
		return nil, fmt.Errorf("monshi: Load needs a definition id to name it by")
	}
	s, err := schema.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("monshi: definition %q: %w", id, err)
	}
	if len(s.Errors) != 0 {
		return nil, &program.Error{ID: id, Problems: program.Diagnostics(s.Errors)}
	}
	img, err := program.Compile(id, s)
	if err != nil {
		return nil, err
	}
	return (*Definition)(engine.NewDefinition(img, program.Diagnostics(s.Warnings))), nil
}
