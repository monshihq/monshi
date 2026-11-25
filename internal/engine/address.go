package engine

import "github.com/monshihq/monshi/internal/program"

// addr spells the path a report gives for a slot at an instance path — the
// text a host reads, and the text `Update` accepts back to fix it.
//
// The rule is DESIGN §5.1's: instances are addressed by their stable IDs,
// spelled after the collection they belong to, so "application.holders.42.who"
// names one member of one instance and resolves through §3.9's walk to the
// cell that gap is about. A report whose paths could not be edited would be a
// screenshot rather than a result.
//
// The splice works because a declaration path is the ancestor names in order:
// each step's collection path is a prefix of the slot's own, so the step's ID
// goes at that prefix's end and the remaining names follow it, outermost
// first. A step whose collection path is not this slot's prefix is not a step
// of this slot, and the loop stops rather than emitting text resolution would
// refuse: the declaration path alone is a degraded answer, and a wrong one is
// not.
//
// Nothing is looked up in the data, so a removed instance keeps the address it
// was reported at — which is what a host that still has it on screen needs, and
// what lets §5.11 render a commit entry without asking what the case holds
// today: an entry's address is a question about the definition and the IDs the
// entry carries, and never about rows that are gone.
func addr(img *program.Program, n program.NodeID, p instPath) string {
	full := img.Path(n)
	if len(p) == 0 {
		return full
	}
	var b []byte
	last := 0
	for _, st := range p {
		cp := img.Path(st.coll)
		if len(cp) < last || len(cp) > len(full) ||
			full[:len(cp)] != cp {
			break
		}
		b = append(b, full[last:len(cp)]...)
		b = append(b, '.')
		b = append(b, dec(uint64(st.id))...)
		last = len(cp)
	}
	return string(append(b, full[last:]...))
}
