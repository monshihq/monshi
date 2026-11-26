package engine

import "github.com/monshihq/monshi/internal/program"

// rolesHeld combines the actor's primary role with assignments in committed
// role fields. Draft edits cannot grant permissions.
//
// The scan is over the definition's role-typed slots — a bounded,
// definition-shaped count, known before any request arrives, so a
// per-case index is not worth its memory here (the permission
// resolve costs more than this loop).
func rolesHeld(img *program.Program, a Actor, s state) []program.RoleID {
	out := make([]program.RoleID, 0, 4)
	if r, ok := img.RoleByName(a.PrimaryRole); ok {
		out = append(out, r)
	}
	// An empty UserID is "nobody", and nobody matches any stored value.
	if a.User == "" {
		return out
	}
	holds := func(want program.RoleID) bool {
		for _, r := range out {
			if r == want {
				return true
			}
		}
		return false
	}
	for n := range img.SlotCount() {
		sl := img.Slot(program.NodeID(n))
		// A role-typed field has no type and no value class: the role it
		// confers is its whole meaning. One inside a collection has no single
		// value in the base row.
		if sl.Kind != program.KindField || sl.Role == 0 || holds(sl.Role) {
			continue
		}
		c, ok := s.cellOf(program.NodeID(n))
		if !ok {
			continue
		}
		// The test is on the *value*, which is what makes the answer
		// independent of how a field came to be empty: the cleared field
		// is unset, one never assigned is unset, and an assignment of an
		// identity is refused — so none of the three matches the non-empty
		// identity this loop reaches with.
		if v := c.get(); v.Set && v.Text == string(a.User) {
			out = append(out, sl.Role)
		}
	}
	return out
}

// canEdit is the authority question an `Update` asks of one slot: does
// this actor, at this stage, hold `edit` on it. It is
// stated here rather than open-coded at the call sites so that the role
// *source* — the committed state, never the candidate — has exactly one
// place to be wrong. `Review` asks the same question of `review`;
// Transition asked the stage masks in its own file, and Sign asks it of
// `sign` when that operation lands.
func canEdit(img *program.Program, a Actor, s state, stage program.StageID, n program.NodeID) bool {
	return img.MayEdit(rolesHeld(img, a, s), stage, n)
}
