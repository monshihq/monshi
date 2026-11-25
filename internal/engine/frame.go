package engine

import "github.com/monshihq/monshi/internal/program"

// instanceID is a collection instance's stable identity, the thing a path
// segment names — "application.shareholders.42" (DESIGN §5.1). The runtime
// issues them and never reuses one within a state, so a host's saved path
// keeps pointing where it pointed.
type instanceID uint64

// instStep is one instance named in a path: which collection, which row in
// it. A path from the case root is a list of steps, outermost first, and it
// is what §3.9's walk produces — addressing is data, and only these two
// small integers survive it.
type instStep struct {
	coll program.NodeID
	id   instanceID
}

// instPath addresses an instance, or the parent of one.
type instPath []instStep

// cell is one addressed value: the slot it belongs to, and where its row
// keeps it. A slot outside every collection is addressed by its own ordinal
// in the base row; a member is addressed by its offset from the collection's
// row start, which is possible only because numbering is preorder (§3.2).
// Carrying the pair, instead of recomputing it, is what lets a read, a write
// and a view say the same thing about one value.
type cell struct {
	slot program.NodeID
	row  program.Row
	idx  int
}

func (c cell) get() program.Cell { return c.row[c.idx] }

func (c cell) set(v program.Cell) { c.row[c.idx] = v }

// clear is DESIGN §5.1's `Empty`: the value is gone, the field is not.
// Presence is a guard's answer and never a value's absence, so a cleared
// field stays part of the structure and still fails `required` (§5.2).
func (c cell) clear() { c.row[c.idx] = program.Cell{} }

// instRow is one instance: its stable ID, its values, and any collection
// declared inside it. A member of "shareholders.42.addresses.7" lives in the
// inner row and not in the outer one, which is why a member's offset is
// always taken from the innermost enclosing collection (§5.1).
type instRow struct {
	id   instanceID
	row  program.Row
	nest []*instances
}

// instances is one collection's rows, in the order they were created, plus
// the map that makes a stable ID addressable. That map is the only place in
// the runtime where a key taken from case data indexes anything (§3.9), and
// it is bounded by the collection's own row count.
type instances struct {
	coll  program.NodeID
	rows  []instRow
	index map[instanceID]int
}

// count answers "does this collection have instances" for callers that may
// hold a nil set: a collection nobody has added to has no set at all.
func (i *instances) count() int {
	if i == nil {
		return 0
	}
	return len(i.rows)
}

// find locates an instance's position by stable ID.
func (i *instances) find(id instanceID) (int, bool) {
	if i == nil {
		return 0, false
	}
	k, ok := i.index[id]
	return k, ok
}

// sub finds a collection nested inside this row. The scan is bounded by how
// many collections one collection declares — a handful — so a second index
// is not worth its memory here.
func (r *instRow) sub(coll program.NodeID) (*instances, bool) {
	for _, in := range r.nest {
		if in.coll == coll {
			return in, true
		}
	}
	return nil, false
}

// state is one snapshot of a case's data: the base row plus every
// collection's rows. A committed snapshot and a candidate working copy are
// the same shape, which is what lets one condition run against either
// (§5.1's swap).
type state struct {
	img   *program.Program
	row   program.Row
	insts []*instances // indexed by slot.coll-1; the sets a state holds
	seq   instanceID   // next stable ID; part of the case's data (§5.1)
}

// newState builds an empty state: rows sized, nothing stored. Defaults are a
// separate step — see applyDefaults for why they must be.
func newState(img *program.Program) state {
	s := state{
		img:   img,
		row:   make(program.Row, img.SlotCount()),
		insts: make([]*instances, len(img.Collections())),
	}
	for i, c := range img.Collections() {
		s.insts[i] = &instances{coll: c, index: map[instanceID]int{}}
	}
	return s
}

