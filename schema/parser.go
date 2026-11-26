package schema

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Parse compiles one Monshi definition read from r. The
// returned error is non-nil only when parsing cannot run at all, e.g. an
// I/O failure; every lexical, syntactic and semantic problem is reported
// in Schema.Errors.
func Parse(r io.Reader) (*Schema, error) { return NewParser(r).Parse() }

// Parser turns one definition file into a Schema.
type Parser struct {
	src  io.Reader
	toks []token
	i    int
	s    *Schema
}

// NewParser returns a parser reading one definition from r.
func NewParser(r io.Reader) *Parser { return &Parser{src: r, s: &Schema{}} }

// Parse reads, lexes, parses and semantically checks the definition.
// The pipeline is lexer → recursive-descent parser → AST → checker →
// Schema; the intermediate AST stays unexported.
func (p *Parser) Parse() (*Schema, error) {
	b, err := io.ReadAll(p.src)
	if err != nil {
		return nil, err
	}
	toks, probs := lex(string(b))
	p.toks = toks
	p.s.Errors = append(p.s.Errors, probs...)
	def := p.parseDefinition()
	analyze(def, p.s)
	return p.s, nil
}

// Tokens.

func (p *Parser) peek() token { return p.toks[p.i] }
func (p *Parser) atEOF() bool { return p.toks[p.i].kind == tkEOF }

func (p *Parser) next() token {
	t := p.toks[p.i]
	if t.kind != tkEOF {
		p.i++
	}
	return t
}

func (p *Parser) atPunct(w string) bool { t := p.peek(); return t.kind == tkPunct && t.text == w }
func (p *Parser) atWord(w string) bool  { t := p.peek(); return t.kind == tkIdent && t.text == w }

func (p *Parser) eatPunct(w string) bool {
	if p.atPunct(w) {
		p.i++
		return true
	}
	return false
}

func (p *Parser) eatWord(w string) bool {
	if p.atWord(w) {
		p.i++
		return true
	}
	return false
}

func describe(t token) string {
	if t.kind == tkEOF {
		return "end of file"
	}
	return strconv.Quote(t.text)
}

func (p *Parser) expectPunct(w string) {
	if !p.eatPunct(w) {
		p.fail(p.peek(), CodeUnexpectedToken, fmt.Sprintf("expected %q, found %s", w, describe(p.peek())))
	}
}

// expectName consumes a non-reserved identifier usable as a declared name or
// path segment.
func (p *Parser) expectName(what string) string {
	t := p.peek()
	if t.kind != tkIdent {
		p.fail(t, CodeUnexpectedToken, "expected "+what+", found "+describe(t))
	}
	if keywords[t.text] {
		p.fail(t, CodeReservedName, t.text)
	}
	p.i++
	return t.text
}

// Errors and recovery.

type bail struct{}

func (p *Parser) problem(t token, code Code, ctx string) {
	p.s.Errors = append(p.s.Errors, Problem{Line: t.line, Col: t.col, Code: code, Context: ctx})
}

// fail records a problem and unwinds to the innermost recovery point.
func (p *Parser) fail(t token, code Code, ctx string) {
	p.problem(t, code, ctx)
	panic(bail{})
}

// attempt runs fn under recovery and reports whether it completed.
func (p *Parser) attempt(fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isBail := r.(bail); !isBail {
				panic(r)
			}
			ok = false
		}
	}()
	fn()
	return true
}

// resync advances past a statement-terminating ';' or to the '}' closing
// the enclosing block, skipping balanced brace groups.
func (p *Parser) resync() {
	depth := 0
	for {
		t := p.peek()
		switch {
		case t.kind == tkEOF:
			return
		case t.kind == tkPunct && t.text == "{":
			depth++
			p.i++
		case t.kind == tkPunct && t.text == "}":
			if depth == 0 {
				return
			}
			depth--
			p.i++
		case t.kind == tkPunct && t.text == ";":
			p.i++
			return
		default:
			p.i++
		}
	}
}

// resyncTop advances to the next plausible top-level block start,
// consuming a brace group when one is found first.
func (p *Parser) resyncTop() {
	for {
		t := p.peek()
		switch {
		case t.kind == tkEOF:
			return
		case t.kind == tkPunct && t.text == "{":
			p.skipBraces()
			return
		case t.kind == tkIdent && blockWords[t.text]:
			return
		default:
			p.i++
		}
	}
}

