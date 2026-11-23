package monshi_test

// Review seen from outside the module (DESIGN §6.4; RUNTIME §5.9): what a host
// observes when somebody decides about a `verifiable` declaration — which
// refusal arrives with which category, what the view says about the decision,
// who is told about it, and what erases it.
//
// What a host cannot observe is in the in-package file: the review table's
// allocation, the shape of the history entry, and the merge filter that keeps a
// decision out of a rebase.
//
// The fixture splits the actors the way a registry splits them, because most of
// these claims are about two people holding one case: the Applicant may type and
// may not decide, the Registrar may decide at both units and may not type, and
// the Auditor's single grant sits at a declaration F3 takes the power to decide
// away from.

import (
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

const regSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
        allow view, edit, submit memo;
    }
    Registrar {
        allow view all;
        allow review application;
        allow review archive;
        allow review memo.rows;
    }
    Auditor {
        allow view all;
        allow review application.audit;
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
    }

    archive verifiable {
        filed_at : string;
    }

    memo {
        body : string;

        rows verifiable multiple {
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
	filing = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}
	ruling = monshi.Actor{User: "elliot", PrimaryRole: "Registrar"}
)

// registry is a runtime plus two handles on one fresh case: the applicant's,
// because content has to exist before a decision can be about it, and the
// reviewer's, because every claim below is about what he is told.
func registry(t *testing.T) (*monshi.Runtime, monshi.Case, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "registry", regSrc)
	dana, err := rt.Create(ctx(), "registry", filing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	elliot, err := rt.Open(ctx(), dana.ID(), ruling)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return rt, dana, elliot
}

