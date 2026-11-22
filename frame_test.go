package monshi

// RUNTIME §5.1's layout and §5.2's presence and requiredness, tested against
// the SYNTAX §7 example and two small definitions the example cannot express
// (a cleared default, and collections inside collections).
//
// Nothing here goes through permissions or the operations: §5.2 is a pure
// function of a row, and keeping it that way is why one closure serves both
// the committed and the candidate state.

import (
	"testing"

	"github.com/shopspring/decimal"
)

// enumTag finds one tag of a slot's enum type, by name.
func enumTag(t *testing.T, img *image, n nodeID, tag string) tagID {
	t.Helper()
	for _, tid := range img.types[img.slots[n].typ].tags {
		if img.tagName(tid) == tag {
			return tid
		}
	}
	t.Fatalf("%s has no tag %s", img.path(n), tag)
	return 0
}

// setKind writes application.kind — the guard operand of the example's two
// conditional blocks — into both states of a frame, so a test can ask either
// snapshot the question it means. It returns the frame for chaining.
func setKind(t *testing.T, f *frame, img *image, tag string) *frame {
	t.Helper()
	kind := slotByPath(t, img, "application.kind")
	id := int64(enumTag(t, img, kind, tag))
	for _, s := range []*state{&f.commit, &f.work} {
		c, ok := s.cellOf(kind)
		if !ok {
			t.Fatal("application.kind has no cell in the base row")
		}
		c.set(slotValue{set: true, i: id})
	}
	return f
}

func TestFrameMaterializesDefaults(t *testing.T) {
	img := lowerExample(t)
	f := newFrame(img)

	reg := slotByPath(t, img, "application.registered")
	if v := f.commit.value(reg); !v.set || !v.t.Equal(day(2026, 1, 1)) {
		t.Errorf("registered = %v, want the declared 2026-01-01", v)
	}
	fee := slotByPath(t, img, "application.fee")
	if v := f.commit.value(fee); !v.set || !v.d.Equal(decimal.NewFromInt(0)) {
		t.Errorf("fee = %v, want the declared 0.00", v)
	}
	// A stored false is a value, not an absence: §5.2's emptiness test asks
	// whether something was entered.
	urgent := slotByPath(t, img, "application.urgent")
	if v := f.commit.value(urgent); !v.set || v.i != 0 {
		t.Errorf("urgent = %v, want a stored false", v)
	}
	if !img.nonEmpty(urgent, f.commit.value(urgent)) {
		t.Error("a defaulted false must count as a value")
	}
	// An undeclared value is absent, and nothing else.
	note := slotByPath(t, img, "application.note")
	if v := f.commit.value(note); v.set {
		t.Errorf("note = %v, want absent", v)
	}
	// The working copy starts from the committed state (DESIGN §6.5) and is
	// independent of it: no working edit reaches committed data (invariant 1).
	c, ok := f.work.cellOf(note)
	if !ok {
		t.Fatal("application.note has no cell")
	}
	c.set(slotValue{set: true, s: "draft only"})
	if f.commit.value(note).set {
		t.Error("a working edit reached the committed row")
	}
}

func TestPresenceGuardsAndDerivedPruning(t *testing.T) {
	img := lowerExample(t)
	internal := slotByPath(t, img, "application.internal")
	risk := slotByPath(t, img, "application.internal.risk_score")

	// Absent operand: the guard is unsatisfied, so the group is not part of
	// the effective state (E6).
	fresh := newFrame(img)
	if fresh.commit.present(internal) {
		t.Error("internal is present with no kind entered")
	}
	if fresh.commit.reached(risk) {
		t.Error("a descendant of an absent group is reachable")
	}

	person := setKind(t, newFrame(img), img, "PERSON")
	if person.commit.present(internal) {
		t.Error("internal is present for a PERSON")
	}
	company := setKind(t, newFrame(img), img, "COMPANY")
	if !company.commit.present(internal) {
		t.Error("internal is absent for a COMPANY")
	}
	// reached is the chain, present is one guard.
	if !company.commit.reached(risk) {
		t.Error("risk_score is not reached while its group is present")
	}
	if !company.commit.reached(internal) || !company.commit.reached(
		slotByPath(t, img, "application")) {
		t.Error("a present chain answered false for itself")
	}
	// A node with no guard is present on its own account, whatever its
	// sibling's guard says.
	if !fresh.commit.present(slotByPath(t, img, "application.note")) {
		t.Error("an unconditional field is not present")
	}
	// One closure, two states: editing the candidate does not move the
	// committed answer, which is §5.1's swap and invariant 1 in miniature.
	kind := slotByPath(t, img, "application.kind")
	c, ok := company.work.cellOf(kind)
	if !ok {
		t.Fatal("application.kind has no cell")
	}
	c.set(slotValue{set: true, i: int64(enumTag(t, img, kind, "PERSON"))})
	if !company.commit.present(internal) {
		t.Error("the committed row changed when the working row was edited")
	}
	if company.work.present(internal) {
		t.Error("the candidate row still says COMPANY")
	}
}

