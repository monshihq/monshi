package engine

import (
	"errors"
	"strconv"
	"strings"
)

// The refusal surface for case operations.
//
// This is a different axis from `Code` and `Problem`, and the two stay apart on
// purpose. A Code is about *source* an author can still fix. A Category is
// about a *request* the engine would not carry out: nothing in the definition
// is wrong, and the host's own state — who asked, which revision they observed,
// whether the store answered — is. Keeping the lists separate is what lets a
// host show one to an author and log the other as an operational event without
// parsing a message to tell them apart.
//
// Hosts can branch on these stable categories without parsing the detail text.

// Category is one of the error categories.
type Category string

// The categories.
const (
	NotFound       Category = "NOT_FOUND"
	AccessDenied   Category = "ACCESS_DENIED"
	InvalidOp      Category = "INVALID_OPERATION"
	SchemaMismatch Category = "SCHEMA_MISMATCH"
	StaleRevision  Category = "STALE_REVISION"
	Conflict       Category = "CONFLICT"
	HookFailure    Category = "HOOK_FAILURE"
	StorageFailure Category = "STORAGE_FAILURE"
)

// CaseError is a refused operation. It is structured.
type CaseError struct {
	// Op is the operation that refused, in the words the exported API uses
	// for it: "create", "open", "stage", "revision", "register", "replace",
	// and the edit and commit operations that follow. It is the host's own
	// word, never an internal helper's, because a host reads this field to
	// find the call in its code.
	Op string

	// Reason is one of the categories.
	Reason Category

	// Case is the case the refusal is about; 0 when it is not about a
	// case at all, which is what a registry refusal looks like.
	Case CaseID

	// Detail says what about this case, in a sentence a log line can
	// carry. It never repeats the category.
	Detail string

	// Cause is a backend's own error, kept so errors.Is still reaches its
	// sentinel. It is nil for every refusal this package produced itself.
	Cause error
}

func (e *CaseError) Error() string {
	var b strings.Builder
	b.WriteString("monshi: ")
	b.WriteString(string(e.Reason))
	b.WriteString(": ")
	b.WriteString(e.Op)
	if e.Case != 0 {
		b.WriteString(" ")
		b.WriteString(e.Case.String())
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

// Unwrap exposes a storage cause, so a host can reach a backend's sentinel
// with errors.Is without knowing this type's shape.
func (e *CaseError) Unwrap() error { return e.Cause }

// Failed reports whether err is a refusal of this category. It is the
// reading a host usually wants: the categories are the contract, and
// taking a *CaseError apart to compare one field is a host's busywork.
func Failed(err error, want Category) bool {
	var ce *CaseError
	return errors.As(err, &ce) && ce.Reason == want
}

// refused builds the one refusal shape this package emits. It is the
// primitive, and the only thing that calls it is a `refuser` below: an
// operation's name and the case it was asked of travel together through
// every refusal of that call, and building a `CaseError` by hand anywhere
// else would put two hand-typed words back where one binding belongs.
func refused(op string, c Category, id CaseID, detail string) error {
	return &CaseError{Op: op, Reason: c, Case: id, Detail: detail}
}

// refuser is one operation's name bound to one case, so that a call path
// states who is refusing exactly once — at its top — and every refusal it
// then returns agrees.
//
// The property is worth the indirection because `CaseError.Op` is how a host
// finds the call that answered it: `Op` is a field of the contract, not
// decoration, and a store refusal or a hook failure that said `update` when
// the host called `submit` would send it to the wrong line of its own code.
// Before this type, that agreement was maintained by typing the same string
// at thirty-odd call sites, including the ones where the name is handed to
// the store and comes back inside an error the operation did not build.
//
// It also folds the case ID into the same binding, which is the other half
// of the argument: a store method that receives an ID *and* an operation
// name can be passed a pair that does not belong together, and only the
// caller knows which is which.
type refuser struct {
	op string
	id CaseID
}

// newRefuser binds a name to a case for a path that has no handle yet —
// `Create`, before the case it is making exists, and the registry's
// definition-level refusals, which are about no case in particular and so
// carry the zero ID the way `CaseError` documents it.
func newRefuser(op string, id CaseID) refuser { return refuser{op: op, id: id} }

// refusing is newRefuser for the case this handle points at. Each operation
// calls it once, as its first act, and passes the result wherever a refusal
// can be built downstream — including into the store.
func (c Case) refusing(op string) refuser { return newRefuser(op, c.id) }

// now is the ordinary refusal: category `c`, sentence `detail`, in this
// binding's operation name and against its case.
func (r refuser) now(c Category, detail string) error {
	return refused(r.op, c, r.id, detail)
}

// caused is `now` with a cause beside it, for the refusals whose reason is
// another error's — a hook that returned its own failure, or a store driver
// that will. The cause is the host's to read and the category is the
// engine's answer, which is why they are one value and not two.
func (r refuser) caused(c Category, detail string, cause error) error {
	err := refused(r.op, c, r.id, detail)
	err.(*CaseError).Cause = cause
	return err
}

// staleCopy is the one sentence the guard needs, stated twice
// because the check is made twice: once when an operation reads the copy it
// is about to write, so a caller whose view is out of date hears about it
// before anything is resolved or checked against permissions, and once under
// the store's lock, because a second session of the same actor can have saved
// in between. Both say the same thing, which is the point: the race is not a
// different error from the stale case, only a rarer one. It is a method
// rather than a sentence in two places so that "the same thing" is true by
// construction, and it carries the operation's name from whichever of the two
// checks reached it.
func (r refuser) staleCopy(cur, want uint64) error {
	return r.now(StaleRevision,
		"the working copy is at revision "+dec(cur)+", not "+dec(want))
}

func dec(v uint64) string   { return strconv.FormatUint(v, 10) }
func quote(s string) string { return strconv.Quote(s) }
