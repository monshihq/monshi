package monshi_test

// Transition seen from outside the module: what a
// host observes when it moves a case along a declared edge — the stage it
// reached, the paths its own view gained or lost, and which check answered
// first when the move was refused.
//
// What a host cannot observe is left to the in-package file: the history entry
// the commit wrote, the fact that it carries a stage and no diff, and the
// definition-level walk behind `Affected` (`transition_view_test.go`).
import (
	"fmt"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

const transitionSrc = `
types {
    legal : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
    }
    Reviewer {
        allow view, edit, submit application;
    }
    Clerk {
        allow view, edit, submit ledger;
    }
}
forms {
    application {
        kind : legal required;
        name : string required;
        note : string;
        boss : Reviewer;
    }

    ledger {
        total : integer required;
    }
}
stages {
    draft {
        allow Reviewer accept, reject;
        accept[application.kind == PERSON] reviewed;
        accept[application.kind == COMPANY] audited;
        reject corrected;
    }

    reviewed {
        require application;
        allow Reviewer view ledger;
        allow Reviewer accept, reject;
        accept closed;
        reject corrected;
    }

    audited {
        require application;
    }

    corrected {
        require application;
        allow Applicant view, edit, submit application;
        allow Reviewer accept;
        accept draft;
    }

    closed {
        require ledger;
    }
}
`

var (
	reviewer = monshi.Actor{User: "elliot", PrimaryRole: "Reviewer"}
	clerk    = monshi.Actor{User: "bo", PrimaryRole: "Clerk"}
)

// transitionCase is a runtime and one actor's handle on a new case, so a test
// can hand the same case to a second actor — which is what half of these
// claims are about.
func transitionCase(t *testing.T, actor monshi.Actor) (
	*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "transition", transitionSrc)
	c, err := rt.Create(ctx(), "transition", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// submittedApplication is the content the guarded edge out of `draft` asks for
// and the destination `require` insists on: the two required fields, committed.
func submittedApplication(t *testing.T, c monshi.Case) uint64 {
	t.Helper()
	base := mustEdit(t, c, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")))
	res, err := c.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("submitting the application: %v", err)
	}
	return res.Revision
}

func TestTransitionMovesTheCaseAlongAGuardedEdge(t *testing.T) {
	rt, dana := transitionCase(t, applicant)
	at := submittedApplication(t, dana)

	// The reviewer is a different actor, on a different handle, with the number
	// *the case* reported — not a working copy's. The whole point about these
	// operations is that they act on committed state, so there is no draft of
	// elliot's involved here at all.
	elliot, err := rt.Open(ctx(), dana.ID(), reviewer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := elliot.Transition(ctx(), "reviewed", at)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if res.Revision != at+1 {
		t.Errorf("the case is at revision %d, want %d: a stage move is a "+
			"commit", res.Revision, at+1)
	}
	if res.Stage != "reviewed" {
		t.Errorf("the case is at stage %q, want reviewed", res.Stage)
	}
	if stage, err := elliot.Stage(ctx()); err != nil || stage != "reviewed" {
		t.Errorf("Stage() = %q (%v), want reviewed", stage, err)
	}

	// `ledger` is granted to the Reviewer only inside `reviewed`, so moving
	// the case moved this actor's view of that form and of everything in it.
	// Nothing about the ledger's values changed — the operation writes no
	// content — so this is the one commit whose affected set is about masks.
	if got := strings.Join(res.Affected, " "); got != "ledger" {
		t.Errorf("Affected = %q, want the one form the stage unlocked", got)
	}

	// The next edge's `require` names a form its actor cannot even write: the
	// ledger, which belongs to the Clerk. A refusal is still an answer about
	// where the case is, and the gap arrives as an editable path.
	res, err = elliot.Transition(ctx(), "closed", res.Revision)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("entering a stage whose require is unmet = %v, want "+
			"INVALID_OPERATION", err)
	}
	if got := strings.Join(res.Missing, " "); got != "ledger.total" {
		t.Errorf("Missing = %q, want the required form's gap", got)
	}
	if res.Revision != 2 || res.Stage != "reviewed" {
		t.Errorf("a refused transition reported %d at %q, want 2 at reviewed"+
			" (nothing moved)", res.Revision, res.Stage)
	}
	if rev, err := elliot.Revision(ctx()); err != nil || rev != 2 {
		t.Errorf("the case is at %d (%v) after a refused transition, want 2",
			rev, err)
	}

	// The Clerk fills and commits the ledger, and the same transition now
	// lands. Leaving `reviewed` takes the stage's grant away again, so the
	// second result reports `ledger` too — as a loss. The list answers "which
	// views are now rendered differently", not "which views were gained".
	bo, err := rt.Open(ctx(), dana.ID(), clerk)
	if err != nil {
		t.Fatalf("Open as the clerk: %v", err)
	}
	base := mustEdit(t, bo, 0, monshi.Set("ledger.total", monshi.Int(7)))
	led, err := bo.Submit(ctx(), "ledger", base)
	if err != nil {
		t.Fatalf("submitting the ledger: %v", err)
	}
	res, err = elliot.Transition(ctx(), "closed", led.Revision)
	if err != nil {
		t.Fatalf("Transition to closed: %v", err)
	}
	if res.Stage != "closed" || res.Revision != led.Revision+1 {
		t.Errorf("the case reached %d at %q, want %d at closed", res.Revision,
			res.Stage, led.Revision+1)
	}
	if got := strings.Join(res.Affected, " "); got != "ledger" {
		t.Errorf("Affected = %q, want the form whose view the stage took back",
			got)
	}

	// `closed` is terminal: it declares no edges, so every destination is
	// undeclared from here.
	if _, err := elliot.Transition(ctx(), "draft", res.Revision); !monshi.
		Failed(err, monshi.InvalidOp) {
		t.Errorf("transitioning out of a terminal stage = %v, want "+
			"INVALID_OPERATION", err)
	}
}

func TestTransitionRefusesAStateTheCaseIsNotIn(t *testing.T) {
	_, c := transitionCase(t, reviewer)
	if _, err := c.Transition(ctx(), "filed", 0); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("an undeclared stage name = %v, want NOT_FOUND", err)
	}

	// Every other stage in this definition exists, and none of them is
	// reachable from `draft` except the two guarded accepts and the reject.
	// A destination that exists but has no edge is not a typo about a name:
	// it is the case sitting in the wrong place, which is what
	// INVALID_OPERATION means here. Note that the name is resolved *before*
	// permission is asked, exactly as `Submit` resolves the form first: the
	// difference between "no such stage" and "you may not" is definition
	// structure a host already has, and collapsing the two would make a
	// wrong destination look like a refused actor.
	for _, dest := range []string{"closed", "audited", "draft"} {
		if _, err := c.Transition(ctx(), dest, 0); !monshi.Failed(err,
			monshi.InvalidOp) {
			t.Errorf("Transition(%q) from draft = %v, want an undeclared "+
				"transition", dest, err)
		}
	}

	// The two edges that *are* declared both carry a condition, and nothing
	// has been committed, so an unmet guard is refused before the destination
	// is asked about — and the refusal is about the case, not the caller. Which
	// is why it arrives with the case's position beside it: this caller holds
	// the grant and named the right revision, so everything the result says is
	// information he was entitled to before the call.
	res, err := c.Transition(ctx(), "reviewed", 0)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("an edge whose guard no committed content meets = %v", err)
	}
	if res.Revision != 0 || res.Stage != "draft" {
		t.Errorf("the guard refusal reported %d at %q, want 0 at draft",
			res.Revision, res.Stage)
	}
	if len(res.Missing) != 0 {
		t.Errorf("a guard refusal reported %q, want no gap list: the walk that"+
			" fills it never ran", res.Missing)
	}
	if !strings.Contains(err.Error(), "condition") {
		t.Errorf("the guard refusal says %q, want it to name the condition as"+
			" what failed", err)
	}
	if !strings.Contains(err.Error(), "reviewed") {
		t.Errorf("the guard refusal says %q, want the destination in it", err)
	}
}

