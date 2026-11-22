package schema_test

import (
	"testing"

	"github.com/monshihq/monshi/schema"
)

// The lexer's public contract is observable only through diagnostics
// and decoded literal values, which is exactly what these tests check.

func TestStringEscapesDecode(t *testing.T) {
	const src = `forms { f { a : string default "q\"w\nx\\y\tz\rp"; } } stages { d { } }`
	s := mustParse(t, src)
	if len(s.Errors) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("diagnostics = %s / %s, want none", render(s.Errors), render(s.Warnings))
	}
	got := s.Forms[0].Nodes[0].Default
	if got == nil || got.Kind != "string" {
		t.Fatalf("default = %+v, want a string value", got)
	}
	want := "q\"w\nx\\y\tz\rp"
	if got.Str != want {
		t.Errorf("decoded string = %q, want %q", got.Str, want)
	}
}

func TestInvalidEscapeKeepsCharacterAndContinues(t *testing.T) {
	const src = `forms { f { a : string default "x\qy"; } } stages { d { } }`
	s := mustParse(t, src)
	if !hasCode(codes(s.Errors), schema.CodeInvalidEscape) {
		t.Fatalf("want INVALID_ESCAPE, got [%s]", render(s.Errors))
	}
	if len(s.Forms) != 1 || len(s.Forms[0].Nodes) != 1 {
		t.Fatalf("definition lost to a recoverable escape error: forms=%d", len(s.Forms))
	}
	if got := s.Forms[0].Nodes[0].Default; got == nil || got.Str != "xqy" {
		t.Errorf("default = %+v, want string %q", got, "xqy")
	}
}

func TestUnterminatedStringStopsLexingWithoutPanic(t *testing.T) {
	// The string swallows the rest of the source; the compile must still
	// finish and report the required-block failure.
	s := mustParse(t, `forms { f { a : string default "oops } }`)
	ec := codes(s.Errors)
	if !hasCode(ec, schema.CodeUnterminatedString) || !hasCode(ec, schema.CodeMissingBlock) {
		t.Fatalf("diagnostics = [%s], want UNTERMINATED_STRING and MISSING_BLOCK", render(s.Errors))
	}
}

func TestUnterminatedCommentKeepsCompletedDefinition(t *testing.T) {
	s := mustParse(t, `forms { f { b : string; } } stages { d { } } /* dangling`)
	if len(s.Errors) != 1 || s.Errors[0].Code != schema.CodeUnterminatedComment {
		t.Fatalf("errors = [%s], want exactly one UNTERMINATED_COMMENT", render(s.Errors))
	}
	if len(s.Forms) != 1 || len(s.Stages) != 1 {
		t.Errorf("blocks lost to trailing comment: forms=%d stages=%d", len(s.Forms), len(s.Stages))
	}
}

func TestCommentsInvisibleToParser(t *testing.T) {
	const src = `// a { comment " with punctuation
forms { // another } tricky one
    f { a : string; } /* block
    comment with } and ; and " */
}
stages { d { } }`
	s := mustParse(t, src)
	if len(s.Errors) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("diagnostics = %s / %s, want none", render(s.Errors), render(s.Warnings))
	}
	if len(s.Forms) != 1 || len(s.Forms[0].Nodes) != 1 {
		t.Errorf("model = %d forms / %d members, want 1/1", len(s.Forms), len(s.Forms[0].Nodes))
	}
}

func TestNegativeAndDecimalNumbersAreSingleTokens(t *testing.T) {
	const src = `forms { f { n : integer default -7; d : decimal(4, 2) default -12.50; } } stages { d { } }`
	s := mustParse(t, src)
	if len(s.Errors) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("diagnostics = %s / %s, want none", render(s.Errors), render(s.Warnings))
	}
	nodes := s.Forms[0].Nodes
	if nodes[0].Default == nil || nodes[0].Default.Int != -7 {
		t.Errorf("integer default = %+v, want -7", nodes[0].Default)
	}
	if nodes[1].Default == nil || nodes[1].Default.Dec.StringFixed(2) != "-12.50" {
		t.Errorf("decimal default = %+v, want -12.50", nodes[1].Default)
	}
}

func TestDotsSeparatePathSegmentsNotNumbers(t *testing.T) {
	// `1.20` lexes as one number, while `f.a` stays three tokens: the
	// path below must resolve against the declared member.
	const src = `roles { R { allow view f.a; } }
forms { f { a : decimal(4, 2) default 1.20; } }
stages { d { } }`
	s := mustParse(t, src)
	for _, p := range s.Errors {
		if p.Code == schema.CodeUnknownTarget {
			t.Errorf("grant path f.a failed to resolve: %v", p)
		}
	}
	if hasCode(codes(s.Errors), schema.CodeUnexpectedToken) {
		t.Errorf("tokenization split a path or number: [%s]", render(s.Errors))
	}
	if s.Forms[0].Nodes[0].Default == nil || s.Forms[0].Nodes[0].Default.Dec.StringFixed(2) != "1.20" {
		t.Errorf("decimal default wrong: %+v", s.Forms[0].Nodes[0].Default)
	}
}
