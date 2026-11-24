package monshi

// Synchronization's machinery, seen from inside the module (RUNTIME §5.8;
// DESIGN §6.5). `rebase_test.go` holds what a host can observe: which values a
// draft ends up holding, which revisions a following call accepts, which codes
// come back. What is here is the four things that file cannot reach —
//
//   - the third version every comparison needs, and the history walk that
//     reconstructs it, because "what the copy descended from" is not stored
//     anywhere and a merge that guessed it wrong would report conflicts that
//     never happened;
//   - the row-list comparison, including the branch a definition cannot reach
//     on its own and the order of the passes, which decides which values a
//     draft still has a claim on;
//   - the two store guards a rebase leans on, whose out-of-range cases no
//     exported call can reach — §10's rule that a runtime defect is not
//     laundered into a refusal about the case is only honest if the guard is
//     tested where it is reachable at all;
//   - the address keys the merge indexes by, since a nested instance path is a
//     slice and every map here is keyed by a rendering of one.
//
// The fixture is the shape the external file uses, spelled for what the walk
// asks: a defaulted decimal, so "never written" and "written as its default"
// have somewhere to differ, and a collection with a nested one, so a path has
// two levels to key. The states a test hands the merge are built by ID, not by
// `Add`, because the claim is about which instance the history names.

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const mergeSrc = `
types {
    money : decimal(14, 2);
}
roles {
    A { allow view, edit, submit application; }
}
forms {
    application {
        name : string;
        fee  : money default 1.00;

        holders multiple {
            who : string;
            homes multiple {
                where : string;
            }
        }
    }
}
stages {
    draft { accept filed; }
    filed { }
}
`

// mergeFx is the image and the ordinals a hand-built entry is spelled with:
// one fixture per test, so every state, every entry and every merge in it
// agrees on what slot 12 is.
type mergeFx struct {
	img *image
	ns  mergeSlots
}

type mergeSlots struct {
	name, fee      nodeID
	holders, homes nodeID
	who, where     nodeID
}

func aMerge(t *testing.T) *mergeFx {
	t.Helper()
	img := mustLowerSource(t, mergeSrc)
	f := &mergeFx{img: img, ns: mergeSlots{
		name:    held(t, img, "application.name"),
		fee:     held(t, img, "application.fee"),
		holders: held(t, img, "application.holders"),
		homes:   held(t, img, "application.holders.homes"),
		who:     held(t, img, "application.holders.who"),
		where:   held(t, img, "application.holders.homes.where"),
	}}
	for _, p := range []struct {
		what string
		n    nodeID
	}{{"name", f.ns.name}, {"fee", f.ns.fee}, {"holders", f.ns.holders},
		{"homes", f.ns.homes}, {"who", f.ns.who}, {"where", f.ns.where}} {
		if p.n == 0 {
			t.Fatalf("%s resolves to no slot", p.what)
		}
	}
	return f
}

// rebaser is a merge over the fixture: every slot visible, and the draft, the
// committed snapshot and the merged output all start as the materialized empty
// case, which the caller then moves. `seen` and the window are the split of one
// history that `Rebase` computes from the copy's base.
func (f *mergeFx) rebaser(t *testing.T, seen, window []commitRecord) *rebaser {
	t.Helper()
	base := newState(f.img)
	base.applyDefaults()
	ours, cur := base.clone(), base.clone()
	vis := make([]bool, len(f.img.slots))
	for i := range vis {
		vis[i] = true
	}
	m := &rebaser{
		img:     f.img,
		vis:     vis,
		ours:    &ours,
		cur:     &cur,
		out:     base.clone(),
		touched: map[instanceID]bool{},
		dropped: map[setKey]bool{},
		lost:    map[instanceID]bool{},
	}
	m.collect(window)
	m.resolve(seen)
	return m
}

// rowsMade is a draft that created `n` holder rows and wrote nothing else: the
// shape a user is in when they have started a list of their own.
func (f *mergeFx) rowsMade(t *testing.T, n int) *state {
	t.Helper()
	s := newState(f.img)
	s.applyDefaults()
	for i := 0; i < n; i++ {
		if _, _, ok := s.addAt(f.ns.holders, nil); !ok {
			t.Fatalf("the fixture cannot make holder %d", i+1)
		}
	}
	return &s
}

// holderIs and homeIs are the fixture's instance addresses, spelled by the IDs
// a history entry names: a state whose allocator is at 4 issues 5, and the test
// says so in one place rather than hoping `Add` agrees.
func (f *mergeFx) holderIs(id instanceID) instPath {
	return instPath{{coll: f.ns.holders, id: id}}
}

func (f *mergeFx) homeIs(holder, home instanceID) instPath {
	return append(f.holderIs(holder), instStep{coll: f.ns.homes, id: home})
}

// madeAs is a draft holding exactly the rows the history names, which is what
// "the copy adopted the case's list" looks like from the inside: the outer
// collection first, then the nested one inside each row.
func (f *mergeFx) madeAs(t *testing.T, holder, home instanceID) *state {
	t.Helper()
	s := newState(f.img)
	s.applyDefaults()
	s.seq = holder - 1
	id, _, ok := s.addAt(f.ns.holders, nil)
	if !ok || id != holder {
		t.Fatalf("the fixture made holder %d, want %d", id, holder)
	}
	s.seq = home - 1
	id, _, ok = s.addAt(f.ns.homes, f.holderIs(holder))
	if !ok || id != home {
		t.Fatalf("the fixture made home %d, want %d", id, home)
	}
	return &s
}

// holdersAre is the committed side of a set merge, with exactly the IDs the
// window's list names. `apply` copies rows *out of the committed snapshot*, so
// a fixture that wrote a list without building the rows it names would be
// describing a history the case could not have committed — which is the
// difference between this package's entries and a store that trusts them.
func (f *mergeFx) holdersAre(t *testing.T, ids ...instanceID) *state {
	t.Helper()
	s := newState(f.img)
	s.applyDefaults()
	for _, id := range ids {
		s.seq = id - 1
		got, _, ok := s.addAt(f.ns.holders, nil)
		if !ok || got != id {
			t.Fatalf("the fixture made holder %d, want %d", got, id)
		}
	}
	return &s
}

