package monshi

// The forward diff and the graft: what a commit *says* it changed, and what
// it replaces.
//
// These are the two halves of §6.3 that a host cannot see. `submit_test.go`
// watches the revision number, which is the diff's consequence; what is here
// is the diff itself, because the entry is what an audit read and a replay
// are built from (DESIGN §5.1: new values, never old), and a wrong entry is
// a wrong history that still committed cleanly.
//
// Three claims are worth the space. A removal is an entry rather than a
// silence — §5.1 counts it among the semantic effects, and a diff computed by
// comparing only the values that exist now would drop it. A `Remove` of an
// outer row says something about the rows *inside* it, which is why the
// instances compared are a union of both states rather than the new state's.
// And the graft replaces rather than merges, because a candidate that removed
// a row has to be able to commit that removal, which a merge of two row sets
// could not express.

import (
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// diffSrc is the fixture the diff needs and nothing more: two forms, so the
// scope has an outside; a repeated group with a nested collection, so a
// removal has something inside it to report; and string fields throughout,
// so a rendered change says what it holds without a class-dependent reader.
const diffSrc = `
roles {
    A {
        allow view, edit, submit application;
        allow view, edit, submit archive;
    }
}
forms {
    application {
        name   : string;
        note   : string;
        when   : datetime;
        amount : decimal(14, 2);

        holders multiple {
            who : string;

            homes multiple {
                city : string;
            }
        }
    }

    archive {
        filed : string;
    }
}
stages {
    draft { }
}
`

// diffStates is a committed snapshot and a candidate descended from it, which
// is the pair `diffForm` is called with. The candidate starts as a clone
// because that is what §5.1's copy is: a diff between unrelated states would
// report every field the draft never mentioned.
func diffStates(t *testing.T) (*image, *state, *state) {
	t.Helper()
	img := mustLowerSource(t, diffSrc)
	old := newState(img)
	next := old.clone()
	return img, &old, &next
}

// changeStrings renders a diff the way a failure wants to read it: one line
// per entry, addressed the way the entry addresses it. `rows=` marks a
// collection whose instance set moved, `=` a value, and `<none>` the absence
// a committed removal leaves behind.
func changeStrings(next *state, cs []commitChange) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		where := next.addr(c.slot, c.path)
		switch c.kind {
		case chgRows:
			out = append(out, where+" rows="+strconv.FormatInt(c.v.i, 10))
		case chgValue:
			out = append(out, where+"="+payload(c.v))
		}
	}
	return out
}

// payload renders one stored value without assuming which class it is, so a
// failure about a datetime says something a reader can check.
func payload(v slotValue) string {
	switch {
	case !v.set:
		return "<none>"
	case v.s != "":
		return v.s
	case !v.d.IsZero():
		return v.d.String() // the short form, which is enough to read a diff
	case !v.t.IsZero():
		return v.t.Format(time.RFC3339)
	}
	return "<value>"
}

// wantChanges fails unless the diff is exactly these rendered entries, in
// this order. Order is a claim: the walk is the preorder the numbering gives,
// and an audit read that interleaved a row's content with its collection's
// would not replay into the state it says it produced.
func wantChanges(t *testing.T, old, next *state, root nodeID,
	want ...string) {
	t.Helper()
	got := changeStrings(next, diffForm(old, next, root))
	if len(got) != len(want) {
		t.Fatalf("changes = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %q, want %q (whole diff %q)", i, got[i],
				want[i], got)
		}
	}
}

