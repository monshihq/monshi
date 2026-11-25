package engine

import "slices"

// collect builds the work list from the window: one entry per cell and per
// collection the unseen commits touched, holding the *newest* committed value
// for it and the address the entries give. The lists are kept in the order the
// window first names each thing, because a report that walked a map would put
// the same clash at a different place in the list on every run (§7.3).
func (m *rebaser) collect(window []commitRecord) {
	atCell := map[cellKey]int{}
	atSet := map[setKey]int{}
	for _, rec := range window {
		for _, ch := range rec.changes {
			for _, st := range ch.path {
				m.touched[st.id] = true
			}
			if ch.kind == chgRows {
				k := setKey{coll: ch.slot, steps: pathKey(ch.path)}
				if i, seen := atSet[k]; seen {
					m.sets[i].now = ch.ids
					continue
				}
				atSet[k] = len(m.sets)
				m.sets = append(m.sets,
					mergedSet{coll: ch.slot, host: ch.path, now: ch.ids})
				continue
			}
			if ch.kind != chgValue {
				// A verdict is not a question about content, and its address may
				// name a cell slot all the same: a `verifiable` field carries
				// both, and so does a `signature` one. A merge that read either
				// as a base value would answer "what did the case hold here"
				// with "who approved it" or "who signed it", and then treat the
				// answer as a value a `Submit` may conflict with.
				continue
			}
			k := cellKey{slot: ch.slot, steps: pathKey(ch.path)}
			if i, seen := atCell[k]; seen {
				m.cells[i].now = ch.v
				continue
			}
			atCell[k] = len(m.cells)
			m.cells = append(m.cells,
				mergedCell{slot: ch.slot, path: ch.path, now: ch.v})
		}
	}
	m.nsets = len(m.sets)
	// A value inside a collection the window never moved still needs that
	// collection's base membership, twice over: to say whether its row was
	// there at the base revision at all, and to say whether this draft deleted
	// a row the unseen commits changed. Those sets are appended after the
	// mergeable ones, so the same scan resolves them and no walk of `m.sets`
	// ever treats them as a change to apply.
	m.probe()
}

// probe appends the collections the window's values live in but did not move,
// every level of an address from the outermost in, in the order their first
// entries name them.
//
// All the levels and not only the innermost, because the deletion test a probe
// exists to serve is about rows the *draft* removed: a value inside a nested
// list whose outer row was deleted is a clash at the row the user threw away,
// and nothing marks that row lost unless the merge asks about it too. Order is
// what makes the suppression work — the outer collection is answered first, so
// `lostAbove` already knows by the time the inner one is compared.
func (m *rebaser) probe() {
	seen := map[setKey]bool{}
	for i := range m.cells {
		path := m.cells[i].path
		for j := range path {
			k := setKey{coll: path[j].coll, steps: pathKey(path[:j])}
			if seen[k] || m.inSets(k) {
				continue
			}
			seen[k] = true
			m.sets = append(m.sets,
				mergedSet{coll: path[j].coll, host: path[:j]})
		}
	}
}

func (m *rebaser) inSets(k setKey) bool {
	for i := 0; i < m.nsets; i++ {
		if m.setKey(&m.sets[i]) == k {
			return true
		}
	}
	return false
}

