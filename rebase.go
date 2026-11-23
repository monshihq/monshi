package monshi

// Synchronization (DESIGN §6.5): `Rebase` and `Discard`, the two ways a stale
// working copy stops being stale.
//
// The rule these two exist to serve is §6.5's first sentence: committed
// changes made by other users never automatically rewrite an existing working
// state. Everything here follows from the way that rule strands a client — a
// draft that descends from a revision the case no longer has cannot submit
// (§5.4's base guard refuses it) and must not be dropped either, because it
// holds the actor's own work. So the engine offers the two exits and no
// third: pull the newer committed content *into* the draft, or throw the
// draft away.
//
// Five decisions are worth their reasons:
//
//   - **A rebase merges mechanically and refuses to be clever.** Each thing
//     the newer commits changed is compared against what the draft holds and
//     against what the copy descended from: the draft's value survives where
//     the draft touched it, the committed value lands where it did not, and a
//     thing *both* touched differently is a `CONFLICT` naming the path. §6.5
//     defers "detailed automatic merge rules and conflict presentation", and
//     the three-way test over a frame addressed by stable IDs is the part
//     that is not deference. Deciding that a re-typed amount beats a
//     committed one, or interleaving two row lists that both grew, is what is
//     deferred — so a rebase never *combines*, it only chooses an unchanged
//     side, and everything else is reported.
//   - **History is the merge's only account of the past.** A forward diff
//     says which slot moved, at which instance, and what it holds now (§5.1).
//     Read backwards from the copy's base, the same entries say what the copy
//     descended from, and read forwards they say what the case holds now — so
//     the three versions a three-way test needs come from one source, with no
//     stored snapshot and no replay. §5.8 says what the walk asks and why the
//     two questions a diff cannot answer directly — a collection's rows at an
//     older revision, and a cell that was never written — have exact answers
//     anyway.
//   - **A refusal changes nothing.** §9's atomicity, stated for the operation
//     whose whole subject is other people's writes: the merge runs on a clone
//     and only a clone that produced no conflict is saved, so a caller told
//     about a clash still holds the draft it had, at the base it had. Its
//     remaining exits are the other two — re-edit at the old base, or
//     `Discard` — and neither is this engine choosing for the user.
//   - **`Discard` is guarded and never creative.** It drops the copy with the
//     working revision the caller observed as the guard, because the thing it
//     destroys is unsaved work and the session that knows about the newest of
//     it may be a different one (invariant 4). A copy that does not exist is a
//     success at revision 0 and a refusal otherwise: the first is nothing to
//     drop, the second says the caller believes it holds edits the store has
//     never heard of.
//   - **A conflict says no more than a view would.** The paths a rebase names
//     are filtered by the mask §5.7 renders with (§7's property 7). A clash at
//     content this actor may not view is still a clash — silently moving a
//     copy's base past an edit made blind would be the engine resolving a
//     conflict it was told to report — but it is named by its *form* alone,
//     because an instance ID inside a hidden collection is case data and so is
//     the fact that a particular field moved. A name the definition already
//     gives discloses nothing, which is the same line §5.7 draws for a
//     container that grants nothing.
//
// Neither operation commits. `Rebase` moves a copy's *lineage* and `Discard`
// removes a copy; DESIGN §5's history records committed content, invariant 2
// counts commits, and a discarded draft was never content — so a case's
// revision stands untouched through both, which is the difference between
// this file and every one above it.

import (
	"context"
	"slices"
	"sort"
	"strings"
)

// RebaseResult is what a rebase says about the working copy afterwards.
type RebaseResult struct {
	// Working is the copy's revision after the rebase. A rebase that merged
	// anything writes the copy, so the number moves — and it must: a second
	// session holding the old one would otherwise be able to save a batch
	// built from the *unsynchronized* draft over the merge, which is
	// invariant 4's failure with a nicer name. It is the value the caller's
	// next `Update` presents as `base`, exactly as §6.2 says.
	Working uint64

	// Base is the committed revision the copy now descends from: the case's
	// own revision, which is the number the copy did not have before and the
	// one §5.4's submit guard reads from the store. It is reported because the
	// question after a rebase is always "can I submit now", and the honest
	// answer is a number rather than a second read.
	Base uint64

	// Conflicts names, in the order the merge found them, the things both the
	// draft and the newer commits changed, at the addresses §5.1 gives them.
	// The list arrives beside a `CONFLICT` refusal and never beside a success:
	// an empty list is a success's whole answer, and a host that has to ask
	// whether a success still had work left is reading two sources of truth.
	Conflicts []string
}

