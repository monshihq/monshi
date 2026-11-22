package monshi

// The diagnostic surface a host sees (RUNTIME §10).
//
// A host reads problems about definitions without importing the front end:
// SYNTAX §5's codes and the front end's wording are the language's, not a
// package's. So the boundary mirrors the checker's Problem, carries the
// rendering the checker produced, and converts by field — the mapping is
// total by construction, which means a code added to the front end cannot
// be forgotten here. There is no list to keep in step.

import "github.com/monshihq/monshi/schema"

// Code names one rule violation, exactly as SYNTAX §5 spells it
// (`"UNKNOWN_TYPE"`, `"DUPLICATE_BLOCK"`). Hosts branch on it by string:
// the values are the document's, and they are stable across releases the
// way rule IDs are.
type Code string

// Problem is one diagnostic about definition source. The fields say where
// and why in machine terms; Msg is the front end's own sentence, already
// rendered as "line:col: text", so a host can show it without knowing the
// code table. Line and Col are 1-based, and a position of 0 means the
// problem is about the definition as a whole.
type Problem struct {
	Line    int
	Col     int
	Code    Code
	Context string // the name or fragment the checker singled out
	Msg     string
}

// Error returns the front end's rendering, which is what a log line wants.
func (p Problem) Error() string { return p.Msg }

// problems converts the checker's diagnostics. Order is preserved: the
// front end reports a definition's problems in source order, and an author
// reading them top to bottom should find the first mistake first.
func problems(ps []schema.Problem) []Problem {
	if len(ps) == 0 {
		return nil
	}
	out := make([]Problem, len(ps))
	for i, p := range ps {
		out[i] = Problem{
			Line:    p.Line,
			Col:     p.Col,
			Code:    Code(p.Code),
			Context: p.Context,
			Msg:     p.Error(),
		}
	}
	return out
}