func TestRequirednessIsEmptinessNotExistence(t *testing.T) {
	const src = `
roles {
    A { allow view, edit all; }
}
forms {
    f {
        n : integer required default 5;
        s : string required;
        b : boolean;
        kind : string;
        cond : integer required[.kind == "x"];
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`
	img, warnings := compile(t, src)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", diagnostics(warnings))
	}
	f := newFrame(img)
	n := slotByPath(t, img, "f.n")
	s := slotByPath(t, img, "f.s")
	b := slotByPath(t, img, "f.b")
	kind := slotByPath(t, img, "f.kind")
	cond := slotByPath(t, img, "f.cond")

	// A default is a value, so a required defaulted field is satisfied
	// before anyone types anything (DESIGN §4.1).
	if sat, ok := f.work.satisfiedHere(n); !ok || !sat {
		t.Errorf("required with a default: satisfied=%v answered=%v", sat, ok)
	}
	// Clearing it does not make the field absent; it makes it empty, and
	// empty fails required even though a default exists (§5.2).
	c, _ := f.work.cellOf(n)
	c.clear()
	if sat, _ := f.work.satisfiedHere(n); sat {
		t.Error("a cleared default satisfied required")
	}
	if !f.work.present(n) {
		t.Error("Empty removed the field from the structure")
	}
	// A required field never entered fails; zero and false are values.
	if sat, _ := f.work.satisfiedHere(s); sat {
		t.Error("an absent required string satisfied required")
	}
	c, _ = f.work.cellOf(s)
	c.set(slotValue{set: true, s: ""})
	if sat, _ := f.work.satisfiedHere(s); sat {
		t.Error("an empty string satisfied required")
	}
	// Not required, not answered by a guard: nothing to satisfy.
	if sat, ok := f.work.satisfiedHere(b); !ok || !sat {
		t.Errorf("an optional unset field: satisfied=%v answered=%v", sat, ok)
	}
	// required[expr] asks the guard first: unsatisfied guard, no demand.
	if sat, _ := f.work.satisfiedHere(cond); !sat {
		t.Error("cond is demanded while .kind is absent (E6)")
	}
	c, _ = f.work.cellOf(kind)
	c.set(slotValue{set: true, s: "x"})
	if sat, _ := f.work.satisfiedHere(cond); sat {
		t.Error("cond is not demanded once the guard holds")
	}
	c, _ = f.work.cellOf(cond)
	c.set(slotValue{set: true, i: 0})
	if sat, _ := f.work.satisfiedHere(cond); !sat {
		t.Error("a stored zero did not satisfy required[expr]")
	}
}

