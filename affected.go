package monshi

import "slices"

// The update fanout (RUNTIME §5.3): which declarations a batch may have
// changed the *view* of.
//
// `Update` writes a handful of cells, and DESIGN §6.2 requires it to answer
// with every path whose view may have moved, because a client that refreshes
// only what it typed keeps showing a subtree the definition stopped including.
// The gap between those two sets is what this file closes, using the adjacency
// lowering already built: F7's condition graph is `img.depFrom`/`img.depOwn`,
// so the answer is a breadth-first walk over array ranges — no map, no
// allocation per edge, and no path string built for a slot that turns out not
// to be reported.
//
// Three rules do the work, and each is a sentence of the language rather than
// a choice made here:
//
//   - A write marks the field it wrote, because that is the key a guard's
//     operand sits under (§3.9's resolution is the only other place a path
//     becomes a slot).
//   - A re-asked owner marks its subtree, because a group whose `present`
//     answer moved changed the visibility of everything under it — including a
//     member some *other* guard reads. Queuing the descendants rather than only
//     reporting them is what stops the closure dying at the group's own path,
//     and §5.1 already says a base row's members are ordinary slots, so they
//     are ordinary keys.
//   - A re-asked owner inside a collection is re-asked *as that collection*.
//     F6 makes a condition inside a `multiple` declaration uniform over it, so
//     the answer that moved is the group's, not one member's; and E2 refuses a
//     guard whose operand traverses a collection, so a member is never a key in
//     the adjacency and there is nothing to propagate from.
//
// The third rule is also why `Add` and `Remove` mark the collection alone. An
// instance appearing or vanishing is a change to that declaration's rows, which
// is what a client re-renders, and no guard can be reading the members
// directly.
//
// The result is a superset by construction, which §5.3 states outright. A
// declaration whose guard was asked again and answered the same way is reported
// beside one whose answer moved, because telling those apart means holding the
// pre-edit state to compare against, and the batch has already overwritten it
// in place. Over-reporting costs a client a redundant fetch; under-reporting is
// a stale screen.
//
// What is deliberately *not* here is a permission component. §3.5's masks are a
// function of stage and role set and of no value at all, and an `Update` moves
// neither: the stage is the committed one, and the roles that answer for it fold
// from committed data (§4.3). So the set of paths this actor may see is
// identical before and after any batch, and "paths whose permission changed"
// contributes nothing to an update's answer — where §5.5's transition moves the
// stage and so does carry that component. Options need no separate component
// either: a field's list must be re-fetched exactly when the field becomes
// reachable or unreachable, or when its own condition moved, and both of those
// are presence answers, which is what the walk computes. See
// TestAffectedReachesAFieldWhoseOptionsBindingWasTouched, the case §5.3 calls
// out.
//
// A commit is this walk's second caller (RUNTIME §5.4, §9). The seed set is
// the commit's own entries, what §5.6's effects erased beside them, and —
// when the commit conferred or withdrew one of *this* actor's roles — the
// topmost nodes whose mask moved, which no adjacency edge can reach because
// §3.5's masks read the role set and no guard's operand does. The committer's
// list then answers exactly what the batch list answers, one act later: what
// may now render differently for the actor who acted.
//
// What stays out of any result, by the argument this file made first: a
// commit moves §3.5's function for the *other* actors a role change reaches,
// one list cannot answer a question per reader, and who learns of it belongs
// to whoever decides whom to notify (DESIGN §6.5) — starting, since §6, from
// the observer record each commit dispatches. The seed rules and the role
// walk below are the caller's half; the fan-out to other actors was never a
// field of anything here.
//
// The one place the walk is charged rather than free is the visited set: one
// byte per slot and one `nodeID` per slot, both sized by the count lowering
// fixed as `depBound`, so the cost of a batch does not depend on what it wrote.
// §8's `BenchmarkUpdateFanout` is that claim in numbers.

// fanout is the re-evaluated set of one batch: the slots it wrote, the
// declarations whose guard reads those, and transitively whatever those moved.
// It is built per batch, not shared, because two racing `Update` calls must not
// be able to see each other's partial closure (invariant 4's discipline about
// state, applied to a scratch array).
type fanout struct {
	img   *image
	mark  []bool   // per slot: reached by the walk, so its view may differ
	queue []nodeID // breadth-first frontier, bounded by img.depBound
}

func newFanout(img *image) *fanout {
	return &fanout{
		img:   img,
		mark:  make([]bool, len(img.slots)),
		queue: make([]nodeID, 0, img.depBound),
	}
}

// wroteField records that one field's cell changed. A group is never the target
// of a write — §5.1's verbs refuse it before this is reached — so the slot is a
// value's own and marking it alone is exact.
func (f *fanout) wroteField(n nodeID) { f.add(n) }

// wroteRows records that an instance of a collection appeared or went away: a
// change to the collection, which is the declaration whose rows a client counts,
// and — by E2 — the only one any guard could be reached through.
func (f *fanout) wroteRows(coll nodeID) { f.add(coll) }

