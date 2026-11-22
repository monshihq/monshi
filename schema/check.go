package schema

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// analyze runs the static semantics of SYNTAX.md §5 over the parsed AST
// and fills the compiled schema. It is best-effort: every independent
// problem is reported, and constructs whose resolution failed are skipped
// by later checks instead of cascading false errors.
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
	a.checkRoleGrants(def)
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
	tags      map[string]*Type // enum tag → owning enum type (N3 global)
	formNodes map[string]*Node
	nodes     map[string]*Node // absolute form-rooted path → node
	stages    map[string]*Stage
	stagePos  map[string]pos
	rootNS    map[string]pos // forms ∪ stages ∪ types (N3)
	upperNS   map[string]pos // roles ∪ enum tags (N3)
	builtins  map[string]*Type
	bad       map[*Type]bool // types whose declaration failed; dependents skip checks
	usedRoles map[string]bool
	usedTypes map[string]bool
	sigs      []*Node // signature-typed fields, declaration order
	sigRef    map[*Node]*astOperand
	sites     []*site
	allows    map[string]map[string]pos // P5: role → "perm\x00target"
	denies    map[string]map[string]pos
}

// site is one condition to type-check after the trees exist (§5.3-F6).
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

// checkBlocks enforces §3.1: order, uniqueness and obligation of the four
// top-level blocks, and non-empty forms/stages.
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

// collectRoles registers the uppercase namespace entries (N3) for roles.
func (a *analyzer) collectRoles(def *astDef) {
	for _, rb := range def.roles {
		for _, d := range rb.decls {
			p := pos{d.line, d.col}
			if !isUpperStart(d.name) {
				a.errf(p, CodeNameCase, d.name)
			}
			if _, dup := a.upperNS[d.name]; dup {
				a.errf(p, CodeDuplicateName, d.name)
				continue
			}
			a.upperNS[d.name] = p
			r := &Role{Name: d.name}
			a.roles[d.name] = r
			a.roleAST[d.name] = d
			a.s.Roles = append(a.s.Roles, r)
		}
	}
}

// declareUpper adds a role-or-tag name to the global uppercase namespace
// and records its owning enum type (tags only).
func (a *analyzer) declareUpper(p pos, name string, enumType *Type) {
	if !isUpperStart(name) {
		a.errf(p, CodeNameCase, name)
	}
	if _, dup := a.upperNS[name]; dup {
		a.errf(p, CodeDuplicateName, name)
		return
	}
	a.upperNS[name] = p
	if enumType != nil {
		a.tags[name] = enumType
	}
}

// collectTypes checks type declarations (T1–T3) and registers custom
// types in the global root namespace (N3).
func (a *analyzer) collectTypes(def *astDef) {
	for _, tb := range def.types {
		for _, d := range tb.decls {
			p := pos{d.line, d.col}
			if isUpperStart(d.name) {
				a.errf(p, CodeNameCase, d.name)
			}
			if _, dup := a.rootNS[d.name]; dup {
				a.errf(p, CodeDuplicateName, d.name)
				continue
			}
			a.rootNS[d.name] = p
			t := &Type{Name: d.name}
			if d.base != nil {
				switch d.base.base {
				case "string", "integer", "boolean", "date", "datetime", "binary":
					t.Base = d.base.base
				case "decimal":
					t.Base = "decimal"
					pr, sc, ok := a.decimalArgs(d.base, p)
					t.Precision, t.Scale = pr, sc
					if !ok {
						a.bad[t] = true
					}
				case "enum":
					t.Base = "enum"
					if len(d.base.tags) == 0 {
						a.errf(p, CodeEmptyEnum, d.name)
					}
					for _, tg := range d.base.tags {
						a.declareUpper(pos{tg.line, tg.col}, tg.name, t)
						t.Tags = append(t.Tags, tg.name)
					}
				default: // T1: signature, options, custom, role — no other bases
					t.Base = d.base.base
					a.errf(pos{d.base.line, d.base.col}, CodeInvalidTypeBase, d.base.base)
					a.bad[t] = true
				}
			}
			a.types[d.name] = t
			a.typePos[d.name] = p
			a.s.Types = append(a.s.Types, t)
		}
	}
}

// decimalArgs enforces T2: 1 ≤ p ≤ 38, 0 ≤ s ≤ p.
func (a *analyzer) decimalArgs(te *astTypeExpr, p pos) (int, int, bool) {
	pr, err1 := strconv.Atoi(te.precision)
	sc, err2 := strconv.Atoi(te.scale)
	if err1 != nil || err2 != nil || pr < 1 || pr > 38 || sc < 0 || sc > pr {
		a.errf(p, CodeInvalidDecimalArgs, fmt.Sprintf("decimal(%s, %s)", te.precision, te.scale))
		return pr, sc, false
	}
	return pr, sc, true
}