// setAt puts one value at one instance address, which is how a test says "this
// draft holds X in row 4" without routing through an operation that has its own
// claims to test.
func setAt(t *testing.T, s *state, slot nodeID, path instPath, v slotValue) {
	t.Helper()
	c, ok := s.cellAt(slot, path)
	if !ok {
		t.Fatalf("slot %d has no cell at %v", slot, path)
	}
	c.set(v)
}

// valueChange and rowsChange are the two entry kinds, spelled as the store
// spells them, and each answers for a revision of its own: a rebase's walk is
// over records, and two records claiming one revision describe a history §5.4
// cannot write.
func valueChange(rev uint64, slot nodeID, path instPath,
	v slotValue) commitRecord {
	return commitRecord{revision: rev, changes: []commitChange{
		{kind: chgValue, slot: slot, path: path, v: v}}}
}

func rowsChange(rev uint64, coll nodeID, host instPath,
	ids ...instanceID) commitRecord {
	return commitRecord{revision: rev, changes: []commitChange{
		{kind: chgRows, slot: coll, path: host, ids: ids}}}
}

// oneCommit is a revision that said several things at once, which is what a
// batch that added a row and filled it in commits as.
func oneCommit(rev uint64, cs ...commitChange) commitRecord {
	return commitRecord{revision: rev, changes: cs}
}

func val(slot nodeID, path instPath, v slotValue) commitChange {
	return commitChange{kind: chgValue, slot: slot, path: path, v: v}
}

func rows(coll nodeID, host instPath, ids ...instanceID) commitChange {
	return commitChange{kind: chgRows, slot: coll, path: host, ids: ids}
}

func money(s string) slotValue {
	return slotValue{set: true, d: decimal.RequireFromString(s)}
}

func txt(s string) slotValue { return slotValue{set: true, s: s} }

// reportOf is the list a merge would hand its caller, for the failure
// messages: a count alone cannot say which address went wrong.
func reportOf(m *rebaser) []string {
	paths, _ := m.report()
	return paths
}

func TestPathKeyIsPrefixFree(t *testing.T) {
	// The key is a map key, so the property that matters is not readability
	// but that no sequence renders into another's prefix: a lookup of a
	// one-level address must not answer for a two-level one whose numbers
	// happen to start the same way.
	outer := instPath{{coll: 11, id: 2}}
	nested := instPath{{coll: 11, id: 2}, {coll: 33, id: 4}}
	one, two := pathKey(outer), pathKey(nested)
	if one == two {
		t.Fatalf("one level renders %q and two render %q: the longer key must"+
			" extend the shorter, not collide with it", one, two)
	}
	// Two digits against one: a fixed-width encoding would have hidden this,
	// and `1,2` beside `11,2` is exactly one step off.
	if pathKey(instPath{{coll: 1, id: 2}}) == one {
		t.Error("a one-digit and a two-digit collection render the same key")
	}
	if pathKey(instPath{{coll: 11, id: 22}}) == one {
		t.Error("a two-digit and a one-digit instance render the same key")
	}
	if got := pathKey(nil); got != "" {
		t.Errorf("pathKey of the base row = %q, want the empty key", got)
	}

	// `enclosingKey` names the collection whose rows hold a cell, at the
	// address above it: for a field one level down that is the outer
	// collection at the base row, and for a field two levels down it is the
	// inner collection at the outer row's address. Both are the set that must
	// have existed for the value to mean anything, which is what `probe`,
	// `dropped` and `crossesLost` all ask.
	if got := enclosingKey(outer); got != (setKey{coll: 11, steps: ""}) {
		t.Errorf("the enclosing key of a top-level row is %+v, want the base"+
			" row's set", got)
	}
	if got := enclosingKey(nil); got != (setKey{}) {
		t.Errorf("enclosingKey of no path = %+v, want the empty key", got)
	}
	if got := enclosingKey(nested); got != (setKey{coll: 33, steps: one}) {
		t.Errorf("the enclosing key of a nested address is %+v, want the"+
			" inner collection at its outer row's key", got)
	}
	// The two must differ, or a nested collection's own set and the row
	// holding it would answer to one entry: two collections at one address
	// are two lists the merge compares separately.
	if enclosingKey(nested) == enclosingKey(outer) {
		t.Error("a nested collection keys the same as its parent")
	}
}

func TestRowListsCompareInOrderAndMissingKeepsTheBase(t *testing.T) {
	// Order is part of the comparison, because it is part of §5.6's diff: a
	// list that only moved is a list the merge has no policy for, and a set
	// compared as a bag would call that agreement.
	if sameIDs([]instanceID{1, 2}, []instanceID{2, 1}) {
		t.Error("two orders of one set compare equal")
	}
	if !sameIDs(nil, []instanceID{}) {
		t.Error("an empty list and no list are the same answer to a merge," +
			" and are nil and empty to Go")
	}
	if sameIDs([]instanceID{1}, []instanceID{1, 1}) {
		t.Error("a repeated ID is not the same set")
	}

	// `missing` answers which rows this draft removed. It walks the base list
	// rather than the other, which is what makes a report repeatable: §7.3's
	// rule that a list a caller reads is ordered by the definition and not by
	// a map applies to the rows named in a clash sentence too.
	got := missing([]instanceID{3, 1, 4, 1}, []instanceID{1})
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("missing = %v, want [3 4]: base order, first occurrence once",
			got)
	}
	if got := missing(nil, []instanceID{1}); len(got) != 0 {
		t.Errorf("missing of no base list = %v, want nothing", got)
	}
	if got := missing([]instanceID{1, 2}, nil); len(got) != 2 {
		t.Errorf("every row is missing from a draft that holds none: got %v",
			got)
	}
}