// typed is the content the required fields ask for, committed, and the case's
// revision afterwards: a decision about an empty form is indistinguishable from
// a decision about nothing.
func typed(t *testing.T, dana monshi.Case) uint64 {
	t.Helper()
	draft(t, dana,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	return committed(t, dana, "application")
}

// draft is the way a host actually edits: read the copy's own number off the
// view, send the batch, keep the new one. A test that tracked working revisions
// by hand would be asserting its own arithmetic, and a rebase or a decision
// moves the number out from under it — which several claims below depend on.
// draft is the way a host actually edits: sync first (DESIGN §6.5 — the case may
// have moved, and a decision moves it even when no content does), read the copy's
// number off the view, send the batch. A test that tracked working revisions by
// hand would be asserting its own arithmetic.
func draft(t *testing.T, c monshi.Case, edits ...monshi.Edit) uint64 {
	t.Helper()
	res, err := c.Rebase(ctx())
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	ed, err := c.Update(ctx(), v.Working, edits...)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	_ = res
	return ed.Working
}

// added is a row a test actually commits: §5.2 keeps an instance with no data
// out of the effective state, so `Add` alone is a row only the actor who made
// it can still see. The address comes back as its own batch reported it, which
// is the only source a host has for §5.1's instance IDs.
func added(t *testing.T, c monshi.Case, coll, field string,
	val monshi.Value) string {
	t.Helper()
	if _, err := c.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	one, err := c.Update(ctx(), v.Working, monshi.Add(coll))
	if err != nil {
		t.Fatalf("Add(%s): %v", coll, err)
	}
	row := one.Created[0]
	if _, err = c.Update(ctx(), one.Working, monshi.Set(row+"."+field,
		val)); err != nil {
		t.Fatalf("Set(%s.%s): %v", row, field, err)
	}
	return row
}

// committed is a submit that must work, and the case's revision afterwards. The
// caller presents the copy's current number, which is what the view reports after
// a sync.
func committed(t *testing.T, c monshi.Case, form string) uint64 {
	t.Helper()
	if _, err := c.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	res, err := c.Submit(ctx(), form, v.Working)
	if err != nil {
		t.Fatalf("Submit(%s): %v", form, err)
	}
	return res.Revision
}

// TestReviewStateIsPendingUntilSomebodyDecides is §6.1's verification state at
// its plainest: a case nobody has reviewed has a state to report, it is reported
// on the governing unit rather than the path a caller typed, and a node under no
// `verifiable` declaration reports nothing at all.
func TestReviewStateIsPendingUntilSomebodyDecides(t *testing.T) {
	_, _, elliot := registry(t)
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	app := node(t, v, "application")
	if app.Review.Unit != "application" ||
		app.Review.State != monshi.ReviewPending {
		t.Errorf("the form reports %q/%q, want its own unit pending",
			app.Review.Unit, app.Review.State)
	}
	if app.Review.By != "" || app.Review.At != 0 || app.Review.Comment != "" {
		t.Errorf("a pending unit carries %q@%d %q, want nobody and nothing",
			app.Review.By, app.Review.At, app.Review.Comment)
	}
	if !app.Reviewable {
		t.Error("the reviewer is told he may not decide at a unit he may")
	}
	// F3 read from the other side: a `verifiable` field and a `verifiable` group
	// inside a verifiable form are governed by the form, so they report the
	// form's verdict and neither is a node a decision can be made at.
	off := node(t, v, "application.signed_off")
	if off.Review.Unit != "application" || off.Reviewable {
		t.Errorf("the nested field reports unit %q reviewable=%v, want the form"+
			" and not decidable", off.Review.Unit, off.Reviewable)
	}
	group := node(t, v, "application.audit")
	if group.Review.Unit != "application" || group.Reviewable {
		t.Errorf("the nested group reports %q reviewable=%v, want the form and"+
			" not decidable", group.Review.Unit, group.Reviewable)
	}
	// A second unit is a second answer, and it starts pending on its own: no
	// form's decision stands in for another's (§4.2's "not inherited").
	if arch := node(t, v, "archive"); arch.Review.Unit != "archive" ||
		arch.Review.State != monshi.ReviewPending || !arch.Reviewable {
		t.Errorf("the archive reports %q/%q reviewable=%v, want its own unit"+
			" pending and decidable", arch.Review.Unit, arch.Review.State,
			arch.Reviewable)
	}
	// And a node under no `verifiable` declaration has no unit to report, which
	// is a zero value and not an empty wrapper (§5.7's rule about absence).
	body := node(t, v, "memo.body")
	if body.Review != (monshi.ReviewInfo{}) {
		t.Errorf("a plain field reports %+v, want no review at all",
			body.Review)
	}
}

// TestOnlyTheUnitIsReviewable is §4.3's promise that a view carries the
// operations its actor may perform, tested on the hardest shape in the language:
// an actor with a real `review` grant at a declaration the engine will not take
// a decision for.
func TestOnlyTheUnitIsReviewable(t *testing.T) {
	rt, dana, _ := registry(t)
	bo, err := rt.Open(ctx(), dana.ID(), monshi.Actor{User: "bo",
		PrimaryRole: "Auditor"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v, err := bo.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	for _, path := range []string{"application", "application.audit"} {
		if n := node(t, v, path); n.Reviewable {
			t.Errorf("%s offers the auditor a decision", path)
		}
	}
	// The grant is real at that slot and useless there, so the refusal has to
	// explain that rather than contradict the definition: not "you may not" but
	// "this is not the unit", with the one that is named.
	if _, err := bo.Review(ctx(), "application.audit", monshi.ReviewApprove,
		v.Revision, ""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("a decision at a governed group = %v, want INVALID_OPERATION",
			err)
	} else if !strings.Contains(err.Error(), "\"application\" governs") {
		t.Errorf("the refusal says %q, want the governing unit named", err)
	}
	// The Applicant may not decide at all, and is answered about his permission
	// rather than about the definition.
	if _, err := dana.Review(ctx(), "application", monshi.ReviewApprove, 0,
		""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("an applicant approving his own form = %v, want ACCESS_DENIED",
			err)
	}
}

// TestADecisionCommitsAndIsReadBack is the operation's own account of itself: the
// revision it produced, the stage it did not move, and the view that says who
// decided and where.
func TestADecisionCommitsAndIsReadBack(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	res, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Revision != rev+1 {
		t.Errorf("the decision committed at %d, want %d", res.Revision, rev+1)
	}
	if res.Stage != "draft" {
		t.Errorf("a review moved the stage to %q, want draft", res.Stage)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	app := node(t, v, "application")
	if app.Review.State != monshi.ReviewApproved || app.Review.By != "elliot" {
		t.Errorf("the unit reads %q by %q, want APPROVED by elliot",
			app.Review.State, app.Review.By)
	}
	// `At` is the revision this decision's own commit produced, which is the
	// number the view above reports as current. A mark that quoted the revision
	// of the content approved would read as a statement about a case that no
	// longer exists the moment the approval lands — and §4.2's reset, not a
	// comparison of two numbers, is what decides when a decision stops applying.
	if app.Review.At != res.Revision || app.Review.At != v.Revision {
		t.Errorf("the decision quotes revision %d, want the commit's own %d (case at %d)",
			app.Review.At, res.Revision, v.Revision)
	}
	// Every node in the unit's subtree answers with the unit's verdict, because
	// it is the only verdict there is.
	if off := node(t, v, "application.signed_off"); off.Review.State !=
		monshi.ReviewApproved || off.Review.By != "elliot" {
		t.Errorf("a nested node reports %q by %q, want the form's decision",
			off.Review.State, off.Review.By)
	}
	// The other unit is untouched: a decision is about one declaration.
	if node(t, v, "archive").Review.State != monshi.ReviewPending {
		t.Error("approving the form decided the archive too")
	}
}

// TestARejectedUnitTellsTheApplicantWhy covers §4.2's two rules that belong to
// each other: a rejection must say why, and the reason exists for the person who
// may not decide.
func TestARejectedUnitTellsTheApplicantWhy(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewReject, rev,
		"   "); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("a rejection with no reason = %v, want INVALID_OPERATION", err)
	} else if !strings.Contains(err.Error(), "must say why") {
		t.Errorf("the refusal says %q, want the missing reason in it", err)
	}
	if again, err := elliot.Revision(ctx()); err != nil || again != rev {
		t.Errorf("the refused rejection left the case at %d (%v), want %d",
			again, err, rev)
	}
	// A note beside an approval is kept rather than dropped: §6.1 asks for "who,
	// when, which decision, comment", and a host that wants a note field on its
	// approve button should not have to file it somewhere else.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"checked against the register"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	mid, err := elliot.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application").Review; got.Comment !=
		"checked against the register" {
		t.Errorf("an approval with a note reports %q, want the note kept",
			got.Comment)
	}
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewReject,
		mid, "the name does not match"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// The applicant, who may not decide and may not even see the archive, reads
	// the decision about the form he wrote. Invariant 7 withholds what is not
	// viewable; a rejection nobody could read would be a wall instead.
	v, err = dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	app := node(t, v, "application")
	if app.Review.State != monshi.ReviewRejected ||
		app.Review.Comment != "the name does not match" {
		t.Errorf("the applicant reads %q %q, want REJECTED with the reason",
			app.Review.State, app.Review.Comment)
	}
	if app.Reviewable {
		t.Error("the applicant is offered a decision he cannot make")
	}
	if find(v.Roots, "archive") != nil {
		t.Error("the applicant's view holds the archive he may not view")
	}
	// The unit's reviewer is not disclosed either: the applicant sees that the
	// form was rejected by whom, and §4.3 gives him no flag to act on.
	if got := app.Review; got.By != "elliot" {
		t.Errorf("the applicant reads the decision by %q, want elliot: the"+
			" author of a rejection is who he has to go and talk to", got.By)
	}
}

// TestRepeatingADecisionCommitsNothing is §6.3's no-meaningless-commit rule
// stated for a decision, including the boundary the rule must not swallow: a
// note the first call did not carry is a different request.
func TestRepeatingADecisionCommitsNothing(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	first, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		rev, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	again, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		first.Revision, "")
	if err != nil {
		t.Fatalf("a repeated decision was refused: %v", err)
	}
	if again.Revision != first.Revision {
		t.Errorf("the repeat committed at %d, want the case still at %d",
			again.Revision, first.Revision)
	}
	noted, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		first.Revision, "second look")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if noted.Revision != first.Revision+1 {
		t.Errorf("adding a note committed at %d, want %d", noted.Revision,
			first.Revision+1)
	}
	// A different action from the same hand is a new fact, and it is stated here
	// with the *same* comment as the decision it replaces: author and note being
	// unchanged is exactly what a comparison of the request against the stored
	// decision could mistake for a duplicate, and the state is the one thing that
	// says this reviewer changed his mind.
	back, err := elliot.Review(ctx(), "application", monshi.ReviewReject,
		noted.Revision, "second look")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if back.Revision != noted.Revision+1 {
		t.Errorf("the reversal committed at %d, want %d", back.Revision,
			noted.Revision+1)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application").Review; got.State !=
		monshi.ReviewRejected || got.Comment != "second look" {
		t.Errorf("the case reads %q %q, want the reversal and its reason",
			got.State, got.Comment)
	}
	// And the same flip back, at the number the reversal reported: three facts
	// decide whether a commit is owed, and every one of them has to be able to
	// carry the difference alone.
	forward, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		back.Revision, "second look")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if forward.Revision != back.Revision+1 {
		t.Errorf("the flip back committed at %d, want %d", forward.Revision,
			back.Revision+1)
	}
}

