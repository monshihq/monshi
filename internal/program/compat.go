package program

// dataDrift compares the shape of two images registered under one name and
// says, in one sentence, where they first disagree.
func DataDrift(old, next *Program) (string, bool) {
	if len(old.slots) != len(next.slots) {
		return "the definition has " + dec(uint64(len(next.slots))) +
			" slots, the stored case was built with " +
			dec(uint64(len(old.slots))), false
	}
	for n := range old.slots {
		at, by := old.Path(NodeID(n)), next.Path(NodeID(n))
		// The path is compared first: it is how a stored ordinal is
		// identified, so a slot that moved makes every message after this
		// one about the wrong field.
		if at != by {
			return "slot " + dec(uint64(n)) + " is " + quote(by) +
				" there, and the stored case has " + quote(at), false
		}
		// Two comparisons are enough here, and what they do not check is
		// worth stating. Node kind and subtree extent are implied: a slot
		// that became a group, or gained or lost a descendant, changed the
		// ordinal count or some slot's path, and both were heard above. What
		// is left is the pair of facts a path does not carry — which field
		// of the row the payload lives in, and whether this slot's values
		// are its own or a collection's rows.
		switch {
		case old.BaseOf(NodeID(n)) != next.BaseOf(NodeID(n)):
			// Across value classes the payload lives in a different field
			// of the row (§3.3), so the stored bytes stop meaning anything.
			// Within one class — decimal(14,2) to decimal(14,4) — this test
			// passes, and §3's answer is that old content becomes invalid,
			// not incompatible: the next Submit's validation is the
			// operation entitled to refuse it.
			return quote(by) + " changed its value class", false
		case old.slots[n].Coll != next.slots[n].Coll:
			return quote(by) + " became, or stopped being, a collection", false
		}
	}
	// No separate comparison of the collection list or of any collection's
	// row bounds: `coll`, `span` and `kind` per ordinal determine them —
	// rowLo is the collection's own slot when it is a repeated field and its
	// next ordinal otherwise, and rowLen runs from there to the subtree end
	// (§5.1) — so re-checking them here would compare the same four words
	// twice and imply they could disagree.
	//
	// A stored stage is an ordinal into the stage table, and a stage's name
	// is the only thing saying which declaration that ordinal was. This is
	// also what hears a reordering: the count survives it, and without the
	// names a case would silently move to whichever stage the same number
	// now names. The initial stage needs no separate check for the same
	// reason — it is ordinal 1, so its name is compared here too.
	if len(old.stages) != len(next.stages) {
		return "the definition declares " + dec(uint64(len(next.stages)-1)) +
			" stages, the stored case was built with " +
			dec(uint64(len(old.stages)-1)), false
	}
	for s := StageID(1); int(s) < len(old.stages); s++ {
		if old.StageName(s) != next.StageName(s) {
			return "stage " + dec(uint64(s)) + " is " + quote(next.StageName(s)) +
				" there, and the stored case has " + quote(old.StageName(s)), false
		}
	}
	return "", true
}
