package engine

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

// Set assigns a field's value. It is the only verb that carries a value, and
// the value's class must be the field's: a `Text` into an `integer` field is
// refused as a structural error rather than coerced.
func Set(path string, v Value) Edit { return Edit{kind: editSet, path: path, v: v} }

// Empty clears a field's value and preserves the field, which is why it is
// a verb of its own rather than `Set(path, Text(""))`. The value is gone; the
// field stays part of the structure, so its `required` rule is still reached
// and still fails, whereas a node `present` prunes is never asked. And
// nothing re-materializes a default nobody re-applied: a cleared field stays
// cleared. What the two spellings differ about is a
// comparison — over an empty string it is answered, over no value it is not
// — so the choice between them is a host's, deliberately made.
func Empty(path string) Edit { return Edit{kind: editEmpty, path: path} }

// Add creates one empty instance of a `multiple` collection. So an instance is
// created here and filled in by a later `Update`, on the path this batch
// reports as created: the caller's own text plus the new ID. Two calls, and the
// first one is what makes the second addressable.
func Add(path string) Edit { return Edit{kind: editAdd, path: path} }

// Remove drops one instance of a `multiple` collection and everything nested
// inside it. The path names an instance, not a collection.
func Remove(path string) Edit { return Edit{kind: editRemove, path: path} }
