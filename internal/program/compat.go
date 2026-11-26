package program

import (
	"crypto/sha256"
	"encoding/binary"
)

// LayoutID identifies the interpretation of stored ordinals, independently
// of defaults, decimal constraints, permissions, hooks and other policy.
func (p *Program) LayoutID() [32]byte { return p.layout }

func (p *Program) ValidStage(n uint64) bool {
	return n < uint64(len(p.stages)) && (n != 0 || p.initial == 0)
}

func layoutID(p *Program) [32]byte {
	var b []byte
	number := func(n uint64) { b = binary.AppendUvarint(b, n) }
	text := func(s string) { number(uint64(len(s))); b = append(b, s...) }
	number(uint64(len(p.slots)))
	for i, s := range p.slots {
		text(p.Path(NodeID(i)))
		number(uint64(s.Kind))
		if s.Role != 0 {
			number(1)
		} else {
			number(0)
		}
		number(uint64(s.Coll))
		base := p.BaseOf(NodeID(i))
		number(uint64(base))
		if base == BaseEnum {
			// Tag ordinals are contiguous within each enum.
			tags := p.types[s.Type].Tags
			number(uint64(len(tags)))
			if len(tags) != 0 {
				number(uint64(tags[0]))
			}
		}
	}
	number(uint64(len(p.tagNames)))
	for i := range p.tagNames {
		text(p.TagName(TagID(i)))
	}
	number(uint64(len(p.stages)))
	for i := range p.stages {
		text(p.StageName(StageID(i)))
	}
	return sha256.Sum256(b)
}

// dataDrift compares the shape of two images registered under one name and
// says, in one sentence, where they first disagree.
func DataDrift(old, next *Program) (string, bool) {
	if old.layout == next.layout {
		return "", true
	}
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
			// of the row, so the stored bytes stop meaning anything.
			// Within one class — decimal(14,2) to decimal(14,4) — this test
			// passes, and the answer is that old content becomes invalid,
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
	// — so re-checking them here would compare the same four words
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
	for i := 1; i < len(old.stages); i++ {
		s := StageID(i)
		if old.StageName(s) != next.StageName(s) {
			return "stage " + dec(uint64(s)) + " is " + quote(next.StageName(s)) +
				" there, and the stored case has " + quote(old.StageName(s)), false
		}
	}
	return "the definition changed the stored declaration or enum layout", false
}
