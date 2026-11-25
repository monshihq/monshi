package schema

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
