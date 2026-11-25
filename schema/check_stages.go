package schema

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
