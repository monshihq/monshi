package schema

import (
	"fmt"
	"strconv"
)

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