// buildForms creates the form trees: node index, member names (N2/N3),
// type resolution (N4/T1–T3), attributes (§4/F2) and defaults (F5).
// Conditions are recorded as sites and checked after the trees exist.
func (a *analyzer) buildForms(def *astDef) {
	for _, fb := range def.forms {
		for _, fd := range fb.decls {
			p := pos{fd.line, fd.col}
			if isUpperStart(fd.name) {
				a.errf(p, CodeNameCase, fd.name)
			}
			if _, dup := a.rootNS[fd.name]; dup {
				a.errf(p, CodeDuplicateName, fd.name)
				continue
			}
			a.rootNS[fd.name] = p
			n := &Node{Kind: "form", Name: fd.name, Path: fd.name, resolved: true, line: p.line, col: p.col}
			a.processAttrs(n, fd.attrs)
			if len(fd.members) == 0 {
				a.warnf(p, CodeEmptyForm, fd.name)
			}
			a.formNodes[fd.name] = n
			a.nodes[fd.name] = n
			a.s.Forms = append(a.s.Forms, n)
			a.buildMembers(n, fd.members)
		}
	}
}

func (a *analyzer) buildMembers(container *Node, ms []astNode) {
	scoped := map[string]bool{}
	for _, m := range ms {
		switch d := m.(type) {
		case *astFieldDecl:
			p := pos{d.line, d.col}
			if !a.memberName(container, d.name, p, scoped) {
				continue
			}
			scoped[d.name] = true
			n := &Node{Kind: "field", Name: d.name, Path: container.Path + "." + d.name, Parent: container, line: p.line, col: p.col}
			container.Nodes = append(container.Nodes, n)
			a.nodes[n.Path] = n
			a.resolveType(n, d.typ)
			if n.Type != nil && n.Type.Base == "signature" && container.Kind == "group" {
				a.errf(p, CodeSignatureInGroup, n.Path)
			}
			a.processAttrs(n, d.attrs)
		case *astGroupDecl:
			p := pos{d.line, d.col}
			if !a.memberName(container, d.name, p, scoped) {
				continue
			}
			scoped[d.name] = true
			n := &Node{Kind: "group", Name: d.name, Path: container.Path + "." + d.name, Parent: container, resolved: true, line: p.line, col: p.col}
			container.Nodes = append(container.Nodes, n)
			a.nodes[n.Path] = n
			a.processAttrs(n, d.attrs)
			if len(d.members) == 0 {
				a.errf(p, CodeEmptyGroup, n.Path) // F1
			}
			a.buildMembers(n, d.members)
		}
	}
}

func (a *analyzer) memberName(container *Node, name string, p pos, scoped map[string]bool) bool {
	if isUpperStart(name) {
		a.errf(p, CodeNameCase, name)
	}
	if scoped[name] {
		a.errf(p, CodeDuplicateName, container.Path+"."+name)
		return false
	}
	return true
}

// resolveType maps a value_type to a resolved *Type or role (N4).
func (a *analyzer) resolveType(n *Node, te *astTypeExpr) {
	if te == nil {
		return
	}
	p := pos{te.line, te.col}
	switch te.base {
	case "string", "integer", "boolean", "date", "datetime", "binary", "options", "signature":
		n.Type = a.builtins[te.base]
		if te.base == "signature" {
			n.resolved = true
			a.sigs = append(a.sigs, n)
			if te.sigRef != nil {
				a.sigRef[n] = te.sigRef
			}
			return
		}
	case "decimal":
		pr, sc, ok := a.decimalArgs(te, p)
		t := &Type{Base: "decimal", Precision: pr, Scale: sc}
		n.Type = t
		if !ok {
			a.bad[t] = true
		}
	case "enum":
		t := &Type{Base: "enum"}
		for _, tg := range te.tags {
			a.declareUpper(pos{tg.line, tg.col}, tg.name, t)
			t.Tags = append(t.Tags, tg.name)
		}
		if len(t.Tags) == 0 {
			a.errf(p, CodeEmptyEnum, n.Path)
		}
		n.Type = t
	default:
		if isUpperStart(te.base) {
			r, ok := a.roles[te.base]
			if !ok {
				a.errf(p, CodeUnknownRole, te.base)
				return
			}
			n.Role = r
			a.usedRoles[te.base] = true
		} else {
			t, ok := a.types[te.base]
			if !ok {
				a.errf(p, CodeUnknownType, te.base)
				return
			}
			n.Type = t
			a.usedTypes[te.base] = true
		}
	}
	n.resolved = true
}

