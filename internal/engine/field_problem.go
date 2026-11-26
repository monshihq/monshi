package engine

// FieldProblem describes a validation failure at a case path, alongside the
// value the actor needs to fix.
//
// Definition diagnostics use source positions. Field problems instead carry a
// case path that a host can pass to View or Update.
type FieldProblem struct {
	Path string
	Code string
	Msg  string
}

// invalidValue identifies a value rejected by a host validator. The hook's
// message supplies the reason.
const invalidValue = "INVALID_VALUE"

// String renders one field problem the way a log line wants it: where, then
// what. `Problem` does not do this — its `Error` is the front end's own
// sentence, which already names what to fix — but a path and a message arrive
// here as two fields, and a host that prints one string would otherwise drop
// the half that says which field it is about.
func (p FieldProblem) String() string {
	return p.Path + ": " + p.Msg
}
