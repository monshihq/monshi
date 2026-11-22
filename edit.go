package monshi

// The four working edits (DESIGN §5.1), and the batch that applies them
// (DESIGN §6.2's `Update`).
//
// `Update` is the operation and these are its arguments, so the four verbs
// live at package level rather than as methods on a handle: a host that
// wanted four separate calls each with their own revision guard would have
// four transactions where §6.2 specifies one, and "changes such as
// province and city are evaluated together" would be a claim nobody could
// make. An `Edit` therefore carries no revision and changes nothing on its
// own — it is a description, applied or discarded as part of one batch.
//
// The names are the document's, spelled as §5.1 spells them, because that
// block is what a definition's author reads when they wonder why `Add`
// takes no value.

// Edit is one working edit: a verb, a path, and for `Set` a value.
type Edit struct {
	kind editKind
	path string
	v    Value
}

type editKind uint8

const (
	editSet editKind = iota + 1
	editEmpty
	editAdd
	editRemove
)

// Set assigns a field's value. It is the only verb that carries a value,
// and the value's class must be the field's: a `Text` into an `integer`
// field is refused as a structural error rather than coerced, which is what
// makes §6.2's "structural errors reject the whole update" the same rule as
// value.go's refusal to accept `any`.
func Set(path string, v Value) Edit { return Edit{kind: editSet, path: path, v: v} }

// Empty clears a field's value and preserves the field, which is why it is
// a verb of its own rather than `Set(path, Text(""))`. The value is gone; the
// field stays part of the structure, so its `required` rule is still reached
// and still fails, whereas a node `present` prunes is never asked (§5.2). And
// nothing re-materializes a default nobody re-applied: a cleared field stays
// cleared (invariant 8). What the two spellings differ about is a
// comparison — over an empty string it is answered, over no value it is not
// (SYNTAX E6) — so the choice between them is a host's, deliberately made.
func Empty(path string) Edit { return Edit{kind: editEmpty, path: path} }

// Add creates one empty instance of a `multiple` collection. It takes no
// value: §5.1 says so, and the reason it is not a stylistic choice is that
// the instance's ID is allocated by the engine (§3.1) — a host cannot name
// a row that does not exist yet, and the language invents no syntax for
// "the one I just added". So an instance is created here and filled in by a
// later `Update`, on the path this batch reports as created: the caller's
// own text plus the new ID. Two calls, and the first one is what makes the
// second addressable.
func Add(path string) Edit { return Edit{kind: editAdd, path: path} }

// Remove drops one instance of a `multiple` collection and everything
// nested inside it. The path names an instance, not a collection: §5.1 says
// `Remove` is not for deleting fixed schema nodes, and a declaration is
// structure that the definition owns, not content this case owns.
func Remove(path string) Edit { return Edit{kind: editRemove, path: path} }
