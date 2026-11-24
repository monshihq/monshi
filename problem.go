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

// FieldProblem is one validation error about a value in a case: the path
// that holds it, a code, and the message — DESIGN §9's field-level triple,
// and the shape §6.1's view reports beside the values themselves.
//
// It is deliberately not `Problem`. That type is about definition source an
// author can still fix, so it carries a line, a column and a SYNTAX §5 rule;
// this is about one value in one case, so it carries a path and a code from a
// different table. The two axes stay apart for the reason caseerror.go keeps
// refusals apart from codes: a host shows one list to whoever is filling in a
// form and the other to whoever wrote the definition, and merging them means
// parsing text to find out which reader a message is for.
//
// `Code` is a plain string rather than a `Code` value for the same reason.
// SYNTAX §5's rule IDs are frozen and about source; the codes here are the
// engine's, additive as §9 says, and today there is one: `invalidValue`.
type FieldProblem struct {
	Path string
	Code string
	Msg  string
}

// invalidValue is the code a value the host's `Validate` hook refused carries
// (DESIGN §9's field-level list). It says "this value is not acceptable here"
// and nothing more, because *why* is the hook's sentence rather than the
// engine's, and a code invented here would be the engine guessing at what a
// host meant. A machine-readable reason needs a hook result that *returns* a
// code instead of failing with a Go error, which is §7's tri-state question
// and stays open: `CaseContext` gave a host the case and the address, and not
// a vocabulary. The view is the first caller to report these problems, and it
// reports the one code the engine honestly owns.
const invalidValue = "INVALID_VALUE"

// String renders one field problem the way a log line wants it: where, then
// what. `Problem` does not do this — its `Error` is the front end's own
// sentence, which already names what to fix — but a path and a message arrive
// here as two fields, and a host that prints one string would otherwise drop
// the half that says which field it is about.
func (p FieldProblem) String() string {
	return p.Path + ": " + p.Msg
}

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
