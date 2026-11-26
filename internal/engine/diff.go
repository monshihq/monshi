package engine

import (
	"slices"

	"github.com/monshihq/monshi/internal/program"
)

// differ accumulates one commit's forward diff over two states.
type differ struct {
	old, next *state
	out       []commitChange
}

// diffForm compares two states over one subtree and returns the changes that
// landed. `old` is the committed snapshot the candidate descends from and
// `next` the candidate; the order matters because the diff records `next`'s
// values (new values, never old), and it is why a submit of one form
// reports nothing about another (the diff limited to the selected form).
func diffForm(old, next *state, root program.NodeID) []commitChange {
	d := differ{old: old, next: next}
	img := old.img
	lo, hi := img.Subtree(root)
	for n := lo; n < hi; n++ {
		sl := img.Slot(n)
		switch {
		case sl.Coll != 0:
			// A collection's own rows, wherever they live: a nested one is
			// reached by the same call, because its hosts are its parent's
			// instances and this loop visits every slot in the subtree.
			d.set(n)
		case sl.Kind == program.KindField && img.CollOf(n) == 0:
			d.value(n, nil)
		default:
			// A member of a collection is compared per instance inside `set`,
			// and structure has no value to compare: the two answers, and
			// the reason a group never appears in a diff as itself.
		}
	}
	return d.out
}

// value records a change to one field at one address, if there is one. An unset
// payload is a change when the other state had a value: that is how a committed
// `Empty`, or a removed instance's content, reaches the entry (a forward diff
// records the removal as an effect).
func (d *differ) value(n program.NodeID, p instPath) {
	v := d.next.cellValue(n, p)
	if d.old.img.Same(n, d.old.cellValue(n, p), v) {
		return
	}
	d.out = append(d.out, commitChange{kind: chgValue, slot: n, path: p, v: v})
}

// set compares one collection's instance set at each place it has rows in
// either state, recording a moved set and then the rows' contents.
func (d *differ) set(coll program.NodeID) {
	img := d.old.img
	for _, host := range d.hosts(coll) {
		before := d.old.rowsFor(coll, host).list()
		after := d.next.rowsFor(coll, host).list()
		if !sameRows(before, after) {
			// The count is the forward fact about a set, and the ID list is the
			// same fact spelled far enough to be *used*: the rebase compares a
			// draft's rows against the set as it stood at the copy's base, and
			// a count cannot say which rows those were. The reset and
			// invalidate indexing does not read the list — it is per-slot, not
			// per-row — so this costs a commit only what its own diff already
			// knows.
			d.out = append(d.out, commitChange{
				kind: chgRows, slot: coll, path: host,
				ids: rowIDs(after),
			})
		}
		lo, hi := img.RowLo(coll), coll+program.NodeID(img.Slot(coll).Span)
		for _, id := range unionRows(after, before) {
			p := append(host[:len(host):len(host)], instStep{coll: coll, id: id})
			// The row's own fields: the slots this collection's rows cover,
			// whose innermost value-owner is this collection. That test is
			// what keeps a deeper collection's members out of this loop —
			// they are compared when their own set is visited, at the address
			// that includes this instance's ID. A repeated *field* passes it
			// too, and correctly: its rows are its own values.
			for x := lo; x < hi; x++ {
				if img.Slot(x).Kind == program.KindField && img.CollOf(x) == coll {
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
func (d *differ) hosts(coll program.NodeID) []instPath {
	out := d.next.hostsOf(coll)
	for _, p := range d.old.hostsOf(coll) {
		if !hasPath(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// sameRows compares instance sets by their IDs in order, which is the whole
// question "did the rows move": a value inside a row is compared separately, so
// a set is the same when the same instances exist in the same order, but the
// creation order is what makes the order meaningful.
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

// rowIDs lists a set's instance IDs in order, or nil when it has none.
func rowIDs(rows []instRow) []instanceID {
	if len(rows) == 0 {
		return nil
	}
	out := make([]instanceID, len(rows))
	for i, r := range rows {
		out[i] = r.id
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