// TestAnotherReviewerIsACommit states what one active decision per unit means: a
// second approval is not a duplicate of the first, it is a replacement, and the
// case says whose judgement stands.
func TestAnotherReviewerIsACommit(t *testing.T) {
	rt, dana, elliot := registry(t)
	rev := typed(t, dana)
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		rev, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	nell, err := rt.Open(ctx(), dana.ID(), monshi.Actor{User: "nell",
		PrimaryRole: "Registrar"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cur, err := elliot.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	res, err := nell.Review(ctx(), "application", monshi.ReviewApprove, cur,
		"")
	if err != nil {
		t.Fatalf("a second reviewer's approval was refused: %v", err)
	}
	if res.Revision != cur+1 {
		t.Errorf("the second approval committed at %d, want %d", res.Revision,
			cur+1)
	}
	v, err := nell.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	got := node(t, v, "application").Review
	if got.State != monshi.ReviewApproved || got.By != "nell" {
		t.Errorf("the unit reads %q by %q, want APPROVED by the last hand",
			got.State, got.By)
	}
	// And now the first reviewer cannot revoke it, which is the same fact seen
	// from the other side: §4.2's restriction follows the decision and not the
	// role.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewRevoke,
		res.Revision, ""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("revoking another reviewer's decision = %v want"+
			" ACCESS_DENIED", err)
	}
}

// TestRevocationIsTheAuthorsAlone is §4.2's restriction, including the two ways
// it declines to explain itself.
func TestRevocationIsTheAuthorsAlone(t *testing.T) {
	rt, dana, elliot := registry(t)
	rev := typed(t, dana)
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	nell, err := rt.Open(ctx(), dana.ID(), monshi.Actor{User: "nell",
		PrimaryRole: "Registrar"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cur, err := elliot.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	_, err = nell.Review(ctx(), "application", monshi.ReviewRevoke, cur, "")
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Fatalf("a revocation by the other reviewer = %v, want ACCESS_DENIED",
			err)
	}
	// The refusal names whose decision it is. Both reviewers may see the unit, so
	// its author is not a secret, and without the name the second one cannot tell
	// "you may not" from "there is nothing to take back".
	if !strings.Contains(err.Error(), "elliot") {
		t.Errorf("the refusal says %q, want the author it refuses to overrule",
			err)
	}
	// A revocation leaves no decision standing for a note to belong to, and the
	// comment is part of the stored decision rather than a message in the margin:
	// taking one would mean either dropping it or inventing a fourth state.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewRevoke, cur,
		"changed my mind"); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("a revocation with a comment = %v, want INVALID_OPERATION",
			err)
	}
	gone, err := elliot.Review(ctx(), "application", monshi.ReviewRevoke, cur,
		"")
	if err != nil {
		t.Fatalf("the author's own revocation was refused: %v", err)
	}
	if gone.Revision != cur+1 {
		t.Errorf("the revocation committed at %d, want %d", gone.Revision,
			cur+1)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	got := node(t, v, "application").Review
	if got.State != monshi.ReviewPending || got.By != "" || got.At != 0 ||
		got.Comment != "" {
		t.Errorf("after the revocation the unit reads %q by %q@%d %q, want"+
			" pending with nobody on it", got.State, got.By, got.At,
			got.Comment)
	}
	// Nothing decided is not a permission question. The two refusals must not be
	// confusable, or a caller cannot tell whose decision to go and ask about.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewRevoke,
		gone.Revision, ""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("revoking a pending unit = %v, want INVALID_OPERATION", err)
	} else if !strings.Contains(err.Error(), "nothing is decided") {
		t.Errorf("the refusal says %q, want the absence it found", err)
	}
}

