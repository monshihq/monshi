package monshi_test

// Synchronization seen from outside the module (DESIGN §6.5; RUNTIME §5.8):
// what a stale draft can and cannot do once the case moved underneath it, what
// a rebase brings in, what it refuses to decide, and what `Discard` gives up.
//
// Every claim here is about the draft as a host sees it: the values a
// following `View` renders, the revisions a following `Update` or `Submit`
// accepts, and the codes that come back when either refuses. What the merge
// compares to reach those answers — the three versions, the row lists, the
// history entries they are read out of — is in `rebase_store_test.go`, in
// package, because no host can see any of it.
//
// The fixture states each claim with one declaration. Two roles with the same
// grants and different identities are what make a stale pair at all: a working
// copy belongs to a *user* (invariant 4), so two handles of one identity would
// share the draft and could never disagree with the case. `fee` carries a
// default, and it is not decoration — the merge's third version has to answer
// what the copy descended from at a cell nobody wrote, and a field whose base
// is its default rather than its absence is the only thing in a definition that
// can tell those two apart. `Shadow` may write a field it may not see, which is
// how §7's property 7 is stated at all: an actor with no view grant anywhere is
// the one whose clashes have to be named without naming anything.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

const rebaseSrc = `
types {
    money : decimal(14, 2);
}
roles {
    Applicant {
        allow view, edit, submit application;
    }
    Clerk {
        allow view, edit, submit application;
    }
    Shadow {
        allow edit application.note;
    }
}
forms {
    application {
        name : string;
        fee  : money default 0.00;
        note : string;

        holders multiple {
            who : string;
            homes multiple {
                where : string;
            }
        }
    }
}
stages {
    draft {
        accept filed;
    }

    filed { }
}
`

// The three actors these tests need: `applicant` is edit_test.go's dana, and
// elliot is a second writer of the same fields under a different identity.
// fred holds the one grant no one may read back.
var (
	rebaseClerk  = monshi.Actor{User: "elliot", PrimaryRole: "Clerk"}
	rebaseShadow = monshi.Actor{User: "fred", PrimaryRole: "Shadow"}
)

