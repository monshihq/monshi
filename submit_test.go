package monshi_test

// The commit path seen from outside the module (DESIGN §6.3; RUNTIME §5.4):
// what a host can observe about a submit, which is the revision it reached,
// the list it was refused for, and which of the checks answered first.
//
// Two things a host cannot observe are deliberately absent here. What a
// commit *wrote* — the diff, the history entry, which part of the snapshot a
// graft replaced — is asserted in package (`store_test.go`'s subject), and so
// is whether a `Validate` hook ran, because binding a hook needs the seam
// that is not exported yet (RUNTIME §6). `submit_hook_test.go` covers those.
//
// What is here instead is the order, because the order is the security
// argument: a refusal that reports a stale revision to an actor the
// definition refuses, or reports a form's gaps to somebody who may not see
// the form, teaches something the definition did not mean to teach.

import (
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi"
)

// submitSrc states each claim below with one declaration: a form an
// Applicant may commit, one she may only draft, one the Manager may commit
// but not write, a gated subtree, a required collection, a role-typed field
// whose assignment is the subject of §4.3's test, and an optional field whose
// removal still has to be a commit.
const submitSrc = `
types {
    legal : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
        allow edit archive;
    }
    Manager {
        allow view all;
        allow submit archive;
    }
}
forms {
    application {
        kind  : legal required;
        name  : string required;
        note  : string;
        owner : Manager;
        gate  : string;

        deep present[.gate == "y"] {
            risk : integer required;
        }

        holders multiple required {
            who : string required;
        }
    }

    archive {
        filed : datetime required;
    }
}
stages {
    draft { }
    filed { }
}
`