// processAttrs enforces the §4 validity matrix, attribute uniqueness,
// signature-field restrictions and F5 defaults.
func (a *analyzer) processAttrs(n *Node, attrs []*astAttr) {
	seen := map[string]bool{}
	isSig := n.Kind == "field" && n.Type != nil && n.Type.Base == "signature"
	for _, at := range attrs {
		p := pos{at.line, at.col}
		slot := at.kind // required and required[cond] share one slot (§4)
		if slot == "requiredCond" {
			slot = "required"
		}
		switch at.kind {
		case "verifiable":
			if seen[slot] {
				a.errf(p, CodeDuplicateAttribute, at.kind)
				continue
			}
			seen[slot] = true
			if a.verifiableAncestor(n) {
				a.warnf(p, CodeRedundantVerifiable, n.Path) // F3
			}
			n.Verifiable = true
		case "required", "requiredCond", "present", "multiple":
			if n.Kind == "form" {
				a.errf(p, CodeInvalidAttribute, at.kind+" on a form")
				continue
			}
			if isSig && at.kind != "required" {
				a.errf(p, CodeInvalidAttribute, at.kind+" on a signature field")
				continue
			}
			if seen[slot] {
				a.errf(p, CodeDuplicateAttribute, at.kind)
				continue
			}
			seen[slot] = true
			switch at.kind {
			case "required":
				n.Required = true
			case "multiple":
				n.Multiple = true
			default:
				kind := "present"
				if at.kind == "requiredCond" {
					kind = "required"
				}
				a.sites = append(a.sites, &site{owner: n, form: rootForm(n), p: p, kind: kind, x: at.cond, label: n.Path})
			}
		case "default":
			if n.Kind != "field" {
				a.errf(p, CodeInvalidAttribute, "default outside a field")
				continue
			}
			if isSig {
				a.errf(p, CodeInvalidAttribute, "default on a signature field")
				continue
			}
			if seen["default"] {
				a.errf(p, CodeDuplicateAttribute, at.kind)
				continue
			}
			seen["default"] = true
			a.checkDefault(n, at)
		}
	}
}

func (a *analyzer) verifiableAncestor(n *Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Verifiable {
			return true
		}
	}
	return false
}

// resolveSignatures implements S1–S3 reference resolution: role form
// (signature(Role)) xor assigned form (signature(path) → role-typed
// field of the containing form).
func (a *analyzer) resolveSignatures() {
	for _, n := range a.sigs {
		o := a.sigRef[n]
		if o == nil {
			continue // parse failure already reported
		}
		p := pos{o.line, o.col}
		switch {
		case o.kind != "path":
			a.errf(p, CodeInvalidSignatureRef, operandText(o))
		case len(o.segs) == 1 && isUpperStart(o.segs[0]) && !o.relative:
			r, ok := a.roles[o.segs[0]]
			if !ok {
				a.errf(p, CodeUnknownRole, o.segs[0])
				continue
			}
			n.Signer = r
			a.usedRoles[r.Name] = true
		case len(o.segs) == 1 && !o.relative:
			a.errf(p, CodeInvalidSignatureRef, "use ."+o.segs[0]+", not a bare name")
		default:
			f := rootForm(n)
			var target *Node
			wrongForm := false
			if o.relative {
				target = walkPath(f, o.segs)
			} else if o.segs[0] == f.Name {
				target = walkPath(f, o.segs[1:])
			} else {
				wrongForm = true
			}
			switch {
			case wrongForm:
				a.errf(p, CodeInvalidSignatureRef, "must name a role or a field of the containing form: "+operandText(o))
			case target == nil:
				a.errf(p, CodeUnknownTarget, operandText(o))
			case target == n:
				a.errf(p, CodeInvalidSignatureRef, "a signature must not reference itself")
			case !target.resolved:
				// already reported on the target's type
			case target.Role == nil:
				a.errf(p, CodeInvalidSignatureRef, target.Path+" is not a role-typed field")
			default:
				n.SignField = target
				a.usedRoles[target.Role.Name] = true
			}
		}
	}
}

// checkRoleGrants validates role-block grants (P1–P3, P5 inputs).
func (a *analyzer) checkRoleGrants(def *astDef) {
	for _, r := range a.s.Roles {
		d := a.roleAST[r.Name]
		if d == nil {
			continue // duplicate declaration; grants belong to the first
		}
		for _, g := range d.grants {
			r.Grants = append(r.Grants, a.checkGrant(g, r, nil))
		}
	}
}

