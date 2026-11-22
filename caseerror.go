package monshi

// The refusal surface for case operations (DESIGN §9).
//
// This is a different axis from `Code` and `Problem`, and the two stay
// apart on purpose. A Code is about *source* an author can still fix: it
// names a SYNTAX §5 rule and points at a line. A Category is about a
// *request* the engine would not carry out: nothing in the definition is
// wrong, and the host's own state — who asked, which revision they
// observed, whether the store answered — is. Keeping the lists separate is
// what lets a host show one to an author and log the other as an
// operational event without parsing a message to tell them apart.
//
// The categories are DESIGN §9's, spelled as the document spells them, and
// they are additive: a category appears here when an operation can refuse
// for it. `HookFailure` is declared before hooks are registered (§7)
// because §9 already names it, and a host reading a stable list of
// categories should not have to relearn one per release.

import (
	"errors"
	"strconv"
	"strings"
)

// Category is one of DESIGN §9's error categories.
type Category string

// The categories (DESIGN §9). A field-level validation list — path, code,
// message — is an operation *result*, not a refusal, and §9 keeps it out
// of this list for the same reason (DESIGN §6.2's ChangeResult reports it
// once the edit operations land).
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

// CaseError is a refused operation. It is structured, as §9 requires: a
// host branches on Reason, shows Detail to a human if it has one, and
// reaches for Cause only when Reason is StorageFailure — because that is
// the one category whose explanation belongs to somebody else's error.
type CaseError struct {
	// Op is the operation that refused, in the words the exported API uses
	// for it: "create", "open", "stage", "revision", "register", "replace",
	// and the edit and commit operations that follow. It is the host's own
	// word, never an internal helper's, because a host reads this field to
	// find the call in its code.
	Op string

	// Reason is one of DESIGN §9's categories.
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
// reading a host usually wants: §9's categories are the contract, and
// taking a *CaseError apart to compare one field is a host's busywork.
func Failed(err error, want Category) bool {
	var ce *CaseError
	return errors.As(err, &ce) && ce.Reason == want
}

// refused builds the one refusal shape this package emits. `op` is stated
// by the caller because the same store path serves several operations and
// a host wants to know which one refused.
func refused(op string, c Category, id CaseID, detail string) error {
	return &CaseError{Op: op, Reason: c, Case: id, Detail: detail}
}

// dec and quote render the two things a refusal names: a revision and a
// host-supplied identifier. Quoting matters more than it looks — an empty
// definition ID is a real mistake a host makes, and "definition """" is
// legible where "definition " is not.
func dec(v uint64) string { return strconv.FormatUint(v, 10) }

func quote(s string) string { return strconv.Quote(s) }