// submitCase is a runtime and one actor's handle on a new case of the
// fixture. The runtime comes back because a second actor opening the *same*
// case is half of what the staleness tests are about.
func submitCase(t *testing.T, actor monshi.Actor) (
	*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "submit", submitSrc)
	c, err := rt.Create(ctx(), "submit", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// complete is the batch that makes the fixture's `application` form whole,
// minus the field each test is about: a helper rather than a fixture, so a
// test that forgot one of these would fail as a missing gap and not as a
// silence.
func complete() []monshi.Edit {
	return []monshi.Edit{
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")),
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Ada")),
	}
}

func TestSubmitNamesWhatTheFormStillNeeds(t *testing.T) {
	_, c := submitCase(t, applicant)

	// A draft with nothing in it: the two unconditional required fields and
	// the collection with no instances. `deep.risk` is not among them,
	// because its group is not part of the case's effective state — a host
	// asked to fill it would be filling in a field its own definition
	// withdrew (§5.2's pruning).
	res, err := c.Submit(ctx(), "application", 0)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("an empty submit = %v, want INVALID_OPERATION", err)
	}
	want := "application.kind application.name application.holders"
	if got := strings.Join(res.Missing, " "); got != want {
		t.Errorf("Missing = %q, want %q", got, want)
	}
	if len(res.Invalid) != 0 {
		t.Errorf("Invalid = %+v, want nothing: an empty field is a gap and "+
			"not a value a hook refused", res.Invalid)
	}
	// The refusal still says where the case is — the revision a host reloads
	// at and the stage a form header renders — and nothing moved.
	if res.Revision != 0 || res.Stage != "draft" {
		t.Errorf("a refused submit reported revision %d stage %q, want 0 "+
			"at draft", res.Revision, res.Stage)
	}
	if rev, err := c.Revision(ctx()); err != nil || rev != 0 {
		t.Errorf("the case is at %d (%v) after a refused submit, want 0",
			rev, err)
	}
	// The detail says how much, and the lists say which: one sentence per
	// place, so a host renders `Missing` without parsing an error.
	if !strings.Contains(err.Error(), "3 required fields") {
		t.Errorf("the refusal says %q, want it to count what it refused",
			err)
	}

	// Opening the gate puts the field behind it into the same list, in
	// declaration order: the walk is the preorder the numbering gives it,
	// which is the order a form renders.
	base := mustEdit(t, c, 0,
		monshi.Set("application.gate", monshi.Text("y")))
	res, err = c.Submit(ctx(), "application", base)
	if err == nil || len(res.Missing) != 4 ||
		res.Missing[2] != "application.deep.risk" {
		t.Errorf("Missing = %q with err %v, want the gated field third",
			res.Missing, err)
	}

	// Filling the two fields and giving the collection a row leaves exactly
	// one gap, and it is the gated field's own path. That is the list a host
	// can act on: a value for the path it was given, and nothing else, turns
	// the refusal into a commit — §6.3's reason the report spells an address
	// rather than a label. The collection-gap half of the same claim, that
	// every reported path resolves back to what it names, is checked in
	// package against the walk itself, because it needs to see the row the
	// address found.
	base = mustEdit(t, c, base,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")),
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Ada")))
	res, err = c.Submit(ctx(), "application", base)
	if err == nil || len(res.Missing) != 1 ||
		res.Missing[0] != "application.deep.risk" {
		t.Fatalf("Missing = %q with err %v, want only the gated field",
			res.Missing, err)
	}

	base = mustEdit(t, c, base,
		monshi.Set("application.deep.risk", monshi.Int(7)))
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 1 {
		t.Errorf("submitting the reported fix = %+v (%v), want revision 1",
			out, err)
	}
}

func TestSubmitCommitsOnceAndRefusesToCommitNothing(t *testing.T) {
	_, c := submitCase(t, applicant)

	base := mustEdit(t, c, 0, complete()...)
	out, err := c.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("a complete form: %v", err)
	}
	if out.Revision != 1 || out.Stage != "draft" {
		t.Errorf("commit = revision %d stage %q, want 1 at draft",
			out.Revision, out.Stage)
	}
	if len(out.Missing) != 0 || len(out.Invalid) != 0 {
		t.Errorf("a committed submit reported %+v, want no lists", out)
	}
	if rev, err := c.Revision(ctx()); err != nil || rev != 1 {
		t.Errorf("Revision after the commit = %d (%v), want 1", rev, err)
	}

	// The same draft submitted again writes nothing, and that is a success
	// rather than a refusal: the caller's request — this form is committed —
	// is already true. §6.3's "no meaningless commit" and §5.1's reason it
	// matters, that a revision is the count of commits, so an idempotent
	// retry has to leave the number alone. Submitting at the number the copy
	// still holds also proves the commit rebased this actor's copy rather
	// than advancing it: an edit-free second submit is refused as stale by
	// either guard if the store got that wrong.
	again, err := c.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("a repeat submit: %v", err)
	}
	if again.Revision != 1 {
		t.Errorf("a repeat submit reached revision %d, want 1",
			again.Revision)
	}

	// And the next real change lands at 2 and not 3, which is the same claim
	// from the other side: the no-op above added nothing to the count.
	base = mustEdit(t, c, base,
		monshi.Set("application.name", monshi.Text("Monshi Limited")))
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 2 {
		t.Errorf("submit after an edit = %+v (%v), want revision 2", out,
			err)
	}
}

func TestSubmitCommitsARemoval(t *testing.T) {
	// A commit reports semantic effects, and §5.1 counts a removal among
	// them: content the case said and no longer says is a change, so a
	// submit whose only edit was `Empty` must advance the revision and not
	// return the no-op it would have been under a value-equality diff.
	_, c := submitCase(t, applicant)
	base := mustEdit(t, c, 0, append(complete(),
		monshi.Set("application.note", monshi.Text("see attached")))...)
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 1 {
		t.Fatalf("submit = %+v (%v), want revision 1", out, err)
	}

	base = mustEdit(t, c, base, monshi.Empty("application.note"))
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 2 {
		t.Errorf("submit after Empty = %+v (%v), want revision 2: clearing "+
			"a field is a change to commit", out, err)
	}

	// Removing an instance is the same claim about a row instead of a value
	// (§5.3's distinction between writing a collection and adding to it), and
	// the walk then refuses to commit the form again until a row is back.
	base = mustEdit(t, c, base, monshi.Remove("application.holders.1"))
	res, err := c.Submit(ctx(), "application", base)
	if !monshi.Failed(err, monshi.InvalidOp) || len(res.Missing) != 1 ||
		res.Missing[0] != "application.holders" {
		t.Errorf("submit after Remove = %+v (%v), want the collection back "+
			"on the missing list", res, err)
	}
	base = mustEdit(t, c, base, monshi.Add("application.holders"))
	base = mustEdit(t, c, base,
		monshi.Set("application.holders.2.who", monshi.Text("Ada")))
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 3 {
		t.Errorf("submit with the replacement row = %+v (%v), want "+
			"revision 3", out, err)
	}
}