func (p *Parser) skipBraces() {
	depth := 0
	for {
		t := p.peek()
		if t.kind == tkEOF {
			return
		}
		switch {
		case t.kind == tkPunct && t.text == "{":
			depth++
		case t.kind == tkPunct && t.text == "}":
			depth--
			if depth == 0 {
				p.i++
				return
			}
		}
		p.i++
	}
}

// Definition. Block order, duplicates and obligation are checked
// semantically; the parser accepts blocks by keyword.

func (p *Parser) parseDefinition() *astDef {
	def := &astDef{}
	for {
		t := p.peek()
		if t.kind == tkEOF {
			break
		}
		if t.kind != tkIdent || !blockWords[t.text] {
			p.problem(t, CodeUnexpectedToken, "expected a top-level block, found "+describe(t))
			p.resyncTop()
			continue
		}
		start := p.i
		if !p.attempt(func() { p.parseBlock(def) }) {
			if p.i <= start && p.i < len(p.toks)-1 {
				p.i++
			}
			p.resyncTop()
		}
	}
	return def
}

func (p *Parser) parseBlock(def *astDef) {
	t := p.next()
	switch t.text {
	case "types":
		b := &astTypesBlock{line: t.line, col: t.col}
		p.blockBody(func() { b.decls = append(b.decls, p.parseTypeDecl()) })
		def.types = append(def.types, b)
	case "roles":
		b := &astRolesBlock{line: t.line, col: t.col}
		p.blockBody(func() { b.decls = append(b.decls, p.parseRoleDecl()) })
		def.roles = append(def.roles, b)
	case "forms":
		b := &astFormsBlock{line: t.line, col: t.col}
		p.blockBody(func() { b.decls = append(b.decls, p.parseFormDecl()) })
		def.forms = append(def.forms, b)
	case "stages":
		b := &astStagesBlock{line: t.line, col: t.col}
		p.blockBody(func() { b.decls = append(b.decls, p.parseStageDecl()) })
		def.stages = append(def.stages, b)
	}
}

// blockBody parses "{" decl "}" with per-declaration recovery.
func (p *Parser) blockBody(decl func()) {
	if !p.eatPunct("{") {
		p.fail(p.peek(), CodeUnexpectedToken, fmt.Sprintf("expected %q, found %s", "{", describe(p.peek())))
	}
	for !p.atPunct("}") && !p.atEOF() {
		start := p.i
		if !p.attempt(decl) {
			if p.i <= start && p.i < len(p.toks)-1 {
				p.i++
			}
			p.resync()
		}
	}
	p.expectPunct("}")
}

// Types.

func (p *Parser) parseTypeDecl() *astTypeDecl {
	t := p.peek()
	d := &astTypeDecl{line: t.line, col: t.col}
	d.name = p.expectName("a type name")
	p.expectPunct(":")
	d.base = p.parseTypeExpr()
	p.expectPunct(";")
	return d
}

func (p *Parser) expectInt(what string) string {
	t := p.peek()
	if t.kind != tkNumber || strings.ContainsAny(t.text, "-.") {
		p.fail(t, CodeUnexpectedToken, "expected "+what+", found "+describe(t))
	}
	p.i++
	return t.text
}

// Roles.

func (p *Parser) parseRoleDecl() *astRoleDecl {
	t := p.peek()
	d := &astRoleDecl{line: t.line, col: t.col}
	d.name = p.expectName("a role name")
	p.blockBody(func() { d.grants = append(d.grants, p.parseGrant(false)) })
	return d
}

