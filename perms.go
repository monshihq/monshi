package monshi

// Lowering, pass: permissions (RUNTIME §3.5, §4 rows P1–P6).
//
// Every grant becomes bits on slots, and scope inheritance becomes one
// fold along the parent chain in preorder — so DESIGN §4.3's
// Case → Form → Group → Field lookup at request time is two array reads,
// an overlay probe when the stage has one, and an AND-NOT. Nothing here
// resolves a name: P2 turned every target into a declaration and P4 turned
// every grant subject into a role ordinal.

import (
	"sort"

	"github.com/monshihq/monshi/schema"
)

// pendingBits is the accumulated, not yet emitted overlay data: for one
// role and one stage, the bits each touched slot gains or loses.
type overlayBits struct {
	add uint8
	del uint8
}

// overlays indexes roleID → stageID → slot. Emitting sorted arrays turns a
// sparse grant set into the binary-searchable form §3.5 describes.
type overlays map[roleID]map[stageID]map[nodeID]overlayBits

func (l *lowerer) permissions() {
	n := len(l.img.slots)
	tables := make([]permTable, len(l.s.Roles)+1)
	for r := range tables {
		tables[r] = permTable{allow: make([]uint8, n), deny: make([]uint8, n)}
	}
	l.img.perms = permTables{byRole: tables}

	pend := overlays{}
	l.roleGrants(tables)
	l.stageGrants(pend)
	l.buildOverlays(tables, pend)
	l.foldScope(tables, n)
}

// roleGrants folds the role block: unconditional grants for the enclosing
// role, and the only place targeted permissions can come from at all.
func (l *lowerer) roleGrants(tables []permTable) {
	for _, role := range l.s.Roles {
		r := l.roleIDOf(role)
		if r == 0 {
			continue
		}
		for _, g := range role.Grants {
			if l.transitionPerms(g, role.Name) {
				continue // P1 made this an error; reported, not laundered
			}
			bits := l.permBits(g, role.Name)
			if bits == 0 {
				continue
			}
			for _, s := range l.grantTargets(g, role.Name) {
				if g.Deny {
					tables[r].deny[s] |= bits
				} else {
					tables[r].allow[s] |= bits
				}
			}
		}
	}
}

// stageGrants reads the stage blocks. Targeted grants are stage-scoped and
// accumulate in the per-role/per-stage overlay map; accept and reject are
// transition authority and go to the stage's role masks, which is the only
// representation they have (P1, §3.7).
func (l *lowerer) stageGrants(pend overlays) {
	for _, st := range l.s.Stages {
		sid := l.stageOf[st]
		if sid == 0 {
			l.reportf(st.Name, "stage was not numbered")
			continue
		}
		info := &l.img.stages[sid]
		for _, g := range st.Grants {
			if g.Role == nil {
				l.reportf(st.Name, "a grant names no declared role") // P4
				continue
			}
			r := l.roleIDOf(g.Role)
			if r == 0 {
				continue
			}
			if l.transitionPerms(g, st.Name) {
				l.transitionBits(info, r, g)
				continue
			}
			bits := l.permBits(g, st.Name)
			if bits == 0 {
				continue
			}
			byStage := pend[r]
			if byStage == nil {
				byStage = map[stageID]map[nodeID]overlayBits{}
				pend[r] = byStage
			}
			bySlot := byStage[sid]
			if bySlot == nil {
				bySlot = map[nodeID]overlayBits{}
				byStage[sid] = bySlot
			}
			for _, s := range l.grantTargets(g, st.Name) {
				// A stage grant on a container reaches its subtree, exactly
				// like a role grant, so the overlay lists every slot it
				// touches instead of the declared target alone.
				for t := s; t < s+nodeID(l.img.slots[s].span); t++ {
					cur := bySlot[t]
					if g.Deny {
						cur.del |= bits
					} else {
						cur.add |= bits
					}
					bySlot[t] = cur
				}
			}
		}
	}
}