func TestSubmitRefusesAFormTheActorMayNotCommit(t *testing.T) {
	_, c := submitCase(t, applicant)

	// Dana may draft `archive` and no more, so the answer is about authority
	// even though the form is empty: an actor who cannot commit a form does
	// not get to learn what it is missing, because that is a fact about a
	// part of the case the definition kept from her.
	if res, err := c.Submit(ctx(), "archive", 0); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("submitting outside the grant = %v, want ACCESS_DENIED",
			err)
	} else if len(res.Missing) != 0 {
		t.Errorf("a submit refused for authority also reported %q",
			res.Missing)
	}

	// The order, stated as a test: a working revision that is plainly wrong
	// would be refused as stale if the number were checked first. The
	// Manager holds no grant on `application` at all.
	_, boss := submitCase(t, watchman)
	if _, err := boss.Submit(ctx(), "application", 41); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("a Manager submitting the Applicant's form = %v, want "+
			"ACCESS_DENIED", err)
	}

	// An actor with no role hears the same refusal, and not a different one:
	// the absence of a grant and the presence of a denial are the same
	// answer, so a probe cannot tell the two fixtures apart (RUNTIME §7).
	_, orphan := submitCase(t, monshi.Actor{User: "frank"})
	if _, err := orphan.Submit(ctx(), "application", 0); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("a roleless submit = %v, want ACCESS_DENIED", err)
	}

	// A name that is not a form is NOT_FOUND, and a path is the caller's
	// mistake about which operation it wanted: §6.2's edits take paths and
	// §6.3 commits one top-level form, and SYNTAX P3 says a `submit` grant
	// can only ever name the second.
	if _, err := c.Submit(ctx(), "applications", 0); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("an unknown form = %v, want NOT_FOUND", err)
	}
	if _, err := c.Submit(ctx(), "application.name", 0); !monshi.Failed(err,
		monshi.InvalidOp) {
		t.Errorf("a path where a form belongs = %v, want "+
			"INVALID_OPERATION", err)
	}
}

func TestSubmitRefusesTheCopyItWasNotGiven(t *testing.T) {
	_, c := submitCase(t, applicant)
	mustEdit(t, c, 0, monshi.Set("application.name", monshi.Text("Monshi")))

	// One batch moved the copy to 1, so presenting 0 is what two sessions of
	// one actor do to each other (invariant 4), and the answer is a refusal
	// rather than a merge. The detail gives both numbers, because the fix is
	// to reload at the one the store holds.
	res, err := c.Submit(ctx(), "application", 0)
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("a submit at a stale working revision = %v, want "+
			"STALE_REVISION", err)
	}
	if !strings.Contains(err.Error(), "at revision 1, not 0") {
		t.Errorf("the stale refusal says %q, want both numbers", err)
	}
	if len(res.Missing) != 0 {
		t.Errorf("a copy refused for its number also reported %q: the walk "+
			"is not run over data the caller did not present", res.Missing)
	}
}