// rebaseCase is a runtime and one actor's handle on a new case, so a test can
// hand the same case to a second actor — which is what every claim below is
// about.
func rebaseCase(t *testing.T, actor monshi.Actor) (*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "rebase", rebaseSrc)
	c, err := rt.Create(ctx(), "rebase", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// openAs is the second handle, and the only reason a test needs the runtime
// back: the case the two actors share is identified by one ID.
func openAs(t *testing.T, rt *monshi.Runtime, id monshi.CaseID,
	actor monshi.Actor) monshi.Case {
	t.Helper()
	c, err := rt.Open(ctx(), id, actor)
	if err != nil {
		t.Fatalf("Open as %s: %v", actor.User, err)
	}
	return c
}

// mustRebase is a rebase expected to succeed, so every refusal in these tests
// is one the test did not ask for.
func mustRebase(t *testing.T, c monshi.Case) monshi.RebaseResult {
	t.Helper()
	res, err := c.Rebase(ctx())
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(res.Conflicts) > 0 {
		t.Fatalf("Rebase reported %v, want a clean merge", res.Conflicts)
	}
	return res
}

// textAt and moneyAt read one cell of one actor's *draft* through the view,
// which is the only exported way these tests can see a working state. A path
// is selected alone rather than read out of a wider view because §5.7's answer
// for content the actor may not see is a refusal, and a refusal is what some of
// these tests are asserting.
func textAt(t *testing.T, c monshi.Case, path string) string {
	t.Helper()
	return node(t, look(t, c, path), path).Value.Text()
}

func moneyAt(t *testing.T, c monshi.Case, path string) decimal.Decimal {
	t.Helper()
	return node(t, look(t, c, path), path).Value.Decimal()
}

// isBlank is how a test says a cell holds nothing rather than the empty
// string: §5.7 renders an untouched field, and only its kind says it was never
// written.
func isBlank(t *testing.T, c monshi.Case, path string) bool {
	t.Helper()
	return node(t, look(t, c, path), path).Value.Kind() == monshi.KindNone
}

// committedFee is one actor's move on the case that makes the other's draft
// stale: write `fee` and submit it, answering with the case's new revision.
func committedFee(t *testing.T, c monshi.Case, at string) uint64 {
	t.Helper()
	rev, _ := submitEdits(t, c, 0, monshi.Set("application.fee",
		monshi.Decimal(decimal.RequireFromString(at))))
	return rev
}

// submitEdits is one actor's whole move on the case: a batch and the submit
// that publishes it. It answers with both numbers a following call needs — the
// case's new revision, which is what another actor's copy is now behind, and
// the actor's own working revision, which the next batch *from them* presents.
// A §5.8 test is about two lineages at once, and reading one out of the other
// is how a fixture gets the order wrong.
func submitEdits(t *testing.T, c monshi.Case, working uint64,
	edits ...monshi.Edit) (uint64, uint64) {
	t.Helper()
	at := mustEdit(t, c, working, edits...)
	res, err := c.Submit(ctx(), "application", at)
	if err != nil {
		t.Fatalf("submitting working %d: %v", at, err)
	}
	return res.Revision, at
}

// mustAdd is a row creation, answered with the path §5.1 issued for it — the
// only address the other actor's draft will hold it under too.
func mustAdd(t *testing.T, c monshi.Case, working uint64,
	path string) (string, uint64) {
	t.Helper()
	res, err := c.Update(ctx(), working, monshi.Add(path))
	if err != nil {
		t.Fatalf("adding %s: %v", path, err)
	}
	if len(res.Created) != 1 {
		t.Fatalf("adding %s created %d rows, want 1", path, len(res.Created))
	}
	return res.Created[0], res.Working
}

// assertDraftAt is how a test says an operation left the copy alone. A batch
// built on a number the copy no longer carries is refused before it resolves or
// writes anything (§5.1's all-or-none), and the refusal names the number the
// stored copy really carries — which is the only exported way these tests can
// read a working revision without changing it.
func assertDraftAt(t *testing.T, c monshi.Case, want uint64) {
	t.Helper()
	_, err := c.Update(ctx(), want+7,
		monshi.Set("application.name", monshi.Text("probe")))
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("a batch at working %d = %v, want STALE_REVISION", want+7, err)
	}
	at := "is at revision " + strconv.FormatUint(want, 10)
	if !strings.Contains(err.Error(), at) {
		t.Errorf("the refusal %q does not say the copy "+at, err.Error())
	}
}

func TestAStaleDraftSubmitsOnlyAfterRebasing(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// One edit each, in that order: dana's draft descends from revision 0 and
	// the case is at 1 before she does anything else.
	base := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("from dana")))
	if at := committedFee(t, elliot, "25.00"); at != 1 {
		t.Fatalf("the case is at %d, want 1", at)
	}

	// §5.4's base guard is the reason this file exists: a submit of a draft
	// that descends from a revision the case no longer has is refused, and no
	// amount of editing fixes it.
	if _, err := dana.Submit(ctx(), "application", base); !monshi.Failed(err,
		monshi.StaleRevision) {
		t.Fatalf("submitting a stale draft = %v, want STALE_REVISION", err)
	}

	res := mustRebase(t, dana)
	if res.Base != 1 {
		t.Errorf("the copy now descends from %d, want 1", res.Base)
	}
	if res.Working != base+1 {
		t.Errorf("the copy is at working %d, want %d: a rebase writes the"+
			" draft, so the number the next batch presents changes",
			res.Working, base+1)
	}

	// The whole point of §6.5's "synchronization is mandatory before a
	// subsequent submit": the same submit now goes through, and the commit it
	// writes holds both actors' content, not one of them.
	sub, err := dana.Submit(ctx(), "application", res.Working)
	if err != nil {
		t.Fatalf("submitting after a rebase: %v", err)
	}
	if sub.Revision != 2 {
		t.Errorf("the case is at %d, want 2", sub.Revision)
	}
	if got := textAt(t, dana, "application.name"); got != "from dana" {
		t.Errorf("the committed name is %q, want the draft's own", got)
	}
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("25")) {
		t.Errorf("the committed fee is %s, want the other actor's 25.00", got)
	}
}

