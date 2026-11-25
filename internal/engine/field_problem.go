package engine

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