// buildOverlays emits the accumulated bits as sorted, sparse per-role
// overlays. Map order is irrelevant: every list is sorted before it is
// written, so two runs over the same definition produce the same image.
func (l *lowerer) buildOverlays(tables []permTable, pend overlays) {
	for r, byStage := range pend {
		if int(r) >= len(tables) {
			l.reportf("", "a stage grant names role %d, not declared", r)
			continue
		}
		stages := make([]stageID, 0, len(byStage))
		for at := range byStage {
			stages = append(stages, at)
		}
		sort.Slice(stages, func(i, j int) bool { return stages[i] < stages[j] })
		for _, at := range stages {
			bySlot := byStage[at]
			over := stageOverlay{at: at, slots: make([]nodeID, 0, len(bySlot))}
			slots := make([]nodeID, 0, len(bySlot))
			for s := range bySlot {
				slots = append(slots, s)
			}
			sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
			for _, s := range slots {
				over.slots = append(over.slots, s)
				over.add = append(over.add, bySlot[s].add)
				over.del = append(over.del, bySlot[s].del)
			}
			tables[r].over = append(tables[r].over, over)
		}
	}
}

// transitionBits records accept and reject authority for one role. A deny
// is kept as its own mask instead of clearing the allow bit, because P6
// lets a deny held through one role strip a permission granted through
// another, and roles are only known per request.
func (l *lowerer) transitionBits(info *stageInfo, r roleID, g schema.Grant) {
	bit := roleMask(1) << (r - 1)
	for _, p := range g.Perms {
		switch {
		case p == "accept" && g.Deny:
			info.acceptNo |= bit
		case p == "accept":
			info.accept |= bit
		case p == "reject" && g.Deny:
			info.rejectNo |= bit
		case p == "reject":
			info.reject |= bit
		}
	}
}

// transitionPerms reports whether a grant is a transition grant, flagging
// the malformed shape: accept and reject exist only in stage blocks, alone,
// and without targets (P1).
func (l *lowerer) transitionPerms(g schema.Grant, at string) bool {
	nTrans, other := 0, 0
	for _, p := range g.Perms {
		if p == "accept" || p == "reject" {
			nTrans++
		} else {
			other++
		}
	}
	if nTrans == 0 {
		return false
	}
	if other != 0 || g.All || len(g.Nodes) != 0 {
		l.reportf(at, "accept and reject must be sole permissions without targets")
	}
	return true
}

// permBits maps a grant's permission names to bits. The five targeted
// permissions are the whole set; anything else means the front end emitted
// a grant the language does not have.
func (l *lowerer) permBits(g schema.Grant, at string) uint8 {
	var bits uint8
	for _, p := range g.Perms {
		switch p {
		case "view":
			bits |= permView
		case "edit":
			bits |= permEdit
		case "submit":
			bits |= permSubmit
		case "review":
			bits |= permReview
		case "sign":
			bits |= permSign
		default:
			l.reportf(at, "permission %q is not a targeted permission", p)
		}
	}
	return bits
}

// grantTargets resolves a grant's target set to slots. "all" is the case
// root, because every declaration inherits from it; anything else is
// already a declaration (P2).
func (l *lowerer) grantTargets(g schema.Grant, at string) []nodeID {
	if g.All {
		return []nodeID{0}
	}
	out := make([]nodeID, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		s, ok := l.slotOf[n]
		if !ok {
			l.reportf(at, "grant target %s is not a declaration", n.Path) // P2
			continue
		}
		out = append(out, s)
	}
	return out
}

// foldScope turns a grant on a container into a grant on everything under
// it. Slots are numbered preorder, so a parent always precedes its children
// and one linear pass inherits the bits: the compiler walks the scope chain
// once instead of every request (DESIGN §4.3).
func (l *lowerer) foldScope(tables []permTable, n int) {
	for r := range tables {
		allow, deny := tables[r].allow, tables[r].deny
		for s := 1; s < n; s++ {
			p := l.img.slots[s].parent
			allow[s] |= allow[p]
			deny[s] |= deny[p]
		}
	}
}