func TestACopyOfARowDoesNotFollowItsOriginal(t *testing.T) {
	// `apply` moves a committed row into a draft that never held it by
	// copying and not by aliasing: the source is the committed snapshot, and a
	// later edit of an adopted row would otherwise be a write to the case
	// (§5.6's replace-never-mutate rule, arriving from the other side).
	f := aMerge(t)
	src := newState(f.img)
	src.applyDefaults()
	holder, _, ok := src.addAt(f.ns.holders, nil)
	if !ok {
		t.Fatal("the fixture cannot make a holder row")
	}
	home, _, ok := src.addAt(f.ns.homes, f.holderIs(holder))
	if !ok {
		t.Fatal("the fixture cannot make a home row")
	}
	setAt(t, &src, f.ns.who, f.holderIs(holder), txt("Elliot Ltd"))
	setAt(t, &src, f.ns.where, f.homeIs(holder, home), txt("Pune"))

	set, ok := src.rowsBelow(f.ns.holders, nil, false)
	if !ok || set == nil || len(set.rows) != 1 {
		t.Fatalf("the committed state's holders are %+v, want one row", set)
	}
	cp := copyRow(set.rows[0])
	if cp.id != holder {
		t.Errorf("the copy is ID %d, want the ID the case issued (%d)",
			cp.id, holder)
	}

	// The nested set came with it, or an adopted holder would be a holder
	// whose homes were silently deleted — the half of §6.5 a shallow copy of
	// the row's own view gets wrong.
	if len(cp.nest) != 1 || cp.nest[0].count() != 1 {
		t.Fatalf("the copy carries %d nested sets with %d rows in the first",
			len(cp.nest), cp.nest[0].count())
	}

	dst := newState(f.img)
	dst.applyDefaults()
	into, _ := dst.rowsBelow(f.ns.holders, nil, true)
	into.rows = append(into.rows, cp)
	into.renumber()
	// The index has to be rebuilt, or the adopted row cannot be addressed at
	// all: `rowsFor` finds a nested set by finding its parent row first.
	nested := dst.rowsFor(f.ns.homes, f.holderIs(holder))
	if got := rowIDs(nested.list()); len(got) != 1 {
		t.Fatalf("the adopted row holds %d homes, want 1", len(got))
	}

	// And the copy is independent in both directions, which is the claim.
	setAt(t, &src, f.ns.who, f.holderIs(holder), txt("changed afterwards"))
	if got := dst.cellValue(f.ns.who, f.holderIs(holder)); got.s !=
		"Elliot Ltd" {
		t.Errorf("the adopted row reads %q after its source moved, want the"+
			" value it was copied at", got.s)
	}
	setAt(t, &dst, f.ns.who, f.holderIs(holder), txt("the draft's own edit"))
	if got := src.cellValue(f.ns.who, f.holderIs(holder)); got.s !=
		"changed afterwards" {
		t.Errorf("the committed snapshot reads %q: the merged draft wrote"+
			" through into case data", got.s)
	}
}

func TestResolveReadsTheNewestEntryAtOrBeforeTheBase(t *testing.T) {
	// The whole of the reconstruction. History holds forward diffs (§5.1's
	// "new values, never old"), so the third version is what the *latest*
	// entry at or below the copy's base says, and not the first entry that
	// mentions the cell. A scan that stopped at the first hit would hand the
	// merge a base from ten commits of editing ago and report a conflict at
	// every field anybody had ever touched.
	f := aMerge(t)
	m := f.rebaser(t, []commitRecord{
		valueChange(1, f.ns.name, nil, txt("first")),
		valueChange(2, f.ns.name, nil, txt("second")),
	}, []commitRecord{valueChange(3, f.ns.name, nil, txt("third"))})
	if len(m.cells) != 1 {
		t.Fatalf("the window names %d cells, want 1", len(m.cells))
	}
	c := &m.cells[0]
	if !c.written {
		t.Fatal("no seen entry answered for `name`, so the scan found nothing")
	}
	if c.base.s != "second" {
		t.Errorf("the base reads %q, want revision 2's answer", c.base.s)
	}
	if c.now.s != "third" {
		t.Errorf("the newest committed value reads %q, want the window's",
			c.now.s)
	}

	// A collection is answered the same way, and with the list rather than a
	// count: a row the case issued and a row this draft issued are different
	// rows, and only the IDs say which is which. And the *newest* answer
	// below the base is the base — history holds forward diffs, so a walk
	// that let an older entry overwrite a newer one would describe a state
	// the case had not been in for commits, and every edit the draft shares
	// with it would turn into a conflict.
	m = f.rebaser(t, []commitRecord{
		rowsChange(1, f.ns.holders, nil, 4, 5, 6),
		oneCommit(2, rows(f.ns.holders, nil, 4, 5),
			val(f.ns.fee, nil, money("2.00"))),
	}, []commitRecord{rowsChange(3, f.ns.holders, nil, 7, 8)})
	if len(m.sets) != 1 {
		t.Fatalf("the window moved %d collections, want 1", len(m.sets))
	}
	s := &m.sets[0]
	if len(s.base) != 2 || s.base[0] != 4 || s.base[1] != 5 {
		t.Errorf("the base row list is %v, want revision 2's [4 5], not"+
			" revision 1's: the walk must let the newest entry win", s.base)
	}
	if len(s.now) != 2 || s.now[1] != 8 {
		t.Errorf("the committed list is %v, want the window's [7 8]", s.now)
	}

	// A *list* the ledger cannot answer keeps the walk running to the very
	// bottom, and the answers it has already found must survive the older
	// entries it passes on the way: history holds forward diffs, so a
	// revision-1 word for a list that revision 2 already rewrote describes
	// the case's past, not the base. That makes this a different claim from
	// the one above. The block above says the newest answer wins; this one
	// says an answer is never taken back while another question is still
	// open, which is the resolution guard's job and not the count of
	// questions' — a walk stopped by its count alone would have to be shown
	// to keep the guard honest only when an answer is missing, which is
	// exactly the case a case whose rows churn commits lives in. The nested
	// list is the open question: the window moved it and nothing at or below
	// the base ever did.
	m = f.rebaser(t, []commitRecord{
		rowsChange(1, f.ns.holders, nil, 4, 5, 6),
		oneCommit(2, rows(f.ns.holders, nil, 4, 5)),
	}, []commitRecord{oneCommit(3, rows(f.ns.holders, nil, 7, 8),
		rows(f.ns.homes, instPath{{coll: f.ns.holders, id: 7}}, 9))})
	if len(m.sets) != 2 {
		t.Fatalf("the window moved %d collections, want the holders and the"+
			" homes inside row 7: %+v", len(m.sets), m.sets)
	}
	s = &m.sets[0]
	if !s.resolved || len(s.base) != 2 || s.base[1] != 5 {
		t.Errorf("the base row list reads %v (resolved %v) after an"+
			" unanswered question kept the walk running to revision 1, want"+
			" the [4 5] revision 2 resolved and never taken back",
			s.base, s.resolved)
	}
	h := &m.sets[1]
	if h.resolved || len(h.base) != 0 {
		t.Errorf("the nested list reads %v (resolved %v), want no answer at"+
			" all from a history that never moved it", h.base, h.resolved)
	}

	// A collection the window itself moved twice is merged as one change, from
	// the newest word: two commits that both moved the rows are not two
	// decisions, and a work list that kept both would report the first as a
	// clash the second already resolved.
	m = f.rebaser(t, []commitRecord{rowsChange(1, f.ns.holders, nil, 4, 5)},
		[]commitRecord{
			rowsChange(2, f.ns.holders, nil, 4, 5, 6),
			rowsChange(3, f.ns.holders, nil, 4),
		})
	if len(m.sets) != 1 {
		t.Fatalf("the window moved one collection twice and the merge made"+
			" %d entries, want 1", len(m.sets))
	}
	if len(m.sets[0].now) != 1 || m.sets[0].now[0] != 4 {
		t.Errorf("the committed list is %v, want [4]: revision 2's added row"+
			" was deleted again by revision 3", m.sets[0].now)
	}
	if m.nsets != 1 {
		t.Errorf("nsets = %d, want the collection the window moved to be"+
			" mergeable and not probe-only", m.nsets)
	}

	// The same for a value: the window's last word is `now`, and the seen
	// entries are only ever the base.
	m = f.rebaser(t, []commitRecord{
		valueChange(1, f.ns.name, nil, txt("first")),
		valueChange(2, f.ns.name, nil, txt("second")),
	}, []commitRecord{
		valueChange(3, f.ns.name, nil, txt("third")),
		valueChange(4, f.ns.name, nil, txt("fourth")),
	})
	if len(m.cells) != 1 {
		t.Fatalf("the window named `name` %d times, want one entry",
			len(m.cells))
	}
	if m.cells[0].now.s != "fourth" {
		t.Errorf("the committed value is %q, want the window's newest",
			m.cells[0].now.s)
	}

	// The walk stops once every question is answered, and it asks only about
	// the window's keys: entries below the base that mention something else
	// are not holes the merge has to fill.
	m = f.rebaser(t, []commitRecord{
		valueChange(1, f.ns.name, nil, txt("first")),
		valueChange(2, f.ns.who, f.holderIs(9), txt("elsewhere")),
	}, []commitRecord{valueChange(3, f.ns.fee, nil, money("3.00"))})
	if len(m.cells) != 1 || m.cells[0].slot != f.ns.fee {
		t.Fatalf("the work list is %+v, want the window's one cell", m.cells)
	}
	if len(m.sets) != 0 {
		t.Errorf("a cell the base row holds needed %d probes, want none:"+
			" the window's keys are the only questions", len(m.sets))
	}
	if m.cells[0].written {
		t.Error("a commit below the base answered for a cell that has never" +
			" been written: the scan took an entry for a different address")
	}
}

