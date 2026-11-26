package schema

import (
	"maps"
	"slices"
	"strings"
)

// checkRoleGrants validates role-block grants.
func (a *analyzer) checkRoleGrants() {
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

// checkGrant enforces statement shape, target resolution and permission
// compatibility.
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
				a.errf(g.pathPos[i], CodeUnknownTarget, path)
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

// checkPermShapes rejects permissions that do not apply to the target's
// declaration kind.
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
// target for the same role.
func (a *analyzer) reportContradictions() {
	for _, r := range a.s.Roles {
		for _, key := range slices.Sorted(maps.Keys(a.denies[r.Name])) {
			dp := a.denies[r.Name][key]
			if _, ok := a.allows[r.Name][key]; ok {
				perm, target, _ := strings.Cut(key, "\x00")
				a.warnf(dp, CodeContradictoryGrants, r.Name+" "+perm+" "+target)
			}
		}
	}
}