// resolve walks the *seen* commits backwards to fill in what the copy
// descended from, which is the third version every comparison needs and the
// part a forward diff cannot state directly (§5.1's reason for recording only
// new values).
//
// It is exact rather than reconstructed. Entries are ordered by revision, so
// the newest one at or below the copy's base states the case's answer at that
// base — for a cell, the value it held; for a collection, the very list of
// instance IDs that made it up, which §5.8's entry-side list spells out and a
// count could not. Anything the scan does not find was never written: a
// collection had no rows, since rows come only from a commit that says so, and
// a cell holds what creating its row materialized — the default the definition
// declares, which is why the fallback asks the declaration and not the draft.
//
// The walk is two passes, lists first, because that order is what bounds its
// cost. Every list has an answer the ledger can give or an answer it withholds
// — the newest entry at or below the base that moved it, or, if none did, the
// empty list, since rows come only from a commit that says so — and a cell can
// only be judged once its row's list is known: a cell in a row the case did not
// have at the base has nothing below the base to find, and the right reading of
// that is absence, not a search for it. Without the split, one such cell — and
// a case whose rows churn commits makes a new one every rebase — would hold the
// walk open to revision 1, formatting keys for every entry of a case's whole
// past to prove none of them is the question. A list no commit ever moved has
// the same problem with no shortcut: its answer is the ledger's silence, and
// silence is only established at the beginning.
//
// The newest answer wins because the scan runs backwards and because the guard
// at the head of each branch refuses a second word for a question already
// answered — an older entry reaching a resolved cell would be a lie about the
// case's own past, since history holds forward diffs (§5.1). Stopping at zero
// is only the cost bound, never the correctness.
func (m *rebaser) resolve(seen []commitRecord) {
	atSet := map[setKey]*mergedSet{}
	for i := range m.sets {
		atSet[m.setKey(&m.sets[i])] = &m.sets[i]
	}
	atCell := map[cellKey]*mergedCell{}
	for i := range m.cells {
		atCell[m.cellKey(&m.cells[i])] = &m.cells[i]
	}
	want := len(m.sets)
	for i := len(seen) - 1; i >= 0 && want > 0; i-- {
		for _, ch := range seen[i].changes {
			if ch.kind != chgRows {
				continue
			}
			s, ok := atSet[setKey{coll: ch.slot, steps: pathKey(ch.path)}]
			if !ok || s.resolved {
				continue
			}
			s.base, s.resolved = ch.ids, true
			want--
		}
	}
	want = 0
	for i := range m.cells {
		c := &m.cells[i]
		if len(c.path) > 0 && !m.existedAtBase(c) {
			// Answered by the list, and answered as absent: no row at the
			// base means no value at the base, whatever the ledger said
			// about a row the case has since forgotten. A row ID is issued
			// once and never reused (§5.1), so no entry at or below the base
			// can hold this cell's address either, and what the mark does is
			// say so in the one place the value pass reads: a question with
			// no answer in the ledger should not be a question the ledger is
			// searched for. The base stays blank, and blank is what §5.8's
			// comparison means when the case had nothing here.
			c.written = true
			continue
		}
		want++
	}
	for i := len(seen) - 1; i >= 0 && want > 0; i-- {
		for _, ch := range seen[i].changes {
			if ch.kind != chgValue {
				continue
			}
			c := atCell[cellKey{slot: ch.slot, steps: pathKey(ch.path)}]
			if c == nil || c.written {
				continue
			}
			c.base, c.written = ch.v, true
			want--
		}
	}
	for i := range m.cells {
		c := &m.cells[i]
		if c.written {
			continue
		}
		if m.existedAtBase(c) {
			c.base = m.img.DefaultOf(c.slot)
		}
	}
}

// existedAtBase answers whether the row a cell lives in was part of the case
// at the copy's base revision. The base row lists are resolved by the time
// this is asked.
func (m *rebaser) existedAtBase(c *mergedCell) bool {
	if len(c.path) == 0 {
		return true
	}
	st := c.path[len(c.path)-1]
	for i := range m.sets {
		s := &m.sets[i]
		if m.setKey(s) == enclosingKey(c.path) {
			return slices.Contains(s.base, st.id)
		}
	}
	// Unreachable from a merge `collect` built: every level of a cell's
	// address is either a set the window moved or one `probe` appended, so a
	// cell always has a list to ask. The answer stays *no* rather than panicking
	// because a fixture that skipped the probe is a state in which the safe
	// reading is that the row is not the draft's to keep — §5.8's rule that an
	// unanswered question falls back to refusing, not to merging.
	return false
}