// parseGrant reads an allow/deny statement. Role-name position is enforced
// by block context; statement shape rules are semantic.
func (p *Parser) parseGrant(stage bool) *astGrant {
	t := p.next()
	g := &astGrant{line: t.line, col: t.col, deny: t.text == "deny"}
	if stage {
		rt := p.peek()
		g.role = p.expectName("a role name")
		g.rolePos = pos{rt.line, rt.col}
	}
	for {
		tt := p.peek()
		if tt.kind != tkIdent || !permissions[tt.text] {
			break
		}
		g.perms = append(g.perms, tt.text)
		p.i++
		if !p.eatPunct(",") {
			break
		}
	}
	if len(g.perms) == 0 {
		p.fail(p.peek(), CodeUnexpectedToken, "expected a permission, found "+describe(p.peek()))
	}
	if !p.atPunct(";") {
		if p.eatWord("all") {
			g.hasTargets, g.all = true, true
		} else {
			for {
				pt := p.peek()
				g.paths = append(g.paths, p.parseGrantPath())
				g.pathPos = append(g.pathPos, pos{pt.line, pt.col})
				g.hasTargets = true
				if !p.eatPunct(",") {
					break
				}
			}
		}
	}
	p.expectPunct(";")
	return g
}

func (p *Parser) parseGrantPath() []string {
	segs := []string{p.expectName("a grant target")}
	for p.eatPunct(".") {
		segs = append(segs, p.expectName("a grant target"))
	}
	return segs
}

// Forms.

func (p *Parser) parseFormDecl() *astFormDecl {
	t := p.peek()
	d := &astFormDecl{line: t.line, col: t.col}
	d.name = p.expectName("a form name")
	d.attrs = p.parseAttrs()
	d.members = p.parseMemberBody()
	return d
}

func (p *Parser) parseMemberBody() []astNode {
	var ms []astNode
	p.blockBody(func() { ms = append(ms, p.parseMember()) })
	return ms
}

func (p *Parser) parseMember() astNode {
	t := p.peek()
	name := p.expectName("a member name")
	if p.atPunct(":") {
		p.i++
		f := &astFieldDecl{line: t.line, col: t.col, name: name}
		f.typ = p.parseTypeExpr()
		f.attrs = p.parseAttrs()
		p.expectPunct(";")
		return f
	}
	g := &astGroupDecl{line: t.line, col: t.col, name: name}
	g.attrs = p.parseAttrs()
	g.members = p.parseMemberBody()
	return g
}

func (p *Parser) parseTypeExpr() *astTypeExpr {
	t := p.peek()
	if t.kind != tkIdent {
		p.fail(t, CodeUnexpectedToken, "expected a type, found "+describe(t))
	}
	p.i++
	te := &astTypeExpr{line: t.line, col: t.col, base: t.text}
	switch t.text {
	case "decimal":
		p.expectPunct("(")
		te.precision = p.expectInt("a precision")
		p.expectPunct(",")
		te.scale = p.expectInt("a scale")
		p.expectPunct(")")
	case "enum":
		p.expectPunct("(")
		for {
			tt := p.peek()
			if tt.kind != tkIdent || keywords[tt.text] {
				p.fail(tt, CodeUnexpectedToken, "expected an enum tag, found "+describe(tt))
			}
			te.tags = append(te.tags, tagDecl{line: tt.line, col: tt.col, name: tt.text})
			p.i++
			if !p.eatPunct(",") {
				break
			}
		}
		p.expectPunct(")")
	case "signature":
		p.expectPunct("(")
		te.sigRef = p.parseOperand()
		p.expectPunct(")")
	case "string", "integer", "boolean", "date", "datetime", "binary", "options":
		// bare built-ins
	default:
		if keywords[t.text] {
			p.fail(t, CodeReservedName, t.text)
		}
	}
	return te
}

// Attributes. Kind/shape validity per node is semantic.

func (p *Parser) parseAttrs() []*astAttr {
	var as []*astAttr
	for {
		t := p.peek()
		if t.kind != tkIdent || !attrWords[t.text] {
			return as
		}
		a := &astAttr{line: t.line, col: t.col}
		switch t.text {
		case "required":
			p.i++
			if p.eatPunct("[") {
				a.kind = "requiredCond"
				a.cond = p.parseExpr()
				p.expectPunct("]")
			} else {
				a.kind = "required"
			}
		case "present":
			p.i++
			p.expectPunct("[")
			a.kind = "present"
			a.cond = p.parseExpr()
			p.expectPunct("]")
		case "default":
			p.i++
			a.kind = "default"
			a.value = p.parseOperand()
		default: // verifiable, multiple
			a.kind = t.text
			p.i++
		}
		as = append(as, a)
	}
}

// Stages. Statements dispatch on their first keyword token.

