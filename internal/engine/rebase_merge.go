package engine

import (
	"slices"

	"github.com/monshihq/monshi/internal/program"
)

// cellKey and setKey are the two things a rebase compares: one value at one
// instance address, and one collection's rows at one place in the data. They
// are structs of ordinals rather than rendered paths because §7.2's rule that
// nothing keys off letters a caller typed holds at the merge as it does at
// resolution, and an instance path is a slice, which Go will not let a map key
// be.
type cellKey struct {
	slot  program.NodeID
	steps instanceKey
}

type setKey struct {
	coll  program.NodeID
	steps instanceKey
}

// mergedCell and mergedSet are the two things the merge settles: one value at
// one address, and one collection's rows at one place. Each carries the
// address its entries name and the two versions the draft is compared against
// — `base`, what the copy descended from, and `now`, what the newest unseen
// commit left there.
//
// `written` says whether a commit at or before the copy's base ever touched
// this cell, which is the difference between "the base held nothing here" and
// "the base held what creating the row materialized" — and only one of those
// leaves the draft's value as the base's.
type mergedCell struct {
	slot    program.NodeID
	path    instPath
	base    program.Cell
	now     program.Cell
	written bool
}

// mergedSet is one collection's row set. `resolved` says whether the scan
// found a commit at or before the base that moved it, and is not read by the
// merge: an unresolved list and an empty one are the same correct answer,
// since rows come only from a commit that says so. The flag exists so that the
// scan's "every question answered" counter counts each question once.
type mergedSet struct {
	coll     program.NodeID
	host     instPath
	base     []instanceID
	now      []instanceID
	resolved bool
}

// clash is one thing the engine could not decide. A set's clash names the
// collection at the place its rows live; a cell's names the field at the row
// it holds.
//
// `veiled` says the address sits at content this actor may not view. It is
// carried rather than filtered because the clash still has to *happen* — a
// rebase that silently moved a copy's base past an edit the actor made blind
// would be the engine resolving a conflict it was told to report — and only
// the name changes (§5.7's reading of what a container may disclose).
type clash struct {
	slot   program.NodeID
	path   instPath
	veiled bool
}

// rebaser folds a run of commits into one actor's draft. The struct holds the
// merged state, the versions the comparison needs, and the clashes, because
// the walk that finds a clash is the same walk that must not then overwrite
// the value it clashed with, and a function that split those would be a
// function with a flag in its signature.
type rebaser struct {
	img *program.Program
	vis []bool

	// out is the merged draft, `ours` the copy as the caller left it and
	// `cur` the committed snapshot the window describes: three states because
	// the comparison asks what the *draft* changed, which a walk that wrote as
	// it went could not answer afterwards, and what the *case* changed, which
	// the draft has no account of.
	out  state
	ours *state
	cur  *state

	// sets and cells are the merge's work list, in the order the window names
	// them. The first `nsets` collections are the ones the window moved; the
	// rest are the collections its values live in, asked about only to answer
	// whether a row existed at the base revision.
	sets  []mergedSet
	nsets int
	cells []mergedCell

	// touched lists the instances any unseen entry addressed, which is how a
	// row the draft deleted is recognized as one the case also changed.
	touched map[instanceID]bool

	// dropped marks the collections whose rows both sides moved: their
	// members' content is not reported as well, because the clash the caller
	// must resolve is the collection, and sixty field paths beside "the
	// holders changed" would hide the one decision left to the user.
	//
	// lost marks the rows the *draft* removed out from under a committed
	// change. The report is once per collection, for the same reason.
	dropped map[setKey]bool
	lost    map[instanceID]bool

	clashes []clash
}

// run collects the window, resolves the base it descends from, and applies the
// three-way test — sets first, then the collections the window wrote into
// without moving, then the values. The sets come before the values because a
// cell's test needs its row to exist in the merged state to mean anything, and
// the unmoved pass comes between the two because it is the one that decides
// which values this draft no longer has a claim on.
func (m *rebaser) run(current *state, seen, window []commitRecord,
	w *workingState) {
	m.ours = &w.s
	m.cur = current
	m.out = w.s.clone()
	// A draft that adopted rows the case issued must not hand those numbers
	// out again: §5.1's IDs are never reused within a case, so the merged copy
	// inherits the case's allocator along with its content, which is the same
	// reconciliation §5.4's commit makes on the way the other way.
	if m.out.seq < m.cur.seq {
		m.out.seq = m.cur.seq
	}
	m.touched = map[instanceID]bool{}
	m.dropped = map[setKey]bool{}
	m.lost = map[instanceID]bool{}
	m.collect(window)
	m.resolve(seen)

	for i := 0; i < m.nsets; i++ {
		m.mergeSet(&m.sets[i])
	}
	m.settledUnmoved()
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
}

