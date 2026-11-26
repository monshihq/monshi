// Package testfixture supplies language sources shared by tests and benchmarks.
package testfixture

import (
	"github.com/monshihq/monshi/schema"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Example reads the complete language example from the repository documentation.
func Example(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(dir, "docs/SYNTAX.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(b), "## 7. Complete example")
	_, example, open := strings.Cut(section, "```\n")
	src, _, close := strings.Cut(example, "\n```")
	if !ok || !open || !close {
		t.Fatal("missing fenced example in docs/SYNTAX.md")
	}
	return src + "\n"
}

// wideSrc is the generated corpus: one form, 40 fields, 6 roles, 5 stages, and
// one collection whose instances a request would carry. The roles carry `view,
// edit all` rather than view alone so a benchmark on this corpus can hold
// content.
func Wide(tb testing.TB) string {
	tb.Helper()
	var w strings.Builder
	w.WriteString("roles {\n")
	for r := 0; r < 6; r++ {
		name := "R" + strconv.Itoa(r)
		w.WriteString("    " + name + " { allow view, edit all; }\n")
	}
	w.WriteString("}\n\nforms {\n    f {\n")
	for i := 0; i < 40; i++ {
		n := strconv.Itoa(i)
		switch i % 4 {
		case 0:
			w.WriteString("        i" + n + " : integer;\n")
		case 1:
			w.WriteString("        s" + n + ` : string;` + "\n")
		case 2:
			w.WriteString("        d" + n +
				" : decimal(12,2) default 1.00;\n")
		default:
			w.WriteString("        b" + n + " : boolean;\n")
		}
	}
	w.WriteString("        rows multiple {\n")
	for i := 0; i < 8; i++ {
		w.WriteString("            c" + strconv.Itoa(i) + " : integer;\n")
	}
	w.WriteString("        }\n    }\n}\n\nstages {\n")
	for s := 0; s < 5; s++ {
		name := "s" + strconv.Itoa(s)
		w.WriteString("    " + name + " {\n")
		w.WriteString("        allow R" + strconv.Itoa(s) + " submit f;\n")
		if s < 4 {
			w.WriteString("        accept s" + strconv.Itoa(s+1) + ";\n")
		}
		w.WriteString("    }\n")
	}
	w.WriteString("}\n")
	return w.String()
}

// Parse checks a benchmark definition before timing compilation so the
// measurement excludes lexing and parsing.
func Parse(tb testing.TB, src string) *schema.Schema {
	tb.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		tb.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		tb.Fatalf("definition does not compile: %v", s.Errors)
	}
	return s
}