func TestResolveAsksTheDeclarationAboutACellNobodyWrote(t *testing.T) {
	// The fallback is the difference between a clean merge and a conflict at
	// every defaulted field in the definition. A cell the history never
	// touched did not hold *nothing*: the row it lives in was made at some
	// point, and making it materialized the declared default. Read the base as
	// absence and a draft's untouched 1.00 is an edit its actor made, so the
	// merge refuses a change they never conflicted with.
	f := aMerge(t)
	m := f.rebaser(t, nil,
		[]commitRecord{valueChange(1, f.ns.fee, nil, money("9.00"))})
	if len(m.cells) != 1 || m.cells[0].written {
		t.Fatalf("the walk was expected to leave `fee` unanswered: %+v",
			m.cells)
	}
	if !f.img.same(f.ns.fee, m.cells[0].base, f.img.defaultOf(f.ns.fee)) {
		t.Errorf("an unwritten cell's base is %v, want the declared default",
			m.cells[0].base)
	}
	// The consequence, which is the whole reason the fallback exists: the
	// draft's untouched default compares as unchanged, so the committed value
	// arrives and nothing is asked of the user.
	m.out = m.ours.clone()
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
	if len(m.clashes) != 0 {
		t.Fatalf("an untouched defaulted field is a clash: %v", reportOf(m))
	}
	if v := m.out.cellValue(f.ns.fee, nil); !f.img.same(f.ns.fee, v,
		money("9.00")) {
		t.Errorf("the merged draft holds %v for the defaulted field, want"+
			" the committed 9.00", v)
	}

	// A member of a row that existed at the base takes the same answer,
	// reached by asking the collection's base list rather than the declaration
	// alone — which is why `probe` exists, and why the answer would be wrong
	// for a row this draft made itself.
	m = f.rebaser(t, []commitRecord{rowsChange(1, f.ns.holders, nil, 5)},
		[]commitRecord{valueChange(2, f.ns.who, f.holderIs(5),
			txt("from the case"))})
	if len(m.cells) != 1 || m.cells[0].written {
		t.Fatalf("`who` was expected to fall through to the fallback: %+v",
			m.cells)
	}
	if len(m.sets) != 1 || m.nsets != 0 {
		t.Fatalf("the merge made %d set entries with %d mergeable, want the"+
			" enclosing collection probed and not merged", len(m.sets),
			m.nsets)
	}
	if !m.existedAtBase(&m.cells[0]) {
		t.Error("row 5 was committed at revision 1 and the merge says it did" +
			" not exist at the base")
	}
	if !f.img.same(f.ns.who, m.cells[0].base, f.img.defaultOf(f.ns.who)) {
		t.Errorf("a cell in an existing row has base %v, want the default"+
			" that creating the row wrote", m.cells[0].base)
	}

	// And the other way: a row this draft made and the case never committed
	// has no base at all. Its cells answer unset, which leaves the draft's own
	// value as the only edit anybody made there — the case cannot claim to
	// have changed under it, and the merge does not pretend a row it never
	// issued was ever the draft's to lose.
	m = f.rebaser(t, nil, []commitRecord{valueChange(1, f.ns.who,
		f.holderIs(5), txt("from the case"))})
	if m.existedAtBase(&m.cells[0]) {
		t.Error("a collection the case never moved still answers that its" +
			" rows existed")
	}
	if m.cells[0].base.set {
		t.Errorf("a cell in a row that never existed has base %v, want the"+
			" unset answer that leaves the draft's value unopposed",
			m.cells[0].base)
	}

	// A cell outside every collection has no row to ask about, which is the
	// same answer as a base row that always existed — and the absence of a
	// probe is not a bug, because there is no list to consult.
	m = f.rebaser(t, nil,
		[]commitRecord{valueChange(1, f.ns.name, nil, txt("from the case"))})
	if len(m.sets) != 0 {
		t.Errorf("`name` needed %d collection probes, want none", len(m.sets))
	}
	if !m.existedAtBase(&m.cells[0]) {
		t.Error("the form's base row did not exist at the base revision")
	}
	if m.cells[0].base.set {
		t.Errorf("`name` was never written and its base is %v, want unset",
			m.cells[0].base)
	}
}