// applyDefaults materializes every declared default this state does not
// already have a value for (§5.1: defaults are there before any condition
// runs). It belongs to *creating* data — a new case, a new instance — and
// not to reading it: a field the host cleared with Empty stays cleared, and
// a definition change that adds a default never silently fills persisted
// values (DESIGN §3, invariant 8).
func (s *state) applyDefaults() {
	for n := range s.img.SlotCount() {
		id := program.NodeID(n)
		if s.img.CollOf(id) == 0 {
			s.materialize(id)
		}
	}
}

// materialize writes one slot's default if the slot has one and no value.
func (s *state) materialize(n program.NodeID) {
	def := s.img.Slot(n).Def
	if def == 0 {
		return
	}
	if c, ok := s.cellOf(n); ok && !c.get().Set {
		c.set(s.img.ConstOf(def))
	}
}

// clone copies a state deeply enough that editing one cannot reach the other.
// Rows are value slices, so a copy is a copy; the payloads inside are scalars
// or values the language treats as immutable — a decimal, a string, and an
// opaque BinaryRef naming retained content (DESIGN §5.2).
func (s *state) clone() state {
	out := state{
		img:   s.img,
		row:   append(program.Row(nil), s.row...),
		insts: make([]*instances, len(s.insts)),
		seq:   s.seq,
	}
	for i, in := range s.insts {
		ni := &instances{coll: in.coll, index: make(map[instanceID]int, len(in.rows))}
		for k := range in.rows {
			nr := instRow{
				id:  in.rows[k].id,
				row: append(program.Row(nil), in.rows[k].row...),
			}
			for _, sub := range in.rows[k].nest {
				nr.nest = append(nr.nest, sub.clone())
			}
			ni.index[nr.id] = k
			ni.rows = append(ni.rows, nr)
		}
		out.insts[i] = ni
	}
	return out
}

// clone copies a collection's rows one level down from an instance.
func (i *instances) clone() *instances {
	out := &instances{coll: i.coll, index: make(map[instanceID]int, len(i.rows))}
	for k := range i.rows {
		nr := instRow{
			id:  i.rows[k].id,
			row: append(program.Row(nil), i.rows[k].row...),
		}
		for _, sub := range i.rows[k].nest {
			nr.nest = append(nr.nest, sub.clone())
		}
		out.index[nr.id] = k
		out.rows = append(out.rows, nr)
	}
	return out
}

// frame holds the two states one operation reasons about: what is committed,
// and what the actor is about to commit (§5.1). A condition takes a row and
// cannot tell which of the two it came from, which is why Update's candidate
// evaluation and Submit's final one run the same closures.
type frame struct {
	img    *program.Program
	commit state
	work   state
}

// newFrame opens a frame for a case with no data yet: an empty committed
// snapshot with its defaults materialized, and a working copy of it.
func newFrame(img *program.Program) *frame {
	commit := newState(img)
	commit.applyDefaults()
	return &frame{img: img, commit: commit, work: commit.clone()}
}

// rows returns a top-level collection's row set. The dense ordinal lowering
// assigned makes this one index rather than a search (§5.1).
func (s *state) rows(coll program.NodeID) *instances {
	c := s.img.Slot(coll).Coll
	if c == 0 {
		return nil
	}
	return s.insts[c-1]
}

// rowsBelow resolves the set a collection's instances live in, creating the
// nested set when create asks for it. path addresses the enclosing instances,
// outermost first.
func (s *state) rowsBelow(coll program.NodeID, path instPath, create bool) (*instances, bool) {
	if s.img.Slot(coll).Coll == 0 {
		return nil, false
	}
	if len(path) == 0 {
		set := s.rows(coll)
		return set, set != nil
	}
	// Reach the instance the path ends at, then look inside it.
	row, ok := s.rowAtPath(path)
	if !ok {
		return nil, false // the address itself is not data
	}
	if set, ok := row.sub(coll); ok {
		return set, true
	}
	if !create {
		return nil, true // a valid address with no rows yet: zero, not failure
	}
	set := &instances{coll: coll, index: map[instanceID]int{}}
	row.nest = append(row.nest, set)
	return set, true
}

