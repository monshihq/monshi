package schema

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Code identifies a problem with a stable keyword (SYNTAX.md §2.4 rule IDs).
type Code string

// Lexical and syntactic codes.
const (
	CodeUnexpectedToken     Code = "UNEXPECTED_TOKEN"
	CodeUnterminatedString  Code = "UNTERMINATED_STRING"
	CodeUnterminatedComment Code = "UNTERMINATED_COMMENT"
	CodeInvalidEscape       Code = "INVALID_ESCAPE"
	CodeInvalidCharacter    Code = "INVALID_CHARACTER"
	CodeReservedName        Code = "RESERVED_NAME"
	CodeMissingBlock        Code = "MISSING_BLOCK"
	CodeDuplicateBlock      Code = "DUPLICATE_BLOCK"
	CodeBlockOrder          Code = "BLOCK_ORDER"
	CodeEmptyBlock          Code = "EMPTY_BLOCK"
)

// Semantic error codes.
const (
	CodeNameCase               Code = "NAME_CASE"
	CodeDuplicateName          Code = "DUPLICATE_NAME"
	CodeUnknownType            Code = "UNKNOWN_TYPE"
	CodeUnknownRole            Code = "UNKNOWN_ROLE"
	CodeInvalidTypeBase        Code = "INVALID_TYPE_BASE"
	CodeInvalidDecimalArgs     Code = "INVALID_DECIMAL_ARGS"
	CodeEmptyEnum              Code = "EMPTY_ENUM"
	CodeEmptyGroup             Code = "EMPTY_GROUP"
	CodeInvalidAttribute       Code = "INVALID_ATTRIBUTE"
	CodeDuplicateAttribute     Code = "DUPLICATE_ATTRIBUTE"
	CodeInvalidDefault         Code = "INVALID_DEFAULT"
	CodeInvalidCondition       Code = "INVALID_CONDITION"
	CodeSelfReference          Code = "SELF_REFERENCE"
	CodeCyclicCondition        Code = "CYCLIC_CONDITION"
	CodeSignatureInGroup       Code = "SIGNATURE_IN_GROUP"
	CodeInvalidSignatureRef    Code = "INVALID_SIGNATURE_REF"
	CodeInvalidGrant           Code = "INVALID_GRANT"
	CodeUnknownTarget          Code = "UNKNOWN_TARGET"
	CodeUnknownForm            Code = "UNKNOWN_FORM"
	CodeIncompatiblePermission Code = "INCOMPATIBLE_PERMISSION"
	CodeUnknownStage           Code = "UNKNOWN_STAGE"
	CodeSelfTransition         Code = "SELF_TRANSITION"
	CodeDuplicateTransition    Code = "DUPLICATE_TRANSITION"
	CodeInvalidOperand         Code = "INVALID_OPERAND"
	CodeTypeMismatch           Code = "TYPE_MISMATCH"
	CodeNotBoolean             Code = "NOT_BOOLEAN"
)

// Warning codes.
const (
	CodeEmptyForm           Code = "EMPTY_FORM"
	CodeRedundantVerifiable Code = "REDUNDANT_VERIFIABLE"
	CodeUnusedRole          Code = "UNUSED_ROLE"
	CodeUnusedType          Code = "UNUSED_TYPE"
	CodeContradictoryGrants Code = "CONTRADICTORY_GRANTS"
	CodeUnreachableStage    Code = "UNREACHABLE_STAGE"
	CodeConstantCondition   Code = "CONSTANT_CONDITION"
)

