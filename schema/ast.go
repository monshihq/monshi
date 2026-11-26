package schema

// The parser records source positions for diagnostics and leaves name
// resolution to the checker.

type pos struct{ line, col int }

type astDef struct {
	types  []*astTypesBlock
	roles  []*astRolesBlock
	forms  []*astFormsBlock
	stages []*astStagesBlock
}

type astTypesBlock struct {
	line, col int
	decls     []*astTypeDecl
}

type astTypeDecl struct {
	line, col int
	name      string
	base      *astTypeExpr
}

type astRolesBlock struct {
	line, col int
	decls     []*astRoleDecl
}

type astRoleDecl struct {
	line, col int
	name      string
	grants    []*astGrant
}

type astFormsBlock struct {
	line, col int
	decls     []*astFormDecl
}

type astFormDecl struct {
	line, col int
	name      string
	attrs     []*astAttr
	members   []astNode
}

type astStagesBlock struct {
	line, col int
	decls     []*astStageDecl
}

type astStageDecl struct {
	line, col   int
	name        string
	grants      []*astGrant
	requires    []*astRequire
	transitions []*astTransition
}

// astNode is a form or group member: a field or a group.
type astNode interface {
	nodePos() (int, int)
}

type astFieldDecl struct {
	line, col int
	name      string
	typ       *astTypeExpr
	attrs     []*astAttr
}

type astGroupDecl struct {
	line, col int
	name      string
	attrs     []*astAttr
	members   []astNode
}

func (f *astFieldDecl) nodePos() (int, int) { return f.line, f.col }
func (g *astGroupDecl) nodePos() (int, int) { return g.line, g.col }

// astTypeExpr is any type reference: built-in (with raw decimal args or
// enum tags), signature constructor (sigRef), or a bare name resolved by
// the checker as custom type or role.
type astTypeExpr struct {
	line, col int
	base      string
	precision string // decimal, raw INT token
	scale     string // decimal, raw INT token
	tags      []tagDecl
	sigRef    *astOperand
}

type tagDecl struct {
	line, col int
	name      string
}

// astAttr kinds: "required", "requiredCond", "present", "verifiable",
// "multiple", "default".
type astAttr struct {
	line, col int
	kind      string
	cond      *astExpr
	value     *astOperand
}

// astExpr: op "" for leaves; "&&" "||" "!" (Left only) and the six
// comparisons for interior nodes.
type astExpr struct {
	line, col int
	op        string
	left      *astExpr
	right     *astExpr
	operand   *astOperand
}

// astOperand kinds: "string", "number", "bool", "path".
type astOperand struct {
	line, col int
	kind      string
	text      string
	relative  bool
	segs      []string
}

type astGrant struct {
	line, col  int
	deny       bool
	role       string // stage-block grants; "" in role blocks
	rolePos    pos
	perms      []string
	hasTargets bool
	all        bool
	paths      [][]string
	pathPos    []pos
}

type astRequire struct {
	line, col int
	forms     []string
	formPos   []pos
}

type astTransition struct {
	line, col int
	reject    bool
	cond      *astExpr
	dest      string
	destPos   pos
}
