package schema

type kind int

const (
	tkEOF    kind = iota
	tkIdent       // identifiers and keywords alike; classified by text
	tkString      // unescaped string literal content
	tkNumber      // integer or decimal literal, raw text
	tkPunct
)

type token struct {
	kind      kind
	text      string
	line, col int
}

// keywords is the full reserved-word set (SYNTAX.md §2.4). Reserved words
// never appear as declared names (N1); the parser rejects them in name
// positions.
var keywords = map[string]bool{
	// Block keywords.
	"types": true, "roles": true, "forms": true, "stages": true,
	// Statement keywords.
	"allow": true, "deny": true, "accept": true, "reject": true, "require": true, "all": true,
	// Attributes.
	"required": true, "present": true, "verifiable": true, "multiple": true, "default": true,
	// Built-in type constructors.
	"string": true, "integer": true, "decimal": true, "boolean": true, "date": true,
	"datetime": true, "binary": true, "enum": true, "options": true, "signature": true,
	// Literals.
	"true": true, "false": true,
	// Permissions.
	"view": true, "edit": true, "submit": true, "review": true, "sign": true,
}

// blockWords are the four top-level block keywords (§3.1).
var blockWords = map[string]bool{"types": true, "roles": true, "forms": true, "stages": true}

// permissions are the seven grantable verbs (§3.3).
var permissions = map[string]bool{
	"view": true, "edit": true, "submit": true, "review": true,
	"sign": true, "accept": true, "reject": true,
}

// attrWords introduce a field or group attribute (§3.8).
var attrWords = map[string]bool{
	"required": true, "present": true, "verifiable": true, "multiple": true, "default": true,
}

// compOps are the six comparison operators (§3.6).
var compOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

// orderBases are the types permitting < <= > >= (§5.7-E3).
var orderBases = map[string]bool{"integer": true, "decimal": true, "date": true, "datetime": true, "string": true}

// isTransitionPerm reports whether perm is accept or reject (§5.5-P1).
func isTransitionPerm(perm string) bool { return perm == "accept" || perm == "reject" }