func TestApplyWritesNothingForAListWhoseHostIsNotData(t *testing.T) {
	// The window can name a nested collection whose parent row the merged
	// draft does not hold. `apply` is the step that turns a committed list
	// into rows, and it asks for the set with `ensure`, because a list the
	// case moved and the draft never had needs a place to live — but the
	// address of a row that is not in the draft is not data at all, and
	// §5.8's rule there is to write nothing rather than to invent a holder to
	// hang the case's rows on. A merge that ignored the answer would leave
	// the draft holding a row nobody committed, at an ID the case may issue
	// next week.
	f := aMerge(t)
	m := f.rebaser(t,
		[]commitRecord{oneCommit(1, rows(f.ns.holders, nil, 4),
			rows(f.ns.homes, f.holderIs(9)))},
		[]commitRecord{rowsChange(3, f.ns.homes, f.holderIs(9), 7)})
	if len(m.sets) != 1 || !m.sets[0].resolved {
		t.Fatalf("the window moved %d collections (resolved %+v), want the"+
			" homes list inside holder 9", len(m.sets), m.sets[0].resolved)
	}
	m.mergeSet(&m.sets[0])
	if len(m.clashes) != 0 {
		t.Errorf("a list with no host was told %v, want silence: nothing"+
			" here disagrees, the address simply is not data", reportOf(m))
	}
	if _, ok := m.out.rowAtPath(f.holderIs(9)); ok {
		t.Errorf("the merge created holder 9 in the draft")
	}
	if got := rowIDs(m.out.rowsFor(f.ns.homes, f.holderIs(9)).list()); len(got) !=
		0 {
		t.Errorf("the draft holds %v under holder 9, want no rows at all", got)
	}
}

func TestMergeSetAppliesACommittedListTheDraftDidNotMove(t *testing.T) {
	// The branch the external file cannot separate on its own: a draft that
	// has not touched the rows takes the case's list whole, including a row
	// that reappears at a *higher* ID than the one deleted. `apply` runs
	// before the values, so if it dropped the re-added row, `mergeCell` would
	// find no home for its committed value and the case's work would be
	// deleted with no conflict reported at all. That ordering is the claim,
	// and the IDs are how the test says which row is which.
	f := aMerge(t)
	m := f.rebaser(t,
		[]commitRecord{rowsChange(1, f.ns.holders, nil, 1, 2)},
		[]commitRecord{oneCommit(2, rows(f.ns.holders, nil, 1, 3),
			val(f.ns.who, f.holderIs(3), txt("new in the case")))})

	// The draft holds the base's two rows and wrote nothing in either, and the
	// committed snapshot is the list the window says it is.
	ours := f.rowsMade(t, 2)
	m.ours = ours
	m.out = ours.clone()
	m.cur = f.holdersAre(t, 1, 3)
	setAt(t, m.cur, f.ns.who, f.holderIs(3), txt("new in the case"))
	m.mergeSet(&m.sets[0])

	if len(m.clashes) != 0 {
		t.Fatalf("a draft that never moved the rows is told %v, want nothing",
			reportOf(m))
	}
	got := rowIDs(m.out.rowsFor(f.ns.holders, nil).list())
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("the merged rows are %v, want [1 3]: the kept row holds its"+
			" ID and the re-added one is the case's, not this draft's old 2",
			got)
	}

	// Now the value pass, which is the reason the ordering matters: the
	// committed field lands in a row the draft never had.
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
	if len(m.clashes) != 0 {
		t.Fatalf("the value in an adopted row was reported as a clash: %v",
			reportOf(m))
	}
	if v := m.out.cellValue(f.ns.who, f.holderIs(3)); v.s != "new in the case" {
		t.Errorf("the adopted row reads %q, want the committed value", v.s)
	}

	// The row this draft made and named keeps its own content through the
	// rebuild: §6.5's "the draft's edits are preserved" says nothing about a
	// row its actor never touched, but a merge that rebuilt a set by copying
	// every row from the snapshot would overwrite the actor's value with the
	// case's and still report nothing.
	f2 := aMerge(t)
	m = f2.rebaser(t,
		[]commitRecord{rowsChange(1, f2.ns.holders, nil, 1, 2)},
		[]commitRecord{rowsChange(2, f2.ns.holders, nil, 1, 3)})
	ours = f2.rowsMade(t, 2)
	setAt(t, ours, f2.ns.who, f2.holderIs(1), txt("the draft's own holder"))
	m.ours = ours
	m.out = ours.clone()
	m.cur = f2.holdersAre(t, 1, 3)
	m.mergeSet(&m.sets[0])
	if v := m.out.cellValue(f2.ns.who, f2.holderIs(1)); v.s !=
		"the draft's own holder" {
		t.Errorf("the kept row reads %q after the merge, want the value its"+
			" author typed into it", v.s)
	}
}