func TestTransitionAsksPermissionBeforeTheCaseNumber(t *testing.T) {
	rt, dana := transitionCase(t, applicant)
	at := submittedApplication(t, dana)

	// dana is the Applicant: `draft` grants accept and reject to the Reviewer
	// alone. She presents a number that is wrong on purpose — an actor the
	// definition refuses learns that it refuses, and nothing about the case's
	// revision traffic.
	if _, err := dana.Transition(ctx(), "reviewed", 99); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("an unauthorized transition = %v, want ACCESS_DENIED", err)
	}

	// The same actor, on the edge she does hold: `corrected` is the reject
	// destination and the Applicant has no reject grant either, while the
	// reviewer does. Permission is asked of the *edge taken*, not of the
	// destination (the masks live on the source stage).
	elliot, err := rt.Open(ctx(), dana.ID(), reviewer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := elliot.Transition(ctx(), "corrected", 41); !monshi.Failed(err,
		monshi.StaleRevision) {
		t.Errorf("a stale number from an authorized actor = %v, want "+
			"STALE_REVISION", err)
	} else if !strings.Contains(err.Error(), "the case is at revision "+
		fmt.Sprint(at)) {
		t.Errorf("the staleness refusal says %q, want the case's own number "+
			"in it", err)
	}

	// And nothing moved while all of this was being refused.
	if rev, err := elliot.Revision(ctx()); err != nil || rev != at {
		t.Errorf("the case is at %d (%v) after three refusals, want %d", rev,
			err, at)
	}
}