func TestRebaseTakesACommittedChangeToADefaultedField(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// dana never writes `fee`. Her copy holds its materialized default, which
	// is the case's own answer at revision 0 — and that is the fact the merge
	// has to get right: read the base as *absence* instead and her untouched
	// 0.00 looks like an edit she made, every defaulted field the case changes
	// becomes a conflict, and the user is asked to decide a thing they never
	// touched.
	base := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("from dana")))
	committedFee(t, elliot, "25.00")

	res := mustRebase(t, dana)
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("25")) {
		t.Errorf("the draft holds %s for a field its actor never wrote,"+
			" want the committed 25.00", got)
	}
	if got := textAt(t, dana, "application.name"); got != "from dana" {
		t.Errorf("the draft's own edit reads %q, want it kept", got)
	}
	if rev, err := dana.Revision(ctx()); err != nil || rev != 1 {
		t.Errorf("the case is at %d (%v) after a rebase, want 1: a rebase"+
			" commits nothing", rev, err)
	}

	// The copy is now synchronized, which is the number the submit guard
	// reads, and the working revision moved by one because the draft was
	// written.
	if res.Working != base+1 || res.Base != 1 {
		t.Errorf("Rebase = {%d, %d}, want working %d at base 1", res.Working,
			res.Base, base+1)
	}
}

func TestRebaseReportsTheFieldBothSidesChanged(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// Both actors moved the same cell to different values, and this is the
	// one case §6.5 will not decide for them.
	base := mustEdit(t, dana, 0,
		monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("7.50"))))
	committedFee(t, elliot, "25.00")

	res, err := dana.Rebase(ctx())
	if !monshi.Failed(err, monshi.Conflict) {
		t.Fatalf("rebasing over a clashed field = %v, want CONFLICT", err)
	}
	if got := strings.Join(res.Conflicts, " "); got != "application.fee" {
		t.Errorf("Conflicts = %q, want the one address both sides changed",
			got)
	}

	// The refusal changes nothing, which is §6.5's "a failed/conflicting
	// rebase leaves the previous working state intact" and the reason the
	// merge runs on a clone: same draft, same base, same revision number for
	// the next batch.
	if res.Base != 0 {
		t.Errorf("a refused rebase reports base %d, want the copy's own 0",
			res.Base)
	}
	if res.Working != base {
		t.Errorf("a refused rebase reports working %d, want the untouched %d",
			res.Working, base)
	}
	assertDraftAt(t, dana, base)
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("7.5")) {
		t.Errorf("the draft reads %s after a refused rebase, want the"+
			" actor's own 7.50", got)
	}
	if rev, err := dana.Revision(ctx()); err != nil || rev != 1 {
		t.Errorf("the case is at %d (%v), want 1: nothing committed", rev, err)
	}

	// And the copy really is where it was: a batch built against the number
	// the caller already holds still writes it, which is what tells a host it
	// may show the same form again.
	mustEdit(t, dana, base, monshi.Set("application.name", monshi.Text("x")))

	// The other exit is `Discard`, and it stays open: the clash is not a dead
	// end, it is a decision the user has to make.
	if err := dana.Discard(ctx(), base+1); err != nil {
		t.Fatalf("discarding after a conflict: %v", err)
	}
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("25")) {
		t.Errorf("after a discard the draft reads %s, want the case's 25.00",
			got)
	}
}