// mergeSet settles one collection's rows: the draft's list, the committed
// list, and the list as the copy descended from it, compared by IDs in order —
// the same comparison §5.6's diff makes, and the only thing a set can be
// compared by when the values inside its rows are compared separately.
func (m *rebaser) mergeSet(s *mergedSet) {
	ours := rowIDs(m.ours.rowsFor(s.coll, s.host).list())
	m.detectLost(s, ours)
	switch {
	case sameIDs(s.now, s.base):
		// The commits left the rows where the copy found them, so the draft's
		// list is the merged one and there is nothing to bring in or drop.
	case sameIDs(ours, s.base):
		// The draft has not moved the rows, so the newer list is the only one
		// anybody asked for.
		m.apply(s)
	case sameIDs(ours, s.now):
		// Both sides arrived at the same list by themselves: there is nothing
		// left to decide, and saying so is not a conflict.
	default:
		// Both sides moved the rows differently. Reconciling two lists is the
		// deferred half of §6.5 — an `Add` on each side is two facts about
		// order and one about which user is looking at the screen — so the
		// draft keeps its own rows, they are marked unresolved, and the
		// entries that write into that set are dropped rather than
		// half-applied.
		m.dropped[m.setKey(s)] = true
		m.clash(s.coll, s.host)
	}
}

// detectLost reports the one deletion a set comparison cannot see: a row this
// draft removed while the unseen commits changed what was inside it. Neither
// side's list says anything about the other's intent, and letting the
// deletion win quietly would swallow a committed edit with no word — the loss
// §6.5 exists to prevent — so the row is marked resolved-away, its own
// members' clashes are suppressed as the same decision, and the collection is
// named once.
//
// A row the *commits* removed is excluded by the list they left: this draft's
// deletion of a row the case also deleted is agreement, not a clash, and
// `mergeSet`'s converged branch says so without asking here.
func (m *rebaser) detectLost(s *mergedSet, ours []instanceID) {
	// A collection inside a row this merge already marked lost is not a second
	// decision: the actor deleted the outer row, and everything inside it went
	// with it. Without this, deleting one holder of a nested list would name
	// `application.holders` *and* every collection under the deleted row.
	if m.lostAbove(s.host) {
		return
	}
	for _, id := range missing(s.base, ours) {
		if !slices.Contains(s.now, id) || !m.touched[id] {
			continue
		}
		m.lost[id] = true
		m.clash(s.coll, s.host)
	}
}

// lostAbove says whether any row this address lives in was lost — that is,
// removed by this draft while the unseen commits changed something inside it.
// The clash is real, and `mergeSet` reports it once at the enclosing
// collection; a per-field list beside it would name the same deleted row sixty
// times, and a per-collection list would name its nested ones as separate
// decisions. The walk is outermost first, which is the order instance paths
// are spelled in (§5.1), and the collections are merged and probed in that
// order too, so an outer row is always marked before the inner one asks.
func (m *rebaser) lostAbove(path instPath) bool {
	for _, st := range path {
		if m.lost[st.id] {
			return true
		}
	}
	return false
}

// settledUnmoved runs the deletion test over the collections the window wrote
// values into without moving their rows. They are not merged — nothing tells
// the case's list to change — and their committed set is by definition the one
// the copy descended from, which is the answer `detectLost` compares against.
func (m *rebaser) settledUnmoved() {
	for i := m.nsets; i < len(m.sets); i++ {
		s := &m.sets[i]
		s.now = s.base
		m.detectLost(s, rowIDs(m.ours.rowsFor(s.coll, s.host).list()))
	}
}

// mergeCell settles one value cell against the three versions: the draft's,
// the newest committed one, and what the copy descended from.
func (m *rebaser) mergeCell(c *mergedCell) {
	if m.dropped[enclosingKey(c.path)] || m.crossesLost(c) {
		return
	}
	ours := m.ours.cellValue(c.slot, c.path)
	switch {
	case m.img.Same(c.slot, c.now, c.base):
		// The commits' net effect on this cell is nothing — it moved away and
		// back, or the entry is only the bookkeeping a row's removal leaves
		// behind. The draft's value stands, and reporting a clash here would
		// be reporting a change the case does not contain.
	case m.img.Same(c.slot, ours, c.base):
		m.pour(c)
	case m.img.Same(c.slot, ours, c.now):
		// Both sides wrote the same thing: nothing to tell anybody.
	default:
		m.clash(c.slot, c.path)
	}
}