// add marks one slot and puts it in the frontier, which is the only place a slot
// is enqueued. §5.3's bound is that no slot is visited twice, so the queue cannot
// outgrow the slot count no matter what the batch wrote; F7's acyclicity is what
// would make an unbounded walk impossible in the first place, and this is the
// property that makes the walk *finite* without relying on that proof being
// right.
func (f *fanout) add(n nodeID) {
	if f.mark[n] {
		return
	}
	f.mark[n] = true
	f.queue = append(f.queue, n)
}

// reask marks one owner as the unit §5.2 and §5.4 will walk: itself, everything
// under it, and — for an owner whose rows a collection holds — the collection
// rather than the member.
func (f *fanout) reask(owner nodeID) {
	if coll := f.img.collOf(owner); coll != 0 {
		owner = coll
	}
	lo, hi := f.img.subtree(owner)
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
		r := f.img.depFrom[s]
		for _, o := range f.img.depOwn[r.first : r.first+r.n] {
			f.reask(o)
		}
	}

	var out []string
	cover := nodeID(0) // end of the last reported subtree
	for n := range f.mark {
		id := nodeID(n)
		if !f.mark[n] {
			continue
		}
		if id < cover {
			continue // inside a subtree already reported by its own root
		}
		_, hi := f.img.subtree(id)
		out = append(out, f.img.path(id))
		cover = hi
	}
	return out
}

// viewChanges is §5.3's affected set for the one operation that writes no
// value: a stage move, which changes which paths an actor may see by changing
// the stage the masks are resolved at (§3.5's overlays) and nothing else.
//
// The walk is a definition walk rather than a data walk, and that is the whole
// cost story. A condition never reads the stage — `condFn` is a closure over
// one row (RUNTIME §3.4) — so moving a case between stages cannot change what
// is present, what is required, or what any value is: only the permission fold
// takes the stage as an input. So this compares one word per slot and reports
// the topmost difference, and no cell is touched. A transition's affected list
// answers "which views of this case may now be rendered differently", which is
// a question about the actor and the definition rather than about the data.
//
// The collapse rule is §5.3's own: preorder makes an ancestor's range
// contiguous, so the first differing slot is reported and everything it covers
// is skipped, which leaves the result sorted in declaration order with no path
// contained in another. Reporting the group rather than the sixteen fields
// inside it is the honest granularity, because a client told "this subtree may
// render differently" re-reads the subtree.
func (img *image) viewChanges(roles []roleID, from, to stageID) []string {
	if from == to || !img.overlaysDiffer(roles, from, to) {
		return nil
	}
	var out []string
	cover := nodeID(0) // end of the last reported subtree
	for n := nodeID(1); n < nodeID(len(img.slots)); n++ {
		if n < cover {
			continue // inside a subtree already reported by its own root
		}
		was := img.effective(roles, from, n)&permView != 0
		now := img.effective(roles, to, n)&permView != 0
		if was == now {
			continue
		}
		out = append(out, img.path(n))
		_, cover = img.subtree(n)
	}
	return out
}

// overlaysDiffer answers whether any role the actor holds resolves a
// *different* overlay at `to` than it did at `from`. If none does, then
// `permAt` hands back the same allow and deny word for every slot at both
// stages, because a stage reaches §3.5's algebra only by selecting an overlay —
// so the walk above would compare len(slots) identical pairs and report
// nothing. Definitions with no stage-scoped grants are the common case, and the
// test costs one binary search per held role.
//
// Which is a claim about cost and not about the answer: replacing the test with
// "always differ" changes no result a test can see, and that is the intended
// reading — the shortcut may only skip work, never verdicts. What is checked is
// the other direction, that a role whose overlay index *does* move is not
// skipped, because a shortcut that swallows one of those is a lost refresh.
func (img *image) overlaysDiffer(roles []roleID, from, to stageID) bool {
	for _, r := range roles {
		if r == 0 {
			continue // ordinal 0 is "no role": it sees the same at any stage
		}
		over := img.perms.byRole[r].over
		if overlayIndex(over, from) != overlayIndex(over, to) {
			return true
		}
	}
	return false
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
func seedCommit(fan *fanout, changes []commitChange, erased []nodeID) {
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
func roleSetsMatch(a, b []roleID) bool {
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

// roleChanges is `viewChanges`' mirror, asked of the argument a commit can
// move and a stage move cannot: two role sets at the one stage the case sits
// at. The skip that keeps the common case free is the caller's own — equal
// folded lists mean every slot was handed the same inputs, which is
// overlaysDiffer's argument transposed to the other parameter, and it is a
// claim about cost in the same way: the walk may only skip work, never a
// verdict.
func (img *image) roleChanges(from, to []roleID,
	stage stageID) []nodeID {
	var out []nodeID
	cover := nodeID(0)
	for n := nodeID(1); n < nodeID(len(img.slots)); n++ {
		if n < cover {
			continue
		}
		was := img.effective(from, stage, n)&permView != 0
		now := img.effective(to, stage, n)&permView != 0
		if was == now {
			continue
		}
		out = append(out, n)
		_, cover = img.subtree(n)
	}
	return out
}