func TestSubmitRefusesACopyAnotherCommitLeftBehind(t *testing.T) {
	rt, dana := submitCase(t, applicant)
	// The Manager opens the same case before anything commits, so her copy
	// descends from revision 0 and nothing has moved it since.
	elliot, err := rt.Open(ctx(), dana.ID(), watchman)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	base := mustEdit(t, dana, 0, complete()...)
	if out, err := dana.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 1 {
		t.Fatalf("dana's submit = %+v (%v), want revision 1", out, err)
	}

	// Her copy is untouched by that commit — same working revision, same
	// data — which is what makes her next submit answer with the *case's*
	// movement rather than her own: she presents a working revision the copy
	// still holds, and the base is what fails.
	if _, err := elliot.Submit(ctx(), "archive", 0); !monshi.Failed(err,
		monshi.StaleRevision) {
		t.Errorf("a submit from a copy left behind = %v, want "+
			"STALE_REVISION", err)
	}

	// The two staleness refusals say different things, and the difference is
	// the one a client has to act on: presenting a number its copy never had
	// is its own bug, while descending from a superseded revision is the
	// case's. Both are STALE_REVISION, because §9 gives one category to "the
	// copy you are holding is not the one you think", but the detail names
	// the pair that disagrees.
	if _, err := elliot.Submit(ctx(), "archive", 1); err == nil ||
		!strings.Contains(err.Error(), "working copy is at revision 0, "+
			"not 1") {
		t.Errorf("a submit presenting a number the copy never held says "+
			"%v, want it to name the copy's own revision", err)
	}

	// Reconnecting is not the way out. One working copy per `(case, user)`
	// (invariant 4) means a fresh handle finds the copy that is already
	// there rather than a new one, so a client cannot fall through to the
	// current revision by opening again — §6.5's rebase is the operation
	// that moves a copy onto a new base, and until it exists the refusal
	// stands. That is a decision worth a test, because the tempting
	// implementation of `Open` is "make a copy if none exists".
	fresh, err := rt.Open(ctx(), dana.ID(), watchman)
	if err != nil {
		t.Fatalf("Open after the commit: %v", err)
	}
	if _, err := fresh.Submit(ctx(), "archive", 0); err == nil ||
		!strings.Contains(err.Error(),
			"descends from revision 0, the case is at 1") {
		t.Errorf("a reopened copy says %v, want the same base refusal: a "+
			"handle is not a new working copy", err)
	}
}

func TestARoleAssignedByADraftConfersNothingUntilItIsCommitted(t *testing.T) {
	_, c := submitCase(t, applicant)

	// A role-typed field's holder is part of the role set an actor has
	// (§4.3), and the committed snapshot is the only thing authority reads.
	// The submit path is where that matters most: the draft being committed
	// is exactly the content that would grant the right to commit it, so a
	// submit that folded roles from the candidate would let an actor publish
	// a grant to themselves.
	base := mustEdit(t, c, 0, append(complete(),
		monshi.Set("application.owner", monshi.User("dana")))...)
	if _, err := c.Submit(ctx(), "archive", base); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Fatalf("a submit granting itself a role = %v, want "+
			"ACCESS_DENIED", err)
	}

	// The same draft, committed as `application`, does confer it: the role
	// assignment is now part of the case, and the next submit reads it from
	// there. Both halves are asserted because either one alone is a rule a
	// future change could satisfy by accident — always reading the candidate
	// passes the second, and always reading the store passes the first.
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 1 {
		t.Fatalf("submitting the assignment = %+v (%v), want revision 1",
			out, err)
	}
	filed := monshi.DateTime(time.Date(2026, time.November, 23, 10, 0, 0,
		0, time.UTC))
	withFiled := mustEdit(t, c, base, monshi.Set("archive.filed", filed))
	if out, err := c.Submit(ctx(), "archive", withFiled); err != nil ||
		out.Revision != 2 {
		t.Errorf("the second form after the role landed = %+v (%v), want "+
			"revision 2", out, err)
	}
}

// mustEdit applies one batch to a case's copy and returns the revision the
// next call presents. The number is threaded by hand rather than read from
// the handle, because a submit that checked the wrong one is exactly the bug
// a helper that hid it would swallow.
func mustEdit(t *testing.T, c monshi.Case, base uint64,
	edits ...monshi.Edit) uint64 {
	t.Helper()
	res, err := c.Update(ctx(), base, edits...)
	if err != nil {
		t.Fatalf("Update at %d: %v", base, err)
	}
	return res.Working
}
