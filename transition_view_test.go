package monshi

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
func stageCase(t *testing.T) (*Runtime, *image, Case) {
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
	draft, ok := img.stageByName("draft")
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
	for _, n := range img.requires(draft) {
		got = append(got, img.path(n))
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
	if n := img.stages[mustStage(t, img, "gone")].requires; n != 0 {
		t.Errorf("a stage with no require block requires bits %#x, want none",
			n)
	}
	if got := img.requires(mustStage(t, img, "gone")); got != nil {
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
	got := img.viewChanges([]roleID{boss}, draft, reviewed)
	if len(got) != 2 || got[0] != "notes" || got[1] != "ledger" {
		t.Errorf("viewChanges(draft, reviewed) = %q, want [notes ledger]", got)
	}

	// Going back reports the same two paths. The list answers "which views are
	// now answered differently", not "which were gained": a client holding a
	// rendered `ledger` has to un-render it, and a list that only carried
	// arrivals would leave the field on screen for an actor the definition no
	// longer lets see it — invariant 7's failure mode, reached through an
	// audit list nobody was supposed to trust.
	back := img.viewChanges([]roleID{boss}, reviewed, draft)
	if len(back) != 2 || back[0] != "notes" || back[1] != "ledger" {
		t.Errorf("viewChanges(reviewed, draft) = %q, want the same two", back)
	}

	// The Clerk holds a stage grant of her own — the denial of an accept — and
	// still gains no path by moving, because a transition permission is not a
	// view. This is the answer the `overlaysDiffer` shortcut skips the walk
	// for, so it is asserted as a value: the shortcut is only safe because the
	// walk it skips reports nothing for exactly this actor.
	if got := img.viewChanges([]roleID{clerk}, draft, reviewed); got != nil {
		t.Errorf("a role with no stage overlay got %q, want nothing", got)
	}

	// The same for no roles at all, and for the one ordinal that means "no
	// role": ordinal 0 confers nothing at any stage, so there is nothing to
	// report and nothing to search. Both answers come from the fold rather
	// than from a special case in the walk, which is why they are asserted
	// beside each other.
	if got := img.viewChanges(nil, draft, reviewed); got != nil {
		t.Errorf("an actor with no roles got %q, want nothing", got)
	}
	if got := img.viewChanges([]roleID{0}, draft, reviewed); got != nil {
		t.Errorf("the no-role ordinal got %q, want nothing", got)
	}

	// One held role that has an overlay and one that does not: the answer is
	// still the unlocked forms, because the fold is over what the actor holds
	// and a denial or grant the other role says is somebody else's business.
	if got := img.viewChanges([]roleID{clerk, boss}, draft, reviewed); len(
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
	if got := img.viewChanges([]roleID{boss}, draft, mustStage(t, img,
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
	before, err := c.w.cases.load(newRefuser("open", c.id))
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

	after, err := c.w.cases.load(newRefuser("open", c.id))
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
	if got := cell.get(); !got.set || got.s != "Monshi Ltd" {
		t.Errorf("the snapshot holds %q, want the value the submit wrote",
			got.s)
	}

	// The history, which is where a stage change has to be visible: two
	// entries, the second one the transition's, carrying no changes and a
	// different stage from the entry before it.
	hist, err := c.w.cases.history(newRefuser("open", c.id))
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
func workingOf(t *testing.T, c Case, img *image) (workingState, error) {
	t.Helper()
	return c.w.cases.working(newRefuser("open", c.id), c.actor.User, img)
}

// mustStage and mustRole resolve a fixture's names to ordinals, and fail the
// test rather than returning the "no stage" and "no role" answers, which mean
// something in the image and nothing here.
func mustStage(t *testing.T, img *image, name string) stageID {
	t.Helper()
	s, ok := img.stageByName(name)
	if !ok {
		t.Fatalf("no stage is named %q", name)
	}
	return s
}

func mustRole(t *testing.T, img *image, name string) roleID {
	t.Helper()
	r, ok := img.roleByName(name)
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
	st := &img.stages[mustStage(t, img, "draft")]
	boss, clerk := mustRole(t, img, "Boss"), mustRole(t, img, "Clerk")

	if !st.mayAccept([]roleID{boss}) {
		t.Error("the Boss may not accept out of draft, want the grant to work")
	}
	// Held alone, the Clerk's denial is only a bit in the mask; held beside
	// the Boss's grant, it is what refuses the call.
	if st.mayAccept([]roleID{clerk}) {
		t.Error("the Clerk accepts out of draft, want no grant to answer")
	}
	if st.mayAccept([]roleID{boss, clerk}) {
		t.Error("both roles held accepts out of draft, want the Clerk's" +
			" denial to strip the Boss's grant")
	}

	// The same actor, the same two roles, the reject edge: `draft` denies
	// nothing about rejecting, so the grant stands. A denial written for one
	// direction has to leave the other alone (ST3's edges are separate).
	if !st.mayReject([]roleID{boss, clerk}) {
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
	rec, err := elliot.w.cases.load(newRefuser("open", dana.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	roles := img.rolesHeld(elliot.actor, rec.commit.s)
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
	st := &img.stages[draft]
	dest := mustStage(t, img, "company_review")
	e, ok := st.edgeTo(dest)
	if !ok || e.reject {
		t.Fatalf("draft's edge to company_review = %+v (%v), want a declared"+
			" accept", e, ok)
	}
	if !st.mayAccept(roles) {
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
	if e.guard == 0 || !img.cond(e.guard)(rec.commit.s.row) {
		t.Error("the COMPANY guard does not hold on the committed frame," +
			" want step 4 to admit this destination")
	}
	person, ok := st.edgeTo(mustStage(t, img, "person_review"))
	if !ok || person.guard == 0 || img.cond(person.guard)(rec.commit.s.row) {
		t.Error("the PERSON twin's guard holds too, want the two edges to" +
			" answer differently about one case")
	}

	// Step 5: the destination's `require`, which is the completeness half.
	// `company_review` requires `application`, and the content above is what
	// makes that answer empty — the conditional collection is the part worth
	// naming, because it is the destination's own guard that made it required.
	if gaps := img.requires(dest); len(gaps) != 1 || img.path(gaps[0]) !=
		"application" {
		t.Errorf("company_review requires %v, want the application form",
			gaps)
	}
	if g := rec.commit.s.incomplete(img.requires(dest)[0]); len(g) != 0 {
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
		img.requires(dest)[0]); len(got) != 0 {
		t.Errorf("company_review asks %+v, want nothing: §7 marks no signature"+
			" required", got)
	}
	if n := len(img.sigSlotsOf(img.slots[img.requires(dest)[0]].form)); n != 2 {
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
	after, err := elliot.w.cases.load(newRefuser("open", dana.ID()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if after.commit.stage != dest {
		t.Errorf("the snapshot is at stage %d, want company_review",
			after.commit.stage)
	}
	hist, err := elliot.w.cases.history(newRefuser("open", dana.ID()))
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
	if img.effective([]roleID{registrar}, draft,
		formSlot(img, "archive"))&permEdit != 0 {
		t.Error("a Registrar may edit the archive at draft, want the" +
			" overlay confined to company_review")
	}
	if img.effective([]roleID{registrar}, dest,
		formSlot(img, "archive"))&permEdit == 0 {
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
	form := img.requires(dest)[0]
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
	rec, err := elliot.w.cases.load(newRefuser("open", dana.ID()))
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
	same, err := dana.w.cases.working(newRefuser("open", dana.ID()), "dana",
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

// committedFrame is the committed state the trace's step 5 asks about, read
// from the store rather than from a snapshot this test kept alive: the refusal
// above is what says the scan runs over what the case holds now.
func committedFrame(t *testing.T, by, of Case) committedState {
	t.Helper()
	rec, err := by.w.cases.load(newRefuser("open", of.ID()))
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
func holds(roles []roleID, want roleID) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func formSlot(img *image, name string) nodeID {
	for i, slot := range img.forms {
		if i != 0 && img.path(slot) == name {
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
