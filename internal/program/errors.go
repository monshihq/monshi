package program

import (
	"fmt"
	"strings"
)

// Error is a definition that Load refused. Hosts read it with errors.As:
// exactly one of the two lists is non-empty, and that alone says which
// side of the seam the fault is on.
type Error struct {
	// ID is the definition identifier the host passed to Load.
	ID string

	// Problems are the front end's diagnostics, in the order the checker
	// reported them. Non-empty exactly when the source did not compile;
	// a definition with problems never reaches lowering.
	Problems []Problem

	// Defects lists the §4 invariants lowering found violated. Non-empty
	// exactly when the front end accepted a definition the runtime could
	// not compile. Each entry names the slot or declaration at fault.
	Defects []string
}

// Mismatch reports the internal-defect case: the definition compiled and
// still did not become an image. Hosts log this as a bug in Monshi rather
// than showing it to a definition author (DESIGN §9).
func (e *Error) Mismatch() bool { return len(e.Defects) != 0 }

func (e *Error) Error() string {
	var b strings.Builder
	if e.Mismatch() {
		fmt.Fprintf(&b, "SCHEMA_MISMATCH: definition %q did not lower: %d defect(s)",
			e.ID, len(e.Defects))
		for _, d := range e.Defects {
			fmt.Fprintf(&b, "\n  %s", d)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "definition %q does not compile: %d problem(s)",
		e.ID, len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %s", p)
	}
	return b.String()
}