// rowAtPath walks an instance path to the instance it names. Each step is
// resolved inside the row the step before it reached, so any nesting depth
// uses this one function — and a stale ID anywhere on the path stops it.
func (s *state) rowAtPath(path instPath) (*instRow, bool) {
	if len(path) == 0 {
		return nil, false
	}
	set := s.rows(path[0].coll)
	var row *instRow
	for _, step := range path {
		if row != nil {
			sub, ok := row.sub(step.coll)
			if !ok {
				return nil, false
			}
			set = sub
		}
		k, ok := set.find(step.id)
		if !ok {
			return nil, false
		}
		row = &set.rows[k]
	}
	return row, true
}

// cellOf addresses a slot's value in this state's base row, and refuses any
// slot with no single value there: a form or group is structure (its
// requiredness is its presence, §5.2), a member of a collection lives one row
// per instance (which is why E2 keeps members out of conditions), and a
// collection or repeated field holds instances rather than a payload.
func (s *state) cellOf(n program.NodeID) (cell, bool) {
	sl := s.img.Slot(n)
	if n >= program.NodeID(len(s.row)) || sl.Kind != program.KindField || sl.Coll != 0 ||
		s.img.CollOf(n) != 0 {
		return cell{}, false
	}
	return cell{slot: n, row: s.row, idx: int(n)}, true
}

// cellAt addresses a member inside the instance a path names. The path's last
// step must be the innermost collection enclosing n, which is what §3.9's
// walk guarantees.
func (s *state) cellAt(n program.NodeID, path instPath) (cell, bool) {
	if len(path) == 0 {
		return s.cellOf(n)
	}
	step := path[len(path)-1]
	if s.img.CollOf(n) != step.coll {
		return cell{}, false
	}
	// A repeated group's own slot has rows, not a value; a repeated field is
	// its own member, so the row does hold its value (§5.1). A group inside a
	// collection is structure, like any other group.
	if s.img.Slot(n).Kind != program.KindField {
		return cell{}, false
	}
	if s.img.Slot(n).Coll != 0 && s.img.RowLo(n) != n {
		return cell{}, false
	}
	row, ok := s.rowAtPath(path)
	if !ok {
		return cell{}, false
	}
	off := int(n - s.img.RowLo(step.coll))
	if off < 0 || off >= len(row.row) {
		return cell{}, false
	}
	return cell{slot: n, row: row.row, idx: off}, true
}

// addAt creates an empty instance of one collection and returns its stable ID
// and position. DESIGN §5.1's `Add` takes no value, so the row holds only
// materialized defaults. The position — not a pointer — comes back because
// appending to the set moves the rows.
func (s *state) addAt(coll program.NodeID, path instPath) (instanceID, int, bool) {
	set, ok := s.rowsBelow(coll, path, true)
	if !ok {
		return 0, 0, false
	}
	s.seq++
	id := s.seq
	r := instRow{id: id, row: make(program.Row, s.img.RowLen(coll))}
	set.index[id] = len(set.rows)
	set.rows = append(set.rows, r)
	s.defaults(&set.rows[len(set.rows)-1], coll)
	return id, len(set.rows) - 1, true
}

// countAt is a collection's instance count at one place in the data: its
// whole set for a top-level collection, one outer instance's rows for a nested
// one. §5.4's completeness walk over instances asks it per row.
func (s *state) countAt(coll program.NodeID, path instPath) (int, bool) {
	set, ok := s.rowsBelow(coll, path, false)
	if !ok {
		return 0, false
	}
	return set.count(), true
}

// defaults materializes one instance row's declared defaults. Members of a
// nested collection are not filled: they have rows of their own.
func (s *state) defaults(r *instRow, coll program.NodeID) {
	lo := s.img.RowLo(coll)
	for off := range r.row {
		n := lo + program.NodeID(off)
		if s.img.Slot(n).Coll != 0 {
			continue // a nested collection holds rows, not a value
		}
		if def := s.img.Slot(n).Def; def != 0 {
			r.row[off] = s.img.ConstOf(def)
		}
	}
}