// TestAStaleObservationIsRefusedWithTheNumber is §6.4's guard, and the one place
// a refusal comes back with a result worth reading: the caller is behind, and the
// case's number is the whole content of the answer.
func TestAStaleObservationIsRefusedWithTheNumber(t *testing.T) {
	rt, dana, elliot := registry(t)
	rev := typed(t, dana)
	// A second registrar moves the case over an unrelated form while elliot is
	// reading: §6.4 refuses even then, because what the reviewer assessed was
	// the whole case.
	kate, err := rt.Open(ctx(), dana.ID(), monshi.Actor{User: "kate",
		PrimaryRole: "Registrar"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	moved, err := kate.Review(ctx(), "archive", monshi.ReviewApprove, rev, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	res, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"")
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("a decision about a case that moved = %v, want"+
			" STALE_REVISION", err)
	}
	if res.Revision != moved.Revision || res.Stage != "draft" {
		t.Errorf("the refusal carries %d at %q, want %d at draft", res.Revision,
			res.Stage, moved.Revision)
	}
	if !strings.Contains(err.Error(), "at revision 2, not 1") {
		t.Errorf("the refusal says %q, want both numbers in it", err)
	}
	// Nothing was written by the refused call: the case's number says so, and so
	// does the unit the caller tried to decide.
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if node(t, v, "application").Review.State != monshi.ReviewPending {
		t.Error("a stale decision was stored beside a fresh one")
	}
	if node(t, v, "archive").Review.State != monshi.ReviewApproved {
		t.Error("the approval the case does hold was disturbed")
	}
	// The same call at the number the refusal quoted is accepted, which is the
	// point of carrying a result: the host refreshes and asks again rather than
	// guessing what moved.
	again, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		res.Revision, "")
	if err != nil {
		t.Fatalf("retrying at the quoted revision: %v", err)
	}
	if again.Revision != res.Revision+1 {
		t.Errorf("the retry committed at %d, want %d", again.Revision,
			res.Revision+1)
	}
}