func (p *Parser) parseStageDecl() *astStageDecl {
	t := p.peek()
	d := &astStageDecl{line: t.line, col: t.col}
	d.name = p.expectName("a stage name")
	p.blockBody(func() {
		st := p.peek()
		if st.kind != tkIdent {
			p.fail(st, CodeUnexpectedToken, "expected a stage statement, found "+describe(st))
		}
		switch st.text {
		case "allow", "deny":
			d.grants = append(d.grants, p.parseGrant(true))
		case "require":
			d.requires = append(d.requires, p.parseRequire())
		case "accept", "reject":
			d.transitions = append(d.transitions, p.parseTransition())
		default:
			p.fail(st, CodeUnexpectedToken, "expected a stage statement, found "+describe(st))
		}
	})
	return d
}

func (p *Parser) parseRequire() *astRequire {
	t := p.next()
	r := &astRequire{line: t.line, col: t.col}
	for {
		ft := p.peek()
		r.forms = append(r.forms, p.expectName("a form name"))
		r.formPos = append(r.formPos, pos{ft.line, ft.col})
		if !p.eatPunct(",") {
			break
		}
	}
	p.expectPunct(";")
	return r
}

func (p *Parser) parseTransition() *astTransition {
	t := p.next()
	tr := &astTransition{line: t.line, col: t.col, reject: t.text == "reject"}
	if p.eatPunct("[") {
		tr.cond = p.parseExpr()
		p.expectPunct("]")
	}
	dt := p.peek()
	tr.dest = p.expectName("a stage name")
	tr.destPos = pos{dt.line, dt.col}
	p.expectPunct(";")
	return tr
}

// Expressions.

func (p *Parser) parseExpr() *astExpr { return p.parseOr() }

func (p *Parser) parseOr() *astExpr {
	left := p.parseAnd()
	for p.atPunct("||") {
		op := p.next()
		left = &astExpr{line: op.line, col: op.col, op: "||", left: left, right: p.parseAnd()}
	}
	return left
}

func (p *Parser) parseAnd() *astExpr {
	left := p.parseComparison()
	for p.atPunct("&&") {
		op := p.next()
		left = &astExpr{line: op.line, col: op.col, op: "&&", left: left, right: p.parseComparison()}
	}
	return left
}

func (p *Parser) parseComparison() *astExpr {
	left := p.parseUnary()
	t := p.peek()
	if t.kind == tkPunct && compOps[t.text] {
		p.i++
		return &astExpr{line: t.line, col: t.col, op: t.text, left: left, right: p.parseUnary()}
	}
	return left
}

func (p *Parser) parseUnary() *astExpr {
	if p.atPunct("!") {
		op := p.next()
		return &astExpr{line: op.line, col: op.col, op: "!", left: p.parseUnary()}
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() *astExpr {
	if p.eatPunct("(") {
		e := p.parseExpr()
		p.expectPunct(")")
		return e
	}
	o := p.parseOperand()
	return &astExpr{line: o.line, col: o.col, operand: o}
}

func (p *Parser) parseOperand() *astOperand {
	t := p.peek()
	switch {
	case t.kind == tkString:
		p.i++
		return &astOperand{line: t.line, col: t.col, kind: "string", text: t.text}
	case t.kind == tkNumber:
		p.i++
		return &astOperand{line: t.line, col: t.col, kind: "number", text: t.text}
	case t.kind == tkIdent && (t.text == "true" || t.text == "false"):
		p.i++
		return &astOperand{line: t.line, col: t.col, kind: "bool", text: t.text}
	case t.kind == tkPunct && t.text == ".":
		p.i++
		o := &astOperand{line: t.line, col: t.col, kind: "path", relative: true}
		o.segs = append(o.segs, p.expectName("a path segment"))
		for p.eatPunct(".") {
			o.segs = append(o.segs, p.expectName("a path segment"))
		}
		return o
	case t.kind == tkIdent && !keywords[t.text]:
		p.i++
		o := &astOperand{line: t.line, col: t.col, kind: "path"}
		o.segs = append(o.segs, t.text)
		for p.eatPunct(".") {
			o.segs = append(o.segs, p.expectName("a path segment"))
		}
		return o
	}
	p.fail(t, CodeUnexpectedToken, "expected a value or path, found "+describe(t))
	return nil
}