func TestRebaseTakesNoConflictFromWritesThatAgree(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// Both sides typed the same thing into the same cell. A merge that
	// reported this would be reporting that two users agreed.
	base := mustEdit(t, dana, 0, monshi.Set("application.fee",
		monshi.Decimal(decimal.RequireFromString("25.00"))))
	committedFee(t, elliot, "25.00")

	res := mustRebase(t, dana)
	if res.Working != base+1 || res.Base != 1 {
		t.Errorf("Rebase = {%d, %d}, want working %d at base 1", res.Working,
			res.Base, base+1)
	}
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("25")) {
		t.Errorf("the draft reads %s after two agreeing writes, want 25.00",
			got)
	}
}

func TestRebaseIsSilentWhenTheCommitsCancelOut(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	base := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("from dana")))

	// The other shape of agreement is a change that came back. The case moves
	// `fee` away and then straight to what the copy descended from, while
	// dana's draft still descends from revision 0 and never rebased in
	// between: the base has to be read from the *newest* commit before her
	// copy, not the first, or the window's two commits look like two
	// decisions instead of no decision at all.
	committedFee(t, elliot, "25.00")
	if _, at := submitEdits(t, elliot, 1, monshi.Set("application.fee",
		monshi.Decimal(decimal.RequireFromString("0.00")))); at != 2 {
		t.Fatalf("the second batch is at working %d, want 2", at)
	}

	res := mustRebase(t, dana)
	if res.Base != 2 {
		t.Errorf("the copy now descends from %d, want the case's 2", res.Base)
	}
	if res.Working != base+1 {
		t.Errorf("Rebase reports working %d, want %d", res.Working, base+1)
	}
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("0")) {
		t.Errorf("the draft reads %s, want the committed 0.00: two commits"+
			" whose net effect is nothing still say what the value is", got)
	}
	if got := textAt(t, dana, "application.name"); got != "from dana" {
		t.Errorf("the draft's own edit reads %q, want it kept", got)
	}
}

func TestRebaseBringsInARowTheCaseAdded(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	mustEdit(t, dana, 0, monshi.Set("application.name", monshi.Text("dana")))

	// A committed row, with a nested row inside it: the case's own work, which
	// dana's draft has never seen and has no claim on.
	holder, at := mustAdd(t, elliot, 0, "application.holders")
	home, at := mustAdd(t, elliot, at, holder+".homes")
	if rev, _ := submitEdits(t, elliot, at,
		monshi.Set(holder+".who", monshi.Text("Elliot Ltd")),
		monshi.Set(home+".where", monshi.Text("Pune"))); rev != 1 {
		t.Fatalf("the case is at %d, want 1", rev)
	}

	mustRebase(t, dana)

	// §5.1's stable IDs are what this test is really about: the row arrives in
	// another actor's draft under the *same* address the case issued, because
	// nothing about a rebase renumbers a row that was created elsewhere.
	res := look(t, dana, "application.holders")
	holders := node(t, res, "application.holders")
	if len(holders.Children) != 1 {
		t.Fatalf("the draft holds %d holders, want the one the case added:\n%s",
			len(holders.Children), render(holders.Children, ""))
	}
	got := holders.Children[0]
	if got.Path != holder {
		t.Errorf("the row is at %q, want the ID the case issued, %q",
			got.Path, holder)
	}
	if n := find(got.Children, holder+".who"); n == nil {
		t.Fatalf("%s.who is missing:\n%s", holder, render(got.Children, ""))
	} else if n.Value.Text() != "Elliot Ltd" {
		t.Errorf("the copied row reads %q, want the committed value",
			n.Value.Text())
	}
	inner := node(t, res, holder+".homes")
	if len(inner.Children) != 1 {
		t.Fatalf("%s.homes has %d rows, want 1: a rebase that copied the"+
			" outer row and dropped its nested ones would be deleting work",
			holder, len(inner.Children))
	}
	if n := find(inner.Children, home+".where"); n == nil {
		t.Fatalf("%s.where is missing:\n%s", home, render(inner.Children, ""))
	} else if n.Value.Text() != "Pune" {
		t.Errorf("the nested row reads %q, want the committed value",
			n.Value.Text())
	}
	if got := textAt(t, dana, "application.name"); got != "dana" {
		t.Errorf("the draft's own edit reads %q, want it kept", got)
	}
}