// list is a set's rows, nil-safely: a collection nobody has added to has no
// set at all (§5.1), and "it has no instances" should not depend on whether
// somebody happened to touch it first. The rows are returned in creation
// order, which is the order §5.1 says they are kept in and therefore the
// order a report reads as the list the user filled in.
func (i *instances) list() []instRow {
	if i == nil {
		return nil
	}
	return i.rows
}

// instancesOf lists every instance of one collection as the path that
// addresses it, outermost first.
func (s *state) instancesOf(coll program.NodeID) []instPath {
	var out []instPath
	for _, host := range s.hostsOf(coll) {
		for _, r := range s.rowsFor(coll, host).list() {
			out = append(out,
				append(host[:len(host):len(host)], instStep{coll: coll, id: r.id}))
		}
	}
	return out
}

// hostsOf lists the instance paths at which a collection's row *set* lives:
// the empty path for one the state holds itself, one per instance for one
// declared inside another collection. `rowsIn` is what decides, and §5.1's
// repeated-field rule is why it is not `collOf` — a repeated field's rows are
// its own, not its parent's, even though its value lives in a row.
func (s *state) hostsOf(coll program.NodeID) []instPath {
	outer := s.img.RowsIn(coll)
	if outer == 0 {
		return []instPath{nil}
	}
	return s.instancesOf(outer)
}

// pathsOf lists the instance paths n's values live at: one empty path for a
// slot the base row holds, one per instance for a member of a collection, and
// one per own instance for a repeated field, whose rows hold its values.
//
// The recursion through the enclosing collection is what makes `multiple`
// inside `multiple` honest: a slot two collections deep has one answer per
// pair of instances, and stopping at the innermost row set would ask one of
// them. The language puts no bound on the nesting (SYNTAX §4 allows
// `multiple` on a field and on a group), so the walk does not assume one
// either; §3.9's depth bound is a path-resolution limit, and an address built
// here is one step per enclosing collection by construction.
//
// A path is built from its parent's with a bounded copy, so no two returned
// paths share a tail a caller could write through. These states are read only
// today, but `instPath` is a slice, and a walk that handed out aliased
// addresses would be one append away from reporting a different field.
func (s *state) pathsOf(n program.NodeID) []instPath {
	switch c := s.img.CollOf(n); {
	case c == 0:
		return []instPath{nil}
	case c == n:
		return s.instancesOf(n) // a repeated field: its own rows hold it
	default:
		return s.instancesOf(c)
	}
}

// graft copies one subtree's committed content out of another state, in place:
// the values the base row holds, and every top-level collection's rows whose
// declaration is inside the range. A collection nested inside another comes
// with its parent's rows, because it lives inside them (§5.1), so one pass
// over the preorder range is the whole subtree.
//
// This is what makes a Submit commit one form of a working copy rather than
// the whole copy: DESIGN §6.3 is explicit that other forms' unsubmitted edits
// survive one form's submit, so the new snapshot is the *old* snapshot with
// this subtree replaced — not the candidate, which may hold another form's
// draft. The clone on the way in is what keeps the two states from sharing a
// row the next edit could write.
func (s *state) graft(from *state, lo, hi program.NodeID) {
	for n := lo; n < hi; n++ {
		sl := s.img.Slot(n)
		if sl.Coll != 0 {
			// Only a set the state holds itself: the rows of a collection
			// declared inside another one live *in* those rows, so grafting
			// the outer set brought the inner content with it — which is why
			// this is a replace and not a merge. An instance the candidate
			// removed is gone from the committed content too.
			if s.img.RowsIn(n) != 0 {
				continue
			}
			*s.insts[sl.Coll-1] = *from.insts[sl.Coll-1].clone()
			continue
		}
		if sl.Kind != program.KindField || s.img.CollOf(n) != 0 {
			continue // structure, and a member's value belongs to a row
		}
		s.row[n] = from.row[n]
	}
}