func TestCollectionRequirednessCountsInstances(t *testing.T) {
	img := lowerExample(t)
	coll := slotByPath(t, img, "application.shareholders")
	name := slotByPath(t, img, "application.shareholders.holder_name")

	// required[.kind == COMPANY]: no demand for a PERSON.
	person := setKind(t, newFrame(img), img, "PERSON")
	if sat, ok := person.commit.satisfiedHere(coll); !ok || !sat {
		t.Errorf("shareholders for a PERSON: satisfied=%v answered=%v", sat, ok)
	}
	// The guard holds, so one instance is needed: none exist yet.
	company := setKind(t, newFrame(img), img, "COMPANY")
	if sat, _ := company.work.satisfiedHere(coll); sat {
		t.Error("an empty collection satisfied required[.kind == COMPANY]")
	}
	id, pos, ok := company.work.addAt(coll, nil)
	if !ok {
		t.Fatal("Add refused the collection")
	}
	if id != 1 || pos != 0 {
		t.Errorf("first instance = (%d,%d), want (1,0)", id, pos)
	}
	if sat, _ := company.work.satisfiedHere(coll); !sat {
		t.Error("one instance did not satisfy required[.kind == COMPANY]")
	}
	// SYNTAX §4: members are still required, per instance.
	mc, ok := company.work.cellAt(name, instPath{{coll, id}})
	if !ok {
		t.Fatal("a member of the new instance is not addressable")
	}
	if company.work.satisfied(name, mc.get()) {
		t.Error("an unset required member satisfied requiredness")
	}
	mc.set(slotValue{set: true, s: "Ada Ltd"})
	if !company.work.satisfied(name, mc.get()) {
		t.Error("a filled required member did not satisfy requiredness")
	}
	// A member has no single value, so the state refuses to invent one: this
	// is E2's rule, expressed as an addressing property rather than a check.
	if _, ok := company.work.cellOf(name); ok {
		t.Error("a collection member has a cell in the base row")
	}
	if _, ok := company.work.satisfiedHere(name); ok {
		t.Error("satisfiedHere answered for a member")
	}
}

func TestInstanceRowsAreOffsetAndStable(t *testing.T) {
	img := lowerExample(t)
	coll := slotByPath(t, img, "application.shareholders")
	name := slotByPath(t, img, "application.shareholders.holder_name")
	shares := slotByPath(t, img, "application.shareholders.shares")

	// The row covers the collection's descendants, and the collection's own
	// slot does not live in it (§5.1).
	if got := img.rowLen(coll); got != 2 {
		t.Fatalf("rowLen(shareholders) = %d, want its two members", got)
	}
	if lo := img.rowLo(coll); lo != name {
		t.Errorf("rowLo(shareholders) = %d, want holder_name at %d", lo, name)
	}
	if img.collOf(shares) != coll || img.collOf(coll) != 0 {
		t.Error("collection membership is not the innermost enclosing range")
	}

	f := newFrame(img)
	_, _, _ = f.work.addAt(coll, nil)
	id2, _, _ := f.work.addAt(coll, nil)
	set := f.work.rows(coll)
	if set.count() != 2 {
		t.Fatalf("count = %d, want 2", set.count())
	}
	c, ok := f.work.cellAt(name, instPath{{coll, id2}})
	if !ok {
		t.Fatal("the second instance is not addressable")
	}
	c.set(slotValue{set: true, s: "second"})
	if first, _ := f.work.cellAt(name, instPath{{coll, 1}}); first.get().set {
		t.Error("instances share a row")
	}
	// Removal does not renumber the surviving IDs, and the allocator never
	// reuses one: a host's saved path keeps pointing where it pointed.
	if !f.work.removeAt(coll, nil, 1) {
		t.Fatal("Remove refused an existing instance")
	}
	if _, ok := f.work.rows(coll).find(1); ok {
		t.Error("the removed ID still addresses a row")
	}
	c, ok = f.work.cellAt(name, instPath{{coll, id2}})
	if !ok || c.get().s != "second" {
		t.Fatalf("the surviving instance moved: %v %v", ok, c.get())
	}
	id3, pos, _ := f.work.addAt(coll, nil)
	if id3 == id2 || id3 == 1 {
		t.Errorf("Add reused a stable ID: %d", id3)
	}
	if pos != 1 {
		t.Errorf("position after removal = %d, want 1", pos)
	}
	// Clone copies rows, so a candidate cannot write through to committed
	// data (invariant 1) — including the ID map, which is what a stale row
	// would otherwise expose.
	before, _ := f.work.cellAt(name, instPath{{coll, id2}})
	f.commit = f.work.clone()
	before.set(slotValue{set: true, s: "edited after the copy"})
	c2, _ := f.commit.cellAt(name, instPath{{coll, id2}})
	if c2.get().s == "edited after the copy" {
		t.Error("a working row wrote through into the committed copy")
	}
	if f.commit.rows(coll).count() != f.work.rows(coll).count() {
		t.Error("clone lost rows")
	}
}

