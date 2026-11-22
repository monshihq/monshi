// Package schema_test exercises the public API of the schema package:
// Parse, NewParser, the compiled Schema model and the Problem
// diagnostics. Everything here goes through the documented entry points
// of docs/SYNTAX.md; no internal identifier is reachable.
package schema_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/monshihq/monshi/schema"
)

// minimalSrc is the smallest definition that compiles with no
// diagnostics at all (SYNTAX.md §3.1).
const minimalSrc = `forms { f { a : string; } } stages { d { } }`

// --- helpers shared by every file in this package ---

// mustParse compiles src and fails the test on a Go error. Per the API
// contract, a Go error means the parse could not run at all (I/O
// failure); every lexical, syntactic and semantic problem is reported
// inside the Schema instead.
func mustParse(t *testing.T, src string) *schema.Schema {
	t.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse returned Go error %v for source %q", abbrev(src), err)
	}
	if s == nil {
		t.Fatalf("Parse returned nil schema with nil error for source %q", abbrev(src))
	}
	return s
}

func codes(ps []schema.Problem) []schema.Code {
	out := make([]schema.Code, len(ps))
	for i, p := range ps {
		out[i] = p.Code
	}
	return out
}

func hasCode(list []schema.Code, want schema.Code) bool {
	for _, c := range list {
		if c == want {
			return true
		}
	}
	return false
}

// render formats diagnostics for failure messages.
func render(ps []schema.Problem) string {
	if len(ps) == 0 {
		return "(none)"
	}
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Error()
	}
	return strings.Join(parts, "; ")
}

// abbrev keeps failure messages readable for multi-line sources.
func abbrev(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// --- entry-point contract ---

func TestParseAgreesWithNewParser(t *testing.T) {
	const src = `types { t : string; }
roles { R { allow view f; } }
forms { f { a : t; b : nope; } }
stages { d { } }`

	viaFunc, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	viaParser, err := schema.NewParser(strings.NewReader(src)).Parse()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := render(viaParser.Errors), render(viaFunc.Errors); got != want {
		t.Errorf("Errors differ:\n Parser: %s\n  Parse: %s", got, want)
	}
	if got, want := render(viaParser.Warnings), render(viaFunc.Warnings); got != want {
		t.Errorf("Warnings differ:\n Parser: %s\n  Parse: %s", got, want)
	}
	if len(viaFunc.Errors) == 0 {
		t.Fatal("fixture expected at least one diagnostic")
	}
	if len(viaParser.Forms) != len(viaFunc.Forms) ||
		len(viaParser.Stages) != len(viaFunc.Stages) ||
		len(viaParser.Types) != len(viaFunc.Types) ||
		len(viaParser.Roles) != len(viaFunc.Roles) {
		t.Error("model shape differs between entry points")
	}
}

type errReader struct {
	head string // bytes served before the failure
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.head != "" {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	return 0, r.err
}

func TestIOFailureSurfacesAsGoError(t *testing.T) {
	sentinel := errors.New("disk on fire")
	for _, tc := range []struct {
		name string
		head string
	}{
		{"immediate", ""},
		{"mid-stream", "forms { f { a : string; } } stages { d "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := schema.Parse(&errReader{head: tc.head, err: sentinel})
			if !errors.Is(err, sentinel) {
				t.Errorf("err = %v, want wrapped %v", err, sentinel)
			}
			if s != nil {
				t.Errorf("schema = %+v, want nil on I/O failure", s)
			}
		})
	}
}

func TestDiagnosticsAreNeverGoErrors(t *testing.T) {
	broken := []struct{ name, src string }{
		{"stray-punctuation", `}{}{`},
		{"unclosed-block", `forms {`},
		{"unterminated-string", `forms { f { a : string default "oops } }`},
		{"bad-characters", `???`},
		{"bad-char-midstream", `forms { f { a : string @ ; } } stages { d { } }`},
		{"garbage-words", `nothing at all here`},
	}
	for _, tc := range broken {
		t.Run(tc.name, func(t *testing.T) {
			s, err := schema.Parse(strings.NewReader(tc.src))
			if err != nil {
				t.Fatalf("readable source must not produce a Go error, got %v", err)
			}
			if s == nil {
				t.Fatal("nil schema without Go error")
			}
			if len(s.Errors) == 0 {
				t.Error("expected diagnostics for broken source, got none")
			}
		})
	}
}

func TestEmptySourceReportsMissingBlocks(t *testing.T) {
	s := mustParse(t, "")
	if len(s.Errors) != 2 {
		t.Fatalf("errors = %s, want exactly two MISSING_BLOCK", render(s.Errors))
	}
	for _, p := range s.Errors {
		if p.Code != schema.CodeMissingBlock {
			t.Errorf("unexpected code %s in %v", p.Code, p)
		}
	}
	if s.Initial != nil {
		t.Error("Initial must be nil without stages")
	}
	if len(s.Forms)+len(s.Stages)+len(s.Types)+len(s.Roles)+len(s.Warnings) != 0 {
		t.Error("empty source must compile to an otherwise empty schema")
	}
}

func TestCommentOnlySource(t *testing.T) {
	s := mustParse(t, "// nothing here\n/* nor here\n */\n")
	if got := codes(s.Errors); !hasCode(got, schema.CodeMissingBlock) || len(s.Errors) != 2 {
		t.Errorf("errors = %s, want exactly two MISSING_BLOCK", render(s.Errors))
	}
}

func TestMinimalDefinitionIsClean(t *testing.T) {
	s := mustParse(t, minimalSrc)
	if len(s.Errors) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("diagnostics = %s / %s, want none", render(s.Errors), render(s.Warnings))
	}
	if len(s.Forms) != 1 || len(s.Stages) != 1 {
		t.Fatalf("forms/stages = %d/%d, want 1/1", len(s.Forms), len(s.Stages))
	}
	if s.Initial != s.Stages[0] {
		t.Error("Initial must be the first declared stage (SYNTAX.md §5.6-ST1)")
	}
}
