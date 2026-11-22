package monshi

// RUNTIME §3.9's seam: caller text in, ordinals and a row address out. Most of
// what is asserted here is what resolution *refuses*, because that is the
// property the operations depend on: there is no near miss, and no partial
// answer for an operation to interpret.

import (
	"fmt"
	"testing"
)

func TestResolveWalksTheTreesIndex(t *testing.T) {
	img := lowerExample(t)
	f := newFrame(img)
	s := &f.commit

	for _, path := range []string{
		"application", "archive", "application.internal",
		"application.shareholders",
	} {
		r, ok := s.resolve(path)
		if !ok {
			t.Errorf("resolve(%q) failed: a declaration is not resolvable", path)
			continue
		}
		if r.hasValue {
			t.Errorf("resolve(%q) reports a value; %s has structure, not data",
				path, img.path(r.slot))
		}
		if img.path(r.slot) != path {
			t.Errorf("resolve(%q) = %s, want it back", path, img.path(r.slot))
		}
		if r.depth != 0 {
			t.Errorf("resolve(%q) traversed %d instances", path, r.depth)
		}
	}

	// Member names are scoped to their container (N3), so one last segment at
	// two depths names two slots and only the absolute path decides which.
	// This is why resolution walks rather than searches.
	appNote := slotByPath(t, img, "application.note")
	archNote := slotByPath(t, img, "archive.note")
	if appNote == archNote {
		t.Fatal("the example's two `note` fields share a slot")
	}
	cases := []struct {
		path string
		want nodeID
	}{
		{"application.note", appNote},
		{"archive.note", archNote},
		{"application.internal.risk_score",
			slotByPath(t, img, "application.internal.risk_score")},
		{"application.shareholders.holder_name",
			slotByPath(t, img, "application.shareholders.holder_name")},
		{"application.reviewer", slotByPath(t, img, "application.reviewer")},
	}
	for _, tc := range cases {
		r, ok := s.resolve(tc.path)
		if !ok || r.slot != tc.want {
			t.Errorf("resolve(%q) = (%d,%v), want slot %d",
				tc.path, r.slot, ok, tc.want)
			continue
		}
		// A member of a collection has no single value until an instance
		// is named; a role-typed field is a value like any other, since
		// it is being a *guard operand* that E2 forbids, not being
		// addressable.
		member := img.collOf(tc.want) != 0 && img.collOf(tc.want) != tc.want
		if r.hasValue == member {
			t.Errorf("resolve(%q) hasValue = %v, want %v",
				tc.path, r.hasValue, !member)
		}
	}

	for _, path := range []string{
		"",     // nothing to resolve
		".",    // two empty segments
		"note", // a member name is not a path
		"nope", // no such form
		"application.nope",
		"application.", // a trailing separator is not an empty name
		"application.note.x",
		"application.5", // a number where a name belongs
		"APPLICATION",   // names are not folded
	} {
		if r, ok := s.resolve(path); ok {
			t.Errorf("resolve(%q) = slot %d, want the unknown-path refusal",
				path, r.slot)
		}
	}
}

