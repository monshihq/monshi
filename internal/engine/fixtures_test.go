package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/monshihq/monshi/internal/testfixture"
	"github.com/monshihq/monshi/schema"
)

var registrationExample = testfixture.Example

// compile checks source through the real front end and lowers it. Errors
// fail the test; warnings are returned for the caller to judge.
func compile(t *testing.T, src string) (*program.Program, []schema.Problem) {
	t.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		t.Fatalf("definition does not compile: %s", join(diagnostics(s.Errors)))
	}
	img, err := program.Compile("test", s)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	return img, s.Warnings
}

func lowerExample(t *testing.T) *program.Program {
	t.Helper()
	img, warnings := compile(t, registrationExample(t))
	if len(warnings) != 0 {
		t.Fatalf("the registration example must compile clean, got %s", join(diagnostics(warnings)))
	}
	return img
}

func diagnostics(ps []schema.Problem) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Error())
	}
	return out
}

func join(list []string) string { return strings.Join(list, "; ") }

// slotByPath finds a declaration by its absolute path. Tests use the
// readable path even though the image's own logic is index-only, which is
// what makes the assertion worth making: it proves the path was interned
// and attached to exactly one slot.
func slotByPath(t *testing.T, img *program.Program, path string) program.NodeID {
	t.Helper()
	var found []program.NodeID
	for i := range img.SlotCount() {
		s := img.Slot(program.NodeID(i))
		if s.Path != 0 && img.Name(s.Path) == path {
			found = append(found, program.NodeID(i))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%q matches %d slots, want exactly 1", path, len(found))
	}
	return found[0]
}

// nestedSrc is the shape the registration example cannot express: a repeated
// field, and a collection inside a collection.
const nestedSrc = `
roles {
    A { allow view, edit all; }
}
forms {
    f {
        tags : string multiple;
        rows multiple {
            n : integer default 7;
            meta {
                m : integer;
            }
            cells multiple {
                v : integer;
            }
        }
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`

func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