func TestDiffIsNothingWhenNothingMoved(t *testing.T) {
	img, old, next := diffStates(t)
	application := slotByPath(t, img, "application")
	holders := slotByPath(t, img, "application.holders")

	// An untouched candidate is the no-op case §6.3 decides the commit with,
	// and the one a wrong comparison would get wrong: a state pair built by
	// cloning has no differences by construction, so any entry at all means
	// something is being compared that should not be.
	wantChanges(t, old, next, application)

	// Fill both the base row and a collection, then diff a clone of the
	// filled state: rows equal by ID *and in order*, values equal by meaning.
	write(t, old, "application.name", "Monshi Ltd")
	write(t, old, "application.note", "")
	if _, _, ok := old.addAt(holders, nil); !ok {
		t.Fatal("Add refused the collection")
	}
	write(t, old, "application.holders.1.who", "Ada")
	same := old.clone()
	wantChanges(t, old, &same, application)

	// Writing the value a field already holds is not a change either — which
	// is what makes an edit that restated a value, and a hook that normalized
	// a value to itself, commit nothing rather than adding a revision whose
	// diff would be a lie.
	write(t, &same, "application.name", "Monshi Ltd")
	write(t, &same, "application.holders.1.who", "Ada")
	if _, _, ok := same.addAt(holders, nil); !ok {
		t.Fatal("Add refused the collection")
	}
	// The row it added is a change: a set that gained an instance moved,
	// whatever its cells hold. This is the entry an `Add` owes and the one a
	// value-only diff would miss entirely.
	wantChanges(t, old, &same, application,
		"application.holders rows=2")
}

func TestDiffComparesAValueByWhatItMeans(t *testing.T) {
	img, old, next := diffStates(t)
	application := slotByPath(t, img, "application")
	when := time.Date(2026, time.November, 23, 10, 0, 0, 0, time.UTC)
	put(t, img, old, "application.when", slotValue{set: true, t: when})
	put(t, img, old, "application.amount",
		slotValue{set: true, d: decimal.RequireFromString("1.00")})
	*next = old.clone()

	// The same instant through another zone, the same amount with another
	// written scale, and a field neither state has filled: none of them is a
	// change, because §5.1's diff records what the case says and not how a
	// client typed it. A commit that reported a reformat would advance the
	// revision for content nobody altered, and the audit read of it would
	// point at a value that had not moved.
	put(t, img, next, "application.when",
		slotValue{set: true, t: when.In(time.FixedZone("WAT", 3600))})
	put(t, img, next, "application.amount",
		slotValue{set: true, d: decimal.RequireFromString("1.0")})
	wantChanges(t, old, next, application)

	// The same two fields, moved, in the subtree's own order. Each entry
	// carries the value the case now holds — a *forward* diff, so a replay
	// needs nothing but the entries themselves.
	put(t, img, next, "application.when",
		slotValue{set: true, t: when.Add(time.Hour)})
	put(t, img, next, "application.amount",
		slotValue{set: true, d: decimal.RequireFromString("2.50")})
	wantChanges(t, old, next, application,
		"application.when=2026-11-23T11:00:00Z",
		"application.amount=2.5")
}

func TestDiffRecordsAValueAndItsRemoval(t *testing.T) {
	img, old, next := diffStates(t)
	application := slotByPath(t, img, "application")
	write(t, old, "application.name", "Monshi Ltd")
	write(t, old, "application.note", "see attached")

	write(t, next, "application.name", "Monshi Limited")
	if c, ok := next.resolve("application.note"); !ok {
		t.Fatal("application.note resolves to nothing")
	} else {
		c.cell.clear() // what `Empty` leaves: no value, field still there
	}

	// One entry for the new text and one for the field that has none. The
	// second is the reason a forward diff cannot be a set of (path, value)
	// pairs alone: §5.1 counts a removal among the committed effects, so the
	// entry has to carry the absence rather than omit it, and an audit that
	// read this diff must be able to tell "cleared" from "never existed".
	wantChanges(t, old, next, application,
		"application.name=Monshi Limited",
		"application.note=<none>")
}