func TestNestedAndRepeatedFieldRows(t *testing.T) {
	img, warnings := compile(t, nestedSrc)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", diagnostics(warnings))
	}
	if len(img.colls) != 3 {
		t.Fatalf("colls = %d, want tags, rows and cells", len(img.colls))
	}
	tags := slotByPath(t, img, "f.tags")
	rows := slotByPath(t, img, "f.rows")
	cells := slotByPath(t, img, "f.rows.cells")
	v := slotByPath(t, img, "f.rows.cells.v")
	n := slotByPath(t, img, "f.rows.n")

	// A repeated field's instances *are* values of that field, so its rows
	// cover the slot itself (rowLo == coll), and the field has no cell in the
	// base row.
	if img.rowLo(tags) != tags || img.rowLen(tags) != 1 {
		t.Errorf("repeated field row = [%d,%d), want one slot at %d",
			img.rowLo(tags), nodeID(int(img.rowLo(tags))+img.rowLen(tags)), tags)
	}
	if img.collOf(tags) != tags {
		t.Error("a repeated field is not its own collection")
	}
	f := newFrame(img)
	if _, ok := f.work.cellOf(tags); ok {
		t.Error("a repeated field has a single value")
	}
	tid, tpos, ok := f.work.addAt(tags, nil)
	if !ok {
		t.Fatal("Add refused a repeated field")
	}
	if c, ok := f.work.cellAt(tags, instPath{{tags, tid}}); !ok || c.get().set {
		t.Error("a fresh repeated field holds a value it was never given")
	} else {
		c.set(slotValue{set: true, s: "one"})
		if !f.work.value(tags).set {
			t.Error("a repeated field with one value counts as empty")
		}
	}
	if got := f.work.rows(tags).count(); got != 1 || tpos != 0 {
		t.Errorf("repeated field has %d rows at position %d, want 1 at 0", got, tpos)
	}
	// A repeated field answers §5.2 at the top level: it is a count, not a
	// value, and the count is global.
	if sat, ok := f.work.satisfiedHere(tags); !ok || !sat {
		t.Errorf("repeated field: satisfied=%v answered=%v", sat, ok)
	}

	// The default of a member materializes per instance, not per state.
	rid, _, ok := f.work.addAt(rows, nil)
	if !ok {
		t.Fatal("Add refused the outer collection")
	}
	rc, ok := f.work.cellAt(n, instPath{{rows, rid}})
	if !ok {
		t.Fatal("the outer member is not addressable")
	}
	if v := rc.get(); !v.set || v.i != 7 {
		t.Errorf("rows.n = %v, want the declared default 7", v)
	}
	// A nested collection's rows live inside the outer instance, and its own
	// slot belongs to the outer row's range, not to its own. It has no global
	// count, so §5.2 refuses to answer for it and countAt does.
	cid, _, ok := f.work.addAt(cells, instPath{{rows, rid}})
	if !ok {
		t.Fatal("Add refused the nested collection")
	}
	if img.collOf(v) != cells {
		t.Error("cells.v's innermost collection is not cells")
	}
	if img.collOf(cells) != rows {
		t.Error("the nested collection's own slot is not in the outer row")
	}
	if img.rowLo(cells) != v {
		t.Errorf("rowLo(cells) = %d, want v at %d", img.rowLo(cells), v)
	}
	vc, ok := f.work.cellAt(v, instPath{{rows, rid}, {cells, cid}})
	if !ok {
		t.Fatal("a nested member is not addressable")
	}
	if _, ok := f.work.cellAt(v, instPath{{rows, rid}}); ok {
		t.Error("a nested member resolved without its inner instance")
	}
	if _, ok := f.work.cellAt(v, instPath{{rows, 999}, {cells, cid}}); ok {
		t.Error("a stale outer ID still addressed a nested row")
	}
	if _, ok := f.work.satisfiedHere(cells); ok {
		t.Error("a nested collection answered §5.2 with no enclosing instance")
	}
	if n, ok := f.work.countAt(cells, instPath{{rows, rid}}); !ok || n != 1 {
		t.Errorf("countAt(cells in rows.%d) = %d %v, want 1", rid, n, ok)
	}
	if _, ok := f.work.countAt(cells, instPath{{rows, 999}}); ok {
		t.Error("countAt answered for an address that is not data")
	}
	vc.set(slotValue{set: true, i: 3})
	// The nested set belongs to one outer instance only.
	rid2, _, _ := f.work.addAt(rows, nil)
	if n, ok := f.work.countAt(cells, instPath{{rows, rid2}}); !ok || n != 0 {
		t.Errorf("countAt(cells in an instance with none) = %d %v, want 0", n, ok)
	}
	if _, ok := f.work.cellAt(v, instPath{{rows, rid2}, {cells, cid}}); ok {
		t.Error("a nested instance leaked to another outer row")
	}
	// Clone carries the nesting, and edits stay on their own side.
	f.commit = f.work.clone()
	if _, ok := f.commit.cellAt(v, instPath{{rows, rid}, {cells, cid}}); !ok {
		t.Fatal("clone lost a nested instance")
	}
	vc.set(slotValue{set: true, i: 4})
	cc, _ := f.commit.cellAt(v, instPath{{rows, rid}, {cells, cid}})
	if cc.get().i == 4 {
		t.Error("a nested working row wrote through into the committed copy")
	}
	// Removing an outer instance takes its nested rows with it (DESIGN §5.1:
	// Remove is not for deleting fixed schema nodes, it is for deleting rows).
	if !f.work.removeAt(rows, nil, rid) {
		t.Fatal("Remove refused the outer instance")
	}
	if _, ok := f.work.cellAt(v, instPath{{rows, rid}, {cells, cid}}); ok {
		t.Error("nested rows survived their outer instance")
	}
}

