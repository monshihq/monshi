package engine

// The definition half of a transition, in package (RUNTIME §5.5, §3.7, §5.3):
// the list a stage's `require` expands to, the walk that answers which paths a
// stage move changes the visibility of, and the shape of the history entry a
// transition commits.
//
// None of the three is observable through the exported seam. `Affected` shows a
// host the *result* of the walk, so the walk's rules — a changed group standing
// for its subtree, an actor whose masks do not move receiving nothing, and the
// shortcut that decides that without visiting a slot — need their own
// assertions; a list that quietly stopped collapsing would look exactly like a
// correct one through `Transition`. The history entry is invisible for the same
// reason in the other direction: a commit whose stage or operation was wrong is
// a replay that lands somewhere else, and milestone 5 is the one that reads it.

import (
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

const stageSrc = `
roles {
    Boss {
        allow view, edit, submit application;
    }
    Clerk {
        allow view, edit, submit application;
    }
}
forms {
    application {
        name : string required;

        deep {
            risk : integer;
        }
    }

    notes {
        text : string;
    }

    ledger {
        total : integer required;
    }
}
stages {
    draft {
        allow Boss accept, reject;
        deny Clerk accept;
        require ledger, application;
        accept reviewed;
        reject gone;
    }

    reviewed {
        require application;
        allow Boss accept, reject;
        allow Boss view ledger;
        allow Boss view notes;
        accept gone;
    }

    gone {
        deny Boss view application.deep;
    }
}
`

// stageCase is a runtime, its image and one Boss's handle, for the tests below
// that reach the store seam rather than the boundary.
func stageCase(t *testing.T) (*Runtime, *program.Program, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, stageSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(),
		Actor{User: "dana", PrimaryRole: "Boss"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

func TestAStagesRequireListIsFormsInDeclarationOrder(t *testing.T) {
	img := mustLowerSource(t, stageSrc)
	draft, ok := img.StageByName("draft")
	if !ok {
		t.Fatal("the fixture has no draft stage")
	}

	// The `require` block names `ledger` first, and ST4 stores the list as a
	// bitset over form ordinals — so the order the author typed is gone, and
	// the order that replaces it is the definition's. That is the right answer
	// rather than a lost one: every other gap list in the engine renders in
	// preorder (§5.2's walk, §5.4's `Missing`), and a host that reads two
	// refusals from two operations should not have to know which of them came
	// from a block whose author was in a hurry.
	want := []string{"application", "ledger"}
	var got []string
	for _, n := range img.Requires(draft) {
		got = append(got, img.Path(n))
	}
	if len(got) != len(want) {
		t.Fatalf("require expands to %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("required form %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A stage that requires nothing expands to nothing, and the operation
	// asks it the same way: an empty list is an answer, not a missing one.
	if n := img.Stage(mustStage(t, img, "gone")).Requires; n != 0 {
		t.Errorf("a stage with no require block requires bits %#x, want none",
			n)
	}
	if got := img.Requires(mustStage(t, img, "gone")); got != nil {
		t.Errorf("requires = %v, want nothing", got)
	}
}

func TestViewChangesReportsTheSubtreeAStageMoveUnlocks(t *testing.T) {
	img := mustLowerSource(t, stageSrc)
	boss := mustRole(t, img, "Boss")
	clerk := mustRole(t, img, "Clerk")
	draft, reviewed := mustStage(t, img, "draft"), mustStage(t, img, "reviewed")

	// The Boss gains `notes` and `ledger` at `reviewed`. Both were outside
	// every grant the role holds unconditionally, so the answer is the two
	// forms — and not the forms plus their members: the walk reports the
	// topmost slot whose view moved and skips what its span covers, which is
	// §5.3's collapse rule reused, and the reason a client is told to re-read
	// a subtree rather than sixteen fields. The order is the definitions's
	// own preorder, which is what makes the list the same sequence the author
	// declared the forms in.
	got := img.ViewChanges([]program.RoleID{boss}, draft, reviewed)
	if len(got) != 2 || got[0] != "notes" || got[1] != "ledger" {
		t.Errorf("viewChanges(draft, reviewed) = %q, want [notes ledger]", got)
	}

	// Going back reports the same two paths. The list answers "which views are
	// now answered differently", not "which were gained": a client holding a
	// rendered `ledger` has to un-render it, and a list that only carried
	// arrivals would leave the field on screen for an actor the definition no
	// longer lets see it — invariant 7's failure mode, reached through an
	// audit list nobody was supposed to trust.
	back := img.ViewChanges([]program.RoleID{boss}, reviewed, draft)
	if len(back) != 2 || back[0] != "notes" || back[1] != "ledger" {
		t.Errorf("viewChanges(reviewed, draft) = %q, want the same two", back)
	}

	// The Clerk holds a stage grant of her own — the denial of an accept — and
	// still gains no path by moving, because a transition permission is not a
	// view. This is the answer the `overlaysDiffer` shortcut skips the walk
	// for, so it is asserted as a value: the shortcut is only safe because the
	// walk it skips reports nothing for exactly this actor.
	if got := img.ViewChanges([]program.RoleID{clerk}, draft, reviewed); got != nil {
		t.Errorf("a role with no stage overlay got %q, want nothing", got)
	}

	// The same for no roles at all, and for the one ordinal that means "no
	// role": ordinal 0 confers nothing at any stage, so there is nothing to
	// report and nothing to search. Both answers come from the fold rather
	// than from a special case in the walk, which is why they are asserted
	// beside each other.
	if got := img.ViewChanges(nil, draft, reviewed); got != nil {
		t.Errorf("an actor with no roles got %q, want nothing", got)
	}
	if got := img.ViewChanges([]program.RoleID{0}, draft, reviewed); got != nil {
		t.Errorf("the no-role ordinal got %q, want nothing", got)
	}

	// One held role that has an overlay and one that does not: the answer is
	// still the unlocked forms, because the fold is over what the actor holds
	// and a denial or grant the other role says is somebody else's business.
	if got := img.ViewChanges([]program.RoleID{clerk, boss}, draft, reviewed); len(
		got) != 2 {
		t.Errorf("both roles held = %q, want the two forms Boss gains", got)
	}

	// And the case the shortcut must not swallow: a *stage denial* on a slot
	// whose form is viewable in both stages, so the walk starts inside a
	// subtree that did not move and reports exactly the node that did.
	// `gone` denies the Boss a view of one group inside a form she may see at
	// both stages, so `application` itself does not move and the walk has to
	// start past it. Reporting the group rather than the field under it is the
	// same collapse rule, seen from the other side of a span.
	if got := img.ViewChanges([]program.RoleID{boss}, draft, mustStage(t, img,
		"gone")); len(got) != 1 || got[0] != "application.deep" {
		t.Errorf("entering the stage that denies = %q, want application.deep"+
			" alone", got)
	}
}

func TestAStagesRequireIsAskedOfCommittedState(t *testing.T) {
	_, _, c := stageCase(t)

	// The edge here is unconditional, so this reaches the destination's
	// `require` and asks it of a case whose only content is a draft. §5.5's
	// "draft cannot satisfy a stage" is the claim, and the drafted value is
	// the same value the require wants: were the check made against the
	// working copy, this transition would land.
	mustUpdate(t, c, 0, Set("application.name", Text("Monshi Ltd")))
	res, err := c.Transition(ctx(), "reviewed", 0)
	if !Failed(err, InvalidOp) {
		t.Fatalf("a require satisfied only by a draft = %v, want a refusal",
			err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "application.name" {
		t.Errorf("Missing = %q, want the drafted field: it is in no snapshot,"+
			" so the destination still wants it", res.Missing)
	}
	if res.Stage != "draft" || res.Revision != 0 {
		t.Errorf("the refusal reported %d at %q, want 0 at draft",
			res.Revision, res.Stage)
	}

	// The same call after the same content is committed, with the copy's base
	// moved under it by the commit: the require is now met and the stage
	// moves. One field, two answers, and the difference is only whether the
	// value is in the case.
	base := mustUpdate(t, c, 1, Set("application.name", Text("Monshi Ltd")))
	if _, err := c.Submit(ctx(), "application", base); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	moved, err := c.Transition(ctx(), "reviewed", 1)
	if err != nil {
		t.Fatalf("Transition once the content was committed = %v", err)
	}
	if moved.Stage != "reviewed" || moved.Revision != 2 {
		t.Errorf("the case reached %d at %q, want 2 at reviewed",
			moved.Revision, moved.Stage)
	}
}

func TestATransitionCommitsAStageAndNoContent(t *testing.T) {
	_, img, c := stageCase(t)

	// One committed value to lose, if a stage move wrote content: the walk
	// below reads the snapshot back and finds the same row it held before.
	base := mustUpdate(t, c, 0, Set("application.name", Text("Monshi Ltd")))
	first, err := c.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	before, err := c.w.cases.load(ctx(), newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// The move itself: `draft` grants Boss accept, and the edge is
	// unconditional, so the operation reaches its commit with nothing but the
	// stage to write.
	res, err := c.Transition(ctx(), "reviewed", first.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if res.Revision != first.Revision+1 || res.Stage != "reviewed" {
		t.Fatalf("the transition reached %d at %q, want %d at reviewed",
			res.Revision, res.Stage, first.Revision+1)
	}

	after, err := c.w.cases.load(ctx(), newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.revision != first.Revision+1 {
		t.Errorf("the case is at %d, want %d", after.revision,
			first.Revision+1)
	}
	if after.commit.stage != mustStage(t, img, "reviewed") {
		t.Errorf("the snapshot is at stage %d, want reviewed",
			after.commit.stage)
	}
	// The content: one field, read out of the new snapshot, exactly as the old
	// one held it. A transition hands the same state forward beside a different
	// stage, which is what makes it the one commit that copies nothing — and if
	// it had grafted or cloned-and-cleared anything, this is where it would show.
	cell, ok := after.commit.s.cellOf(slotByPath(t, img, "application.name"))
	if !ok {
		t.Fatal("the snapshot lost the field the submit committed")
	}
	if got := cell.get(); !got.Set || got.Text != "Monshi Ltd" {
		t.Errorf("the snapshot holds %q, want the value the submit wrote",
			got.Text)
	}

	// The history, which is where a stage change has to be visible: two
	// entries, the second one the transition's, carrying no changes and a
	// different stage from the entry before it.
	hist, err := c.w.cases.history(ctx(), newRefuser("open", c.id), 1, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("%d entries, want 2: a stage move is a commit (§5.1)",
			len(hist))
	}
	tr := hist[1]
	if tr.op != opTransition {
		t.Errorf("the entry's operation is %d, want a transition", tr.op)
	}
	if len(tr.changes) != 0 {
		t.Errorf("the entry carries %q, want no content diff",
			changeStrings(&after.commit.s, tr.changes))
	}
	if tr.stage == hist[0].stage {
		t.Errorf("both entries say stage %d, want the transition's to name"+
			" the destination: replaying history has to land the case where"+
			" it is, and the stage is the only thing this one moved",
			tr.stage)
	}
	if tr.by != "dana" || tr.revision != after.revision {
		t.Errorf("the entry is %q at revision %d, want dana at %d", tr.by,
			tr.revision, after.revision)
	}
	// And the entry before it still says what it said: the history is a record
	// and not a view recomputed at read time, so the submit's entry keeps the
	// stage the case was at *then* even though the case has moved since. That
	// is the half of §5.1's replay claim a transition can break — a stage
	// change that overwrote the shared field would leave one stage in a
	// history of two moves.
	if hist[0].stage != before.commit.stage {
		t.Errorf("the first entry now says stage %d, want %d: the submit"+
			" landed at draft and still must", hist[0].stage,
			before.commit.stage)
	}
	if hist[0].revision != 1 || len(hist[0].changes) == 0 {
		t.Errorf("the first entry is %d with %d changes, want 1 with the"+
			" submit's diff", hist[0].revision, len(hist[0].changes))
	}
}

func TestACommitRebasesOnlyTheActorWhoMadeIt(t *testing.T) {
	rt, img, dana := stageCase(t)
	bo, err := rt.Open(ctx(), dana.ID(), Actor{User: "bo",
		PrimaryRole: "Boss"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Both actors read the case at revision 0 and hold a copy from there. dana
	// then makes two commits of her own — a content one and a stage one — and
	// only her copy is allowed to follow.
	base := mustUpdate(t, dana, 0, Set("application.name", Text("x")))
	if _, err := dana.Submit(ctx(), "application", base); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	moved, err := dana.Transition(ctx(), "reviewed", 1)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	me, err := workingOf(t, dana, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if me.base != moved.Revision {
		t.Errorf("the committer's copy descends from %d, want %d: she wrote"+
			" the commit, so nothing about her draft is stale (§6.3)",
			me.base, moved.Revision)
	}
	other, err := workingOf(t, bo, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if other.base != 0 {
		t.Errorf("the other actor's copy was moved to %d, want 0: DESIGN §6.5"+
			" is explicit that committed changes never rewrite an existing"+
			" working state, and the transition changed no content for it to"+
			" merge", other.base)
	}
}

// workingOf reads a handle's own copy back out of the store, which is the only
// way to see the `base` a commit left it at.
func workingOf(t *testing.T, c Case, img *program.Program) (workingState, error) {
	t.Helper()
	return c.w.cases.working(ctx(), newRefuser("open", c.id), c.actor.User, img)
}

// mustStage and mustRole resolve a fixture's names to ordinals, and fail the
// test rather than returning the "no stage" and "no role" answers, which mean
// something in the image and nothing here.
func mustStage(t *testing.T, img *program.Program, name string) program.StageID {
	t.Helper()
	s, ok := img.StageByName(name)
	if !ok {
		t.Fatalf("no stage is named %q", name)
	}
	return s
}

func mustRole(t *testing.T, img *program.Program, name string) program.RoleID {
	t.Helper()
	r, ok := img.RoleByName(name)
	if !ok {
		t.Fatalf("no role is named %q", name)
	}
	return r
}

func TestAStageDenialStripsATransitionItsGrantGives(t *testing.T) {
	// §5.5's `permit` is two mask tests, and the second one is P6's
	// deny-override applied to a transition: authority granted by one role the
	// actor holds is stripped by a denial held through any other. Only the
	// first half is exercised by every other test here, so this asserts the
	// half that refuses — and that it is the *edge taken* that is denied, not
	// the destination reached.
	img := mustLowerSource(t, stageSrc)
	st := img.Stage(mustStage(t, img, "draft"))
	boss, clerk := mustRole(t, img, "Boss"), mustRole(t, img, "Clerk")

	if !st.MayAccept([]program.RoleID{boss}) {
		t.Error("the Boss may not accept out of draft, want the grant to work")
	}
	// Held alone, the Clerk's denial is only a bit in the mask; held beside
	// the Boss's grant, it is what refuses the call.
	if st.MayAccept([]program.RoleID{clerk}) {
		t.Error("the Clerk accepts out of draft, want no grant to answer")
	}
	if st.MayAccept([]program.RoleID{boss, clerk}) {
		t.Error("both roles held accepts out of draft, want the Clerk's" +
			" denial to strip the Boss's grant")
	}

	// The same actor, the same two roles, the reject edge: `draft` denies
	// nothing about rejecting, so the grant stands. A denial written for one
	// direction has to leave the other alone (ST3's edges are separate).
	if !st.MayReject([]program.RoleID{boss, clerk}) {
		t.Error("both roles held may not reject out of draft, want the" +
			" denial of an accept to leave the reject granted")
	}
}

// TestAppendixATransitionTrace executes RUNTIME Appendix A's steps 1 through 6
// against the SYNTAX §7 example — the conformance gate of §10, clause (c), for
// the transition half of it. The steps are the ones the trace spells out for
// `Transition("draft" → "company_review")`, in its order, on its fixture
// content: an application whose `reviewer` names the user making the call, so
// that the authority to move the case arrives *only* through a committed
// role-typed assignment (T4) and not through anybody's primary role.
//
// Steps 5's other two conjuncts and step 7 are not here: `verif` units
// APPROVED and signature validity need something to have approved or signed
// (DESIGN §10's milestone 5), and `OnCommit` is milestone 6's. What is here is
// every step those two wait behind, so the trace's shape — three integer sets,
// one `condFn` and no name comparison — is checked now rather than promised.
func TestAppendixATransitionTrace(t *testing.T) {
	rt := NewRuntime()
	img := mustLowerSource(t, specExample(t))
	d := &Definition{img: img}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The Applicant writes and commits a company application: the two
	// unconditional required fields, the `reviewer` assignment that makes
	// elliot a Manager here, and one instance of the collection the `COMPANY`
	// guard brings into requiredness (§5.2's F6).
	dana, err := rt.Create(ctx(), d.ID(),
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base := mustUpdate(t, dana, 0,
		Set("application.kind", Tag("COMPANY")),
		Set("application.full_name", Text("Monshi Ltd")),
		Set("application.reviewer", User("elliot")),
		Add("application.shareholders"),
		Set("application.shareholders.1.holder_name", Text("Ada")),
		Set("application.shareholders.1.shares",
			Decimal(decimal.RequireFromString("10.00"))))
	sub, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Step 1: the role set. elliot declares no primary role at all, so
	// everything he holds here is the committed assignment — and `rolesHeld`
	// is the function Appendix A's set is, not a restatement of the call.
	elliot, err := rt.Open(ctx(), dana.ID(), Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec, err := elliot.w.cases.load(ctx(), newRefuser("open", dana.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	roles := rolesHeld(img, elliot.actor, rec.commit.s)
	manager := mustRole(t, img, "Manager")
	if !holds(roles, manager) {
		t.Fatalf("the committed reviewer holds %v, want the Manager role"+
			" conferred by `application.reviewer` (T4)", roles)
	}
	if n := len(roles); n != 1 {
		t.Errorf("the role set is %v, want the one conferred role: an actor"+
			" with no primary role adds nothing of his own", roles)
	}

	// Steps 2 and 3, the edge and its mask: `draft` grants accept to the
	// Manager alone, so this is the one call that answers for both, and the
	// same content refused for the Applicant who wrote it.
	draft := mustStage(t, img, "draft")
	st := img.Stage(draft)
	dest := mustStage(t, img, "company_review")
	e, ok := st.EdgeTo(dest)
	if !ok || e.Reject {
		t.Fatalf("draft's edge to company_review = %+v (%v), want a declared"+
			" accept", e, ok)
	}
	if !st.MayAccept(roles) {
		t.Error("the committed reviewer may not accept, want the Manager" +
			" grant to answer for him")
	}
	if _, err := dana.Transition(ctx(), "company_review",
		rec.revision); !Failed(err, AccessDenied) {
		t.Errorf("the Applicant who wrote the case accepted it: %v, want"+
			" ACCESS_DENIED — Appendix A names this exact refusal", err)
	}

	// Step 4: the guard, on the committed frame. The `PERSON` twin is a second
	// edge out of the same stage, and naming the destination is what decides
	// that it is never consulted: a definition that branches on content has to
	// be told which branch the caller means.
	if e.Guard == 0 || !img.Cond(e.Guard)(rec.commit.s.row) {
		t.Error("the COMPANY guard does not hold on the committed frame," +
			" want step 4 to admit this destination")
	}
	person, ok := st.EdgeTo(mustStage(t, img, "person_review"))
	if !ok || person.Guard == 0 || img.Cond(person.Guard)(rec.commit.s.row) {
		t.Error("the PERSON twin's guard holds too, want the two edges to" +
			" answer differently about one case")
	}

	// Step 5: the destination's `require`, which is the completeness half.
	// `company_review` requires `application`, and the content above is what
	// makes that answer empty — the conditional collection is the part worth
	// naming, because it is the destination's own guard that made it required.
	if gaps := img.Requires(dest); len(gaps) != 1 || img.Path(gaps[0]) !=
		"application" {
		t.Errorf("company_review requires %v, want the application form",
			gaps)
	}
	if g := rec.commit.s.incomplete(img.Requires(dest)[0]); len(g) != 0 {
		t.Errorf("the required form is incomplete at %v, want step 5 to pass",
			gapPaths(g))
	}

	// Step 5's other half, and the reason the trace owes itself a commit before
	// step 6: `application` is the form-wide unit (F3), so the destination's
	// `require` wants the committed reviewer's APPROVED as well as the content
	// above. Manager is in `draft`'s accept mask and holds `review application`,
	// so one call answers both §5.5's conjuncts that ship today.
	rvs, err := elliot.Review(ctx(), "application", ReviewApprove,
		sub.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if rvs.Revision != sub.Revision+1 || rvs.Stage != "draft" {
		t.Errorf("the decision committed at %d in stage %q, want %d in draft",
			rvs.Revision, rvs.Stage, sub.Revision+1)
	}

	// Step 5's third conjunct, and the reason a trace of §5.5 has to say what it
	// does *not* ask: SYNTAX §7's example declares two signature fields and marks
	// neither `required`, so §5.6's "its required signatures are valid" wants
	// nothing of this destination and step 6 below is the same move milestone 3
	// ran. The scan is not a no-op by luck — it walks the form's two fields and
	// reports the absence of a requirement — and one word of the same definition
	// turns it into a refusal, which the test after this one runs.
	if got := unsignedSigs(&rec.commit.s, rec.commit.sigs,
		img.Requires(dest)[0]); len(got) != 0 {
		t.Errorf("company_review asks %+v, want nothing: §7 marks no signature"+
			" required", got)
	}
	if n := len(img.SigSlotsOf(img.Slot(img.Requires(dest)[0]).Form)); n != 2 {
		t.Errorf("the example's application groups %d signature fields, want"+
			" 2 — the conjunct reads a range, not a walk", n)
	}

	// Step 6: the revision check and one atomic commit, seen from the handle
	// that was refused three calls ago. Nothing about the content moves, so
	// the entry this writes is the trace's claim in §5.1: a stage change, and
	// no diff. The number checked is the decision's, because a review is a
	// commit like any other and step 6 asks what the caller was last shown.
	res, err := elliot.Transition(ctx(), "company_review", rvs.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if res.Revision != rvs.Revision+1 || res.Stage != "company_review" {
		t.Errorf("the case reached %d at %q, want %d at company_review",
			res.Revision, res.Stage, rvs.Revision+1)
	}
	after, err := elliot.w.cases.load(ctx(), newRefuser("open", dana.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.commit.stage != dest {
		t.Errorf("the snapshot is at stage %d, want company_review",
			after.commit.stage)
	}
	hist, err := elliot.w.cases.history(ctx(), newRefuser("open", dana.ID()), 1, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	tail := hist[len(hist)-1]
	// The decision's entry sits beside them, and it is the trace's §5.6 claim
	// in miniature: a commit whose changes are decisions maps no reset, so the
	// approval the step-5 call wrote is the one step 6 found standing.
	if len(tail.changes) != 0 {
		t.Errorf("a stage move wrote %d changes, want the diff §5.6 maps to"+
			" nothing here", len(tail.changes))
	}
	mid := hist[len(hist)-2]
	if mid.op != opReview || len(mid.changes) != 1 ||
		mid.changes[0].kind != chgReview || mid.revision != rvs.Revision {
		t.Errorf("the decision's entry is op %d with %d changes, want one"+
			" review entry at revision %d", mid.op, len(mid.changes),
			rvs.Revision)
	}
	if reviewAt(after.commit.reviews,
		slotByPath(t, img, "application")).code != verifApproved {
		t.Error("the transition's commit lost the approval step 5 made")
	}
	if tail.op != opTransition || len(tail.changes) != 0 {
		t.Errorf("the trace's commit is op %d with %d changes, want a"+
			" transition and no diff: §5.6 maps nothing here", tail.op,
			len(tail.changes))
	}
	if tail.stage != dest || hist[len(hist)-2].stage == dest {
		t.Errorf("the two entries say stages %d and %d, want the move to be"+
			" the only thing visible in the last one",
			hist[len(hist)-2].stage, tail.stage)
	}

	// And the stage the case now sits at grants the *Registrar* the archive,
	// which is the half of Appendix A's last bullet a transition is measured
	// by: the same actor, the same content, and a view that answers
	// differently because the mask was resolved at a different stage.
	registrar := mustRole(t, img, "Registrar")
	if img.Effective([]program.RoleID{registrar}, draft,
		formSlot(img, "archive"))&program.PermEdit != 0 {
		t.Error("a Registrar may edit the archive at draft, want the" +
			" overlay confined to company_review")
	}
	if img.Effective([]program.RoleID{registrar}, dest,
		formSlot(img, "archive"))&program.PermEdit == 0 {
		t.Error("a Registrar may not edit the archive at company_review," +
			" want the stage grant the §7 example declares")
	}
}

// TestAppendixARequiredSignatureTrace is Appendix A's steps with §5.5's third
// conjunct load-bearing: the same §7 corpus, with one word added to make the
// reviewer's attestation something the destination asks for. It is a separate
// trace rather than a case inside the one above because the point is the whole
// sequence — a stage that will not open, the remedy that opens it, and the fact
// that the hand entitled to give it is the one §3.5's fold put in charge of this
// case and nobody else.
func TestAppendixARequiredSignatureTrace(t *testing.T) {
	src := strings.Replace(specExample(t),
		"approved_by : signature(.reviewer);",
		"approved_by : signature(.reviewer) required;", 1)
	if src == specExample(t) {
		t.Fatal("SYNTAX §7's example no longer declares the attestation this" +
			" trace makes required")
	}
	rt := NewRuntime()
	img := mustLowerSource(t, src)
	d := &Definition{img: img}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Steps 1 through 4 as the trace above runs them: COMPANY content, the
	// reviewer named in it, and the destination that guard admits.
	dana, err := rt.Create(ctx(), d.ID(),
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base := mustUpdate(t, dana, 0,
		Set("application.kind", Tag("COMPANY")),
		Set("application.full_name", Text("Monshi Ltd")),
		Set("application.reviewer", User("elliot")),
		Add("application.shareholders"),
		Set("application.shareholders.1.holder_name", Text("Ada")),
		Set("application.shareholders.1.shares",
			Decimal(decimal.RequireFromString("10.00"))))
	sub, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	elliot, err := rt.Open(ctx(), dana.ID(), Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dest := mustStage(t, img, "company_review")
	rvs, err := elliot.Review(ctx(), "application", ReviewApprove,
		sub.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	// The destination's first two conjuncts are satisfied by the calls above, so
	// what step 5 finds now is the attestation and nothing else: the same walk,
	// the same gap list, the third kind of entry in it.
	if _, err := elliot.Transition(ctx(), "company_review",
		rvs.Revision); !Failed(err, InvalidOp) {
		t.Fatalf("a stage opened over a required attestation: %v", err)
	} else if !strings.Contains(err.Error(), "1 signature") {
		t.Errorf("the refusal says %q, want the third conjunct counted", err)
	}
	form := img.Requires(dest)[0]
	frame := committedFrame(t, elliot, dana)
	if got := unsignedSigs(&frame.s, frame.sigs, form); len(got) != 1 ||
		got[0].kind != gapSig || got[0].path != "application.approved_by" {
		t.Errorf("the destination asks %+v, want the reviewer's attestation",
			got)
	}

	// Step 5's answer arrives through the operation that owes it. elliot is the
	// hand the *committed* content names, and §5.4's completeness and §5.5's
	// approval conjuncts already hold, so the attestation commits at once — and
	// the signature stands for the content the case holds, not for the version
	// the trace began with.
	sig, err := elliot.Sign(ctx(), "application.approved_by", rvs.Revision,
		"reviewer-note")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if sig.Revision != rvs.Revision+1 || sig.Stage != "draft" {
		t.Errorf("the attestation reached %d at %q, want %d at draft",
			sig.Revision, sig.Stage, rvs.Revision+1)
	}

	// Step 6, now with all three conjuncts paid: the same call that was refused,
	// at the number this commit produced. The trace's last claim is the one §4.2
	// makes about a signature and a stage — a destination asks, the attestation
	// answers, and the content it attested to is the content the case holds.
	res, err := elliot.Transition(ctx(), "company_review", sig.Revision)
	if err != nil {
		t.Fatalf("Transition after the attestation = %v, want the move", err)
	}
	if res.Stage != "company_review" {
		t.Errorf("the case reached %q, want company_review", res.Stage)
	}
	rec, err := elliot.w.cases.load(ctx(), newRefuser("open", dana.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	field := slotByPath(t, img, "application.approved_by")
	g := sigAt(rec.commit.sigs, field)
	if g.by != "elliot" || g.at != sig.Revision || g.ref != "reviewer-note" {
		t.Errorf("the case holds %q@%d %q, want elliot@%d reviewer-note",
			g.by, g.at, g.ref, sig.Revision)
	}
	// And a content commit at the new stage erases the attestation that opened
	// the door, which is §5.6's effect seen from the far end of the trace: the
	// stage does not ask for it again, because the destination's `require` is
	// only consulted on the way in.
	if _, err := dana.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	same, err := dana.w.cases.working(ctx(), newRefuser("open", dana.ID()), "dana",
		img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	later := mustUpdate(t, dana, same.rev, Set("application.note",
		Text("late")))
	if _, err := dana.Submit(ctx(), "application", later); err != nil {
		t.Fatalf("Submit at the new stage: %v", err)
	}
	after := committedFrame(t, dana, dana)
	if sigAt(after.sigs, field).at != 0 {
		t.Error("a content commit left the attestation standing at " +
			"company_review")
	}
}

// TestAppendixAHistoryTrace is Appendix A's step 8: the trail the trace above
// left, read through the exported seam (RUNTIME §5.11). The other two traces in
// this file ask what the operations did to the case. This one asks what the case
// says about having done them, and it uses the second trace's script for the
// reason §10 gives: it is the only sequence the conformance corpus runs in which
// all four commits happen — content, a decision, an attestation, an edge — and
// the attestation is then erased by content, which is the one fact a state
// cannot keep and a trail can.
//
// Three claims, in the order the read has to be able to make them: the six
// records are the six acts in order, with the hands and the stages; the erased
// attestation and the replaced decision are still there in the entries, in the
// shapes the view reports a standing one; and an actor who may not see a form
// sees no entry of a commit about it, while still seeing that the commit
// happened — which is §7's property 7 applied to the past, and the half of it the
// two traces above have no way to state.
func TestAppendixAHistoryTrace(t *testing.T) {
	src := strings.Replace(specExample(t),
		"approved_by : signature(.reviewer);",
		"approved_by : signature(.reviewer) required;", 1)
	rt := NewRuntime()
	img := mustLowerSource(t, src)
	d := &Definition{img: img}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 1. The Applicant's content, as the two traces above commit it.
	dana, err := rt.Create(ctx(), d.ID(),
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base := mustUpdate(t, dana, 0,
		Set("application.kind", Tag("COMPANY")),
		Set("application.full_name", Text("Monshi Ltd")),
		Set("application.reviewer", User("elliot")),
		Add("application.shareholders"),
		Set("application.shareholders.1.holder_name", Text("Ada")),
		Set("application.shareholders.1.shares",
			Decimal(decimal.RequireFromString("10.00"))))
	sub, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// 2 and 3. The approval and the attestation the destination wants, from the
	// hand the committed content named. The comment and the reference are the
	// two payloads the trail is the only keeper of.
	elliot, err := rt.Open(ctx(), dana.ID(), Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rvs, err := elliot.Review(ctx(), "application", ReviewApprove,
		sub.Revision, "clean")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	sig, err := elliot.Sign(ctx(), "application.approved_by", rvs.Revision,
		"reviewer-note")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// 4. The edge, which is the only act of the six that commits no content.
	moved, err := elliot.Transition(ctx(), "company_review", sig.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// 5. Content at the new stage, and the reason this trace is the one to read
	// a history with: the commit erases the attestation that opened the door, so
	// after it the case holds no answer to "whose hand stood behind
	// `approved_by`" and §5.10's claim is that the trail still has one.
	if _, err := dana.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	work, err := dana.w.cases.working(ctx(), newRefuser("open", dana.ID()), "dana",
		img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	later := mustUpdate(t, dana, work.rev, Set("application.note",
		Text("late")))
	note, err := dana.Submit(ctx(), "application", later)
	if err != nil {
		t.Fatalf("Submit at the new stage: %v", err)
	}

	// 6. A form the Applicant has no grant on at all, committed by the role the
	// new stage handed the archive.
	rolf, err := rt.Open(ctx(), dana.ID(), Actor{User: "rolf",
		PrimaryRole: "Registrar"})
	if err != nil {
		t.Fatalf("Open as the Registrar: %v", err)
	}
	arch := mustUpdate(t, rolf, 0,
		Set("archive.filed_at", DateTime(time.Date(2026, 2, 1, 9, 0, 0, 0,
			time.UTC))),
		Set("archive.note", Text("bound")))
	archived, err := rolf.Submit(ctx(), "archive", arch)
	if err != nil {
		t.Fatalf("Submit the archive: %v", err)
	}

	// The trail, from the actor who saw all of it. Six records, in the order the
	// calls above were made, with `Revision` naming the head and no continuation.
	full, err := rolf.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the Registrar: %v", err)
	}
	if len(full.Commits) != 6 || full.Revision != archived.Revision ||
		full.Next != 0 || full.Withheld != 0 {
		t.Fatalf("the page is %d records at head %d, next %d, withheld %d, "+
			"want six of six with nothing withheld", len(full.Commits),
			full.Revision, full.Next, full.Withheld)
	}
	want := []struct {
		rev     uint64
		op      CommitOp
		by      UserID
		stage   string
		what    ChangeKind
		entries int
	}{
		{sub.Revision, CommitSubmit, "dana", "draft", ChangeValue, 6},
		{rvs.Revision, CommitReview, "elliot", "draft", ChangeReview, 1},
		{sig.Revision, CommitSign, "elliot", "draft", ChangeSign, 1},
		{moved.Revision, CommitTransition, "elliot", "company_review", "", 0},
		{note.Revision, CommitSubmit, "dana", "company_review", ChangeValue, 1},
		{archived.Revision, CommitSubmit, "rolf", "company_review",
			ChangeValue, 2},
	}
	for i, w := range want {
		c := full.Commits[i]
		if c.Revision != w.rev || c.Op != w.op || c.By != w.by ||
			c.Stage != w.stage {
			t.Errorf("record %d is %s by %s at %s rev %d, want %s by %s at "+
				"%s rev %d: the trail is the acts in the order they happened",
				i, c.Op, c.By, c.Stage, c.Revision, w.op, w.by, w.stage, w.rev)
		}
		if len(c.Changes) != w.entries {
			t.Errorf("record %d (%s) holds %d entries, want %d", i, c.Op,
				len(c.Changes), w.entries)
			continue
		}
		if w.entries > 0 && c.Changes[0].Kind != w.what {
			t.Errorf("record %d's first entry is %s, want %s", i,
				c.Changes[0].Kind, w.what)
		}
		if c.At.IsZero() {
			t.Errorf("record %d carries no moment, and a commit that happened"+
				" at no time is not an audit", i)
		}
	}

	// Step 5's content is the six declarations §5.1's diff walks, in preorder,
	// and the rows entry is the one that lists instances rather than a value.
	first := full.Commits[0].Changes
	if got := first[0].Path; got != "application.kind" {
		t.Errorf("the content commit begins at %q, want the form's first field",
			got)
	}
	var listed []string
	for _, ch := range first {
		if ch.Kind == ChangeRows {
			listed = ch.Rows
		}
	}
	if len(listed) != 1 || listed[0] != "application.shareholders.1" {
		t.Errorf("the content commit lists the collection as %v, want the one"+
			" instance the batch added", listed)
	}

	// The decision, in the shape `View` prints a standing one, and the two facts
	// a state keeps nowhere: the comment the reviewer typed and the reference the
	// signer brought. `At` is each record's own revision, which is §5.9's and
	// §5.10's "the revision this act's commit produced" read back by a caller who
	// was not the one who made the call.
	decisions := entries(t, full.Commits[1], ChangeReview)
	if len(decisions) != 1 {
		t.Fatalf("the decision record holds %d entries", len(decisions))
	}
	if got := decisions[0].Review; got.Unit != "application" ||
		got.State != ReviewApproved || got.By != "elliot" || got.At != 2 ||
		got.Comment != "clean" {
		t.Errorf("the decision entry reports %+v, want elliot's clean approve"+
			" of application at 2", got)
	}
	attested := entries(t, full.Commits[2], ChangeSign)
	if len(attested) != 1 {
		t.Fatalf("the attestation record holds %d entries", len(attested))
	}
	field := slotByPath(t, img, "application.approved_by")
	if got := attested[0]; got.Path != "application.approved_by" {
		t.Errorf("the attestation entry addresses %q, want the field", got.Path)
	} else if s := got.Signature; s.By != "elliot" || s.At != 3 ||
		s.Evidence != "reviewer-note" {
		t.Errorf("the attestation entry reports %+v, want elliot at 3 with"+
			" reviewer-note", s)
	}

	// The pair that makes this the milestone's claim and not §5.6's promise: the
	// case now answers for no attestation, and the trail answers for the one it
	// got. The record that erased it says nothing about erasing it — a content
	// commit's diff is content, and §4.2's reset is not an entry.
	if g := sigAt(committedFrame(t, rolf, dana).sigs, field); g.at != 0 {
		t.Fatalf("the case still holds an attestation by %q at %d, want the"+
			" content commit to have erased it", g.by, g.at)
	}
	if got := entries(t, full.Commits[4], ChangeValue); len(got) != 1 ||
		got[0].Path != "application.note" {
		t.Errorf("the erasing commit's entries are %+v, want the one note it"+
			" wrote and no account of what it unmade", got[0])
	}
	if s := attested[0].Signature; s.At == 0 || s.By != "elliot" {
		t.Error("the trail lost the attestation the case lost")
	}

	// The edge, which is the record that would vanish if a read filtered by
	// content: it holds no entries at all, and it is the only place in the trail
	// that says how the case got to `company_review`.
	edge := full.Commits[3]
	if edge.Op != CommitTransition || edge.Stage != "company_review" ||
		len(edge.Changes) != 0 {
		t.Errorf("the fourth record is %s at %s with %d entries, want the move"+
			" and nothing else", edge.Op, edge.Stage, len(edge.Changes))
	}

	// The same trail for the actor who may not see the archive. Every record
	// survives; two entries do not, and the count says so rather than leaving a
	// caller to guess whether six acts is all there was.
	hers, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the Applicant: %v", err)
	}
	if len(hers.Commits) != 6 || hers.Withheld != 2 {
		t.Fatalf("the Applicant's page is %d records with %d withheld, want 6"+
			" and 2: the archive's entries, not its commits", len(hers.Commits),
			hers.Withheld)
	}
	for i, c := range hers.Commits {
		if other := full.Commits[i]; c.Revision != other.Revision ||
			c.By != other.By || c.Op != other.Op || c.Stage != other.Stage ||
			!c.At.Equal(other.At) {
			t.Errorf("record %d's envelope differs by reader: %+v", i, c)
		}
	}
	if got := len(hers.Commits[5].Changes); got != 0 {
		t.Errorf("the archive commit shows %d entries to an actor with no"+
			" grant on the form, want none", got)
	}
	// And withholding is not ignorance about the acts the Applicant *is* in:
	// §5.9's disclosure rule read in the past, so dana may now read the approval
	// and the attestation she could never have made.
	if got := entries(t, hers.Commits[1], ChangeReview); len(got) != 1 ||
		got[0].Review.Comment != "clean" {
		t.Errorf("the Applicant cannot read the decision about her form: %+v",
			got)
	}
	if got := entries(t, hers.Commits[2], ChangeSign); len(got) != 1 ||
		got[0].Signature.Evidence != "reviewer-note" {
		t.Error("the Applicant cannot read the attestation her case carries")
	}
	// A withheld entry is not a zero-valued one either: nothing on this page
	// names the archive, not even as an empty slot in a list.
	for _, c := range hers.Commits {
		for _, ch := range c.Changes {
			if strings.HasPrefix(ch.Path, "archive") {
				t.Errorf("the page leaks %q", ch.Path)
			}
		}
	}

	// Paging over the same six: two at a time, following `Next`, and the seventh
	// ask is the empty page that ends the loop.
	var walked []uint64
	for cursor := uint64(0); ; {
		page, err := rolf.History(ctx(), cursor, 2)
		if err != nil {
			t.Fatalf("History(%d, 2): %v", cursor, err)
		}
		for _, c := range page.Commits {
			walked = append(walked, c.Revision)
		}
		if page.Next == 0 {
			break
		}
		cursor = page.Next
	}
	if len(walked) != 6 || walked[0] != 1 || walked[5] != 6 {
		t.Errorf("two at a time walked %v, want the six in order", walked)
	}
	past, err := rolf.History(ctx(), walked[len(walked)-1]+1, 1)
	if err != nil {
		t.Fatalf("History past the head: %v", err)
	}
	if len(past.Commits) != 0 || past.Next != 0 || past.Revision != 6 {
		t.Errorf("the ask after the last page is %d records, next %d, head %d,"+
			" want an empty page that still says where the case is",
			len(past.Commits), past.Next, past.Revision)
	}

	// The addresses the trail printed are the ones `Update` takes, which is what
	// makes a page of history a thing a host can act on rather than a transcript
	// to read. The rows entry's instance and the note's field both resolve at the
	// stage the case sits at now, four commits after either was written.
	again, err := rolf.w.cases.working(ctx(), newRefuser("open", dana.ID()), "rolf",
		img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	var text string
	for _, ch := range entries(t, full.Commits[5], ChangeValue) {
		if ch.Value.Kind() == KindText {
			text = ch.Path
		}
	}
	if text == "" {
		t.Fatal("the archive commit recorded no text field to write back")
	}
	written := mustUpdate(t, rolf, again.rev, Set(text, Text("rebound")),
		Set(listed[0]+".holder_name", Text("Ada Lovelace")))
	if written <= again.rev {
		t.Errorf("writing back the trail's own addresses left the copy at %d",
			written)
	}
}

// entries is one record's entries of one kind, which is how the trace above
// reads a commit it expects to hold exactly one thing about one declaration.
func entries(t *testing.T, c Commit, kind ChangeKind) []CommitChange {
	t.Helper()
	var out []CommitChange
	for _, ch := range c.Changes {
		if ch.Kind == kind {
			out = append(out, ch)
		}
	}
	return out
}

// committedFrame is the committed state the trace's step 5 asks about, read
// from the store rather than from a snapshot this test kept alive: the refusal
// above is what says the scan runs over what the case holds now.
func committedFrame(t *testing.T, by, of Case) committedState {
	t.Helper()
	rec, err := by.w.cases.load(ctx(), newRefuser("open", of.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return rec.commit
}

// holds and formSlot read the two bits of the trace's fixture this test would
// otherwise assert through a name it is trying to avoid comparing: `holds`
// looks for one ordinal in a role set, and `formSlot` is the one place the
// §7 corpus's form is addressed by its declaration slot rather than by
// guessing at which index `img.forms` happens to hold it at.
func holds(roles []program.RoleID, want program.RoleID) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func formSlot(img *program.Program, name string) program.NodeID {
	for i, slot := range img.FormSlots() {
		if i != 0 && img.Path(slot) == name {
			return slot
		}
	}
	panic("no form is named " + name)
}

func TestATransitionDoesNotGiveAStaleDraftThePresent(t *testing.T) {
	rt, img, dana := stageCase(t)

	// dana commits content, so her copy descends from revision 1.
	base := mustUpdate(t, dana, 0, Set("application.name", Text("x")))
	first, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Somebody else commits the same form on top of it. The case moves to
	// revision 2 and dana's copy is left behind — which is §6.5's whole point:
	// the draft stays editable and its lineage is not silently repaired.
	bo, err := rt.Open(ctx(), dana.ID(), Actor{User: "bo",
		PrimaryRole: "Boss"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	second := mustUpdate(t, bo, 0, Set("application.name", Text("yy")))
	if _, err := bo.Submit(ctx(), "application", second); err != nil {
		t.Fatalf("the second actor's Submit: %v", err)
	}

	// dana keeps editing her copy, whose base is still 1, and then moves the
	// stage. She presents the case's *current* revision, which she is entitled
	// to read: `Transition` asks nothing of her draft, so nothing here tells
	// the engine that the hand moving the case is holding content that
	// descends from an older state. That is exactly why the rebase a commit
	// performs has to be conditional — a stage move that advanced her copy to
	// revision 3 would hand her draft a lineage it never had, and her next
	// submit would then overwrite the second actor's commit as though it had
	// seen it.
	stale := mustUpdate(t, dana, 1, Set("application.name", Text("zzz")))
	moved, err := dana.Transition(ctx(), "reviewed", 2)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if moved.Revision != 3 {
		t.Fatalf("the case reached %d, want 3", moved.Revision)
	}
	me, err := workingOf(t, dana, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if me.base != first.Revision {
		t.Errorf("the stale copy now descends from %d, want %d: a commit"+
			" extends the state it was made against, and this one's author"+
			" was not holding that state", me.base, first.Revision)
	}
	_, err = dana.Submit(ctx(), "application", stale)
	if !Failed(err, StaleRevision) {
		t.Fatalf("the stale draft submitted after its owner moved the stage"+
			": %v, want STALE_REVISION", err)
	}

	// And the copy that *did* descend from the state the transition extended
	// follows it, which is the half of §6.3 the unconditional version of this
	// rule was written for.
	bo2, err := workingOf(t, bo, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if bo2.base != 2 {
		t.Errorf("the second actor's copy descends from %d, want 2: his own"+
			" submit rebased it and a later commit by someone else does not"+
			" move it again", bo2.base)
	}
}