func TestResolveAddressesInstances(t *testing.T) {
	img, _ := compile(t, nestedSrc)
	f := newFrame(img)
	s := &f.work
	rows := slotByPath(t, img, "f.rows")
	cells := slotByPath(t, img, "f.rows.cells")
	tags := slotByPath(t, img, "f.tags")
	v := slotByPath(t, img, "f.rows.cells.v")

	// Nothing exists yet, so no ID addresses a row: resolution refuses rather
	// than creating an instance on its way past.
	if _, ok := s.resolve("f.rows.1"); ok {
		t.Error("resolution addressed an instance that was never added")
	}
	rid, _, _ := s.addAt(rows, nil)
	tid, _, _ := s.addAt(tags, nil)
	cid, _, _ := s.addAt(cells, instPath{{rows, rid}})

	// A repeated group's instance is an address; a repeated field's instance
	// is a value of that field (§5.1).
	r, ok := s.resolve(fmt.Sprintf("f.rows.%d", rid))
	if !ok || r.hasValue || r.slot != rows || r.depth != 1 {
		t.Errorf("resolve(f.rows.%d) = (%d,value=%v,depth=%d,%v), want the row",
			rid, r.slot, r.hasValue, r.depth, ok)
	}
	if r, ok := s.resolve(fmt.Sprintf("f.tags.%d", tid)); !ok || !r.hasValue ||
		r.slot != tags {
		t.Errorf("resolve(f.tags.%d) = (%d,value=%v,%v), want a value of f.tags",
			tid, r.slot, r.hasValue, ok)
	}

	// Members, at both depths, with the instance path they traversed.
	deep := fmt.Sprintf("f.rows.%d.cells.%d.v", rid, cid)
	r, ok = s.resolve(deep)
	if !ok || !r.hasValue || r.slot != v {
		t.Fatalf("resolve(%s) = (%d,value=%v,%v), want slot %d",
			deep, r.slot, r.hasValue, ok, v)
	}
	want := instPath{{rows, rid}, {cells, cid}}
	got := r.path()
	if len(got) != len(want) {
		t.Fatalf("resolve(%s) traversed %v, want %v", deep, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %v, want %v", i, got[i], want[i])
		}
	}

	// Writing through the answer reaches the row the path named and no other:
	// the cell and the path are one address, not two guesses.
	r.cell.set(slotValue{set: true, i: 11})
	if c, ok := s.cellAt(v, want); !ok || c.get().i != 11 {
		t.Error("the resolved cell and cellAt disagree about where the value is")
	}
	sib, _ := s.resolve(fmt.Sprintf("f.rows.%d.n", rid))
	if gv := sib.cell.get(); !gv.set || gv.i != 7 {
		t.Errorf("the sibling member of the same instance = %v, want default 7", gv)
	}
	other, _, _ := s.addAt(rows, nil)
	if _, ok := s.resolve(fmt.Sprintf("f.rows.%d.cells.%d.v", other, cid)); ok {
		t.Error("a nested instance leaked to another outer row")
	}

	for _, path := range []string{
		"f.rows.0",  // 0 was never issued
		"f.rows.01", // not how the ID was given to the host
		"f.rows.+1",
		"f.rows.1e0",
		"f.rows.99",                           // a stable ID that is not data
		fmt.Sprintf("f.rows.%d.2", rid),       // one collection addressed twice
		fmt.Sprintf("f.rows.%d.cells.9", rid), // no such nested row
		fmt.Sprintf("f.tags.%d.x", tid),       // a repeated field has no members
	} {
		if r, ok := s.resolve(path); ok {
			t.Errorf("resolve(%q) = slot %d, want refusal", path, r.slot)
		}
	}
	// A collection's own slot inside an instance: resolvable, addressable as a
	// place in the data, and still without a value — what it has is rows.
	if r, ok := s.resolve(fmt.Sprintf("f.rows.%d.cells", rid)); !ok {
		t.Error("a nested collection is not a declaration anyone may name")
	} else if r.hasValue || r.slot != cells {
		t.Errorf("resolve(f.rows.%d.cells) = (%d,value=%v), want the declaration",
			rid, r.slot, r.hasValue)
	}
	if _, ok := s.resolve(deep + ".too.deep"); ok {
		t.Error("a path deeper than the tree still resolved")
	}
}

func TestResolveNamesOrdinals(t *testing.T) {
	img := lowerExample(t)

	for _, name := range []string{"application", "archive"} {
		id, ok := img.formByName(name)
		if !ok || img.name(img.slots[img.forms[id]].name) != name {
			t.Errorf("formByName(%q) = (%d,%v)", name, id, ok)
		}
	}
	for _, name := range []string{"draft", "person_review", "company_review",
		"correction", "closed"} {
		id, ok := img.stageByName(name)
		if !ok || img.name(img.stages[id].name) != name {
			t.Errorf("stageByName(%q) = (%d,%v)", name, id, ok)
		}
	}
	for _, name := range []string{"Applicant", "Registrar", "Manager"} {
		id, ok := img.roleByName(name)
		if !ok || img.roleName(id) != name {
			t.Errorf("roleByName(%q) = (%d,%v)", name, id, ok)
		}
	}
	for _, name := range []string{"PERSON", "COMPANY"} {
		id, ok := img.tagByName(name)
		if !ok || img.tagName(id) != name {
			t.Errorf("tagByName(%q) = (%d,%v)", name, id, ok)
		}
	}
	// Ordinal 0 is "no such thing" everywhere, so a miss cannot be confused
	// with the zero value of a fresh variable (§3.1).
	for _, name := range []string{"application ", "Application", "applicant",
		"person", "Draft", "", "all", "PERSON "} {
		if id, ok := img.roleByName(name); ok || id != 0 {
			t.Errorf("roleByName(%q) = (%d,%v), want a miss", name, id, ok)
		}
		if id, ok := img.stageByName(name); ok || id != 0 {
			t.Errorf("stageByName(%q) = (%d,%v), want a miss", name, id, ok)
		}
		if id, ok := img.formByName(name); ok || id != 0 {
			t.Errorf("formByName(%q) = (%d,%v), want a miss", name, id, ok)
		}
		if id, ok := img.tagByName(name); ok || id != 0 {
			t.Errorf("tagByName(%q) = (%d,%v), want a miss", name, id, ok)
		}
	}
}