func TestTransitionReadsTheCommittedStateAndNotADraft(t *testing.T) {
	_, c := transitionCase(t, reviewer)

	// The content the edge's guard and the destination's require both want,
	// written but not submitted.
	mustEdit(t, c, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")))
	_, err := c.Transition(ctx(), "reviewed", 0)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("a transition satisfied only by a draft = %v, want the "+
			"guard to refuse", err)
	}

	// The draft is still there and still editable.
	if _, err := c.Update(ctx(), 1,
		monshi.Set("application.name", monshi.Text("Monshi Limited"))); err != nil {
		t.Fatalf("editing after a refused transition: %v", err)
	}

	// Commit the content on the same handle, and the very same call lands. The
	// reviewer here holds edit and submit on `application`, so one actor plays
	// both parts; what changed between the two calls is only whether the
	// content is *committed*.
	res, err := c.Submit(ctx(), "application", 2)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	moved, err := c.Transition(ctx(), "reviewed", res.Revision)
	if err != nil {
		t.Fatalf("Transition after the commit = %v, want it to land", err)
	}
	if moved.Stage != "reviewed" || moved.Revision != res.Revision+1 {
		t.Errorf("the case reached %d at %q, want %d at reviewed",
			moved.Revision, moved.Stage, res.Revision+1)
	}
}

func TestTransitionRejectsAnIncompleteCase(t *testing.T) {
	rt, dana := transitionCase(t, applicant)

	// Nothing has ever been committed. The accept this fixture declares out
	// of `draft` is refused twice over — its guard does not hold, and
	// `reviewed` requires the application the case does not have — and the
	// reject here lands in `corrected`, which carries the *same* `require`
	// block. So the two calls differ in nothing but direction, which is the
	// only way to state the asymmetry: the destination's
	// completeness is asked of an accept and skipped by a reject, because the
	// case that cannot go forward is exactly the case somebody must be able to
	// send back. Everything else about a reject is still asked, below.
	elliot, err := rt.Open(ctx(), dana.ID(), reviewer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := elliot.Transition(ctx(), "corrected", 0)
	if err != nil {
		t.Fatalf("rejecting an empty case = %v, want it to commit", err)
	}
	if res.Stage != "corrected" || res.Revision != 1 {
		t.Errorf("the reject reached %d at %q, want 1 at corrected",
			res.Revision, res.Stage)
	}
	if len(res.Missing) != 0 {
		t.Errorf("a refused-to-be-checked require reported %q, want nothing",
			res.Missing)
	}
	// `corrected` grants the Reviewer no view of anything extra and the
	// Applicant's stage grants are ones she already holds from her role, so
	// this move changes nobody's paths. An empty list is a computed answer,
	// not a missing one: compare it with the accept above.
	if len(res.Affected) != 0 {
		t.Errorf("Affected = %q, want nothing: no overlay of this actor's "+
			"differs between draft and corrected", res.Affected)
	}

	// Permission and the edge are still asked of a reject. `closed` is a
	// declared stage with no edge out of draft to it, so the destination is
	// undeclared rather than ungranted...
	if _, err := elliot.Transition(ctx(), "closed", 1); !monshi.Failed(err,
		monshi.InvalidOp) {
		t.Errorf("toward an undeclared destination = %v, want "+
			"INVALID_OPERATION", err)
	}
	// ...and the one edge `corrected` does declare is an accept the Applicant
	// holds no grant for. Skipping completeness is the licence about the
	// destination's require, and nothing else: the role question, the edge and
	// its guard are all still asked of a reject.
	if _, err := dana.Transition(ctx(), "draft", 1); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("an Applicant accepting = %v, want ACCESS_DENIED", err)
	}

	// And from `corrected` the case can be sent back out only by an accept,
	// whose destination's require is then asked for: `draft` requires nothing,
	// so the loop closes without content.
	back, err := elliot.Transition(ctx(), "draft", 1)
	if err != nil {
		t.Fatalf("accepting back to draft = %v, want it to land", err)
	}
	if back.Stage != "draft" || back.Revision != 2 {
		t.Errorf("the case reached %d at %q, want 2 at draft", back.Revision,
			back.Stage)
	}
}

