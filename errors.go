package monshi

// The runtime's construction-time error surface (RUNTIME §1, §10).
//
// Load refuses a definition for exactly one of two reasons, and the
// distinction is the whole point of the type. Problems are the front
// end's: the source says something the language forbids, the host can fix
// it, and the codes and positions are the ones SYNTAX §5 defines. Defects
// are the runtime's: the source compiled, but an invariant lowering
// assumes (§4) did not hold, so the fault is here, not in the definition.
//
// A defect reports the SCHEMA_MISMATCH category (DESIGN §9). It is never
// retryable, the front end is never re-run to recover, and half-lowered
// images do not exist: refusal is the only response.

import (
	"fmt"
	"strings"

	"github.com/monshihq/monshi/schema"
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
	Problems []schema.Problem

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