// TestResolveAllocatesNothing is RUNTIME §3.9's claim in one number: a path
// request costs an answer, not a graph. The instance path lives in a fixed
// array inside that answer, name segments are sliced without copying, and an
// interned path is exactly the string the front end interned — so a request
// neither builds nor retains anything proportional to what it was asked.
func TestResolveAllocatesNothing(t *testing.T) {
	img, _ := compile(t, nestedSrc)
	f := newFrame(img)
	s := &f.work
	rows := slotByPath(t, img, "f.rows")
	cells := slotByPath(t, img, "f.rows.cells")
	v := slotByPath(t, img, "f.rows.cells.v")
	rid, _, _ := s.addAt(rows, nil)
	cid, _, _ := s.addAt(cells, instPath{{rows, rid}})
	deep := fmt.Sprintf("f.rows.%d.cells.%d.v", rid, cid)
	c, ok := s.resolve(deep)
	if !ok || !c.hasValue || c.slot != v {
		t.Fatalf("%s does not resolve to a value: %+v %v", deep, c, ok)
	}
	c.cell.set(slotValue{set: true, i: 11})
	_ = ok
	slot := slotByPath(t, img, "f.rows.cells.v")
	if got := img.name(img.slots[slot].path); got != "f.rows.cells.v" {
		t.Fatalf("an interned path reads back as %q, not the string it was", got)
	}

	allocs := testing.AllocsPerRun(200, func() {
		r, ok := s.resolve(deep)
		if !ok || !r.hasValue {
			t.Fatal(deep + " does not resolve")
		}
		// The row the path named is the row the value lives in: the map
		// lookups that get there read closed arrays and a key taken from
		// the caller's own text, and build nothing on the way.
		if v := r.cell.get(); !v.set || v.i != 11 {
			t.Fatalf("the resolved cell = %v, want the value at %s", v, deep)
		}
		if n, ok := s.countAt(cells, r.path()[:1]); !ok || n != 1 {
			t.Fatalf("countAt(%v) = %d %v, want 1", r.path()[:1], n, ok)
		}
	})
	if _, ok := s.countAt(cells, instPath{{rows, rid}}); !ok {
		t.Fatal("countAt lost the nested set")
	}
	if allocs > 2 {
		t.Errorf("resolving a nested path cost %g allocations, want 2 at most",
			allocs)
	}
	// The same claim for a plain top-level path.
	if allocs := testing.AllocsPerRun(200, func() {
		if _, ok := s.resolve("f.rows"); !ok {
			t.Fatal("f.rows does not resolve")
		}
	}); allocs > 1 {
		t.Errorf("resolving a top-level path cost %g allocations", allocs)
	}
}

func BenchmarkResolvePath(b *testing.B) {
	img := mustLower(b, "bench", nestedSrc)
	f := newFrame(img)
	outer, _, ok := f.work.addAt(findSlot(img, "f.rows"), nil)
	if !ok {
		b.Fatal("the fixture's outer collection refused an Add")
	}
	if _, _, ok := f.work.addAt(findSlot(img, "f.rows.cells"),
		instPath{{findSlot(img, "f.rows"), outer}}); !ok {
		b.Fatal("the fixture's nested collection refused an Add")
	}
	if _, ok := f.work.resolve("f.rows.1.cells.2.v"); !ok {
		b.Fatal("the fixture's nested path does not resolve")
	}
	b.ReportAllocs()
	var slot nodeID
	for i := 0; i < b.N; i++ {
		r, ok := f.work.resolve("f.rows.1.cells.2.v")
		if !ok {
			b.Fatal("the fixture's nested path stopped resolving")
		}
		slot = r.slot
	}
	b.StopTimer()
	if want := findSlot(img, "f.rows.cells.v"); slot != want {
		b.Fatalf("resolved slot %d, want %d", slot, want)
	}
}