func TestACommittedRoleAssignmentConfersTheTransition(t *testing.T) {
	rt, dana := transitionCase(t, applicant)
	base := mustEdit(t, dana, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")),
		monshi.Set("application.boss", monshi.User("dana")))

	// The assignment is in the candidate this very batch would write, and it
	// confers nothing while it is a draft — so the refusal is about authority
	// and not about the case's content, which is also ready by then.
	if _, err := dana.Transition(ctx(), "reviewed", 0); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("a transition a draft role assignment would authorize = %v,"+
			" want ACCESS_DENIED", err)
	}

	res, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	moved, err := dana.Transition(ctx(), "reviewed", res.Revision)
	if err != nil {
		t.Fatalf("Transition after the assignment was committed = %v, want "+
			"it to land", err)
	}
	if moved.Stage != "reviewed" {
		t.Errorf("the case is at %q, want reviewed", moved.Stage)
	}

	// The same case from a handle naming a *different* identity whose primary
	// role is only Applicant: the committed assignment confers Reviewer on the
	// user it names, not on whoever asks, so this actor holds no grant for the
	// edge at all. The reject destination is used because it is the other
	// direction the same mask question is asked of.
	other, err := rt.Open(ctx(), dana.ID(), monshi.Actor{User: "elliot",
		PrimaryRole: "Applicant"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = other.Transition(ctx(), "corrected", moved.Revision)
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("an actor with no committed assignment transitioned: %v,"+
			" want ACCESS_DENIED", err)
	}
}

func TestATransitionAsksForTheCasesNumberAndNotADrafts(t *testing.T) {
	rt, dana := transitionCase(t, applicant)

	// The reviewer holds the grant `draft` asks for and the draft this test
	// presents. `at` is the *working* revision his copy reached: nothing has
	// been committed, so the case itself still sits at 0. This is the mistake a
	// host makes by holding the one number it has, and it is what the second
	// sentence is for — these operations act on committed state, so a draft's
	// own counter says nothing about where the case is.
	elliot, err := rt.Open(ctx(), dana.ID(), reviewer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	at := mustEdit(t, elliot, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")))
	if at == 0 {
		t.Fatal("the fixture's working revision is 0, want a draft step")
	}
	_, err = elliot.Transition(ctx(), "reviewed", at)
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("a transition presented a working revision = %v, want"+
			" STALE_REVISION", err)
	}
	if !strings.Contains(err.Error(), "the case is at revision 0, not") {
		t.Errorf("the refusal says %q, want the case's own number in it",
			err)
	}

	// The content, committed: the guard on the edge out of `draft` asks it of
	// the case rather than of anybody's copy, which is what the refusal above
	// was really about.
	published, err := elliot.Submit(ctx(), "application", at)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// A second draft on the same handle, so the transition below has a copy of
	// its author's to leave behind or take with it. It lands at the case's real
	// number, and his copy is *not* invalidated by the move he made: the store
	// rebased it, which it may do precisely because that copy descended from
	// the revision the transition extended.
	later := mustEdit(t, elliot, at,
		monshi.Set("application.note", monshi.Text("for the review")))
	moved, err := elliot.Transition(ctx(), "reviewed", published.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if moved.Revision != published.Revision+1 {
		t.Fatalf("the case reached %d, want %d", moved.Revision,
			published.Revision+1)
	}
	res, err := elliot.Submit(ctx(), "application", later)
	if err != nil {
		t.Fatalf("his own draft after his own stage move = %v, want it to"+
			" publish", err)
	}
	if res.Revision != moved.Revision+1 {
		t.Errorf("the content landed at %d, want %d", res.Revision,
			moved.Revision+1)
	}

	// Another actor who was looking at revision 0 all along is the one the
	// stage move leaves behind. Nothing is *wrong* with her draft; it is simply
	// not cleared to publish. Synchronization is `Rebase`'s job.
	draft := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("Monshi Limited")))
	_, err = dana.Submit(ctx(), "application", draft)
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("submitting across another actor's stage move = %v, want"+
			" STALE_REVISION", err)
	}
	if !strings.Contains(err.Error(), "descends from revision 0") {
		t.Errorf("the refusal says %q, want the revision the copy descends"+
			" from: the case has moved, and her draft does not say where to",
			err)
	}

	// And the draft survives the refusal, value and all.
	if _, err := dana.Update(ctx(), draft,
		monshi.Set("application.kind", monshi.Tag("COMPANY"))); err != nil {
		t.Fatalf("editing a copy another actor's transition left behind: %v",
			err)
	}
}