// checkGrant enforces statement shape (P1), target resolution (P2) and
// permission compatibility (P3); it collects allow/deny keys for P5.
func (a *analyzer) checkGrant(g *astGrant, role *Role, st *Stage) Grant {
	p := pos{g.line, g.col}
	nTrans, other := 0, 0
	for _, x := range g.perms {
		if isTransitionPerm(x) {
			nTrans++
		} else {
			other++
		}
	}
	gr := Grant{Role: role, Deny: g.deny, Perms: append([]string{}, g.perms...)}
	if st == nil {
		if nTrans > 0 {
			a.errf(p, CodeInvalidGrant, "accept and reject exist only in stage blocks")
		}
		if !g.hasTargets {
			a.errf(p, CodeInvalidGrant, "role grants require targets")
		}
	} else if nTrans > 0 {
		if other > 0 || g.hasTargets {
			a.errf(p, CodeInvalidGrant, "accept and reject must be sole permissions without targets")
		}
	} else if !g.hasTargets {
		a.errf(p, CodeInvalidGrant, "targeted grants require at least one target")
	}
	keys := []string{"*"}
	if g.all {
		gr.All = true
	} else {
		keys = nil
		for i, segs := range g.paths {
			path := strings.Join(segs, ".")
			keys = append(keys, path)
			node, ok := a.nodes[path]
			if !ok {
				a.errf(g.pathPos[i], CodeUnknownTarget, path) // P2
				continue
			}
			gr.Nodes = append(gr.Nodes, node)
			a.checkPermShapes(g.perms, node, path, g.pathPos[i])
		}
	}
	if role != nil {
		m := a.denies
		if !g.deny {
			m = a.allows
		}
		rm := m[role.Name]
		if rm == nil {
			rm = map[string]pos{}
			m[role.Name] = rm
		}
		for _, perm := range g.perms {
			for _, k := range keys {
				if _, dup := rm[perm+"\x00"+k]; !dup {
					rm[perm+"\x00"+k] = p
				}
			}
		}
	}
	return gr
}

// checkPermShapes enforces P3.
func (a *analyzer) checkPermShapes(perms []string, node *Node, path string, p pos) {
	isSig := node.Type != nil && node.Type.Base == "signature"
	for _, perm := range perms {
		switch perm {
		case "submit":
			if node.Kind != "form" {
				a.errf(p, CodeIncompatiblePermission, "submit requires a top-level form: "+path)
			}
		case "edit":
			if isSig {
				a.errf(p, CodeIncompatiblePermission, "edit is not granted on signature fields: "+path)
			}
		case "review":
			if !node.Verifiable {
				a.errf(p, CodeIncompatiblePermission, "review requires a verifiable target: "+path)
			}
		case "sign":
			if !isSig {
				a.errf(p, CodeIncompatiblePermission, "sign requires a signature field: "+path)
			}
		}
	}
}

// reportContradictions warns on allow+deny of the same permission and
// target for the same role (P5).
func (a *analyzer) reportContradictions() {
	for _, r := range a.s.Roles {
		for key, dp := range a.denies[r.Name] {
			if _, ok := a.allows[r.Name][key]; ok {
				perm, target, _ := strings.Cut(key, "\x00")
				a.warnf(dp, CodeContradictoryGrants, r.Name+" "+perm+" "+target)
			}
		}
	}
}

// buildStages creates stage objects (ST1 order), resolves grants (P1–P4),
// requires (ST2/ST4) and transitions (ST2/ST3/ST6 sites).
func (a *analyzer) buildStages(def *astDef) {
	var decls []*astStageDecl
	for _, sb := range def.stages {
		decls = append(decls, sb.decls...)
	}
	for _, sd := range decls { // names first: transitions may point forward
		p := pos{sd.line, sd.col}
		if isUpperStart(sd.name) {
			a.errf(p, CodeNameCase, sd.name)
		}
		if _, dup := a.rootNS[sd.name]; dup {
			a.errf(p, CodeDuplicateName, sd.name)
			continue
		}
		a.rootNS[sd.name] = p
		st := &Stage{Name: sd.name}
		a.stages[sd.name] = st
		a.stagePos[sd.name] = p
		a.s.Stages = append(a.s.Stages, st)
	}
	for _, sd := range decls {
		st := a.stages[sd.name]
		if st == nil {
			continue
		}
		for _, g := range sd.grants {
			var role *Role
			if g.role != "" {
				r, ok := a.roles[g.role]
				if !ok {
					a.errf(g.rolePos, CodeUnknownRole, g.role) // P4
				} else {
					role = r
					a.usedRoles[g.role] = true
				}
			}
			st.Grants = append(st.Grants, a.checkGrant(g, role, st))
		}
		for _, rq := range sd.requires {
			for i, name := range rq.forms {
				fn, ok := a.formNodes[name]
				if !ok {
					a.errf(rq.formPos[i], CodeUnknownForm, name) // ST2/ST4
				} else {
					st.Requires = append(st.Requires, fn)
				}
			}
		}
		destSeen := map[string]bool{}
		for _, tr := range sd.transitions {
			dst, ok := a.stages[tr.dest]
			switch {
			case !ok:
				a.errf(tr.destPos, CodeUnknownStage, tr.dest) // ST2
			case tr.dest == sd.name:
				a.errf(tr.destPos, CodeSelfTransition, tr.dest) // ST3
			case destSeen[tr.dest]:
				a.errf(tr.destPos, CodeDuplicateTransition, tr.dest) // ST3
			}
			destSeen[tr.dest] = true
			st.Edges = append(st.Edges, Edge{Reject: tr.reject, Dest: dst})
			if tr.cond != nil {
				a.sites = append(a.sites, &site{stage: st, edgeIdx: len(st.Edges) - 1,
					p: pos{tr.line, tr.col}, kind: "transition", x: tr.cond, label: sd.name})
			}
		}
	}
}