func TestMergeSetKeepsBothListsWhenBothSidesMovedRows(t *testing.T) {
	// The draft made two rows, the case made one. Nothing in §6.5 says which
	// order the user should see, so the collection is named and left alone —
	// and the entries that would have written into that set are dropped
	// instead of half-applied, because a row list the merge refused to decide
	// with a committed value poured into one of its rows is a state no caller
	// asked for and no report describes.
	f := aMerge(t)
	m := f.rebaser(t, nil, []commitRecord{oneCommit(1,
		rows(f.ns.holders, nil, 4),
		val(f.ns.who, f.holderIs(4), txt("Elliot Ltd")))})

	ours := f.rowsMade(t, 2)
	m.ours = ours
	m.out = ours.clone()
	m.mergeSet(&m.sets[0])
	if len(m.clashes) != 1 {
		t.Fatalf("both sides moved the rows and the merge says %v",
			reportOf(m))
	}
	if !m.dropped[m.setKey(&m.sets[0])] {
		t.Fatal("the clashed set is not marked dropped, so a later entry" +
			" would half-apply into a list this merge refused to decide")
	}
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
	if len(m.clashes) != 1 {
		t.Errorf("the report is %v, want the collection alone: %q is not an"+
			" address the user can resolve on a draft whose rows were never"+
			" decided", reportOf(m), "application.holders.4.who")
	}
	if got := rowIDs(m.out.rowsFor(f.ns.holders, nil).list()); len(got) != 2 {
		t.Errorf("the draft holds %d rows after a refused set merge, want its"+
			" own 2", len(got))
	}
	if v := m.out.cellValue(f.ns.who, f.holderIs(4)); v.set {
		t.Error("a value from the dropped set was written into a row the" +
			" merge refused to reconcile")
	}
}

func TestCommitsThatCancelOutLeaveTheDraftAlone(t *testing.T) {
	// The merge's first branch, and the one that decides who has to do work:
	// the window spoke about a collection, but its last word is the base —
	// the case added row 6 and deleted it again — so nothing arrived that the
	// draft could collide with. A draft that in the meantime deleted one of
	// the base's own rows is not in conflict with a change that cancelled
	// itself out, and a merge that fell through to the both-moved branch here
	// would send this user to `Discard` over commits that left no trace.
	f := aMerge(t)
	m := f.rebaser(t,
		[]commitRecord{rowsChange(1, f.ns.holders, nil, 4, 5)},
		[]commitRecord{
			rowsChange(2, f.ns.holders, nil, 4, 5, 6),
			rowsChange(3, f.ns.holders, nil, 4, 5),
		})
	if len(m.sets) != 1 {
		t.Fatalf("the window moved %d collections, want 1", len(m.sets))
	}
	ours := f.holdersAre(t, 4, 5)
	if !ours.removeAt(f.ns.holders, nil, 4) {
		t.Fatal("the fixture cannot delete a draft row")
	}
	m.ours = ours
	m.out = ours.clone()
	m.mergeSet(&m.sets[0])
	if len(m.clashes) != 0 {
		t.Fatalf("a change that cancelled itself out became a clash: %v",
			reportOf(m))
	}
	if got := rowIDs(m.out.rowsFor(f.ns.holders, nil).list()); len(got) !=
		1 || got[0] != 5 {
		t.Errorf("the merged draft holds %v, want its own [5]: the row the"+
			" case added and removed has no home here, and the one this"+
			" actor deleted must stay deleted", got)
	}
}

func TestARebaseInheritsTheCaseAllocatorBeforeItMerges(t *testing.T) {
	// `run` is the only place the three states and the two passes meet, and
	// this is the one claim it makes that no comparison can test: a draft that
	// adopted the case's rows must not hand those numbers out again, because
	// §5.1's IDs are never reused within a case. The commit ladder makes the
	// same reconciliation going the other way (§5.4), and a rebase that forgot
	// it would issue a second row 1 into a case that had already published one.
	f := aMerge(t)
	rec := storedCase{revision: 1}
	rec.commit.s = newState(f.img)
	rec.commit.s.applyDefaults()
	if _, _, ok := rec.commit.s.addAt(f.ns.holders, nil); !ok {
		t.Fatal("the committed state cannot make a holder row")
	}
	rec.hist = []commitRecord{rowsChange(1, f.ns.holders, nil, 1)}

	ours := newState(f.img)
	ours.applyDefaults()
	vis := make([]bool, len(f.img.slots))
	for i := range vis {
		vis[i] = true
	}
	m := &rebaser{img: f.img, vis: vis}
	w := workingState{base: 0, rev: 1, s: ours}
	m.run(&rec, nil, rec.hist, &w)

	if len(m.clashes) != 0 {
		t.Fatalf("a draft that made no rows of its own is told %v, want a"+
			" clean merge", reportOf(m))
	}
	if m.out.seq < rec.commit.s.seq {
		t.Errorf("the merged draft's allocator is at %d, want the case's %d",
			m.out.seq, rec.commit.s.seq)
	}
	if got := rowIDs(m.out.rowsFor(f.ns.holders, nil).list()); len(got) != 1 {
		t.Errorf("the merged draft holds %d rows, want the case's one",
			len(got))
	}

	// And the merged draft can still make a row, at a number the case has not
	// published: this is the assertion the seq copy exists to make.
	id, _, ok := m.out.addAt(f.ns.holders, nil)
	if !ok {
		t.Fatal("the merged draft cannot add a holder")
	}
	if id != 2 {
		t.Errorf("the adopted case issued ID %d, want the next after the"+
			" case's own row", id)
	}
}

