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
	// conferring anything. §5.2 says `Empty` clears the value and keeps the
	// field, so the cleared field is unset here and the guard below catches
	// it; the guard in turn is what stops an actor with no identity from
	// matching a role field that names nobody.
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
		// independent of how a field came to be empty: §5.2's cleared field
		// is unset, one never assigned is unset, and an assignment of an
		// identity is refused — so none of the three matches the non-empty
		// identity this loop reaches with.
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
// place to be wrong. `Review` asks the same question of `review` (§5.4);
// Transition asked the stage masks in its own file, and Sign asks it of
// `sign` when that operation lands.
func (img *image) canEdit(a Actor, s state, stage stageID, n nodeID) bool {
	return img.mayEdit(img.rolesHeld(a, s), stage, n)
}

// mayEdit is the same question for roles already folded, which is what an
// operation that asks it of several slots uses: the fold walks the
// definition's role-typed fields, and §3.5's masks are bitwise algebra over
// the result, so folding once per request and asking per slot is the shape
// that keeps §8's permission-resolve cost honest. Splitting the two is also
// what makes the *source* of the roles impossible to get wrong at the call
// site — `Update` folds from the committed snapshot and cannot accidentally
// ask about a slot against the candidate it is writing.
func (img *image) mayEdit(roles []roleID, stage stageID, n nodeID) bool {
	return img.effective(roles, stage, n)&permEdit != 0
}

// maySubmit is the question a `Submit` asks, and it is asked of the *form*
// rather than of anything inside it. SYNTAX P3 is why: a `submit` grant's
// target must be a top-level form, so there is no field-level submit to
// reconcile and no member's mask to fold. The consequence is worth stating
// because it reads as a hole: an actor with `submit` on a form can publish
// every value in it, including fields they may not `edit` — and that is the
// definition's own decision, since a draft can only have been written by
// somebody with `edit` or come from a default the author declared.
//
// The masks are consulted at the *committed* stage, which is the only stage
// a submit can act at: §4.4 keeps the stage where a transition put it, and
// §5.1 makes the working copy's stage a reference rather than a field. A
// draft that would satisfy some other stage's grant does not submit under it.
func (img *image) maySubmit(roles []roleID, stage stageID, form nodeID) bool {
	return img.effective(roles, stage, form)&permSubmit != 0
}

// mayReview is the question `Review` asks, and it is asked at the slot the
// caller's path resolves to. SYNTAX P3 requires a `review` grant's target to
// be a `verifiable` declaration and P2 guarantees the runtime never resolves a
// target, so the bit at a member's slot can only have arrived there by
// inheritance — meaning asking a *unit's* own slot is the same answer with one
// fewer questions to get wrong, and asking at the resolved slot is what lets an
// actor who cannot decide there learn nothing about what governs it.
//
// A `verifiable` *group* therefore has no grant of its own unless the
// definition writes one: its slot inherits its parent's, which is §3.5's
// inheritance and not an exception. A host that wants only a specific reviewer
// for one group says so at that group's path.
func (img *image) mayReview(roles []roleID, stage stageID, n nodeID) bool {
	return img.effective(roles, stage, n)&permReview != 0
}

// maySign is the question `Sign` asks, and it is asked at the signature field's
// own slot for the reason `mayReview` gives about its unit: P3 puts a `sign`
// grant's target on a signature field, so the bit can arrive at any other slot
// only by inheritance from an ancestor — and `allow sign all;` is exactly that,
// naming the case root and reaching every field beneath it. That is the reading
// P2 gives `all` in every other permission's hands, and it means the mask cannot
// be the whole authority question: sign.go asks `sigEntry`'s second conjunct
// beside it, because a bit inherited from the root says the definition opened
// this act to a role and S3 says which hand it opened it to.
func (img *image) maySign(roles []roleID, stage stageID, n nodeID) bool {
	return img.effective(roles, stage, n)&permSign != 0
}