// checkCondition types one condition (§5.7) and attaches the resolved
// expression to its owner.
func (a *analyzer) checkCondition(st *site) {
	e, t := a.checkExpr(st.x, st)
	if t.bad {
		return
	}
	if !t.isBoolExpr {
		a.errf(pos{st.x.line, st.x.col}, CodeNotBoolean, "condition is not a boolean expression") // F6/E4
		return
	}
	st.expr = e
	switch st.kind {
	case "required":
		st.owner.RequiredCond = e
	case "present":
		st.owner.PresentCond = e
	case "transition":
		st.stage.Edges[st.edgeIdx].Cond = e
		if constantExpr(e) {
			a.warnf(st.p, CodeConstantCondition, st.label) // ST6
		}
	}
}

// checkDependencies enforces F7: no condition may reference its own
// declaration or a descendant, and the dependency graph must be acyclic.
func (a *analyzer) checkDependencies() {
	condBearing := map[*Node]bool{}
	for _, st := range a.sites {
		if st.owner != nil {
			condBearing[st.owner] = true
		}
	}
	deps := map[*Node][]*Node{}
	selfReported := map[*Node]bool{}
	for _, st := range a.sites {
		if st.owner == nil {
			continue
		}
		for _, r := range st.refs {
			if r == st.owner || strings.HasPrefix(r.Path, st.owner.Path+".") {
				if !selfReported[st.owner] {
					a.errf(st.p, CodeSelfReference, st.owner.Path)
					selfReported[st.owner] = true
				}
				continue
			}
			if condBearing[r] {
				deps[st.owner] = append(deps[st.owner], r)
			}
		}
	}
	color := map[*Node]int{}
	cyclicReported := map[*Node]bool{}
	var dfs func(n *Node)
	dfs = func(n *Node) {
		color[n] = 1
		for _, m := range deps[n] {
			switch color[m] {
			case 1:
				if !cyclicReported[m] {
					a.errf(pos{m.line, m.col}, CodeCyclicCondition, m.Path)
					cyclicReported[m] = true
				}
			case 0:
				dfs(m)
			}
		}
		color[n] = 2
	}
	for _, st := range a.sites {
		if st.owner != nil && color[st.owner] == 0 {
			dfs(st.owner)
		}
	}
}

// warnUnused reports P5 unused roles and custom types.
func (a *analyzer) warnUnused() {
	for _, r := range a.s.Roles {
		if !a.usedRoles[r.Name] {
			a.warnf(a.upperNS[r.Name], CodeUnusedRole, r.Name)
		}
	}
	for _, t := range a.s.Types {
		if !a.usedTypes[t.Name] {
			a.warnf(a.typePos[t.Name], CodeUnusedType, t.Name)
		}
	}
}

// checkReachability reports ST5: stages unreachable from the initial one.
func (a *analyzer) checkReachability() {
	if a.s.Initial == nil {
		return
	}
	seen := map[*Stage]bool{a.s.Initial: true}
	queue := []*Stage{a.s.Initial}
	for len(queue) > 0 {
		st := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, e := range st.Edges {
			if e.Dest != nil && !seen[e.Dest] {
				seen[e.Dest] = true
				queue = append(queue, e.Dest)
			}
		}
	}
	for _, st := range a.s.Stages {
		if !seen[st] {
			a.warnf(a.stagePos[st.Name], CodeUnreachableStage, st.Name)
		}
	}
}