func TestALostRowSuppressesItsMembersAndTheirChildren(t *testing.T) {
	// The suppression is a path test and not an ID test: the row this draft
	// deleted is the *enclosing* row of the field the commits changed, and of
	// the rows inside that. So `crossesLost` walks the address from the outside
	// in, and a deleted subtree is reported once at its collection instead of
	// once per field at every level of it.
	f := aMerge(t)
	history := []commitRecord{oneCommit(1, rows(f.ns.holders, nil, 5),
		rows(f.ns.homes, f.holderIs(5), 6))}
	window := []commitRecord{valueChange(2, f.ns.where, f.homeIs(5, 6),
		txt("Pune"))}

	m := f.rebaser(t, history, window)
	m.ours = f.madeAs(t, 5, 6)
	m.out = m.ours.clone()
	m.lost[5] = true
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
	if len(m.clashes) != 0 {
		t.Fatalf("a field inside a lost row was reported: %v", reportOf(m))
	}
	if v := m.out.cellValue(f.ns.where, f.homeIs(5, 6)); v.set {
		t.Error("the committed value was poured into a row this draft" +
			" removed: the clash belongs to the collection, but the deletion" +
			" still has to stand")
	}

	// The same cell is *not* suppressed when nothing was lost, so the branch
	// above is a policy and not an accident of the fixture: with the draft
	// still holding the rows, the value arrives and nothing is reported.
	m = f.rebaser(t, history, window)
	m.ours = f.madeAs(t, 5, 6)
	m.out = m.ours.clone()
	m.mergeCell(&m.cells[0])
	if len(m.clashes) != 0 {
		t.Fatalf("an untouched cell beside a kept row became a clash: %v",
			reportOf(m))
	}
	if v := m.out.cellValue(f.ns.where, f.homeIs(5, 6)); v.s != "Pune" {
		t.Errorf("the committed value did not arrive in a kept row: %v", v)
	}

	// And the report the suppression is measured against: the same deletion,
	// with the commits having touched the row, is one clash at the enclosing
	// collection — not silence, and not a per-field list. Here the window moved
	// no rows at all, so the pass that runs the deletion test over
	// probe-only collections is the one that finds it.
	m = f.rebaser(t, history, window)
	m.ours = f.rowsMade(t, 0)
	m.out = m.ours.clone()
	m.settledUnmoved()
	if len(m.clashes) != 1 {
		t.Fatalf("a row this draft deleted that the commits changed is told"+
			" as %v, want one clash at the collection", reportOf(m))
	}
	if got := reportOf(m); got[0] != "application.holders" {
		t.Errorf("the report is %q, want the enclosing collection and not a"+
			" path inside the row this draft deleted", got[0])
	}
	if !m.lost[5] {
		t.Fatal("the lost row is not marked, so its members' values would be" +
			" settled one by one beside the decision the user was given")
	}

	// A lost row's suppression is scoped to its own subtree: the same merge,
	// with a field *outside* every collection also clashed, still names that
	// one. A rule that swallowed reports beside the row it explains would turn
	// `Discard`'s "there was more here" into a guess.
	m = f.rebaser(t, history, append(window,
		valueChange(2, f.ns.name, nil, txt("the case's name"))))
	m.ours = f.madeAs(t, 5, 6)
	m.out = m.ours.clone()
	setAt(t, m.ours, f.ns.name, nil, txt("the draft's name"))
	m.out = m.ours.clone()
	m.lost[5] = true
	for i := range m.cells {
		m.mergeCell(&m.cells[i])
	}
	if got := reportOf(m); len(got) != 1 || got[0] != "application.name" {
		t.Fatalf("the report is %v, want the one clash outside the lost"+
			" row: the suppression reached past the subtree it belongs to",
			got)
	}

	// And the shape that is *not* a clash: a row the case deleted and this
	// draft deleted too is agreement, which `detectLost` states and the
	// converged branch proves without asking the deletion test. The draft
	// holds nothing, the base held row 5, and the commits left it gone.
	m = f.rebaser(t, []commitRecord{rowsChange(1, f.ns.holders, nil, 5)},
		[]commitRecord{rowsChange(2, f.ns.holders, nil)})
	m.ours = f.rowsMade(t, 0)
	m.out = m.ours.clone()
	m.touched[5] = true
	m.mergeSet(&m.sets[0])
	if len(m.clashes) != 0 {
		t.Fatalf("two actors deleting one row is told as %v, want silence",
			reportOf(m))
	}
	if m.lost[5] {
		t.Error("a row both sides removed is marked lost, so its committed" +
			" values would be suppressed beside a change nobody made")
	}
}