func TestDiffRecordsASetAndTheRowsThatLeftIt(t *testing.T) {
	img, old, next := diffStates(t)
	application := slotByPath(t, img, "application")
	holders := slotByPath(t, img, "application.holders")

	for i := 0; i < 2; i++ {
		if _, _, ok := old.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	write(t, old, "application.holders.1.who", "Ada")
	write(t, old, "application.holders.2.who", "Bob")
	*next = old.clone()

	if _, _, ok := next.addAt(holders, nil); !ok {
		t.Fatal("Add refused the collection")
	}
	write(t, next, "application.holders.3.who", "Cleo")
	// The set's entry and the value it brought. A row that joined the set
	// *empty* would owe only the first — the case above this one asserts
	// exactly that, and together they say the diff has one entry per fact
	// rather than one per cell touched.
	wantChanges(t, old, next, application,
		"application.holders rows=3",
		"application.holders.3.who=Cleo")

	// Now remove the first row and change a value that survives it. The set
	// moved once, the gone row's content is reported as an absence, and the
	// order is the new state's rows first and the vanished ones after — the
	// diff's own argument for why a replay of it lands on what the case says
	// now.
	if !next.removeAt(holders, nil, 1) {
		t.Fatal("Remove refused the row")
	}
	write(t, next, "application.holders.2.who", "Robert")
	// The diff is against the *committed* state and not against the last
	// call, so row 3 is still a change here: it is part of what this candidate
	// says that the case does not. Order is the new state's rows first, in the
	// order they were created, and the vanished ones after — §5.1's argument
	// that a replay of this lands on what the case says now.
	wantChanges(t, old, next, application,
		"application.holders rows=2",
		"application.holders.2.who=Robert",
		"application.holders.3.who=Cleo",
		"application.holders.1.who=<none>")
}

func TestARemovedRowReportsTheRowsInsideIt(t *testing.T) {
	img, old, next := diffStates(t)
	application := slotByPath(t, img, "application")
	holders := slotByPath(t, img, "application.holders")
	homes := slotByPath(t, img, "application.holders.homes")

	// Two holders, one home each. Instance IDs are issued case-wide, so the
	// inner rows are 3 and 4, and a nested address carries both (§5.1's
	// reason a report says "holder 1's home 3" rather than "home 3").
	for i := 0; i < 2; i++ {
		if _, _, ok := old.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	write(t, old, "application.holders.1.who", "Ada")
	write(t, old, "application.holders.2.who", "Bob")
	for _, id := range []instanceID{1, 2} {
		host := instPath{{holders, id}}
		if _, _, ok := old.addAt(homes, host); !ok {
			t.Fatalf("Add refused the nested collection at holder %d", id)
		}
	}
	write(t, old, "application.holders.1.homes.3.city", "New York")
	write(t, old, "application.holders.2.homes.4.city", "Lagos")
	*next = old.clone()

	// Committing the removal of the *outer* row. A walk over the new state's
	// hosts alone would find no rows under a holder that is gone, and so
	// would report a commit that deleted a home and its city as a commit that
	// deleted one holder — which is the difference between an audit that can
	// replay this and one that cannot.
	if !next.removeAt(holders, nil, 1) {
		t.Fatal("Remove refused the row")
	}
	wantChanges(t, old, next, application,
		"application.holders rows=1",
		"application.holders.1.who=<none>",
		"application.holders.1.homes rows=0",
		"application.holders.1.homes.3.city=<none>")

	// And nothing about the holder that survived: the diff is a statement of
	// what moved, not a dump of the subtree.
	if cs := diffForm(old, next, slotByPath(t, img,
		"application.holders.homes")); len(cs) != 2 {
		t.Errorf("the inner collection's own diff is %q, want the two "+
			"entries about the vanished host", changeStrings(next, cs))
	}
}

func TestDiffStopsAtTheFormsEdge(t *testing.T) {
	img, old, next := diffStates(t)
	write(t, old, "application.name", "Monshi Ltd")
	write(t, old, "archive.filed", "no")

	write(t, next, "application.name", "Monshi Limited")
	write(t, next, "archive.filed", "yes")

	// The same candidate changes both forms, and each commit speaks about
	// one. §6.3 limits the diff to the selected form for the same reason it
	// limits completeness and validation: submitting the application is not
	// an act on the archive, and an entry about a field the caller never
	// presented would put someone else's draft in this commit's history.
	wantChanges(t, old, next, slotByPath(t, img, "application"),
		"application.name=Monshi Limited")
	wantChanges(t, old, next, slotByPath(t, img, "archive"),
		"archive.filed=yes")
}

func TestSubmitGraftsOneFormOntoTheCommittedState(t *testing.T) {
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, diffSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	img := d.img
	c, err := rt.Create(ctx(), d.ID(), Actor{User: "dana", PrimaryRole: "A"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	holders := slotByPath(t, img, "application.holders")

	// A case with one holder already committed, so the graft has a "was".
	base := mustUpdate(t, c, 0,
		Set("application.name", Text("Monshi Ltd")),
		Add("application.holders"),
		Set("application.holders.1.who", Text("Ada")))
	if _, err := c.Submit(ctx(), "application", base); err != nil {
		t.Fatalf("the first submit: %v", err)
	}

	// One draft touching both forms, and a row the candidate removes.
	base = mustUpdate(t, c, base,
		Remove("application.holders.1"),
		Add("application.holders"),
		Set("application.holders.2.who", Text("Ada Lovelace")),
		Set("application.note", Text("amended")),
		Set("archive.filed", Text("yes")))

	if _, err := c.Submit(ctx(), "application", base); err != nil {
		t.Fatalf("submitting one form: %v", err)
	}

	// The committed snapshot is the *old* one with this subtree replaced: the
	// application says what the candidate said, and the archive still says
	// nothing, because §6.3 is explicit that other forms' unsubmitted edits
	// stay unsubmitted. Reading the store rather than the copy is the point —
	// the two differ exactly here, and only in package can a test see it.
	rec, err := c.w.cases.load(newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := valueOf(t, &rec.commit.s, "application.note"); !got.set ||
		got.s != "amended" {
		t.Errorf("the committed application.note is %q, want the draft's",
			got.s)
	}
	if got := valueOf(t, &rec.commit.s, "archive.filed"); got.set {
		t.Errorf("the commit published another form's draft: %q", got.s)
	}
	// Replaced and not merged: the holder the candidate removed is gone from
	// the snapshot, even though the snapshot it was grafted onto had one. A
	// merge would keep it, and the case would say something no actor ever
	// committed.
	if n := rec.commit.s.rows(holders).count(); n != 1 {
		t.Errorf("the committed snapshot holds %d holders, want 1", n)
	}
	if got := valueOf(t, &rec.commit.s,
		"application.holders.2.who"); got.s != "Ada Lovelace" {
		t.Errorf("the surviving row is %q, want the candidate's content",
			got.s)
	}

	// The draft survives its own commit: the archive value is still in the
	// actor's copy, which is what lets her commit that form next without
	// retyping it. The copy's data is untouched by the rebase (§6.3), and the
	// one field the store moved is the copy's base.
	w, err := c.w.cases.working(newRefuser("open", c.id), c.actor.User, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if got := valueOf(t, &w.s, "archive.filed"); !got.set || got.s != "yes" {
		t.Errorf("the commit discarded a draft it did not publish: %q",
			got.s)
	}
	if w.base != 2 || w.rev != 2 {
		t.Errorf("the submitter's copy is base %d rev %d, want rebased onto "+
			"revision 2 with its own two batches still counted",
			w.base, w.rev)
	}

	// And the history is the diff, in the subtree's own order: the field the
	// draft changed, the set that moved, the row that joined it and the one
	// that left. Nothing about the archive, because §6.3 limits an entry to
	// the form the commit was of, and an entry about a field nobody presented
	// would put someone else's draft in this commit's record.
	hist, err := c.w.cases.history(newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("%d history entries, want 2", len(hist))
	}
	wantEntries(t, &rec.commit.s, hist[1], 2, "draft",
		"application.note=amended",
		"application.holders rows=1",
		"application.holders.2.who=Ada Lovelace",
		"application.holders.1.who=<none>")
	// The first commit, from an empty case: what a submit of a form with one
	// row owes, which is the same three shapes and no fourth.
	wantEntries(t, &rec.commit.s, hist[0], 1, "draft",
		"application.name=Monshi Ltd",
		"application.holders rows=1",
		"application.holders.1.who=Ada")
}

// wantEntries checks one recorded commit: the number it produced, the stage
// it was made at, and the diff it carries. The revision and stage are the
// store's and the operation's, and a history entry that got either wrong
// would replay into the wrong state, so they are asserted beside the changes
// rather than only in `authority_test.go`'s direct calls to the store.
func wantEntries(t *testing.T, s *state, rec commitRecord, rev uint64,
	stage string, want ...string) {
	t.Helper()
	if rec.revision != rev {
		t.Errorf("the entry says revision %d, want %d", rec.revision, rev)
	}
	if got := s.img.stageName(rec.stage); got != stage {
		t.Errorf("the entry says stage %q, want %q", got, stage)
	}
	if rec.op != opSubmit {
		t.Errorf("the entry's operation is %d, want a submit", rec.op)
	}
	if rec.by != "dana" {
		t.Errorf("the entry was made by %q, want the submitting actor",
			rec.by)
	}
	got := changeStrings(s, rec.changes)
	if len(got) != len(want) {
		t.Fatalf("entry %d holds %q, want %q", rec.revision, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d change %d = %q, want %q (whole entry %q)",
				rec.revision, i, got[i], want[i], got)
		}
	}
}

func TestACommitNeverTakesTheInstanceNumberBackwards(t *testing.T) {
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, diffSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), Actor{User: "dana", PrimaryRole: "A"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A draft that has added two rows the case has never committed, and a
	// commit of the *other* form. The snapshot that commit writes descends
	// from the previous snapshot and from no candidate subtree at all, so its
	// allocator would still be at zero if the graft did not carry the larger
	// of the two numbers across — and a snapshot at zero would sell instance
	// 1 to the next actor, to a case whose history already names it.
	base := mustUpdate(t, c, 0,
		Add("application.holders"),
		Add("application.holders"),
		Set("archive.filed", Text("yes")))
	if _, err := c.Submit(ctx(), "archive", base); err != nil {
		t.Fatalf("submitting the other form: %v", err)
	}

	rec, err := c.w.cases.load(newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.commit.s.seq != 2 {
		t.Errorf("the committed snapshot's allocator is at %d, want 2: the "+
			"number the draft reached is part of the case's data (§5.1), even "+
			"when the form that committed holds no rows", rec.commit.s.seq)
	}

	// Dana drafts a third row, and a second actor's copy — built from the
	// committed snapshot, which reached 2 — issues 3 as well. That is not a
	// reused ID: no two *committed* rows share one, and the pair of drafts
	// cannot both land, because the second commit is refused as stale and
	// §6.5's rebase is the operation that renumbers it. The alternative would
	// be reserving numbers at edit time, which would make an abandoned draft
	// cost the case an ID and put the allocator outside the state §5 says it
	// belongs to.
	base = mustUpdate(t, c, base, Add("application.holders"))
	other, err := rt.Open(ctx(), c.ID(), Actor{User: "elliot",
		PrimaryRole: "A"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := other.Update(ctx(), 0, Add("application.holders"))
	if err != nil {
		t.Fatalf("Add from the second actor: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "application.holders.3" {
		t.Errorf("a copy made from the committed snapshot issued %q, want "+
			"the next number after what that snapshot had reached",
			res.Created)
	}

	// The second half of the claim, which is the one §5.1 actually makes: once
	// a row is committed, its number is the case's forever. Dana commits the
	// form her three rows live in, and the next copy the case hands out starts
	// above all of them.
	if _, err := c.Submit(ctx(), "application", base); err != nil {
		t.Fatalf("submitting the drafted rows: %v", err)
	}
	if rec, err := c.w.cases.load(newRefuser("open", c.id)); err != nil {
		t.Fatalf("load: %v", err)
	} else if rec.commit.s.seq != 3 {
		t.Errorf("the snapshot holds rows to 3 and its allocator is at %d, "+
			"want 3", rec.commit.s.seq)
	}
	third, err := rt.Open(ctx(), c.ID(), Actor{User: "bo",
		PrimaryRole: "A"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err = third.Update(ctx(), 0, Add("application.holders"))
	if err != nil {
		t.Fatalf("Add from the third actor: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "application.holders.4" {
		t.Errorf("a copy made after three committed rows issued %q, want "+
			"instance 4: a number the case has sold is never sold again",
			res.Created)
	}
}

// valueOf reads one path's payload out of a state the test was handed, whether
// it came from `load` or from `working`. Both hand out a value the caller owns
// — a copy of the record's fields, and a clone of the copy — so reading
// through it is the same read an operation makes, and writing through it would
// be a test bug no operation can commit.
func valueOf(t *testing.T, s *state, path string) slotValue {
	t.Helper()
	r, ok := s.resolve(path)
	if !ok || !r.hasValue {
		t.Fatalf("%q reaches no value", path)
	}
	return r.cell.get()
}