func TestStateValueOfACollectionIsItsRowCount(t *testing.T) {
	img := lowerExample(t)
	coll := slotByPath(t, img, "application.shareholders")
	f := newFrame(img)
	if f.work.value(coll).set {
		t.Error("an empty collection reports instances it does not have")
	}
	// A collection has no cell: what it has is rows, and value() says so
	// without inventing a payload slot (§5.1).
	if _, ok := f.work.cellOf(coll); ok {
		t.Error("a collection has a cell in the base row")
	}
	_, _, _ = f.work.addAt(coll, nil)
	if !f.work.value(coll).set {
		t.Error("one instance did not register as a value")
	}
}

// TestAppendixARequiredness completes RUNTIME Appendix A's trace as far as
// the shipped layer can answer: presence, requiredness and the instance rows
// between them. The rest of the trace is a transition, and that waits for
// Case (§10) — this test is the part that is honest today.
func TestAppendixARequiredness(t *testing.T) {
	img := lowerExample(t)
	kind := slotByPath(t, img, "application.kind")
	internal := slotByPath(t, img, "application.internal")
	risk := slotByPath(t, img, "application.internal.risk_score")
	coll := slotByPath(t, img, "application.shareholders")
	holder := slotByPath(t, img, "application.shareholders.holder_name")
	shares := slotByPath(t, img, "application.shareholders.shares")
	fee := slotByPath(t, img, "application.fee")
	registered := slotByPath(t, img, "application.registered")

	s := &newFrame(img).commit
	// F6, from the state's side: the collection has one condition, and it
	// is asked once for the whole set — never per row, and never per member.
	// The two attributes are one expression each, so the two closures answer
	// alike on every row, which is the most that sharing can mean here.
	if img.slots[coll].condReq == 0 || img.slots[internal].condPres == 0 {
		t.Fatal("the example's two conditional blocks lost their guards")
	}
	if img.slots[coll].condPres != 0 || img.slots[internal].condReq != 0 {
		t.Error("a presence guard and a requiredness guard are not one attribute")
	}
	for _, v := range []slotValue{{},
		{set: true, i: int64(enumTag(t, img, kind, "COMPANY"))},
		{set: true, i: int64(enumTag(t, img, kind, "PERSON"))}} {
		row := make(stateView, len(img.slots))
		row[kind] = v
		if got, want := img.cond(img.slots[coll].condReq)(row),
			img.cond(img.slots[internal].condPres)(row); got != want {
			t.Errorf("the two guards disagree on %v: %v vs %v", v, got, want)
		}
	}
	// A fresh case: the guard's operand was never entered, so E6 makes the
	// group absent and the collection undemanded.
	if s.present(internal) {
		t.Error("internal is present with no kind entered")
	}
	if !s.present(coll) {
		t.Error("shareholders' membership is not guarded, only its requiredness")
	}
	if sat, ok := s.satisfiedHere(coll); !ok || !sat {
		t.Errorf("shareholders: satisfied=%v answered=%v, E6 demands nothing",
			sat, ok)
	}
	if s.reached(risk) {
		t.Error("risk_score is reached under an absent group")
	}
	// Defaults are data the moment the case is (created, not read): the
	// two declared defaults are in the row, so nothing asks for them.
	if v := s.value(fee); !v.set || !v.d.IsZero() {
		t.Errorf("fee = %v, want the declared 0.00 in the row", v)
	}
	if v := s.value(registered); !v.set || !v.t.Equal(day(2026, 1, 1)) {
		t.Errorf("registered = %v, want the declared 2026-01-01 in the row", v)
	}
	for _, n := range []nodeID{slotByPath(t, img, "application.contact"), risk} {
		if sat, ok := s.satisfiedHere(n); !ok || !sat {
			t.Errorf("%s: satisfied=%v answered=%v, want nothing demanded",
				img.path(n), sat, ok)
		}
	}
	// `required` with no default is a demand a fresh case does not meet,
	// and the only way to meet it is to enter a value (§5.2's one test).
	full := slotByPath(t, img, "application.full_name")
	if sat, ok := s.satisfiedHere(full); !ok || sat {
		t.Errorf("%s: satisfied=%v answered=%v, want an unset demand",
			img.path(full), sat, ok)
	}

	// Now a COMPANY case, traced down to one complete shareholder.
	c, ok := s.cellOf(kind)
	if !ok {
		t.Fatal("application.kind has no cell in the base row")
	}
	c.set(slotValue{set: true, i: int64(enumTag(t, img, kind, "COMPANY"))})
	if !s.present(internal) || !s.reached(risk) {
		t.Error("a COMPANY case does not reach its internal block")
	}
	if sat, _ := s.satisfiedHere(coll); sat {
		t.Error("required[.kind == COMPANY] is satisfied with no rows")
	}
	id, _, ok := s.addAt(coll, nil)
	if !ok {
		t.Fatal("Add refused the collection")
	}
	if sat, _ := s.satisfiedHere(coll); !sat {
		t.Error("one instance did not satisfy the collection's requiredness")
	}
	// SYNTAX §4: requiredness inside a collection is per instance, so the
	// state answers it only when the instance is named (§5.4).
	if _, ok := s.satisfiedHere(holder); ok {
		t.Error("a member answered requiredness with no instance named")
	}
	for _, n := range []nodeID{holder, shares} {
		c, ok := s.cellAt(n, instPath{{coll, id}})
		if !ok {
			t.Fatalf("%s is not addressable in instance %d", img.path(n), id)
		}
		if s.satisfied(n, c.get()) {
			t.Errorf("%s is satisfied while unset", img.path(n))
		}
	}
	fill(t, s, holder, instPath{{coll, id}}, slotValue{set: true,
		s: "Ada Ltd"})
	fill(t, s, shares, instPath{{coll, id}}, slotValue{set: true,
		d: decimal.NewFromInt(1)})
	for _, n := range []nodeID{holder, shares} {
		mc, _ := s.cellAt(n, instPath{{coll, id}})
		if !s.satisfied(n, mc.get()) {
			t.Errorf("%s is still demanded once it holds a value", img.path(n))
		}
	}
	// The instance is complete, so the collection's requiredness has one
	// more answer to give — and it is the same one: the guard held, and a
	// row exists. §5.4 will ask it per instance at Submit.
	if sat, _ := s.satisfiedHere(coll); !sat {
		t.Error("a filled collection lost its requiredness answer")
	}
}