func TestSaveWorkingMovesALineageOnlyInsideTheCase(t *testing.T) {
	// `onto` is the one number a caller does not get to choose, and the reason
	// §5.8's merged draft can descend from a revision the case already
	// reached. Both bounds are reachable only from inside the package — an
	// exported rebase hands over a revision it read from the record under the
	// same lock — which is why this is here and not in the external file, and
	// why the refusal carries a defect code instead of a stale-copy sentence.
	f := aMerge(t)
	st := newCaseStore()
	id, err := st.create(ctx(), newRefuser("create", 0),
		&Definition{img: f.img})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	refuse := newRefuser("rebase", id)

	w, err := st.working(ctx(), refuse, "dana", f.img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if at, err := st.saveWorking(ctx(), refuse, "dana", w.rev,
		workingState{s: w.s}, 0); err != nil || at != w.rev+1 {
		t.Fatalf("saving with no lineage to move = {%d, %v}, want {%d, nil}",
			at, err, w.rev+1)
	}
	// The case has not committed, so revision 0 is the only number its history
	// can vouch for and the highest the guard takes.
	if at, err := st.saveWorking(ctx(), refuse, "dana", w.rev+1,
		workingState{s: w.s}, 1); err == nil {
		t.Errorf("saving onto revision 1 of a case at 0 = {%d, nil}, want"+
			" the refusal: a lineage a caller could invent is one that skips"+
			" a commit and jumps §5.4's guard", at)
	} else if !Failed(err, SchemaMismatch) {
		t.Errorf("saving past the case's revision = %v, want SCHEMA_MISMATCH",
			err)
	}
	if _, err := st.saveWorking(ctx(), refuse, "dana", w.rev+1,
		workingState{s: w.s}, 99); !Failed(err, SchemaMismatch) {
		t.Errorf("saving onto a revision that was never issued = %v, want"+
			" SCHEMA_MISMATCH", err)
	}
	if _, err := st.saveWorking(ctx(), refuse, "dana", w.rev+1,
		workingState{s: w.s}, 0); err != nil {
		t.Errorf("saving onto the case's own revision = %v, want success:"+
			" 0 is what a caller passes to say it has no lineage to move,"+
			" and the copy's own base already is the case's revision", err)
	}

	// A save over a copy nobody opened is a caller bug and not a state of the
	// case. This one *is* reachable from a host: two sessions of one actor,
	// the slower one saving after the other discarded.
	if _, err := st.saveWorking(ctx(), refuse, "nima", 0,
		workingState{s: newState(f.img)}, 0); !Failed(err, InvalidOp) {
		t.Errorf("saving over no copy = %v, want INVALID_OPERATION", err)
	}

	// `discard` answers the same two shapes apart: an actor with no copy is
	// already where a discard aims, so 0 is the honest success — and any other
	// number is a caller that believes it holds edits the store never heard
	// of. The actor who does hold a copy hears the guard instead, because
	// invariant 4 makes both the draft and its refusal per-user.
	if err := st.discard(ctx(), refuse, "nima", 0); err != nil {
		t.Errorf("discarding a copy that was never there = %v, want the"+
			" success §6.5's next-access rule makes it", err)
	}
	if err := st.discard(ctx(), refuse, "nima", 4); !Failed(err, InvalidOp) {
		t.Errorf("discarding a number the store never issued = %v, want"+
			" INVALID_OPERATION", err)
	}
	if err := st.discard(ctx(), refuse, "dana", 1); !Failed(err, StaleRevision) {
		t.Errorf("discarding a moved copy at an old number = %v, want"+
			" STALE_REVISION", err)
	}
	// The newest number is the one that destroys the draft, and after it the
	// store's answer is the missing-copy shape again: this is the pair the
	// external file cannot tell apart without a second session.
	if err := st.discard(ctx(), refuse, "dana", w.rev+2); err != nil {
		t.Fatalf("discarding at the copy's newest number: %v", err)
	}
	if err := st.discard(ctx(), refuse, "dana", 0); err != nil {
		t.Errorf("a second discard = %v, want the success that says there"+
			" is nothing left to throw away", err)
	}

	// The lower bound needs a copy whose lineage has already moved, and from
	// outside the package only a merge moves it, so the case's revision is
	// stamped here: the claim is about the guard and not about how the number
	// got there. A lineage that could be rewound is one that lets the same
	// commit merge into the same draft twice — the copy would descend from 2
	// again after having been brought to 4, and §5.4's submit guard would
	// re-admit history the draft has already absorbed.
	st.mu.Lock()
	rec := st.cases[id]
	rec.revision = 4
	st.mu.Unlock()
	bola, err := st.working(ctx(), refuse, "bola", f.img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if _, err := st.saveWorking(ctx(), refuse, "bola", bola.rev,
		workingState{s: bola.s}, 4); err != nil {
		t.Fatalf("moving the lineage onto a revision the case reached: %v",
			err)
	}
	if _, err := st.saveWorking(ctx(), refuse, "bola", bola.rev+1,
		workingState{s: bola.s}, 2); !Failed(err, SchemaMismatch) {
		t.Errorf("rewinding the lineage to revision 2 = %v, want"+
			" SCHEMA_MISMATCH: a base that moves backwards is a skipped"+
			" commit going the other way", err)
	}
	// And onto the very revision the copy already descends from: equal is
	// not rewound, and the upper half of the bound still holds at the edge.
	if _, err := st.saveWorking(ctx(), refuse, "bola", bola.rev+1,
		workingState{s: bola.s}, 4); err != nil {
		t.Errorf("moving onto the same revision = %v, want the save that"+
			" keeps the lineage exactly where it is", err)
	}
}

func TestRebaseRefusalSaysHowMuchWorkIsLeft(t *testing.T) {
	// A host that cannot show paths — a log line, an audit record — still has
	// to say how many fields the user has to look at, so the count is the
	// whole list's length and not the part the sentence has room for.
	short := rebaseRefusal([]string{"application.name", "application.fee"}, 0)
	for _, want := range []string{"2 paths", "application.name",
		"application.fee", "untouched"} {
		if !strings.Contains(short, want) {
			t.Errorf("a two-path refusal %q does not say %q", short, want)
		}
	}
	if strings.Contains(short, "more") {
		t.Errorf("a short refusal was truncated: %q", short)
	}

	many := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		many = append(many, "application.field")
	}
	long := rebaseRefusal(many, 0)
	if !strings.Contains(long, "6 paths") || !strings.Contains(long, "2 more") {
		t.Errorf("a six-path refusal reads %q, want the whole count and the"+
			" part of the list a sentence can hold", long)
	}
	if strings.Count(long, "application.field") > 4 {
		t.Errorf("the refusal pasted the list into a sentence: %q", long)
	}

	// The veiled count is the difference between "resolve these" and a puzzle
	// the actor cannot open, so it belongs beside the way out.
	veiled := rebaseRefusal([]string{"application"}, 3)
	if !strings.Contains(veiled, "3 clashes are") ||
		!strings.Contains(veiled, "Discard") {
		t.Errorf("a veiled refusal reads %q, want the count of what cannot"+
			" be shown and the way out", veiled)
	}
	if one := rebaseRefusal([]string{"application"}, 1); !strings.Contains(one,
		"1 clash is") {
		t.Errorf("one veiled clash reads %q, want the singular", one)
	}
}

func TestReportNamesVeiledClashesByFormAndCollapsesThem(t *testing.T) {
	f := aMerge(t)
	vis := make([]bool, len(f.img.slots))
	for i := range vis {
		vis[i] = true
	}
	vis[f.ns.who] = false
	vis[f.ns.where] = false

	// Two fields this actor may not see, one they may: the veiled ones are
	// named by the form and nothing under it, because an instance ID inside a
	// hidden collection is case data, and so is the fact that this particular
	// field moved (§5.7's container rule, applied where the package next turns
	// an address into text).
	m := &rebaser{img: f.img, vis: vis, out: newState(f.img)}
	m.clash(f.ns.who, f.holderIs(5))
	m.clash(f.ns.where, f.homeIs(5, 6))
	m.clash(f.ns.fee, nil)

	paths, veiled := m.report()
	if veiled != 2 {
		t.Errorf("%d of %v are veiled, want 2", veiled, paths)
	}
	if len(paths) != 2 {
		t.Fatalf("the report is %v, want two entries: the two hidden fields"+
			" degrade to one form name, and a host that reads it twice learns"+
			" nothing new", paths)
	}
	if paths[0] != "application" || paths[1] != "application.fee" {
		t.Errorf("the report is %v, want the form first and the visible"+
			" field after it, in the order the merge found them", paths)
	}

	// Nothing to report is no list at all, and not a slice a host renders as
	// one empty entry.
	if none, count := (&rebaser{img: f.img}).report(); none != nil || count != 0 {
		t.Errorf("an empty merge reported %v (%d veiled)", none, count)
	}
}
