package monshi

// Who holds which roles (DESIGN §4.1, §4.3; SYNTAX §5.2-T4). Appendix A's
// runtime trace starts here: "Role set = primary role ∪ committed
// `Manager` via `slotReviewer`".
//
// Two sources, and the split is the security property. The primary role is
// asserted by the host and is the only thing it gets to assert (DESIGN
// §4.3); everything else is read out of *committed* data. A working edit
// confers nothing, so a user who sets `application.reviewer = "me"` in
// their own draft is not a Manager until that value is committed — which
// is why the state this reads is always the committed snapshot, and why
// §6.2's candidate evaluation cannot grant itself authority by writing a
// field.

// rolesHeld answers T4 for one actor against one state. The result is a
// handful of ordinals in declaration order, and §3.5's masks already know
// how to combine them: a role held twice is one role.
//
// The scan is over the definition's role-typed slots — a bounded,
// definition-shaped count, known before any request arrives, so a
// per-case index is not worth its memory here (RUNTIME §8's permission
// resolve costs more than this loop).
func (img *image) rolesHeld(a Actor, s state) []roleID {
	out := make([]roleID, 0, 4)
	if r, ok := img.roleByName(a.PrimaryRole); ok {
		out = append(out, r)
	}
	// An empty UserID is "nobody", and nobody matches any stored value.
	// UserID's doc says why; what belongs here is that the test is made
	// outside the loop because it is a property of the actor, not of a
	// slot — and that it is also what keeps a *cleared* role field from
	// conferring anything. §5.2 is explicit that an emptied field holds an
	// empty value rather than an absent one, so without this guard the two
	// empties would compare equal and a field nobody is assigned to would
	// hand its role to whoever has no identity.
	if a.User == "" {
		return out
	}
	holds := func(want roleID) bool {
		for _, r := range out {
			if r == want {
				return true
			}
		}
		return false
	}
	for n := range img.slots {
		sl := &img.slots[n]
		// A role-typed field has no type and no value class (§3.3): the
		// role it confers is its whole meaning. One inside a collection has
		// no single value in the base row, so it confers nothing — a
		// definition that wants a role assigned per instance would need a
		// case-level field to name the holder, and E2 already says a
		// condition cannot read a member either (§5.1).
		if sl.kind != kindField || sl.role == 0 || holds(sl.role) {
			continue
		}
		c, ok := s.cellOf(nodeID(n))
		if !ok {
			continue
		}
		// The test is on the *value*, which is what makes the answer
		// independent of how a field came to be empty: §5.2's cleared value
		// is set-but-empty, an unset one is not set, and neither matches the
		// non-empty identity this loop reaches with.
		if v := c.get(); v.set && v.s == string(a.User) {
			out = append(out, sl.role)
		}
	}
	return out
}

// canEdit is the authority question an `Update` asks of one slot: does
// this actor, at this stage, hold `edit` on it (DESIGN §6.2). It is
// stated here rather than open-coded at the call sites so that the role
// *source* — the committed state, never the candidate — has exactly one
// place to be wrong. Milestone 5's Review, Sign and Transition ask the
// same question of `review`, `sign` and the stage masks.
func (img *image) canEdit(a Actor, s state, stage stageID, n nodeID) bool {
	return img.effective(img.rolesHeld(a, s), stage, n)&permEdit != 0
}
