package monshi

// In-package tests for where a decision lives and what moves it (RUNTIME
// §5.9). What a host observes — that a review commits, that a refusal arrives
// with the right category — is asserted externally in review_test.go. This file
// asserts the four things no host call reaches: the review table's allocation
// and replacement, §4.2's reset as `commit` applies it, the two non-content
// readers of a `chgReview` entry, and the gap list a destination's `require`
// builds.

import (
	"testing"
)

// reviewSrc states each of §4.2's shapes with one declaration: a verifiable
// form (`application`), a `verifiable` *field* inside it (F3's governed cell
// slot, which is the case §5.8's merge must not read as content), a `verifiable
// multiple` (F4's whole-collection decision), and a unit that is not a form at
// all (`archive.checks`, guarded so §5.2's pruning has something to remove).
//
// The nested annotations are what SYNTAX F3 says the front end warns about,
// which is why this fixture is compiled by `mustLowerSource` and not held to
// `lowerExample`'s warning-free rule.
const reviewSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
    }
    Registrar {
        allow view all;
        allow review application;
        allow sign application.approved_by;
    }
    Auditor {
        allow view all;
        allow review archive.checks;
    }
}
forms {
    application verifiable {
        kind       : legal_form required;
        full_name  : string required;
        signed_off : boolean verifiable;

        audit verifiable multiple {
            note : string;
        }

        approved_by : signature(Applicant);
    }

    archive {
        gate : boolean default false;

        checks verifiable present[.gate] {
            note : string;
        }
    }
}
stages {
    draft {
        allow Registrar accept, reject;
        accept filed;
        reject reopened;
    }

    filed {
        require application, archive;
    }

    reopened {
        allow Applicant view, edit, submit application;
    }
}
`

var (
	rvApplicant = Actor{User: "dana", PrimaryRole: "Applicant"}
	rvRegistrar = Actor{User: "elliot", PrimaryRole: "Registrar"}
)

// reviewCase is a runtime, its image and one actor's handle: the image because
// every claim below is about a specific slot, the handle because a decision is
// made through an operation.
func reviewCase(t *testing.T, actor Actor) (*Runtime, *image, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, reviewSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

// stored is the case as the store holds it, table and all: the one read that
// shows what a commit wrote rather than what an operation returned.
func stored(t *testing.T, rt *Runtime, c Case) storedCase {
	t.Helper()
	rec, err := rt.w.cases.load(newRefuser(open_, c.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return rec
}

// slotAt is the slot a declaration path names. A fixture that resolved to 0
// would make every claim below a claim about slot 0, which is the image's own
// root and pending forever.
func slotAt(t *testing.T, img *image, path string) nodeID {
	t.Helper()
	n := slotByPath(t, img, path)
	if n == 0 {
		t.Fatalf("%q lowers to slot 0", path)
	}
	return n
}

// decided is the table one review of `unit` would have stored, built by hand so
// the reset tests start from a decision instead of from the operation that
// makes one.
// resetBy runs §5.6's verification reset over a diff the way the store does, and
// hands back the table it produced. The list is one effect rather than the
// package's two, so that a test about a decision is not also a test about a
// signature.
func resetBy(img *image, have []reviewState,
	changes []commitChange) []reviewState {
	next := committedState{reviews: have}
	applyCommitEffects(img, &next, changes, []commitEffect{verifReset{}})
	return next.reviews
}

func decided(img *image, unit nodeID, code uint8, note string) []reviewState {
	tbl := reviewTable(img, nil)
	tbl[unit] = reviewState{code: code, by: "elliot", at: 1, note: note}
	return tbl
}

func TestAnUndecidedCaseHoldsNoTable(t *testing.T) {
	rt, img, c := reviewCase(t, rvApplicant)
	if rec := stored(t, rt, c); rec.commit.reviews != nil {
		t.Errorf("a new case holds a %d-entry table, want nil",
			len(rec.commit.reviews))
	}
	unit := slotAt(t, img, "application")
	if got := reviewAt(nil, unit).state(); got != ReviewPending {
		t.Errorf("an absent entry reads %q, want PENDING", got)
	}
	// Dense by slot: `reviewAt` answers with an array index, so the table is
	// sized to the definition and not to its units, and an entry for the last
	// slot has to exist for that index to mean anything.
	tbl := reviewTable(img, nil)
	if len(tbl) != len(img.slots) {
		t.Errorf("a fresh table has %d entries, want one per slot (%d)",
			len(tbl), len(img.slots))
	}
}

func TestADecisionReplacesTheTableAndNotTheEntry(t *testing.T) {
	rt, img, c := reviewCase(t, rvRegistrar)
	unit := slotAt(t, img, "application")
	before := stored(t, rt, c).commit.reviews
	if _, err := c.Review(ctx(), "application", ReviewApprove, 0, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	rec := stored(t, rt, c)
	got := reviewAt(rec.commit.reviews, unit)
	if got.code != verifApproved {
		t.Errorf("the stored unit reads %q, want APPROVED", got.state())
	}
	// `at` is the revision the decision's own commit produced, and that is the
	// number a later reader quotes: the content approved is the snapshot this
	// commit carried, and it still is.
	if got.at != rec.revision || got.at != 1 {
		t.Errorf("the stored decision says revision %d, want the commit's own",
			got.at)
	}
	if got.by != "elliot" {
		t.Errorf("the stored decision is by %q, want the reviewer", got.by)
	}
	// The replace-never-mutate rule, asserted on the value an earlier read
	// handed out: a commit that wrote through the table it was given would
	// rewrite what a concurrent view reported.
	if before != nil {
		t.Error("a case that had never been reviewed held a table")
	}
	second := reviewTable(img, rec.commit.reviews)
	second[unit] = reviewState{}
	if reviewAt(rec.commit.reviews, unit).code != verifApproved {
		t.Error("a copy of the stored table wrote through it")
	}
}

func TestAReviewCommitDoesNotResetItself(t *testing.T) {
	// §5.6's kind filter, which is the difference between a decision that stands
	// and one that unmakes itself: the entry names a unit whose content did not
	// move, and a reset that read a verdict as content would clear the mark the
	// same commit is writing. The driver asks the content kinds positively, so
	// this is the entry type that never reaches an effect at all.
	rt, img, c := reviewCase(t, rvRegistrar)
	unit := slotAt(t, img, "application")
	have := decided(img, unit, verifApproved, "")
	got := resetBy(img, have, []commitChange{
		{kind: chgReview, slot: unit,
			v: slotValue{set: true, i: int64(verifRejected), s: "no"}},
	})
	if reviewAt(got, unit).code != verifApproved {
		t.Error("a commit whose only change is a decision reset that decision")
	}
	if &got[0] != &have[0] {
		t.Error("a commit that reset nothing copied the table anyway")
	}
	// The same claim through the store, where the reset actually runs: a reject
	// after an approve must leave REJECTED and not pending.
	if _, err := c.Review(ctx(), "application", ReviewApprove, 0, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if _, err := c.Review(ctx(), "application", ReviewReject, 1, "wrong"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	st := reviewAt(stored(t, rt, c).commit.reviews, unit)
	if st.code != verifRejected || st.note != "wrong" {
		t.Errorf("the stored decision is %q %q, want REJECTED with its note",
			st.state(), st.note)
	}
}

func TestAContentCommitResetsItsUnit(t *testing.T) {
	// Two handles on one case, because the definition splits the work: the
	// Applicant may edit and submit and may not decide, and the Registrar may
	// decide and may not type. A fixture that let one actor do both would not
	// say which of the two the reset belongs to.
	rt, img, c := reviewCase(t, rvApplicant)
	elliot, err := rt.Open(ctx(), c.ID(), rvRegistrar)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	unit := slotAt(t, img, "application")
	name := slotAt(t, img, "application.full_name")
	if img.slots[name].verif != unit {
		t.Fatalf("full_name is governed by %d, want the form",
			img.slots[name].verif)
	}
	// Committed content first, so the approval is about something; then the
	// approval; then a reset asked of the diff the next commit would write. The
	// operation's own half of this — that the case really reads pending
	// afterwards — is the external file's claim.
	base, err := c.Update(ctx(), 0,
		Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Ada")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := elliot.Review(ctx(), "application", ReviewApprove,
		res.Revision, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	have := stored(t, rt, c).commit.reviews
	if reviewAt(have, unit).code != verifApproved {
		t.Fatalf("the approval stored %q, want APPROVED",
			reviewAt(have, unit).state())
	}
	got := resetBy(img, have, []commitChange{
		{kind: chgValue, slot: name, v: txt("Grace")}})
	if reviewAt(got, unit).code != verifPending {
		t.Error("a changed unit still read APPROVED after the reset")
	}
	// A reset is the absence of a decision and not a third state: the author and
	// the comment go with it, or a host renders "elliot rejected this" about
	// content nobody has looked at. §4.2 keeps that account in the history,
	// which is why clearing the entry here loses no fact.
	st := reviewAt(got, unit)
	if st.by != "" || st.at != 0 || st.note != "" {
		t.Errorf("the reset left %q@%d %q standing", st.by, st.at, st.note)
	}
}

func TestARowCommitResetsItsUnit(t *testing.T) {
	// F4 says a `verifiable multiple` is reviewed as a whole, so a row appearing
	// under it is content the reviewer never saw. The entry's slot is the
	// collection's declaration, whose `verif` is the governing form: skipping
	// `chgRows` would let an approval stand over new instances.
	_, img, _ := reviewCase(t, rvApplicant)
	app := slotAt(t, img, "application")
	coll := slotAt(t, img, "application.audit")
	if img.slots[coll].verif != app {
		t.Fatalf("the collection is governed by %d, want the form",
			img.slots[coll].verif)
	}
	got := resetBy(img, decided(img, app, verifApproved, ""),
		[]commitChange{rows(coll, nil, 7)})
	if reviewAt(got, app).code != verifPending {
		t.Error("a committed row left its unit APPROVED")
	}
}

func TestACommitOutsideEveryUnitSharesTheTable(t *testing.T) {
	img := mustLowerSource(t, reviewSrc)
	app := slotAt(t, img, "application")
	gate := slotAt(t, img, "archive.gate")
	// `archive.gate` sits under no `verifiable` declaration: a case whose
	// content moved nowhere near a unit pays no allocation for the rule.
	if img.slots[gate].verif != 0 {
		t.Fatalf("gate is governed by %d, want no unit", img.slots[gate].verif)
	}
	have := decided(img, app, verifApproved, "")
	got := resetBy(img, have, []commitChange{
		{kind: chgValue, slot: gate, v: slotValue{set: true, i: 1}}})
	if &got[0] != &have[0] {
		t.Error("a commit outside every unit copied the table")
	}
	if reviewAt(got, app).code != verifApproved {
		t.Error("content in another form cleared this unit")
	}
}

func TestOneCommitResetsEveryUnitItMoved(t *testing.T) {
	// A batch that filled fields of two units is one commit and one new table:
	// the dedup copies once, and both units go back to pending because both
	// moved.
	img := mustLowerSource(t, reviewSrc)
	app := slotAt(t, img, "application")
	checks := slotAt(t, img, "archive.checks")
	name := slotAt(t, img, "application.full_name")
	off := slotAt(t, img, "application.signed_off")
	note := slotAt(t, img, "archive.checks.note")
	have := decided(img, app, verifApproved, "")
	have[checks] = reviewState{code: verifApproved, by: "bo", at: 3}
	got := resetBy(img, have, []commitChange{
		{kind: chgValue, slot: name, v: txt("Ada")},
		{kind: chgValue, slot: name, v: txt("Grace")},
		{kind: chgValue, slot: off, v: slotValue{set: true, i: 1}},
		{kind: chgValue, slot: note, v: txt("seen")},
	})
	for _, pair := range []struct {
		what string
		n    nodeID
	}{{"application", app}, {"archive.checks", checks}} {
		if reviewAt(got, pair.n).code != verifPending {
			t.Errorf("%s still reads %q after its content moved", pair.what,
				reviewAt(got, pair.n).state())
		}
	}
}

func TestAPendingScanListsGoverningUnitsOnce(t *testing.T) {
	img := mustLowerSource(t, reviewSrc)
	app := slotAt(t, img, "application")
	s := newState(img)
	s.applyDefaults()

	// Both nested annotations are governed by the form (F3), so a scan of the
	// form's subtree answers one question and not three.
	audit := slotAt(t, img, "application.audit")
	off := slotAt(t, img, "application.signed_off")
	if img.slots[audit].verif != app || img.slots[off].verif != app {
		t.Fatal("a nested verifiable declaration is its own unit")
	}
	gaps := pendingUnits(&s, nil, app)
	if len(gaps) != 1 {
		t.Fatalf("an undecided form reports %d units, want the form alone",
			len(gaps))
	}
	if gaps[0].kind != gapVerif || gaps[0].path != img.path(app) {
		t.Errorf("the gap is %v, want a decision gap at the unit's path",
			gaps[0])
	}
	// Approved: nothing to ask. Rejected: still asked for, because §5.5's
	// conjunct names APPROVED and no other decision.
	ok := decided(img, app, verifApproved, "")
	if g := pendingUnits(&s, ok, app); len(g) != 0 {
		t.Errorf("an approved form lists %d units", len(g))
	}
	no := decided(img, app, verifRejected, "no")
	if g := pendingUnits(&s, no, app); len(g) != 1 {
		t.Errorf("a rejected form lists %d units, want 1", len(g))
	}
}

func TestAPendingScanHonoursPruning(t *testing.T) {
	// `archive.checks` is a unit that is not a form, and it is guarded: with the
	// gate false, §5.2 takes its subtree out of the effective state, and the
	// destination cannot ask for a decision about content nobody may see.
	img := mustLowerSource(t, reviewSrc)
	archive := slotAt(t, img, "archive")
	checks := slotAt(t, img, "archive.checks")
	gate := slotAt(t, img, "archive.gate")
	if img.slots[checks].verif != checks {
		t.Fatalf("checks is governed by %d, want itself",
			img.slots[checks].verif)
	}

	off := newState(img)
	off.applyDefaults()
	if len(pendingUnits(&off, nil, archive)) != 0 {
		t.Error("a pruned unit was listed as an approval the stage wants")
	}
	on := off.clone()
	c, ok := on.cellOf(gate)
	if !ok {
		t.Fatal("gate has no cell in the base row")
	}
	c.set(slotValue{set: true, i: 1})
	if !on.present(checks) {
		t.Fatal("the guard does not reach the unit once the gate is true")
	}
	if g := pendingUnits(&on, nil, archive); len(g) != 1 ||
		g[0].path != img.path(checks) {
		t.Errorf("a reachable unit lists %v, want one gap at its path", g)
	}
	g := pendingUnits(&on, decided(img, checks, verifApproved, ""), archive)
	if len(g) != 0 {
		t.Errorf("an approved unit still lists %d gaps", len(g))
	}
}

func TestAReviewEntryIsNotAMergeableChange(t *testing.T) {
	// §5.8's hazard, made concrete. `application.signed_off` is both a cell slot
	// and a governed node, so a merge that read a decision as a base value would
	// answer "what did the case hold here" with "who approved it" — and a draft
	// that never touched the box would be told the case says `true`.
	img := mustLowerSource(t, reviewSrc)
	off := slotAt(t, img, "application.signed_off")
	window := []commitRecord{{revision: 1, changes: []commitChange{
		{kind: chgReview, slot: off, v: slotValue{set: true,
			i: int64(verifApproved), s: "ok"}},
	}}}
	m := newReviewMerge(t, img)
	m.collect(window)
	if len(m.cells) != 0 || len(m.sets) != 0 {
		t.Fatalf("the value pass collected %d cells and %d sets from a"+
			" decision, want nothing", len(m.cells), len(m.sets))
	}
	// The base walk is the same entry on the other side of the split: a cell the
	// draft holds must not be answered from a decision entry, or the merge
	// compares the draft against a value the case never had.
	m.collect([]commitRecord{{revision: 1, changes: []commitChange{
		{kind: chgValue, slot: off, v: slotValue{set: true, i: 1}}}}})
	m.resolve([]commitRecord{{revision: 1, changes: window[0].changes}})
	if c := m.cells[0]; c.base.set || c.written {
		t.Errorf("the base walk read a decision as content: %+v", c)
	}
}

// newReviewMerge is the merge shell over a hand-written entry list: every slot
// visible, and the three states starting as the materialized empty case.
func newReviewMerge(t *testing.T, img *image) *rebaser {
	t.Helper()
	base := newState(img)
	base.applyDefaults()
	ours, cur := base.clone(), base.clone()
	vis := make([]bool, len(img.slots))
	for i := range vis {
		vis[i] = true
	}
	return &rebaser{
		img:     img,
		vis:     vis,
		ours:    &ours,
		cur:     &cur,
		out:     base.clone(),
		touched: map[instanceID]bool{},
		dropped: map[setKey]bool{},
		lost:    map[instanceID]bool{},
	}
}

func TestAReviewCommitSaysWhoAndWhen(t *testing.T) {
	// The entry's shape is the claim: a decision writes one entry, at the unit,
	// with the decision as its payload and no instance path — F4 means a
	// decision is never about one row — while the author and the moment are the
	// record's own and not repeated in the payload.
	rt, img, c := reviewCase(t, rvRegistrar)
	unit := slotAt(t, img, "application")
	res, err := c.Review(ctx(), "application", ReviewReject, 0, "incomplete")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	hs, err := rt.w.cases.history(newRefuser(open_, c.ID()))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hs) != 1 {
		t.Fatalf("history holds %d commits, want 1", len(hs))
	}
	rec := hs[0]
	if rec.op != opReview {
		t.Errorf("the entry's op is %d, want opReview", rec.op)
	}
	if rec.by != "elliot" || rec.revision != res.Revision {
		t.Errorf("the entry is by %q at revision %d, want elliot at %d",
			rec.by, rec.revision, res.Revision)
	}
	if len(rec.changes) != 1 {
		t.Fatalf("the decision wrote %d entries, want 1", len(rec.changes))
	}
	ch := rec.changes[0]
	if ch.kind != chgReview || ch.slot != unit || len(ch.path) != 0 {
		t.Errorf("the entry is kind %d at slot %d path %v, want a decision at"+
			" the unit with no instance path", ch.kind, ch.slot, ch.path)
	}
	if ch.v.i != int64(verifRejected) || ch.v.s != "incomplete" {
		t.Errorf("the entry carries %d %q, want the decision and its note",
			ch.v.i, ch.v.s)
	}
	if rec.stage != stored(t, rt, c).commit.stage {
		t.Errorf("the entry's stage is %d, want the stage the case is at",
			rec.stage)
	}
}

func TestATransitionCarriesTheTableForward(t *testing.T) {
	// A stage move commits nothing about content, so the decisions the case holds
	// must arrive at the next snapshot intact: an approval a transition dropped
	// would make the destination's own requirement unmeetable by the act of
	// meeting it.
	rt, img, c := reviewCase(t, rvRegistrar)
	app := slotAt(t, img, "application")
	res, err := c.Review(ctx(), "application", ReviewApprove, 0, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	tbl := stored(t, rt, c).commit.reviews
	if _, err := c.Transition(ctx(), "reopened", res.Revision); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	after := stored(t, rt, c)
	if reviewAt(after.commit.reviews, app).code != verifApproved {
		t.Error("the move lost the decision")
	}
	if &after.commit.reviews[0] != &tbl[0] {
		t.Error("a commit that moved no content rebuilt the table anyway")
	}
}
