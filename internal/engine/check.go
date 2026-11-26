package engine

import "github.com/monshihq/monshi/internal/program"

// gap identifies a declaration whose effective requirement the state does not
// meet.
type gap struct {
	path string
	kind uint8
}

// The shapes a gap takes, which a host renders differently because the remedy
// is different: a field needs a value, a collection needs an instance, a
// verifiable unit needs a decision, a signature field needs a signature. The
// second is why `Missing` cannot be a flat "empty fields" list. `gapSig` is the
// kind with no fifth in front of it because a signature is asked for by *name*:
// the walk reports a value, and the scan reports the signature a form declares
// and does not hold. The kinds are not carried into the rendered list, so a
// host sees one list of paths it must resolve by looking at what the path is.
const (
	gapValue uint8 = iota + 1 // needs a value, holds none or an empty one
	gapRows                   // needs at least one instance, has none
	gapVerif                  // a `verifiable` unit is not APPROVED
	gapSig                    // a required signature field is not signed
)

// incomplete collects unmet requirements in declaration order within one
// subtree.
//
// The walk is an index loop over the dense preorder range, which is what the
// numbering buys: pruning an unreachable subtree costs one arithmetic step
// rather than a visit per descendant, and the order the author declared is the
// order the gaps come back in for free. That order is load-bearing rather than
// cosmetic.
func (s *state) incomplete(root program.NodeID) []gap {
	var out []gap
	lo, hi := s.img.Subtree(root)
	for n := lo; n < hi; n++ {
		if !s.present(n) {
			// An absent subtree contributes no requirements, even if it
			// contains required fields.
			n += program.NodeID(s.img.Slot(n).Span) - 1
			continue
		}
		if !s.asked(n) {
			continue
		}
		sl := s.img.Slot(n)
		if sl.Sig != 0 {
			// A signature field is asked about as a signature and never as a
			// value: `asked` above already says the definition wants one, and
			// the answer is `sigEntry`'s, which the scan reads and a
			// `Sign` writes. Counting it here would report it in the sentence
			// that tells a caller to fill something in, for a field `Update`
			// refuses to touch (value.go's `store`) — and a required
			// signature would then make its own form permanently unsubmitable
			// while `Sign`'s precondition wants exactly that submit first.
			continue
		}
		switch {
		case sl.Coll != 0:
			// A `multiple` declaration's requiredness is at least one
			// element — asked once per place its rows live, which
			// for a collection inside a collection is per outer instance.
			// `hostsOf` and not `pathsOf`: the question is about the set, and
			// for a repeated field the two are not the same list.
			for _, host := range s.hostsOf(n) {
				if c, ok := s.countAt(n, host); !ok || c == 0 {
					out = append(out, gap{s.addr(n, host), gapRows})
				}
			}
		case sl.Kind != program.KindField:
			// A form or plain group holds nothing: its requiredness is that
			// it is reached, which `present` above and the pruning of its
			// ancestors already answered (the third line, "nothing at all").
		default:
			// `asked` settled the guard half of the second line, so what is
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

// asked says whether the second line wants anything of this slot at all:
// plainly `required`, or `required[cond]` whose condition holds. It is
// stated apart from `fills` because the walk above asks it of every slot and
// asks the per-instance questions only of the few that need them: for a
// definition with two required fields in a hundred, that is the difference
// between two row walks and a hundred.
//
// The polarity is the one `satisfied` already commits to.
func (s *state) asked(n program.NodeID) bool {
	sl := s.img.Slot(n)
	if sl.Flags&program.FlagRequired != 0 {
		return true
	}
	// The guard reads the base row, as every guard does, which is what
	// makes "uniform over the collection" true of this answer too.
	return sl.CondReq != 0 && s.img.Cond(sl.CondReq)(s.row)
}

// cellValue is the state's answer for a slot at an instance path: the cell the
// path names, or an unset payload when it names nothing.
func (s *state) cellValue(n program.NodeID, p instPath) program.Cell {
	c, ok := s.cellAt(n, p)
	if !ok {
		return program.Cell{}
	}
	return c.get()
}

// addr is the image's spelling of an address (image.go), reachable from a
// state because every walk that reports a path has one of these to hand. The
// renderer consults no data, so a state is not needed to answer it and the
// history read asks the image the same question.
func (s *state) addr(n program.NodeID, p instPath) string { return addr(s.img, n, p) }

// gapPaths renders gaps as the list the response carries: paths in
// the order they were found, with the duplicates a per-instance walk can
// produce for one declaration collapsed to one entry.
//
// Collapsing is right because the question is about the *form*, and a
// host's answer is one "fill this in" message per field, not one per row:
// five missing members of a collection are one field the user has not filled
// five times. The path that survives is the first, which is the lowest
// instance — declaration order of rows is creation order, so the
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
