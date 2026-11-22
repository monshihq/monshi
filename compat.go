package monshi

// What a definition change does to existing data (DESIGN §3, §5;
// invariant 8).
//
// A definition is not versioned. After a host's explicit reload the same
// stored bytes are interpreted through the newly loaded definition, so
// something has to answer when that is meaningful and when it is nonsense.
// The answer here is: when the ordinal space and the path space are the two
// the case was written into, and nothing else.
//
// Case data is ordinals and paths. A value belongs to a slot ordinal; an
// instance row covers a fixed range of ordinals; the case's stage is a stage
// ordinal; a commit's diff is keyed by a field path (DESIGN §5.1), and
// §5.1's stable paths are how a host's saved selection and milestone 3's
// `affected` reporting address a value. A reworded condition, a changed
// permission or a retyped field within its value class alters what the
// engine *says* about those ordinals, which is exactly what §3 asks
// reinterpretation to do. Inserting, deleting, renaming or reordering a
// declaration alters which ordinal *is* which field, and no amount of
// reinterpreting fixes that: the data would simply start answering a
// different question, or a stored instance row would be read against a
// wider slot range than it was written for.
//
// So a rename is a structural change here, and that is worth stating
// plainly because it is the surprising half. An ordinal's identity is
// knowable across two compilations only by the name its author gave it — the
// language has no declaration UUID for the engine to fall back on — and
// dropping a name from the path space would leave committed history
// pointing at a path that no longer resolves. Refusing is the honest answer
// for every other case too: it is a *false* refusal only for a change that
// was in fact harmless, and never a silent reinterpretation of which stage a
// case is at, which is the failure mode DESIGN §9's SCHEMA_MISMATCH exists
// to name.
//
// What that leaves `Replace` accepting is the purely semantic edit: new
// guards, new permission words, new stage transitions, a tightened decimal,
// a changed default. Anything that adds or moves a declaration is a
// migration, and a migration is the host's decision about its existing
// cases — not something this package performs by rewriting stored data
// (invariant 8). A host that needs both makes a new definition ID.
//
// The comparison costs one pass over the slot array and one over the stage
// table, and runs only when a case's stored image is not pointer-identical
// to the registered one. The common case is pointer equality and costs
// nothing: §7.4's immutability is what makes identity an adequate answer to
// "same schema?".

// dataDrift compares the shape of two images registered under one name and
// says, in one sentence, where they first disagree.
func dataDrift(old, next *image) (string, bool) {
	if len(old.slots) != len(next.slots) {
		return "the definition has " + dec(uint64(len(next.slots))) +
			" slots, the stored case was built with " +
			dec(uint64(len(old.slots))), false
	}
	for n := range old.slots {
		at, by := old.path(nodeID(n)), next.path(nodeID(n))
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
		case old.baseOf(nodeID(n)) != next.baseOf(nodeID(n)):
			// Across value classes the payload lives in a different field
			// of the row (§3.3), so the stored bytes stop meaning anything.
			// Within one class — decimal(14,2) to decimal(14,4) — this test
			// passes, and §3's answer is that old content becomes invalid,
			// not incompatible: the next Submit's validation is the
			// operation entitled to refuse it.
			return quote(by) + " changed its value class", false
		case old.slots[n].coll != next.slots[n].coll:
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
	for s := stageID(1); int(s) < len(old.stages); s++ {
		if old.stageName(s) != next.stageName(s) {
			return "stage " + dec(uint64(s)) + " is " + quote(next.stageName(s)) +
				" there, and the stored case has " + quote(old.stageName(s)), false
		}
	}
	return "", true
}
