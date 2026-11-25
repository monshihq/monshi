package engine

import (
	"slices"

	"github.com/monshihq/monshi/internal/program"
)

// fanout is the re-evaluated set of one batch: the slots it wrote, the
// declarations whose guard reads those, and transitively whatever those moved.
// It is built per batch, not shared, because two racing `Update` calls must not
// be able to see each other's partial closure (invariant 4's discipline about
// state, applied to a scratch array).
type fanout struct {
	img   *program.Program
	mark  []bool           // per slot: reached by the walk, so its view may differ
	queue []program.NodeID // breadth-first frontier, bounded by img.depBound
}

func newFanout(img *program.Program) *fanout {
	return &fanout{
		img:   img,
		mark:  make([]bool, img.SlotCount()),
		queue: make([]program.NodeID, 0, img.FanoutBound()),
	}
}

// wroteField records that one field's cell changed. A group is never the target
// of a write — §5.1's verbs refuse it before this is reached — so the slot is a
// value's own and marking it alone is exact.
func (f *fanout) wroteField(n program.NodeID) { f.add(n) }

// wroteRows records that an instance of a collection appeared or went away: a
// change to the collection, which is the declaration whose rows a client counts,
// and — by E2 — the only one any guard could be reached through.
func (f *fanout) wroteRows(coll program.NodeID) { f.add(coll) }

// add marks one slot and puts it in the frontier, which is the only place a slot
// is enqueued. §5.3's bound is that no slot is visited twice, so the queue cannot
// outgrow the slot count no matter what the batch wrote; F7's acyclicity is what
// would make an unbounded walk impossible in the first place, and this is the
// property that makes the walk *finite* without relying on that proof being
// right.
func (f *fanout) add(n program.NodeID) {
	if f.mark[n] {
		return
	}
	f.mark[n] = true
	f.queue = append(f.queue, n)
}

// reask marks one owner as the unit §5.2 and §5.4 will walk: itself, everything
// under it, and — for an owner whose rows a collection holds — the collection
// rather than the member.
func (f *fanout) reask(owner program.NodeID) {
	if coll := f.img.CollOf(owner); coll != 0 {
		owner = coll
	}
	lo, hi := f.img.Subtree(owner)
	for d := lo; d < hi; d++ {
		f.add(d)
	}
}

// affected finishes the closure and renders it. The walk is the whole cost: for
// each queued slot, the owners that read it, each re-asked as above.
//
// Rendering is one ascending pass over the marks, which preorder makes
// meaningful: a marked slot is reported unless an already-reported slot's span
// covers it, so a group that was re-asked stands for its subtree and a member
// re-asked on its own is named exactly. That leaves the result with two
// properties a host can rely on — sorted in declaration order, and no path
// contained in another — and they are asserted twice, in `affected_test.go`
// from a returned result and in `fanout_test.go` against the marks, because a
// later change to the collapse is invisible in a diff and visible only to a
// client that stopped receiving the paths it asked for.
func (f *fanout) affected() []string {
	for head := 0; head < len(f.queue); head++ {
		s := f.queue[head]
		for _, o := range f.img.Dependents(s) {
			f.reask(o)
		}
	}

	var out []string
	cover := program.NodeID(0) // end of the last reported subtree
	for n := range f.mark {
		id := program.NodeID(n)
		if !f.mark[n] {
			continue
		}
		if id < cover {
			continue // inside a subtree already reported by its own root
		}
		_, hi := f.img.Subtree(id)
		out = append(out, f.img.Path(id))
		cover = hi
	}
	return out
}

// seedCommit feeds one commit's facts into a walk, and the entry's kind
// decides the shape — the same question §5.6's effects and §5.11's rendering
// ask of every entry, because what a client re-renders is a function of what
// kind of fact moved. A value marks its cell and a row set marks the
// collection, which is §5.3's Add/Remove rule at the other end: the entry
// keys the container, and a client counts rows per declaration. A decision
// re-asks the unit's subtree, because the verdict renders across it — this is
// the one place the §5.9 answer says "the group, not the flag". An
// attestation marks its field, whose rendering is the field's own.
//
// `erased` is what the store's effects removed and the diff does not record:
// a reset writes no entry beside the content that caused it (DESIGN §4.2),
// so without this seed a client would learn of a vanished decision only by
// rendering it. The units come back as `reask` seeds for the same reason a
// verdict entry does: what moved is the fact shown across the unit.
func seedCommit(fan *fanout, changes []commitChange, erased []program.NodeID) {
	for _, ch := range changes {
		switch ch.kind {
		case chgValue:
			fan.wroteField(ch.slot)
		case chgRows:
			fan.wroteRows(ch.slot)
		case chgReview:
			fan.reask(ch.slot)
		case chgSign:
			fan.wroteField(ch.slot)
		}
	}
	for _, n := range erased {
		fan.reask(n)
	}
}

// roleSetsMatch asks two folded role lists the one question a commit cares
// about: whether §3.5's function was handed the same input. `rolesHeld`
// orders by data rather than by ordinal, so membership is the comparison and
// the lengths are its cheap no.
func roleSetsMatch(a, b []program.RoleID) bool {
	if len(a) != len(b) {
		return false
	}
	for _, r := range a {
		if !slices.Contains(b, r) {
			return false
		}
	}
	return true
}