// crossesLost says whether this cell lives in — or under — a row the draft
// removed and the commits changed. That clash is real, and it is reported once
// at the collection by `mergeSet`; a per-field list beside it would name the
// same deleted row sixty times.
func (m *rebaser) crossesLost(c *mergedCell) bool { return m.lostAbove(c.path) }

// apply makes one collection's merged set the committed list: rows that
// already exist in the draft keep their identity and their content, so the
// actor's values in a row nobody removed stay exactly where they are, and the
// rows the case has and the draft does not are copied from the committed
// snapshot — by ID, which is what lets the entries after this one find them.
func (m *rebaser) apply(s *mergedSet) {
	dst, ok := m.out.rowsBelow(s.coll, s.host, true)
	if !ok {
		return // the host row is not in the merged data: nothing to set
	}
	src := m.cur.rowsFor(s.coll, s.host)
	keep := make([]instRow, 0, len(s.now))
	for _, id := range s.now {
		if i, alive := dst.find(id); alive {
			keep = append(keep, dst.rows[i])
			continue
		}
		// The case's own row, copied whole so the entries after this one can
		// find it by the ID the case issued. A nil set here means the snapshot
		// has nothing at this place, which the committed list cannot ask for —
		// `rowsFor` answers nil only for an address that is not data.
		if src != nil {
			if i, found := src.find(id); found {
				keep = append(keep, copyRow(src.rows[i]))
			}
		}
	}
	dst.rows = keep
	dst.renumber()
}

// pour writes the committed value into the merged cell, when the address is
// still there. A missing address is not a loss to report: `mergeSet` has
// already adopted the case's row list, so the only way a cell has no home here
// is that the commits removed its row and this draft left that row alone —
// which is exactly the committed change §6.5 says to integrate.
func (m *rebaser) pour(c *mergedCell) {
	if at, ok := m.out.cellAt(c.slot, c.path); ok {
		at.set(c.now)
	}
}

func (m *rebaser) clash(n program.NodeID, p instPath) {
	// The mask is asked at the node and never at its parent, which is §5.7's
	// rule for the same question in `viewable` — one fold, two callers.
	m.clashes = append(m.clashes,
		clash{slot: n, path: p, veiled: !m.vis[n]})
}

func (m *rebaser) cellKey(c *mergedCell) cellKey {
	return cellKey{slot: c.slot, steps: pathKey(c.path)}
}

func (m *rebaser) setKey(s *mergedSet) setKey {
	return setKey{coll: s.coll, steps: pathKey(s.host)}
}

// enclosingKey is the collection a cell's innermost step belongs to, at the
// place its host rows live: the key `dropped` marks. A cell the base row holds
// encloses no collection, and the zero key is never in that map.
func enclosingKey(p instPath) setKey {
	if len(p) == 0 {
		return setKey{}
	}
	st := p[len(p)-1]
	return setKey{coll: st.coll, steps: pathKey(p[:len(p)-1])}
}

// sameIDs compares two instance lists by their IDs in order. An empty list and
// a nil one are the same answer, which is what lets a collection nobody has
// added to compare equal to the list no entry recorded, and is the same rule
// frame.go's `list` gives for a set that does not exist.
func sameIDs(a, b []instanceID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// missing lists what the first set had and the second no longer holds, in the
// older list's order: the rows a draft deleted, which is the question
// `mergeSet` asks when the commits did not move the rows at all.
func missing(was, now []instanceID) []instanceID {
	var out []instanceID
	for _, id := range was {
		if !slices.Contains(now, id) {
			out = append(out, id)
		}
	}
	return out
}

// copyRow brings a committed instance into the merged draft with its own
// identity, content, and nested rows — the last because a rebase that dropped
// the rows *inside* an outer row it was bringing in would be deleting content
// nobody had a chance to refuse, which is the thing §6.5 exists to prevent.
func copyRow(r instRow) instRow {
	out := instRow{id: r.id, row: append(program.Row(nil), r.row...)}
	for _, in := range r.nest {
		out.nest = append(out.nest, in.clone())
	}
	return out
}

// The compiler and storage decoder both bound the nesting depth. Valid row
// IDs are nonzero, so the zero suffix cannot collide with a longer path.
type instanceKey [program.MaxCollDepth]instStep

func pathKey(p instPath) (key instanceKey) { copy(key[:], p); return }
