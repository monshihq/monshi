package monshi

// Completeness and instance addressing (RUNTIME §5.2, §5.4; DESIGN §6.3).
//
// §5.4's Submit check is one sentence — walk the form's subtree and ask
// §5.2 of every slot — but the sentence has three parts that the definition
// tree alone cannot answer: which slots this state asks anything about, how
// many times each is asked, and what a failed answer is *called*. This file
// is those three, and nothing else: the guard tests themselves are
// frame.go's, and the refusal that carries the result is submit.go's.
//
// The two questions multiply in the same place, and the reason is the
// language's rather than this implementation's. E2 keeps a collection's
// members out of every condition, so a guard's answer is uniform over the
// collection it sits in: one `present`, one `required`, for all rows. What
// is *not* uniform is the value — a member has one cell per instance, so
// §5.2's note that such a slot "has one answer per enclosing instance" is
// the rule this file's iteration exists to apply, and F5's "unreachable ⇒
// not required" is the rule that decides there is nothing to iterate.
//
// Everything here reads. Nothing here writes, normalizes or refuses: those
// belong to the operation that asks.

// gap is one §5.4 incompleteness: a declaration path the state is asking
// something of, and is not being given.
type gap struct {
	path string
	kind uint8
}

// The shapes a gap takes, which a host renders differently because the
// remedy is different: a field needs a value, a collection needs an
// instance, a verifiable unit needs a decision, a signature field needs a
// signature. The second is why `Missing` cannot be a flat "empty fields"
// list — a required `multiple` with no rows has no empty field to point at —
// and the third and fourth are why an entry may name a declaration nobody can
// type into: DESIGN §4.4's destination `require` asks for approvals and
// signatures as well as values, and §6.4 says a `Review` and a `Sign` are the
// only ways to answer them. `gapSig` is the kind with no fifth in front of it
// because a signature is asked for by *name*: §5.4's walk reports a value, and
// §5.10's scan reports the signature a form declares and does not hold. The
// kinds are not carried into the rendered list, so a host sees one list of
// paths it must resolve by looking at what the path is.
const (
	gapValue uint8 = iota + 1 // needs a value, holds none or an empty one
	gapRows                   // needs at least one instance, has none
	gapVerif                  // a `verifiable` unit is not APPROVED (§4.2)
	gapSig                    // a required signature field is not signed (§4.2)
)

// incomplete walks one subtree of one state and returns every gap §5.4 would
// refuse a submit for, in declaration order.
//
// The walk is an index loop over the dense preorder range, which is what
// RUNTIME §3.2's numbering buys: pruning an unreachable subtree costs one
// arithmetic step rather than a visit per descendant, and the order the
// author declared is the order the gaps come back in for free. That order is
// load-bearing rather than cosmetic: it is the same preorder §5.3's
// `affected` list renders in, and a host that shows a form top to bottom
// should not meet its fields out of sequence because the engine walked a map.
func (s *state) incomplete(root nodeID) []gap {
	var out []gap
	lo, hi := s.img.subtree(root)
	for n := lo; n < hi; n++ {
		if !s.present(n) {
			// §5.2's pruning, and SYNTAX F5's licence to stop asking: a false
			// `present` takes its subtree out of the effective state, so
			// nothing under it is required and nothing under it is reported.
			n += nodeID(s.img.slots[n].span) - 1
			continue
		}
		if !s.asked(n) {
			continue
		}
		sl := &s.img.slots[n]
		if sl.sig != 0 {
			// A signature field is asked about as a signature and never as a
			// value: `asked` above already says the definition wants one, and
			// the answer is `sigEntry`'s, which §5.10's scan reads and a
			// `Sign` writes. Counting it here would report it in the sentence
			// that tells a caller to fill something in, for a field `Update`
			// refuses to touch (value.go's `store`) — and a required
			// signature would then make its own form permanently unsubmitable
			// while `Sign`'s precondition wants exactly that submit first.
			continue
		}
		switch {
		case sl.coll != 0:
			// A `multiple` declaration's requiredness is at least one
			// element (SYNTAX §4) — asked once per place its rows live, which
			// for a collection inside a collection is per outer instance.
			// `hostsOf` and not `pathsOf`: the question is about the set, and
			// for a repeated field the two are not the same list.
			for _, host := range s.hostsOf(n) {
				if c, ok := s.countAt(n, host); !ok || c == 0 {
					out = append(out, gap{s.addr(n, host), gapRows})
				}
			}
		case sl.kind != kindField:
			// A form or plain group holds nothing: its requiredness is that
			// it is reached, which `present` above and the pruning of its
			// ancestors already answered (§5.2's third line, "nothing at all").
		default:
			// `asked` settled the guard half of §5.2's second line, so what is
			// left is its other half — the same pair `satisfied` applies in one
			// step to a value somebody handed it.
			for _, p := range s.pathsOf(n) {
				if !s.fills(n, s.cellValue(n, p)) {
					out = append(out, gap{s.addr(n, p), gapValue})
				}
			}
		}
	}
	return out
}