var descriptions = map[Code]string{
	CodeUnexpectedToken:        "unexpected token",
	CodeUnterminatedString:     "unterminated string",
	CodeUnterminatedComment:    "unterminated block comment",
	CodeInvalidEscape:          "invalid escape sequence",
	CodeInvalidCharacter:       "invalid character",
	CodeReservedName:           "reserved word used as a name",
	CodeMissingBlock:           "required block missing",
	CodeDuplicateBlock:         "duplicate top-level block",
	CodeBlockOrder:             "top-level blocks out of canonical order",
	CodeEmptyBlock:             "required block is empty",
	CodeNameCase:               "name violates its casing rule",
	CodeDuplicateName:          "duplicate name in namespace",
	CodeUnknownType:            "unknown type",
	CodeUnknownRole:            "unknown role",
	CodeInvalidTypeBase:        "invalid custom type base",
	CodeInvalidDecimalArgs:     "invalid decimal precision/scale",
	CodeEmptyEnum:              "enum must declare at least one tag",
	CodeEmptyGroup:             "group must contain at least one member",
	CodeInvalidAttribute:       "attribute not valid here",
	CodeDuplicateAttribute:     "duplicate attribute",
	CodeInvalidDefault:         "invalid default value",
	CodeInvalidCondition:       "condition must be a boolean expression",
	CodeSelfReference:          "condition references its own declaration",
	CodeCyclicCondition:        "cyclic condition dependency",
	CodeSignatureInGroup:       "signature fields must be top-level form members",
	CodeInvalidSignatureRef:    "invalid signature reference",
	CodeInvalidGrant:           "invalid permission grant",
	CodeUnknownTarget:          "unresolvable grant target",
	CodeUnknownForm:            "unknown form",
	CodeIncompatiblePermission: "permission incompatible with target",
	CodeUnknownStage:           "unknown transition destination",
	CodeSelfTransition:         "stage cannot transition to itself",
	CodeDuplicateTransition:    "duplicate transition destination",
	CodeInvalidOperand:         "invalid operand",
	CodeTypeMismatch:           "incomparable operand types",
	CodeNotBoolean:             "boolean expression required",
	CodeEmptyForm:              "form has no members",
	CodeRedundantVerifiable:    "redundant verifiable annotation; an ancestor is already verifiable",
	CodeUnusedRole:             "role is never referenced",
	CodeUnusedType:             "custom type is never used",
	CodeContradictoryGrants:    "allow and deny for the same permission and target",
	CodeUnreachableStage:       "stage is unreachable from the initial stage",
	CodeConstantCondition:      "condition is constant",
}

// Problem is one lexical, syntactic or semantic diagnostic.
type Problem struct {
	Line    int
	Col     int
	Code    Code
	Context string
}

// Error renders the problem as "line:col: message [CODE]".
func (p Problem) Error() string {
	msg := descriptions[p.Code]
	if p.Context != "" {
		msg += ": " + p.Context
	}
	return fmt.Sprintf("%d:%d: %s [%s]", p.Line, p.Col, msg, p.Code)
}

// Schema is a compiled, immutable case definition (SYNTAX.md §5.8).
// When Errors is non-empty the schema is incomplete and must not be used;
// Warnings never invalidate a definition.
type Schema struct {
	Types    []*Type   // custom types, declaration order
	Roles    []*Role   // declaration order
	Forms    []*Node   // top-level form nodes, declaration order
	Stages   []*Stage  // declaration order
	Initial  *Stage    // first declared stage (SYNTAX.md §5.6-ST1)
	Errors   []Problem // lexical, syntactic and semantic problems
	Warnings []Problem // non-fatal diagnostics
}

// Type is a resolved value type: a built-in, a custom type over a
// primitive built-in, or an anonymous inline enum/decimal/signature.
type Type struct {
	Name      string   // declared name, or the base name for built-ins
	Base      string   // string integer decimal boolean date datetime binary options enum signature role
	Precision int      // decimal
	Scale     int      // decimal
	Tags      []string // enum member tags, declaration order
}

// Node is one definition node: a form root, a group, or a field.
type Node struct {
	Kind      string // "form" | "group" | "field"
	Name      string
	Path      string // absolute, form-rooted ("application.internal.risk")
	Parent    *Node
	Nodes     []*Node // children of forms and groups
	Type      *Type   // value type; nil on role-typed fields
	Role      *Role   // set on role-typed fields
	Signer    *Role   // signature(Role)
	SignField *Node   // signature(field) → role-typed field

	Required     bool
	RequiredCond *Expr
	PresentCond  *Expr
	Verifiable   bool
	Multiple     bool
	Default      *Value

	line, col int // declaration position, for diagnostics
	resolved  bool
}

// Expr is a resolved condition expression. Leaf kinds: Node (value path),
// Tag (enum tag) or Lit; interior nodes carry one of the Op strings.
type Expr struct {
	Op    string // "" | && | || | ! | == | != | < | <= | > | >=
	Left  *Expr
	Right *Expr
	Node  *Node
	Tag   string
	Lit   *Value
}

// Value is a typed constant: a default or a literal operand.
type Value struct {
	Kind string // string integer decimal boolean date datetime enum
	Str  string
	Int  int64
	Dec  decimal.Decimal
	Bool bool
	Time time.Time
}

// Role is a declared role with its role-block grants resolved.
type Role struct {
	Name   string
	Grants []Grant
}

// Grant is one allow/deny statement. Role is the resolved grant subject:
// the enclosing role for role-block grants, the named role for stage
// grants, and nil only when a stage grant names an undeclared role.
type Grant struct {
	Role  *Role
	Deny  bool
	Perms []string
	All   bool
	Nodes []*Node
}

// Stage is one workflow stage with its grants, requires and edges.
type Stage struct {
	Name     string
	Grants   []Grant
	Requires []*Node // top-level form nodes
	Edges    []Edge
}

// Edge is one declared stage transition.
type Edge struct {
	Reject bool
	Dest   *Stage
	Cond   *Expr
}