// removeAt drops one instance and everything nested inside it. path addresses
// the enclosing rows, empty for a top-level collection. Stable IDs are not
// reused, so the allocator never moves backwards (§5.1).
func (s *state) removeAt(coll program.NodeID, path instPath, id instanceID) bool {
	set, ok := s.rowsBelow(coll, path, false)
	if !ok {
		return false
	}
	k, ok := set.find(id)
	if !ok {
		return false
	}
	set.rows = append(set.rows[:k], set.rows[k+1:]...)
	set.renumber()
	return true
}

// renumber rebuilds the ID map after a removal. It costs O(rows) in the one
// operation that needs it, which keeps the common path — append — free of
// bookkeeping.
func (set *instances) renumber() {
	for id := range set.index {
		delete(set.index, id)
	}
	for k := range set.rows {
		set.index[set.rows[k].id] = k
	}
}

// present is §5.2's first line: the slot's own guard, if it has one. A guard
// reads the state's base row, because E2 keeps collection members out of every
// condition — which is what makes F6's one evaluation per collection the only
// possible reading, not a policy choice.
func (s *state) present(n program.NodeID) bool {
	c := s.img.Slot(n).CondPres
	return c == 0 || s.img.Cond(c)(s.row)
}

// reached is §5.2's derived pruning: a node is part of the effective state only
// when its own guard and every ancestor's hold, so a false `present` takes its
// subtree with it and nothing about that is stored.
func (s *state) reached(n program.NodeID) bool {
	for c := n; c != 0; c = s.img.Slot(c).Parent {
		if !s.present(c) {
			return false
		}
	}
	return true
}

// value is what §5.2 reasons about for a slot that has one at the top level:
// its cell in this state, or, for a collection or repeated field, the fact
// that it has at least one instance — SYNTAX §4's "required multiple means at
// least one element". Something nested inside an instance has no single count
// either, so countAt asks that question where it belongs.
func (s *state) value(n program.NodeID) program.Cell {
	if s.img.Slot(n).Coll != 0 {
		return program.Cell{Set: s.rows(n).count() > 0}
	}
	c, ok := s.cellOf(n)
	if !ok {
		return program.Cell{}
	}
	return c.get()
}

// fills is what requiredness asks once presence is established: a value that
// is not empty for a field (§5.2), at least one instance for a collection —
// including a `required` group that is also `multiple` (SYNTAX §4) — and
// nothing more of a plain form or group, whose requiredness is exactly that
// it is there, which the first line of §5.2 already answered.
func (s *state) fills(n program.NodeID, v program.Cell) bool {
	sl := s.img.Slot(n)
	switch {
	case sl.Coll != 0:
		return v.Set
	case sl.Kind != program.KindField:
		return true
	default:
		return s.img.NonEmpty(n, v)
	}
}

// satisfied is §5.2's second line, for a slot whose value the caller holds:
// value() for an ordinary slot, a member's cell for one instance. `required`
// and `required[expr]` are mutually exclusive variants of one attribute
// (SYNTAX §4), so the branches below cannot both apply.
//
// Because defaults are materialized into the row, `required` needs nothing but
// the emptiness test: a defaulted field is satisfied, and one the host cleared
// with Empty is not, default or no default (§5.2).
func (s *state) satisfied(n program.NodeID, v program.Cell) bool {
	sl := s.img.Slot(n)
	switch {
	case sl.Flags&program.FlagRequired != 0:
		return s.fills(n, v)
	case sl.CondReq != 0:
		return !s.img.Cond(sl.CondReq)(s.row) || s.fills(n, v)
	default:
		return true
	}
}

// satisfiedHere is §5.2's whole test for one slot in this state. It answers
// for a slot with one value or one global count — an ordinary slot, a
// top-level collection, a top-level repeated field — and refuses anything
// inside a collection, which has one answer per enclosing instance: the caller
// walks rows and uses satisfied with a member cell (§5.4). collOf names the
// collection a slot belongs to, and a collection's own slot is only its
// collection when it is a repeated field.
func (s *state) satisfiedHere(n program.NodeID) (sat, answered bool) {
	if c := s.img.CollOf(n); c != 0 && c != n {
		return false, false
	}
	return s.satisfied(n, s.value(n)), true
}