// asked says whether §5.2's second line wants anything of this slot at all:
// plainly `required`, or `required[cond]` whose condition holds. It is
// stated apart from `fills` because the walk above asks it of every slot and
// asks the per-instance questions only of the few that need them: for a
// definition with two required fields in a hundred, that is the difference
// between two row walks and a hundred.
//
// The polarity is the one `satisfied` already commits to, and it is worth
// stating because the two readings are one character apart and the wrong one
// demands a field the definition said nothing about: a `required[cond]` slot
// is asked when the condition holds and excused when it does not — E6 makes
// an unsatisfied operand unsatisfied rather than true, so a guard whose
// operand was never entered demands nothing.
func (s *state) asked(n nodeID) bool {
	sl := &s.img.slots[n]
	if sl.flags&flagRequired != 0 {
		return true
	}
	// The guard reads the base row, as every guard does (E2), which is what
	// makes "uniform over the collection" true of this answer too.
	return sl.condReq != 0 && s.img.cond(sl.condReq)(s.row)
}

// cellValue is the state's answer for a slot at an instance path: the cell
// the path names, or an unset payload when it names nothing. The second half
// matters: a path built from rows this state no longer has is not an error,
// it is a field with no value, and §5.2 already says what that means for
// `required`.
func (s *state) cellValue(n nodeID, p instPath) slotValue {
	c, ok := s.cellAt(n, p)
	if !ok {
		return slotValue{}
	}
	return c.get()
}

// addr spells the path a report gives for a slot at an instance path — the
// text a host reads, and the text `Update` accepts back to fix it.
//
// The rule is DESIGN §5.1's: instances are addressed by their stable IDs,
// spelled after the collection they belong to, so "application.holders.42.who"
// names one member of one instance and resolves through §3.9's walk to the
// cell that gap is about. A report whose paths could not be edited would be
// a screenshot rather than a result.
//
// The splice works because a declaration path is the ancestor names in
// order: each step's collection path is a prefix of the slot's own, so the
// step's ID goes at that prefix's end and the remaining names follow it,
// outermost first. Nothing is looked up in the data, so a removed instance
// keeps the address it was reported at — which is what a host that still has
// it on screen needs.
func (s *state) addr(n nodeID, p instPath) string {
	full := s.img.path(n)
	if len(p) == 0 {
		return full
	}
	// Each step's ID goes at the end of its collection's own path, which is
	// cumulative: `f.holders.homes.city` takes holder 1 after "f.holders"
	// and home 3 after "f.holders.homes", so the second splice starts where
	// the first stopped and the text between them — the inner declaration's
	// own name — is emitted once. A step whose collection path is not this
	// slot's prefix is not a step of this slot, and the loop stops rather
	// than emitting text resolution would refuse: the declaration path alone
	// is a degraded answer, and a wrong one is not.
	var b []byte
	last := 0
	for _, st := range p {
		cp := s.img.path(st.coll)
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

// gapPaths renders gaps as the list DESIGN §6.1's response carries: paths in
// the order they were found, with the duplicates a per-instance walk can
// produce for one declaration collapsed to one entry.
//
// Collapsing is right because §5.4's question is about the *form*, and a
// host's answer is one "fill this in" message per field, not one per row:
// five missing members of a collection are one field the user has not filled
// five times. The path that survives is the first, which is the lowest
// instance — declaration order of rows is creation order (§5.1), so the
// first address is the one a client can navigate to.
func gapPaths(gs []gap) []string {
	if len(gs) == 0 {
		return nil
	}
	out := make([]string, 0, len(gs))
	for i, g := range gs {
		if i > 0 && gs[i-1].path == g.path {
			continue
		}
		out = append(out, g.path)
	}
	return out
}
