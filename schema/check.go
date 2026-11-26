package schema

import "sort"

// analyze resolves declarations and checks the model. It collects independent
// errors and skips checks that would only repeat an earlier resolution failure.
func analyze(def *astDef, s *Schema) {
	a := &analyzer{
		s:         s,
		types:     map[string]*Type{},
		typePos:   map[string]pos{},
		roles:     map[string]*Role{},
		roleAST:   map[string]*astRoleDecl{},
		tags:      map[string]*Type{},
		formNodes: map[string]*Node{},
		nodes:     map[string]*Node{},
		stages:    map[string]*Stage{},
		stagePos:  map[string]pos{},
		rootNS:    map[string]pos{},
		upperNS:   map[string]pos{},
		builtins:  map[string]*Type{},
		bad:       map[*Type]bool{},
		usedRoles: map[string]bool{},
		usedTypes: map[string]bool{},
		sigRef:    map[*Node]*astOperand{},
		allows:    map[string]map[string]pos{},
		denies:    map[string]map[string]pos{},
	}
	for _, n := range []string{"string", "integer", "boolean", "date", "datetime", "binary", "options", "signature"} {
		a.builtins[n] = &Type{Name: n, Base: n}
	}
	a.checkBlocks(def)
	a.collectRoles(def)
	a.collectTypes(def)
	a.buildForms(def)
	a.resolveSignatures()
	a.checkRoleGrants()
	a.buildStages(def)
	a.reportContradictions()
	for _, st := range a.sites {
		a.checkCondition(st)
	}
	a.checkDependencies()
	if len(s.Stages) > 0 {
		s.Initial = s.Stages[0]
	}
	a.warnUnused()
	a.checkReachability()
}

type analyzer struct {
	s         *Schema
	types     map[string]*Type // custom declared types
	typePos   map[string]pos
	roles     map[string]*Role
	roleAST   map[string]*astRoleDecl
	tags      map[string]*Type // enum tag → owning enum type
	formNodes map[string]*Node
	nodes     map[string]*Node // absolute form-rooted path → node
	stages    map[string]*Stage
	stagePos  map[string]pos
	rootNS    map[string]pos // forms ∪ stages ∪ types
	upperNS   map[string]pos // roles ∪ enum tags
	builtins  map[string]*Type
	bad       map[*Type]bool // types whose declaration failed; dependents skip checks
	usedRoles map[string]bool
	usedTypes map[string]bool
	sigs      []*Node // signature-typed fields, declaration order
	sigRef    map[*Node]*astOperand
	sites     []*site
	allows    map[string]map[string]pos // role → "perm\x00target"
	denies    map[string]map[string]pos
}

// site is one condition to type-check after the trees exist.
type site struct {
	owner   *Node  // declaration bearing the condition; nil for transitions
	stage   *Stage // stage owning a transition edge
	edgeIdx int    // index of that edge in stage.Edges
	form    *Node  // relative-path anchor; nil outside forms
	x       *astExpr
	p       pos
	kind    string // "required" | "present" | "transition"
	label   string // owner path or stage name, for diagnostics
	refs    []*Node
	expr    *Expr
}

func (a *analyzer) errf(p pos, code Code, ctx string) {
	a.s.Errors = append(a.s.Errors, Problem{Line: p.line, Col: p.col, Code: code, Context: ctx})
}

func (a *analyzer) warnf(p pos, code Code, ctx string) {
	a.s.Warnings = append(a.s.Warnings, Problem{Line: p.line, Col: p.col, Code: code, Context: ctx})
}

func isUpperStart(s string) bool { return s != "" && 'A' <= s[0] && s[0] <= 'Z' }

func typeName(t *Type) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Base
}

func rootForm(n *Node) *Node {
	for n.Parent != nil {
		n = n.Parent
	}
	if n.Kind == "form" {
		return n
	}
	return nil
}

// walkPath resolves segs (without a form-name prefix) from a form root.
func walkPath(form *Node, segs []string) *Node {
	cur := form
	for _, seg := range segs {
		var next *Node
		for _, ch := range cur.Nodes {
			if ch.Name == seg {
				next = ch
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// checkBlocks validates block order, uniqueness and required blocks before
// inspecting their contents.
func (a *analyzer) checkBlocks(def *astDef) {
	type blk struct {
		word string
		p    pos
		n    int
	}
	var blks []blk
	for _, b := range def.types {
		blks = append(blks, blk{"types", pos{b.line, b.col}, len(b.decls)})
	}
	for _, b := range def.roles {
		blks = append(blks, blk{"roles", pos{b.line, b.col}, len(b.decls)})
	}
	for _, b := range def.forms {
		blks = append(blks, blk{"forms", pos{b.line, b.col}, len(b.decls)})
	}
	for _, b := range def.stages {
		blks = append(blks, blk{"stages", pos{b.line, b.col}, len(b.decls)})
	}
	rank := map[string]int{"types": 0, "roles": 1, "forms": 2, "stages": 3}
	sort.Slice(blks, func(i, j int) bool {
		if blks[i].p.line != blks[j].p.line {
			return blks[i].p.line < blks[j].p.line
		}
		return blks[i].p.col < blks[j].p.col
	})
	seen := map[string]int{}
	prev := -1
	for _, b := range blks {
		seen[b.word]++
		if seen[b.word] > 1 {
			a.errf(b.p, CodeDuplicateBlock, b.word)
			continue
		}
		if r := rank[b.word]; r < prev {
			a.errf(b.p, CodeBlockOrder, b.word)
		} else {
			prev = r
		}
	}
	for _, w := range []string{"forms", "stages"} {
		if seen[w] == 0 {
			a.errf(pos{1, 1}, CodeMissingBlock, w)
		}
	}
	for _, b := range blks {
		if seen[b.word] == 1 && (b.word == "forms" || b.word == "stages") && b.n == 0 {
			a.errf(b.p, CodeEmptyBlock, b.word)
		}
	}
}