func TestRebaseDropsARowTheCaseRemoved(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	holder, at := mustAdd(t, elliot, 0, "application.holders")
	rev, his := submitEdits(t, elliot, at,
		monshi.Set(holder+".who", monshi.Text("Elliot Ltd")))
	if rev != 1 {
		t.Fatalf("the case is at %d, want 1", rev)
	}

	// dana now holds the row, at base 1, having written nothing in it.
	mustEdit(t, dana, 0, monshi.Set("application.name", monshi.Text("dana")))
	mustRebase(t, dana)

	// The case then removes it, and the draft's only claim on the row is that
	// it contains the committed value: §6.5's "committed changes are
	// integrated into the working state" means the deletion lands too, and
	// with no conflict to report.
	if rev, _ := submitEdits(t, elliot, his, monshi.Remove(holder)); rev != 2 {
		t.Fatalf("the case is at %d after the removal, want 2", rev)
	}
	mustRebase(t, dana)

	res := look(t, dana, "application.holders")
	if got := len(node(t, res, "application.holders").Children); got != 0 {
		t.Errorf("the draft still holds %d holders, want the case's none", got)
	}
	if got := textAt(t, dana, "application.name"); got != "dana" {
		t.Errorf("the draft's own edit reads %q, want it kept", got)
	}
}

func TestRebaseRefusesToInterleaveTwoSetsOfAddedRows(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// Both actors added a row to the same collection. Unioning the two lists
	// is the part of §6.5 that is deferred — it is a decision about order, and
	// about whose row the user sees first — so the merge says the collection
	// clashed rather than guessing an answer that would look like a fact.
	danaRows, err := dana.Update(ctx(), 0,
		monshi.Add("application.holders"), monshi.Add("application.holders"))
	if err != nil {
		t.Fatalf("adding holders: %v", err)
	}
	one := danaRows.Created[0]
	mustEdit(t, dana, danaRows.Working,
		monshi.Set(one+".who", monshi.Text("Dana One")))

	holder, at := mustAdd(t, elliot, 0, "application.holders")
	if _, err := elliot.Submit(ctx(), "application", mustEdit(t, elliot, at,
		monshi.Set(holder+".who", monshi.Text("Elliot Ltd")))); err != nil {
		t.Fatalf("submitting the holder: %v", err)
	}

	res, err := dana.Rebase(ctx())
	if !monshi.Failed(err, monshi.Conflict) {
		t.Fatalf("rebasing two added rows = %v, want CONFLICT", err)
	}
	// One entry, at the collection: the rows' own fields are inside this
	// decision, not separate ones beside it.
	if got := strings.Join(res.Conflicts, " "); got != "application.holders" {
		t.Errorf("Conflicts = %q, want the collection once", got)
	}

	// The draft keeps its own rows and neither of the case's, which is what
	// "the previous working state is intact" says about a set: the row that
	// clashed is not half-merged into an added one and not emptied of the
	// value its own author typed.
	view := look(t, dana, "application.holders")
	rows := node(t, view, "application.holders").Children
	if got := len(rows); got != 2 {
		t.Fatalf("the draft holds %d holders, want its own 2:\n%s", got,
			render(rows, ""))
	}
	if n := find(rows[0].Children, one+".who"); n == nil {
		t.Fatalf("%s.who is missing from the kept row:\n%s", one,
			render(rows[0].Children, ""))
	} else if n.Value.Text() != "Dana One" {
		t.Errorf("the draft's own row reads %q, want the value it holds",
			n.Value.Text())
	}
}

