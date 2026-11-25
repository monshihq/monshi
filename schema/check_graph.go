package schema

import "strings"

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