// TestContentErasesTheDecision is §4.2's reset as a host sees it: an approval
// does not acquire a "stale" flag, it goes away, because the state the case
// reports is the one the rule defines.
func TestContentErasesTheDecision(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"approved"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// A second unit stands approved beside it, and the reason to ask for two is
	// that "the reset clears a unit and not a case" is only a claim when there
	// is a decision to keep: a commit that rebuilt the whole table would pass
	// every claim below about the unit that moved and fail this pairing.
	cur, err := elliot.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if _, err := elliot.Review(ctx(), "archive", monshi.ReviewApprove, cur,
		""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// The applicant edits and submits what the reviewer had approved. The
	// reviewer's copy is untouched: a reset is a fact about the case and not
	// about anybody's draft.
	draft(t, dana, monshi.Set("application.full_name",
		monshi.Text("Grace")))
	// The decision moved the case's number, so §5.4's descent guard refuses this
	// draft until it syncs: a review is a commit like any other, and the copy
	// descends from a revision that is no longer current even though no content
	// moved. §5.8 has nothing to merge over a decision-only window, so the sync
	// costs nothing and the submit then lands.
	res := committed(t, dana, "application")
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	got := node(t, v, "application").Review
	if got.State != monshi.ReviewPending {
		t.Errorf("after the content moved the unit reads %q, want PENDING",
			got.State)
	}
	if got.By != "" || got.Comment != "" || got.At != 0 {
		t.Errorf("the erased decision left %q %q@%d behind, want nothing: the"+
			" account stays in history", got.By, got.Comment, got.At)
	}
	if arch := node(t, v, "archive").Review; arch.State !=
		monshi.ReviewApproved {
		t.Errorf("another unit's decision reads %q, want the one the commit"+
			" did not touch to stand", arch.State)
	}
	// A row under a `verifiable multiple` is content as much as a value is: F4
	// says the reviewer approved the collection as a whole, and the whole got
	// bigger.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		res, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	added(t, dana, "application.audit", "note", monshi.Text("first look"))
	res = committed(t, dana, "application")
	v, err = elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if node(t, v, "application").Review.State != monshi.ReviewPending {
		t.Error("a new row left its unit APPROVED")
	}
	if node(t, v, "archive").Review.State != monshi.ReviewApproved {
		t.Error("a rows entry cleared a decision in another form")
	}

	// And a decision about the case as it now stands survives its own commit —
	// the skip that keeps an approval from unmaking itself.
	rvs, err := elliot.Review(ctx(), "application", monshi.ReviewApprove,
		res, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	v, err = elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	got = node(t, v, "application").Review
	if got.State != monshi.ReviewApproved || got.At != rvs.Revision {
		t.Errorf("the decision reads %q@%d, want APPROVED at its own commit",
			got.State, got.At)
	}
}

// TestAPathIsNotFoundOrRefused keeps invariant 7 honest on the door a decision
// uses, and states F4's refusal: the case's shape is not a thing a caller may
// probe by presenting numbers he does not have.
func TestAPathIsNotFoundOrRefused(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	if _, err := elliot.Review(ctx(), "application.nope",
		monshi.ReviewApprove, rev, ""); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("a name the definition does not have = %v, want NOT_FOUND for"+
			" an actor who may decide here", err)
	}
	// The same string from an actor with no grant answers about his permission
	// and says nothing about whether the name exists.
	if _, err := dana.Review(ctx(), "application.nope",
		monshi.ReviewApprove, 0, ""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("an ungranted miss = %v, want ACCESS_DENIED", err)
	}
	// A field is not a unit: the refusal names the unit that answers, because a
	// host left to discover the target by trial is being taught the definition
	// by its error messages.
	if _, err := elliot.Review(ctx(), "application.full_name",
		monshi.ReviewApprove, rev, ""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("a decision at a field = %v, want INVALID_OPERATION", err)
	} else if !strings.Contains(err.Error(), "\"application\" governs") {
		t.Errorf("the refusal says %q, want the unit named", err)
	}
	// A path through a row addresses one instance, and F4 reviews a collection
	// as a whole: a host that believed it had approved one holder would be wrong
	// in a way no later answer corrects.
	//
	// The row's address as its own batch reported it, which is the shape a host
	// has to work with: nothing else in the API hands out §5.1's instance IDs.
	row := added(t, dana, "application.audit", "note",
		monshi.Text("a holder"))
	now := committed(t, dana, "application")
	if _, err := elliot.Review(ctx(), row, monshi.ReviewApprove, now,
		""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("a decision at a governed collection's row = %v, want"+
			" INVALID_OPERATION", err)
	} else if !strings.Contains(err.Error(), "\"application\" governs") {
		t.Errorf("the refusal says %q, want the governing unit", err)
	}
	// A unit that is itself a collection, in a form nothing above it makes
	// verifiable: the decision is still made at the declaration, and a row's
	// address is not a place a verdict can live even when the row is the unit's
	// own kind of node.
	deep := added(t, dana, "memo.rows", "note", monshi.Text("filed"))
	now = committed(t, dana, "memo")
	if _, err := elliot.Review(ctx(), deep, monshi.ReviewApprove, now,
		""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("a decision at %q = %v, want INVALID_OPERATION", deep, err)
	} else if !strings.Contains(err.Error(), "names a row") {
		t.Errorf("the refusal says %q, want the whole-collection rule", err)
	}
	if _, err := elliot.Review(ctx(), "memo.rows", monshi.ReviewApprove, now,
		""); err != nil {
		t.Fatalf("a decision at a unit that is not a form = %v, want success",
			err)
	}
	// The reviewer's copy is the one §5.7 splits: his view answers with the
	// case's decisions and with his own content, so seeing a row the applicant
	// committed means syncing to it first. His handle was opened before that
	// commit, and a working copy is a copy, not a window.
	if _, err := elliot.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	coll := node(t, v, "memo.rows")
	if coll.Review.Unit != "memo.rows" || coll.Review.State !=
		monshi.ReviewApproved || !coll.Reviewable {
		t.Errorf("the collection reads %q/%q reviewable=%v, want its own"+
			" APPROVED decision", coll.Review.Unit, coll.Review.State,
			coll.Reviewable)
	}
	// Every instance answers with the unit's verdict and none of them is a place
	// to decide: F4's "reviewed as a whole, not per member" in the shape a form
	// renders, and the reason a host cannot mistake the two.
	inst := node(t, v, deep)
	if inst.Review.Unit != "memo.rows" || inst.Review.State !=
		monshi.ReviewApproved || inst.Reviewable {
		t.Errorf("the instance reports %q/%q reviewable=%v, want the whole"+
			" collection's verdict and no decision of its own",
			inst.Review.Unit, inst.Review.State, inst.Reviewable)
	}
}

// TestTheStageWaitsForTheDecision is §5.5's conjunct and SYNTAX §7's own reading
// of the conformance example: `require application` asks for an approval as well
// as for values, and the refusal says which of the two the caller is short of.
func TestTheStageWaitsForTheDecision(t *testing.T) {
	_, dana, elliot := registry(t)
	rev := typed(t, dana)
	res, err := elliot.Transition(ctx(), "filed", rev)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("a stage move over unapproved content = %v, want"+
			" INVALID_OPERATION", err)
	}
	if len(res.Missing) != 2 || res.Missing[0] != "application" ||
		res.Missing[1] != "archive" {
		t.Errorf("the refusal wants %v, want both units in declaration order",
			res.Missing)
	}
	if !strings.Contains(err.Error(), "2 approvals") {
		t.Errorf("the refusal says %q, want the count of decisions missing",
			err)
	}
	if res.Stage != "draft" || res.Revision != rev {
		t.Errorf("the refused move reports %d at %q, want nothing to have"+
			" happened", res.Revision, res.Stage)
	}
	// One of two: still refused, and now the sentence cannot be read as a form to
	// fill in, because no field of either unit is empty.
	mid, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	res, err = elliot.Transition(ctx(), "filed", mid.Revision)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("one approval short = %v, want INVALID_OPERATION", err)
	}
	if !strings.Contains(err.Error(), "1 approval") ||
		strings.Contains(err.Error(), "with no value") {
		t.Errorf("the refusal says %q, want one approval and no field claim",
			err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "archive" {
		t.Errorf("the refusal wants %v, want the archive alone", res.Missing)
	}
	// Both, and the move lands. The decisions carry across it, because a stage
	// change moves no content and §4.2's reset has nothing to reset — a move that
	// lost the approvals it had just asked for would make the destination
	// unmeetable by the act of meeting it.
	last, err := elliot.Review(ctx(), "archive", monshi.ReviewApprove,
		mid.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	moved, err := elliot.Transition(ctx(), "filed", last.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if moved.Stage != "filed" || moved.Revision != last.Revision+1 {
		t.Errorf("the case reached %d at %q, want filed one revision on",
			moved.Revision, moved.Stage)
	}
	v, err := elliot.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	for _, path := range []string{"application", "archive"} {
		if got := node(t, v, path).Review; got.State != monshi.ReviewApproved {
			t.Errorf("%s reads %q after the move, want the decision to have"+
				" carried across", path, got.State)
		}
	}
}

// TestAReviewIsNotSomethingADraftCanHold is the consequence of where the table
// lives: a decision is about the case, so a draft neither carries one nor has
// anything to reconcile when one lands over its head.
func TestAReviewIsNotSomethingADraftCanHold(t *testing.T) {
	rt, dana, _ := registry(t)
	rev := typed(t, dana)
	elliot, err := rt.Open(ctx(), dana.ID(), ruling)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	shown, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	// The applicant starts a draft, and the reviewer decides over his head.
	if _, err = dana.Update(ctx(), shown.Working,
		monshi.Set("application.full_name", monshi.Text("Ada Lovelace"))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// The draft keeps its own value and reads the case's verdict: the two are
	// about different content, and §5.7's split is what lets both be true in one
	// node.
	v, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application.full_name").Value.Text(); got !=
		"Ada Lovelace" {
		t.Errorf("the draft reads back %q, want the value the actor typed", got)
	}
	if node(t, v, "application").Review.State != monshi.ReviewApproved {
		t.Error("the draft's view did not report the case's decision")
	}
	// Rebase has nothing to reconcile: the window holds one decision entry and no
	// content, so the copy keeps its edit. Had the merge read a `chgReview` entry
	// as a value, the draft would be told the case holds an approval in a field.
	merged, err := dana.Rebase(ctx())
	if err != nil {
		t.Errorf("rebasing over a decision = %v, want the draft kept intact",
			err)
	}
	// The copy is synced to a revision it did not descend from by editing
	// anything: the decision moved the case, and §5.8's lineage follows that
	// number, which is what makes the submit below a merge over nothing.
	if len(merged.Conflicts) != 0 {
		t.Errorf("rebasing over a decision reports %v, want no conflict",
			merged.Conflicts)
	}
	v, err = dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application.full_name").Value.Text(); got !=
		"Ada Lovelace" {
		t.Errorf("after the rebase the draft reads %q, want the value kept", got)
	}
	if merged.Base != v.Revision {
		t.Errorf("the copy descends from %d, want the case's %d", merged.Base,
			v.Revision)
	}
	// Submitting the draft now resets the unit, because the content the decision
	// was about has moved.
	res, err := dana.Submit(ctx(), "application", merged.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Stage != "draft" {
		t.Errorf("a submit moved the stage to %q, want draft", res.Stage)
	}
	v, err = dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application").Review; got.State !=
		monshi.ReviewPending {
		t.Errorf("the submitted draft left the unit %q, want PENDING",
			got.State)
	}
}