func TestRebaseReportsARowTheDraftDeletedThatTheCommitsChanged(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	holder, at := mustAdd(t, elliot, 0, "application.holders")
	rev, his := submitEdits(t, elliot, at,
		monshi.Set(holder+".who", monshi.Text("Elliot Ltd")))
	if rev != 1 {
		t.Fatalf("the case is at %d, want 1", rev)
	}

	// dana takes the row into her draft, then deletes it. The case, meanwhile,
	// changed the value inside the row she removed.
	mustEdit(t, dana, 0, monshi.Set("application.name", monshi.Text("dana")))
	her := mustRebase(t, dana)
	dropped := mustEdit(t, dana, her.Working, monshi.Remove(holder))

	if rev, _ = submitEdits(t, elliot, his, monshi.Set(holder+".who",
		monshi.Text("Elliot Holdings"))); rev != 2 {
		t.Fatalf("the case is at %d after the change, want 2", rev)
	}

	res, err := dana.Rebase(ctx())
	if !monshi.Failed(err, monshi.Conflict) {
		t.Fatalf("rebasing a deleted-but-changed row = %v, want CONFLICT", err)
	}
	// One entry, at the collection: the address `application.holders.1.who`
	// names a row this draft no longer has, and the decision is about the row,
	// not about the field inside it.
	if got := strings.Join(res.Conflicts, " "); got != "application.holders" {
		t.Errorf("Conflicts = %q, want the collection", got)
	}
	if res.Base != 1 {
		t.Errorf("the refused rebase reports base %d, want the copy's own 1",
			res.Base)
	}

	// The deletion stands, and the committed value did not leak back in: a
	// row the actor removed is not restored by a merge that cannot ask them
	// about it.
	view := look(t, dana, "application.holders")
	if got := len(node(t, view, "application.holders").Children); got != 0 {
		t.Errorf("the draft holds %d holders, want its own deletion kept", got)
	}
	if got := textAt(t, dana, "application.name"); got != "dana" {
		t.Errorf("the draft's own edit reads %q, want it kept", got)
	}
	assertDraftAt(t, dana, dropped)
	mustEdit(t, dana, dropped, monshi.Set("application.note",
		monshi.Text("still mine")))
}

func TestRebaseNamesAVeiledClashByItsFormOnly(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	fred := openAs(t, rt, dana.ID(), rebaseShadow)

	// fred may write `application.note` and may see nothing — not the field,
	// not the form, not the case. His draft therefore holds content the engine
	// cannot show him, and the merge still has to know he changed it.
	w := mustEdit(t, fred, 0, monshi.Set("application.note",
		monshi.Text("from fred")))
	at := mustEdit(t, dana, 0,
		monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("25.00"))),
		monshi.Set("application.note", monshi.Text("from dana")))
	if got, err := dana.Submit(ctx(), "application", at); err != nil ||
		got.Revision != 1 {
		t.Fatalf("committing the note: %v (case at %d)", err, got.Revision)
	}

	res, err := fred.Rebase(ctx())
	if !monshi.Failed(err, monshi.Conflict) {
		t.Fatalf("rebasing a clashed field fred may not view = %v, want"+
			" CONFLICT: a silent success would move his base past an edit"+
			" he can neither see nor resolve", err)
	}
	// The field's name is definition text and the value is not, but the *fact*
	// that this particular cell moved is a fact about somebody else's write.
	// §7's property 7 is held by naming the form and nothing under it.
	if got := strings.Join(res.Conflicts, " "); got != "application" {
		t.Errorf("Conflicts = %q, want the form alone", got)
	}
	if _, err := fred.View(ctx(), "application.note"); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("the view of the clashed field = %v, want ACCESS_DENIED",
			err)
	}

	// And the sentence says what the exit is, because a form name with no
	// explanation is a puzzle: the actor cannot open what he must resolve.
	msg := err.Error()
	for _, want := range []string{"application", "Discard"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal %q does not mention %q", msg, want)
		}
	}

	// His base did not move, so nothing he holds can be submitted over the
	// case's newer content — the copy stays stale until he chooses.
	if res.Base != 0 || res.Working != w {
		t.Errorf("the refused rebase reports {%d, %d}, want {0, %d}",
			res.Base, res.Working, w)
	}

	// The other exit works even where the view cannot go, because a discard
	// throws away content rather than naming it: the fresh copy the next
	// access makes is the committed state, and it descends from the case.
	if err := fred.Discard(ctx(), w); err != nil {
		t.Fatalf("discarding an unviewable draft: %v", err)
	}
	res = mustRebase(t, fred)
	if res.Working != 0 || res.Base != 1 {
		t.Errorf("after a discard the copy is at {%d, %d}, want {0, 1}: no"+
			" draft was allocated to answer the question", res.Working,
			res.Base)
	}
}

