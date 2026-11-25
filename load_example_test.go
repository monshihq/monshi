package monshi_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

// Appendix A's gate, clause (a) and (b), reached through the exported
// seam rather than around it: Load is what a host actually calls, and §1
// promises source in and definition out. The two routes must agree.
func TestSpecExampleLoads(t *testing.T) {
	d, err := monshi.Load("example", strings.NewReader(specExample(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.ID() != "example" {
		t.Errorf("ID = %q, want example", d.ID())
	}
	if w := d.Warnings(); len(w) != 0 {
		t.Fatalf("the §7 example must load without warnings, got %v", w)
	}
	rt := monshi.NewRuntime()
	if err := rt.Register(d); err != nil {
		t.Fatal(err)
	}
	c, err := rt.Create(context.Background(), d.ID(), monshi.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Definition(); got != d {
		t.Fatal("case did not resolve its registered definition")
	}
}

func specExample(tb testing.TB) string {
	tb.Helper()
	const marker = "## 7. Complete example"
	data, err := os.ReadFile("docs/SYNTAX.md")
	if err != nil {
		tb.Fatal(err)
	}
	text := string(data)
	i := strings.Index(text, marker)
	if i < 0 {
		tb.Fatalf("marker %q not found in SYNTAX.md", marker)
	}
	rest := text[i:]
	start := strings.Index(rest, "```")
	if start < 0 {
		tb.Fatal("no fenced example under §7")
	}
	rest = rest[start+3:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		tb.Fatal("unterminated fence under §7")
	}
	return strings.TrimPrefix(rest[:end+1], "\n")
}