// fill writes one member of one instance, for the per-row assertions above.
func fill(t *testing.T, s *state, n nodeID, path instPath, v slotValue) {
	t.Helper()
	c, ok := s.cellAt(n, path)
	if !ok {
		t.Fatalf("%s is not addressable at %v", s.img.path(n), path)
	}
	c.set(v)
}

// TestRowsRefuseWhatTheyDoNotHold covers the refusals around the row model —
// the shapes that have no address, no count and no aggregate answer. Each is
// a claim §5.1 makes by its omission: a state does not invent a value for
// structure, and does not answer a question one row cannot ask.
func TestRowsRefuseWhatTheyDoNotHold(t *testing.T) {
	const plainSrc = `
roles {
    A { allow view, edit all; }
}
forms {
    f {
        x : integer;
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`
	img, _ := compile(t, plainSrc)
	p := newFrame(img)
	x := slotByPath(t, img, "f.x")
	if len(img.colls) != 0 {
		t.Fatalf("a definition without `multiple` declared %d collections",
			len(img.colls))
	}
	if p.work.rows(x) != nil {
		t.Error("rows() found a set for a slot that is not a collection")
	}
	if _, ok := p.work.cellOf(x); !ok {
		t.Error("an ordinary field has no cell in a collection-free state")
	}
	if p.work.value(x).set {
		t.Error("a field never entered holds a value")
	}

	img, _ = compile(t, nestedSrc)
	f := newFrame(img)
	s := &f.work
	rows := slotByPath(t, img, "f.rows")
	cells := slotByPath(t, img, "f.rows.cells")
	meta := slotByPath(t, img, "f.rows.meta")
	m := slotByPath(t, img, "f.rows.meta.m")
	if _, _, ok := s.addAt(rows, nil); !ok {
		t.Fatal("Add refused the outer collection")
	}
	// Structure inside an instance: a plain group's member is stored in the
	// enclosing instance's row — `meta` is not a collection, so it gets no
	// rows of its own (§5.1) — while the group itself has no value there.
	mc, ok := s.cellAt(m, instPath{{rows, 1}})
	if !ok {
		t.Fatal("a group inside a collection has no address in the outer row")
	}
	mc.set(slotValue{set: true, i: 2})
	if _, ok := s.cellAt(meta, instPath{{rows, 1}}); ok {
		t.Error("a plain group holds a value")
	}
	if sat, _ := s.satisfiedHere(m); sat {
		t.Error("m answered §5.2 in aggregate while its instance is unnamed")
	}
	if _, ok := s.satisfiedHere(meta); ok {
		t.Error("a group inside a collection answered §5.2 with no instance named")
	}
	if !s.fills(meta, slotValue{}) {
		t.Error("a plain group's requiredness asks for a value it cannot have")
	}
	// A collection at that address is a place in the data, never a payload.
	if _, ok := s.cellAt(cells, instPath{{rows, 1}}); ok {
		t.Error("a nested collection has a value at an instance address")
	}
	// A valid address with no rows is zero, not a failure — and the same
	// address in an instance that does not exist is a failure, not zero.
	if n, ok := s.countAt(cells, instPath{{rows, 1}}); !ok || n != 0 {
		t.Errorf("countAt in an instance with none = %d %v, want 0 true", n, ok)
	}
	if _, ok := s.countAt(cells, instPath{{rows, 7}}); ok {
		t.Error("countAt answered for an instance that is not data")
	}
	if s.removeAt(cells, instPath{{rows, 1}}, 9) {
		t.Error("Remove answered for a nested ID nobody issued")
	}
	if s.removeAt(rows, nil, 7) {
		t.Error("Remove answered for an outer ID nobody issued")
	}
	// A slot inside a collection has no base-row cell and no aggregate value,
	// so §5.2 refuses it in both directions: E2's rule as an addressing fact.
	if _, ok := s.cellOf(m); ok {
		t.Error("a collection's member has a cell in the base row")
	}
	if _, ok := s.satisfiedHere(m); ok {
		t.Error("a plain field inside a collection answered §5.2 in aggregate")
	}
	// `Add` is a collection operation: a field, however addressable, has no
	// rows to append to, and the state says so rather than inventing a set.
	if _, _, ok := s.addAt(m, nil); ok {
		t.Error("Add created instances of a slot that is not a collection")
	}
}