func TestRebaseWritesNothingWhenTheCopyIsAlreadySynchronized(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	// Two shapes of "nothing to merge", and they are different claims. One
	// holds no copy at all — dana, who has thrown hers away, and whom §6.5's
	// "a new working copy starts from the latest committed state" says is
	// already synchronized. The other holds a copy that descends from the
	// case's newest revision already: elliot, who has just committed.
	committedFee(t, elliot, "25.00")
	if err := dana.Discard(ctx(), 0); err != nil {
		t.Fatalf("discarding an untouched draft: %v", err)
	}

	res := mustRebase(t, dana)
	if res.Working != 0 {
		t.Errorf("an actor with no copy reports working %d, want 0: the"+
			" answer describes a draft that does not exist", res.Working)
	}
	if res.Base != 1 {
		t.Errorf("an actor with no copy reports base %d, want the case's 1",
			res.Base)
	}

	// And nothing was allocated on the way to saying so. This is §5.7's rule
	// for `View` — an operation that reads cannot create per-user state —
	// applied to the one other operation that walks committed content and
	// reports on it. It is observable: a draft the call had written would sit
	// one revision higher, and the first batch of a copy made here starts at 1.
	if got := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("dana"))); got != 1 {
		t.Errorf("the first batch after the rebase is at working %d, want 1",
			got)
	}

	// elliot's copy descends from the case already, so his rebase answers with
	// his own numbers and changes neither.
	before := mustRebase(t, elliot)
	if before.Base != 1 {
		t.Errorf("the committer's copy descends from %d, want 1", before.Base)
	}
	after := mustRebase(t, elliot)
	if before.Working != after.Working || before.Base != after.Base {
		t.Errorf("two rebases of a synchronized copy gave {%d, %d} and"+
			" {%d, %d}, want the same pair both times", before.Working,
			before.Base, after.Working, after.Base)
	}
	assertDraftAt(t, elliot, before.Working)

	// A copy with no lineage to move still answers the question a submit asks,
	// which is the reason §6.5 makes synchronization a call and not an error:
	// dana's next submit needs no rebase at all.
	if _, err := dana.Submit(ctx(), "application", 1); err != nil {
		t.Fatalf("submitting a draft made after a discard: %v", err)
	}
}

func TestDiscardDropsTheDraftAndLeavesTheCase(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)

	w := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("unfinished")),
		monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("7.50"))))
	committedFee(t, elliot, "25.00")

	if err := dana.Discard(ctx(), w); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	// §6.5's promise, stated as the view: the draft is gone, and what a new
	// one is made from is the latest committed state — not the revision the
	// discarded copy descended from, and not any of its values.
	if got := moneyAt(t, dana, "application.fee"); !got.Equal(
		decimal.RequireFromString("25")) {
		t.Errorf("the draft now reads %s for a discarded field, want the"+
			" case's 25.00", got)
	}
	if !isBlank(t, dana, "application.name") {
		t.Errorf("a discarded `name` still holds %q, want nothing",
			textAt(t, dana, "application.name"))
	}
	if rev, err := dana.Revision(ctx()); err != nil || rev != 1 {
		t.Errorf("the case is at %d (%v) after a discard, want 1: a discarded"+
			" draft was never content", rev, err)
	}

	// And the fresh copy really is fresh: the batch that makes it descends
	// from revision 1, so a submit of it needs no rebase.
	res, err := dana.Update(ctx(), 0, monshi.Set("application.note",
		monshi.Text("second try")))
	if err != nil {
		t.Fatalf("editing after a discard: %v", err)
	}
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("submitting a draft made after a discard: %v", err)
	}
}