// Rebase pulls the newer committed content into this actor's working copy,
// keeping the edits the copy holds (DESIGN §6.5). It takes no revision number
// because it guards nothing the caller could know better: the copy's own base
// is where the comparison starts and the case's current revision is where it
// ends, both read from the store that owns them.
//
// An actor with no copy is already synchronized — a copy is cloned from the
// committed state when it is first made (§5.7's `working` creates it from the
// snapshot the case is at), so the absence of one means there is nothing to
// bring forward, and a copy whose base is the case's revision needs no
// merge. Both answer success with nothing written, because §6.5's
// "synchronization is mandatory before a subsequent submit" is a claim
// about the state a submit will find and not about the work
// this call had to do. Neither makes a copy either: a read that allocated
// per-user state would be `viewed`'s problem in §5.7's guise, and a rebase has
// the same shape.
func (c Case) Rebase(ctx context.Context) (RebaseResult, error) {
	refuse := c.refusing("rebase")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return RebaseResult{}, err
	}
	img := d.img

	w, have, err := c.w.cases.draft(refuse, c.actor.User, img)
	if err != nil {
		return RebaseResult{}, err
	}
	if !have {
		return RebaseResult{Base: rec.revision}, nil
	}
	if w.base == rec.revision {
		return RebaseResult{Working: w.rev, Base: w.base}, nil
	}

	// The commits this copy has not seen, and the ones it has: history is
	// dense (§5.6) — the entry at index i is the commit that produced
	// revision i+1 — so a slice split at the copy's base is the whole of what
	// the merge reads, in both directions. A rebase that asked the question
	// any other way would be re-deriving what the store already recorded.
	split := sort.Search(len(rec.hist), func(i int) bool {
		return rec.hist[i].revision > w.base
	})

	// Roles from the committed snapshot, as every operation folds them (§4.3),
	// and used here for the mask alone: a rebase grants nothing and refuses
	// nothing on authority, because it writes only the caller's own draft.
	roles := img.rolesHeld(c.actor, rec.commit.s)
	m := rebaser{img: img, vis: viewable(img, roles, rec.commit.stage)}
	m.run(&rec, rec.hist[:split], rec.hist[split:], &w)

	if len(m.clashes) > 0 {
		// Nothing is saved, which is the whole of §6.5's "a failed/conflicting
		// rebase leaves the previous working state intact" and the reason the
		// merge runs on a clone at all. `Base` stays the copy's own: the
		// caller is no more synchronized than it was, and a result implying
		// otherwise would be a lie about the one number this operation exists
		// to fix.
		paths, veiled := m.report()
		out := RebaseResult{Working: w.rev, Base: w.base, Conflicts: paths}
		return out, refuse.now(Conflict, rebaseRefusal(paths, veiled))
	}

	// `base` and `rev` are the store's to set, so the state handed over
	// carries only the merged data (§6.2's division); `rec.revision` is the
	// lineage this call exists to move.
	rev, err := c.w.cases.saveWorking(refuse, c.actor.User, w.rev,
		workingState{s: m.out}, rec.revision)
	if err != nil {
		return RebaseResult{}, err
	}
	return RebaseResult{Working: rev, Base: rec.revision}, nil
}

// Discard drops this actor's working copy (DESIGN §6.5). `expected` is the
// working revision the caller observed, and it is not decoration: the copy
// this call destroys is one another session of the same actor may have written
// a moment ago, and an unguarded drop would be this package deleting work it
// was never shown.
//
// The copy is removed and not reset, which is what makes §6.5's "a new working
// copy starts from the latest committed state on next access" true by
// construction: `working` clones from the committed snapshot it finds, so the
// next edit gets a draft of the case as it is now with no residue of what was
// thrown away.
//
// Nothing is committed and nothing is remembered. Invariant 2 counts commits,
// and a discarded draft was never content, so the case's revision is exactly
// where it was — which is the difference between this operation and `Submit`,
// which ends with a number that moved.
func (c Case) Discard(ctx context.Context, expected uint64) error {
	refuse := c.refusing("discard")
	if _, _, err := c.look(ctx, refuse); err != nil {
		return err
	}
	return c.w.cases.discard(refuse, c.actor.User, expected)
}

// cellKey and setKey are the two things a rebase compares: one value at one
// instance address, and one collection's rows at one place in the data. They
// are structs of ordinals rather than rendered paths because §7.2's rule that
// nothing keys off letters a caller typed holds at the merge as it does at
// resolution, and an instance path is a slice, which Go will not let a map key
// be.
type cellKey struct {
	slot  nodeID
	steps string
}

type setKey struct {
	coll  nodeID
	steps string
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
	slot    nodeID
	path    instPath
	base    slotValue
	now     slotValue
	written bool
}

