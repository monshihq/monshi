package monshi

// The commit diff (DESIGN §5.1, §6.3; RUNTIME §5.6).
//
// A commit records a forward diff: which declarations moved, at which
// instance, and what each holds now. Computing it is the question §6.3 asks
// before the commit is written — "an empty diff means no commit", and a
// submit whose content matches the committed snapshot writes nothing — and
// it is the same comparison the commit's entry needs as its content, so one
// walk answers both.
//
// Three shapes have to be compared, and only the first is a straight cell
// test:
//
//   - a value the base row holds: one comparison;
//   - a member of a collection: one per instance, at the address §5.1 gives
//     with the instance's stable ID. The instances compared are the union of
//     the two states' rows, because an instance that exists in only one of
//     them is exactly what a commit has to say something about, and a
//     removed instance's values come out as absences — which §5.1 counts as
//     a semantic change, "assignment/removal", rather than as nothing;
//   - the instance set itself: recorded as the collection's new count,
//     because a row appearing or vanishing is a change to the declaration's
//     rows rather than to any one value. The same distinction §5.3's fanout
//     draws between a write and an `Add`.
//
// Nesting needs no special case, which is worth saying because it is the
// part that looks like it would. A collection declared inside a collection
// has its host paths derived from its parent's instances (frame.go's
// `hostsOf`), so the outer instance that only the committed state has is
// visited here too, and its inner rows are compared as the removal they are.
// Every path a diff entry carries names instances by stable ID, so an entry
// about a nested row says both IDs and stays findable after a renumber —
// §5.1's reason for stable IDs in the first place.
//
// Nothing here reads a value the definition has no slot for, resolves a
// name, or writes anything. RUNTIME §5.6's reset and invalidate targets
// index these entries directly, which is what makes that claim O(1) per
// changed slot rather than a search.

import "slices"

// differ accumulates one commit's forward diff over two states.
type differ struct {
	old, next *state
	out       []commitChange
}

// diffForm compares two states over one subtree and returns the changes that
// landed. `old` is the committed snapshot the candidate descends from and
// `next` the candidate; the order matters because the diff records `next`'s
// values (§5.1: new values, never old), and it is why a submit of one form
// reports nothing about another (§6.3's diff limited to the selected form).
func diffForm(old, next *state, root nodeID) []commitChange {
	d := differ{old: old, next: next}
	img := old.img
	lo, hi := img.subtree(root)
	for n := lo; n < hi; n++ {
		sl := &img.slots[n]
		switch {
		case sl.coll != 0:
			// A collection's own rows, wherever they live: a nested one is
			// reached by the same call, because its hosts are its parent's
			// instances and this loop visits every slot in the subtree.
			d.set(n)
		case sl.kind == kindField && img.collOf(n) == 0:
			d.value(n, nil)
		default:
			// A member of a collection is compared per instance inside `set`,
			// and structure has no value to compare: §5.2's two answers, and
			// the reason a group never appears in a diff as itself.
		}
	}
	return d.out
}

// value records a change to one field at one address, if there is one. An
// unset payload is a change when the other state had a value: that is how a
// committed `Empty`, or a removed instance's content, reaches the entry (§5.1:
// a forward diff records the removal as an effect).
func (d *differ) value(n nodeID, p instPath) {
	v := d.next.cellValue(n, p)
	if d.old.img.same(n, d.old.cellValue(n, p), v) {
		return
	}
	d.out = append(d.out, commitChange{kind: chgValue, slot: n, path: p, v: v})
}

// set compares one collection's instance set at each place it has rows in
// either state, recording a moved set and then the rows' contents.
func (d *differ) set(coll nodeID) {
	img := d.old.img
	for _, host := range d.hosts(coll) {
		before := d.old.rowsFor(coll, host).list()
		after := d.next.rowsFor(coll, host).list()
		if !sameRows(before, after) {
			// The count is the forward fact about a set. Which instances
			// exist is answerable from the paths the entries below name, and
			// §5.1's diff has no room for a list of IDs that are gone.
			d.out = append(d.out, commitChange{
				kind: chgRows, slot: coll, path: host,
				v: slotValue{set: len(after) > 0, i: int64(len(after))},
			})
		}
		lo, hi := img.rowLo(coll), coll+nodeID(img.slots[coll].span)
		for _, id := range unionRows(after, before) {
			p := append(host[:len(host):len(host)], instStep{coll: coll, id: id})
			// The row's own fields: the slots this collection's rows cover,
			// whose innermost value-owner is this collection. That test is
			// what keeps a deeper collection's members out of this loop —
			// they are compared when their own set is visited, at the address
			// that includes this instance's ID. A repeated *field* passes it
			// too, and correctly: its rows are its own values (§5.1).
			for x := lo; x < hi; x++ {
				if img.slots[x].kind == kindField && img.collOf(x) == coll {
					d.value(x, p)
				}
			}
		}
	}
}

// hosts is where a collection's row set lives in either state, the new
// state's places first. An instance that was removed leaves the collections
// inside it with no host in the new state, and their content is part of what
// this commit removed — so the union is not a nicety, it is what makes a
// `Remove` of an outer row say anything at all about the inner rows.
func (d *differ) hosts(coll nodeID) []instPath {
	out := d.next.hostsOf(coll)
	for _, p := range d.old.hostsOf(coll) {
		if !hasPath(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// sameRows compares instance sets by their IDs in order, which is the whole
// question "did the rows move": a value inside a row is compared separately,
// so a set is the same when the same instances exist in the same order, and
// §5.1's creation order is what makes the order meaningful.
func sameRows(a, b []instRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].id != b[i].id {
			return false
		}
	}
	return true
}

// unionRows lists the instances the new state holds, in creation order, then
// the ones only the committed state had. That order is the diff's: the
// content that exists now is what a replay lands on, and rows that are gone
// are reported after it, as the absences they are.
func unionRows(now, was []instRow) []instanceID {
	out := make([]instanceID, 0, len(now)+len(was))
	for _, r := range now {
		out = append(out, r.id)
	}
	for _, r := range was {
		if !slices.Contains(out, r.id) {
			out = append(out, r.id)
		}
	}
	return out
}

// hasPath answers whether one of these instance paths names the same
// instances, in the same order. Paths are compared by their steps rather
// than as values because `instPath` is a slice, and a slice is not
// comparable — the steps inside it are.
func hasPath(ps []instPath, p instPath) bool {
	for _, q := range ps {
		if len(q) != len(p) {
			continue
		}
		same := true
		for i := range p {
			if q[i] != p[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}