func TestDiscardGuardsTheRevisionItWasShown(t *testing.T) {
	_, dana := rebaseCase(t, applicant)

	w := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("first")))
	again := mustEdit(t, dana, w, monshi.Set("application.note",
		monshi.Text("second")))

	// The number the caller presents is the one it observed, and a discard
	// asked with an old one would delete work this session never saw (§6.5's
	// reason, and invariant 4's).
	err := dana.Discard(ctx(), w)
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("discarding at %d = %v, want STALE_REVISION", w, err)
	}
	if !strings.Contains(err.Error(), "is at revision "+
		strconv.FormatUint(again, 10)) {
		t.Errorf("the refusal %q does not name the copy's newest number",
			err.Error())
	}

	// The draft survives the refusal, whole: both batches are still there for
	// the session that knows about the newest one.
	if got := textAt(t, dana, "application.name"); got != "first" {
		t.Errorf("the draft reads %q, want the first batch kept", got)
	}
	if err := dana.Discard(ctx(), again); err != nil {
		t.Fatalf("discarding at %d: %v", again, err)
	}
	if !isBlank(t, dana, "application.note") {
		t.Error("the draft is still there after a discard that matched")
	}
}

func TestDiscardOfAnAbsentDraftIsSuccessOnlyForTheRightNumber(t *testing.T) {
	rt, dana := rebaseCase(t, applicant)
	elliot := openAs(t, rt, dana.ID(), rebaseClerk)
	committedFee(t, elliot, "25.00")

	// An actor with no copy is already in the state a discard aims at, so 0 is
	// the honest answer and the call is a success.
	if err := dana.Discard(ctx(), 0); err != nil {
		t.Fatalf("discarding a draft that does not exist: %v", err)
	}
	if err := dana.Discard(ctx(), 0); err != nil {
		t.Errorf("discarding twice = %v, want the second to answer the same",
			err)
	}

	// Anything else is a caller that believes it holds edits the store has
	// never heard of, which is the bug §10 says not to launder into a refusal
	// about the case.
	if err := dana.Discard(ctx(), 4); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("discarding at 4 with no draft = %v, want INVALID_OPERATION",
			err)
	}

	// Which is a claim about *this* actor's copies and not about the case:
	// elliot holds a draft that has never been at 4, so for him the same
	// number is not an invention but a copy that has moved, and he hears the
	// guard `Update` and `Submit` give. Invariant 4 makes working state
	// per-user, and so it makes this refusal.
	if err := elliot.Discard(ctx(), 4); !monshi.Failed(err,
		monshi.StaleRevision) {
		t.Errorf("the clerk discarding at 4 = %v, want STALE_REVISION", err)
	}
}

// TestSynchronizationRefusesAHandleOnNothing is §6.5's two operations seen
// from the one caller that cannot be answered any other way: a host that
// ignored `Create`'s error holds a zero Case, and the answer must be the
// store's own NOT_FOUND rather than a merge attempted against whichever record
// happens to address as nothing. `Discard` matters here for a sharper reason
// than `Rebase`: its success case is "there is no draft", and a handle on
// nothing would otherwise report that success and send the caller off to look
// for a case that was never made.
func TestSynchronizationRefusesAHandleOnNothing(t *testing.T) {
	var gone monshi.Case
	if _, err := gone.Rebase(ctx()); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("Rebase on a zero Case = %v, want NOT_FOUND", err)
	}
	if err := gone.Discard(ctx(), 0); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("Discard on a zero Case = %v, want NOT_FOUND", err)
	}
	if err := gone.Discard(ctx(), 7); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("Discard(7) on a zero Case = %v, want NOT_FOUND: the"+
			" number is not this handle's to be told about", err)
	}
}