// mergedSet is one collection's row set. `resolved` says whether the scan
// found a commit at or before the base that moved it, and is not read by the
// merge: an unresolved list and an empty one are the same correct answer,
// since rows come only from a commit that says so. The flag exists so that the
// scan's "every question answered" counter counts each question once.
type mergedSet struct {
	coll     nodeID
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
	slot   nodeID
	path   instPath
	veiled bool
}

// rebaser folds a run of commits into one actor's draft. The struct holds the
// merged state, the versions the comparison needs, and the clashes, because
// the walk that finds a clash is the same walk that must not then overwrite
// the value it clashed with, and a function that split those would be a
// function with a flag in its signature.
type rebaser struct {
	img *image
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
func (m *rebaser) run(rec *storedCase, seen, window []commitRecord,
	w *workingState) {
	m.ours = &w.s
	m.cur = &rec.commit.s
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
			if ch.kind == chgRows {
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
			c.base = m.img.defaultOf(c.slot)
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
	case m.img.same(c.slot, c.now, c.base):
		// The commits' net effect on this cell is nothing — it moved away and
		// back, or the entry is only the bookkeeping a row's removal leaves
		// behind. The draft's value stands, and reporting a clash here would
		// be reporting a change the case does not contain.
	case m.img.same(c.slot, ours, c.base):
		m.pour(c)
	case m.img.same(c.slot, ours, c.now):
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

func (m *rebaser) clash(n nodeID, p instPath) {
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

// report is the caller's list, with the count of entries that sit at content
// this actor may not view.
//
// A visible clash is spelled as §5.1 spells an address, so every entry is a
// path `Update` accepts and `View` renders. A veiled one is named by its form
// and nothing more: the instance IDs inside a hidden collection are case data,
// and so is the fact that a particular field moved — which is §5.7's rule that
// a container grants nothing but its name, applied to the one other place this
// package turns an address into text.
//
// Duplicates are collapsed keeping the first, because two hidden clashes in one
// form degrade to the same string and a host reading "application, application"
// learns nothing new. Nothing else repeats: the merge settles each cell, and
// each collection at each place, exactly once — which is why this list can be
// longer than §5.4's gap list, whose walk visits one declaration per instance
// and needs the collapsing `gapPaths` does.
func (m *rebaser) report() ([]string, int) {
	if len(m.clashes) == 0 {
		return nil, 0
	}
	var (
		out    []string
		seen   = map[string]bool{}
		veiled int
	)
	for _, c := range m.clashes {
		var p string
		if c.veiled {
			// The count is of clashes, not of names: two hidden fields in one
			// form are two things the user cannot see, even though they can
			// only be pointed at once.
			veiled++
			// And the address is never rendered, not even as an alternative:
			// an instance ID inside a hidden collection is case data, and so
			// is the fact that this particular field moved (§5.7).
			p = m.img.path(m.img.owner(c.slot))
		} else {
			p = m.out.addr(c.slot, c.path)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, veiled
}

// rebaseRefusal is the sentence a refused rebase carries beside its list. The
// count comes first, because a host that cannot show paths — a log line, an
// audit record — still says how much work the user has left; and the list is
// truncated, because `Conflicts` is where all of them are and a message that
// ran to three hundred paths would be a result set pasted into a sentence.
//
// The veiled count is stated because a form name is a puzzle otherwise: the
// actor is told to resolve a clash inside a subtree they cannot open, and the
// honest reading of that is `Discard`, which the sentence should not hide.
func rebaseRefusal(paths []string, veiled int) string {
	const shown = 4
	head, tail := paths, ""
	if len(head) > shown {
		tail = ", and " + dec(uint64(len(head)-shown)) + " more"
		head = head[:shown]
	}
	msg := count(len(paths), "path", "paths") +
		" the draft and the newer commits both changed: " +
		strings.Join(head, ", ") + tail +
		". The working copy is untouched; resolve them and rebase again"
	if veiled == 0 {
		return msg
	}
	return msg + " — " + count(veiled, "clash is", "clashes are") +
		" at content this actor may not view, and `Discard` is how those" +
		" are resolved"
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
	out := instRow{id: r.id, row: append(stateView(nil), r.row...)}
	for _, in := range r.nest {
		out.nest = append(out.nest, in.clone())
	}
	return out
}

// pathKey is the map form of an instance path: the steps' ordinals, joined
// with a separator case data cannot reach. It is not a rendered path, because
// §3.9's rule that resolution is the only place text becomes an ordinal still
// holds — a merge runs on numbers the lowering assigned, not on names.
func pathKey(p instPath) string {
	var b strings.Builder
	for _, st := range p {
		b.WriteString(dec(uint64(st.coll)))
		b.WriteByte(':')
		b.WriteString(dec(uint64(st.id)))
		b.WriteByte(':')
	}
	return b.String()
}
