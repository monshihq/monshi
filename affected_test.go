package monshi_test

// The affected set of an accepted batch (RUNTIME §5.3, DESIGN §6.2), seen from
// outside the module: what a client is told to refresh. The claims that matter
// here are the two a host cannot get from anywhere else — that a path it never
// wrote appears because a guard reads what it did, and that a path it did write
// never disappears because it is irrelevant — plus the shape of the list, which
// a client parses by prefix and so has to be stable.
//
// Which slots the walk *marks* is internal and is asserted in `fanout_test.go`;
// this file only has what a returned `ChangeResult` can show.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

// affectedSrc states each claim with one declaration. `region` is a group a
// guard opens and `district` reads a *member* of it, so an edit to `province`
// reaches `district` only through the subtree; `channel` has an `Options`
// binding and its own condition, which is §5.3's named case for a field that
// must be re-fetched rather than re-read; `note` is read by nothing and is the
// control; `reviewer` confers a role and so is the path whose permission answer
// a reader might expect to move.
const affectedSrc = `
types {
    legal : enum(PERSON, COMPANY);
}
roles {
    Applicant { allow view, edit, submit application; }
}
forms {
    application {
        kind     : legal;
        province : string;
        note     : string;
        reviewer : Applicant;
        channel  : options required[.kind == COMPANY];

        region present[.province == "GJ"] {
            city : string;
        }
        district : string present[.region.city == "Central"];

        holders multiple {
            who : string required;
        }
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`

// paths renders an affected set beside the words a test expects, so a failure
// says which path appeared or vanished rather than dumping two slices.
func paths(got []string, want ...string) string {
	if len(got) == 0 {
		got = []string{"<none>"}
	}
	return "got [" + strings.Join(got, " ") + "], want [" +
		strings.Join(want, " ") + "]"
}

// affectedFor runs one batch and returns what it reported.
func affectedFor(t *testing.T, edits ...monshi.Edit) []string {
	t.Helper()
	c := openCase(t, affectedSrc, dana)
	res, err := c.Update(ctx(), 0, edits...)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return res.Affected
}

func TestAffectedNamesWhatAGuardReads(t *testing.T) {
	// `note` is read by no condition, so the answer is the one field and not
	// the form around it. Over-reporting is §5.3's stated direction, but a
	// list that grows with every edit is a client refreshing the screen on
	// every keystroke, which is the cost this assertion keeps honest.
	if got := affectedFor(t, monshi.Set("application.note",
		monshi.Text("nothing depends on me"))); !reflect.DeepEqual(got,
		[]string{"application.note"}) {
		t.Error(paths(got, "application.note"))
	}

	// One write to `province` moves three paths: the field itself, the group
	// whose guard reads it, and `district`, which reads a *member* of that
	// group. The last is the reason the walk expands a re-asked owner to its
	// subtree rather than stopping at the owner's own path: `region.city`
	// became visible or invisible, and a guard on another field reads it.
	//
	// It is also why the group is reported once: `city` is inside `region`, so
	// naming the group names its subtree. A client that refreshes by prefix
	// needs the shortest covering path, not every path in it.
	got := affectedFor(t, monshi.Set("application.province",
		monshi.Text("GJ")))
	if !reflect.DeepEqual(got, []string{"application.province",
		"application.region", "application.district"}) {
		t.Fatal(paths(got, "application.province", "application.region",
			"application.district"))
	}

	// And the two halves of that walk are separate claims: with the guard
	// already satisfied, editing `city` reaches `district` but has nothing to
	// say about `province`, which the batch did not touch and no guard reads.
	c := openCase(t, affectedSrc, dana)
	if _, err := c.Update(ctx(), 0, monshi.Set("application.province",
		monshi.Text("GJ"))); err != nil {
		t.Fatalf("opening the subtree: %v", err)
	}
	res, err := c.Update(ctx(), 1, monshi.Set("application.region.city",
		monshi.Text("Central")))
	if err != nil {
		t.Fatalf("writing the member: %v", err)
	}
	if !reflect.DeepEqual(res.Affected, []string{"application.region.city",
		"application.district"}) {
		t.Fatal(paths(res.Affected, "application.region.city",
			"application.district"))
	}
}

func TestAffectedReachesAFieldWhoseOptionsBindingWasTouched(t *testing.T) {
	// §5.3 counts options in the set beside presence and validation, and
	// `channel` is the shape it means: a field whose *choices* come from host
	// code, with a condition of its own on the field this batch writes. The
	// host cannot know that `kind == COMPANY` decides whether its option list
	// is even asked for, so the engine has to say so — a client that kept the
	// dropdown it had would be showing a choice set for a field that is no
	// longer required, which is exactly the drift §5.2's derived presence is
	// there to prevent.
	//
	// Note the list is unchanged in shape from any other dependent: an options
	// field needs no special reporting because "re-fetch the choices" and
	// "re-read the field" are the same instruction to a client.
	got := affectedFor(t, monshi.Set("application.kind",
		monshi.Tag("COMPANY")))
	if !reflect.DeepEqual(got, []string{"application.kind",
		"application.channel"}) {
		t.Error(paths(got, "application.kind", "application.channel"))
	}
}

func TestAffectedReportsARowAsItsCollection(t *testing.T) {
	// An instance appearing is a change to the group's rows, which is what a
	// client re-renders; the ID belongs to `Created`, which addresses the row
	// so it can be written to next. Two lists, two granularities, and the
	// pair is why §5.1's `Add` takes no value: the engine knows both what the
	// row is called and that the view of the group moved.
	c := openCase(t, affectedSrc, dana)
	res, err := c.Update(ctx(), 0, monshi.Add("application.holders"))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(res.Created) != 1 ||
		!strings.HasPrefix(res.Created[0], "application.holders.") {
		t.Errorf("Created = %v, want the instance path Add allocated",
			res.Created)
	}
	if !reflect.DeepEqual(res.Affected, []string{"application.holders"}) {
		t.Error(paths(res.Affected, "application.holders"))
	}

	// A later batch that writes one member of that row reports the member's
	// declaration path: no instance ID, because the set speaks about the
	// definition rather than about which rows exist, and not the group, because
	// no row began or ended. The asymmetry against the next paragraph is the
	// point — an instance appearing is a change to the collection, a value
	// changing is a change to a field — and it is why a client keys its cache
	// on the declaration path and finds both answers there.
	row := res.Created[0]
	res, err = c.Update(ctx(), res.Working,
		monshi.Set(row+".who", monshi.Text("Dana")))
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !reflect.DeepEqual(res.Affected,
		[]string{"application.holders.who"}) {
		t.Error(paths(res.Affected, "application.holders.who"))
	}

	// Dropping an instance is `Add` from the other side: the subtree the group
	// names is what a client must stop showing, and the row's ID is not a path
	// anyone needs to be told about twice.
	res, err = c.Update(ctx(), res.Working, monshi.Remove(row))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !reflect.DeepEqual(res.Affected, []string{"application.holders"}) {
		t.Error(paths(res.Affected, "application.holders"))
	}
}

func TestARoleAssignmentAffectsItsFieldAndNothingElse(t *testing.T) {
	// The field's value now names somebody, and that somebody holds a role —
	// but §3.5's masks are a function of stage and role set and of no value at
	// all, and an `Update` moves neither input: the stage is the committed one
	// and the roles fold from committed data (§4.3). So no path's permission
	// answer changed, and the affected set says exactly that.
	//
	// This is the assertion to break first if §4.3 ever stops holding: were
	// authority to fold from the draft, an edit like this one would have to
	// report every path the new role reaches, and a client that trusted the
	// list would keep a screen it should no longer be allowed to see.
	got := affectedFor(t, monshi.Set("application.reviewer",
		monshi.User("elliot")))
	if !reflect.DeepEqual(got, []string{"application.reviewer"}) {
		t.Error(paths(got, "application.reviewer"))
	}
}

func TestAffectedIsASetOfDeclarationPaths(t *testing.T) {
	// Two edits to one cell in one batch — which is a host building its
	// message from a form snapshot rather than from what changed — report the
	// path once. The list is a set of slots rendered, not a log of writes, and
	// a client that deduplicated on its side would be repairing an engine
	// answer that already meant "these views may differ".
	got := affectedFor(t,
		monshi.Set("application.note", monshi.Text("first")),
		monshi.Empty("application.note"),
		monshi.Set("application.note", monshi.Text("last")))
	if !reflect.DeepEqual(got, []string{"application.note"}) {
		t.Error(paths(got, "application.note"))
	}

	// Declaration-addressed, always: an instance's members are written through
	// the row's ID, but no reported path carries one, so the set is a statement
	// about the definition rather than about which rows happen to exist. That
	// is what lets a client match a prefix like `application.holders` against a
	// path it cached before the rows were created.
	got = affectedFor(t,
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Dana")),
		monshi.Set("application.province", monshi.Text("GJ")))
	for _, p := range got {
		if strings.HasSuffix(p, ".who") || strings.HasSuffix(p, ".city") {
			t.Errorf("%q is reported beside the subtree that contains it: %s",
				p, paths(got))
		}
	}
	if !reflect.DeepEqual(got, []string{"application.province",
		"application.region", "application.district",
		"application.holders"}) {
		t.Error(paths(got, "application.province", "application.region",
			"application.district", "application.holders"))
	}
}

func TestAffectedIsOrderAndPrefixFree(t *testing.T) {
	// The two properties a client parses by. Declaration order means a diff of
	// two answers reads top-down like the form does, and prefix-freeness means
	// "refresh this path" never implies refreshing a path also on the list.
	// Together they are what a stable rendering needs, and neither survives a
	// refactor to a map, which is the reason they are tested rather than
	// commented.
	got := affectedFor(t,
		monshi.Set("application.district", monshi.Text("x")),
		monshi.Set("application.region.city", monshi.Text("y")),
		monshi.Set("application.note", monshi.Text("z")))
	want := []string{"application.note", "application.region.city",
		"application.district"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(paths(got, want...))
	}
	for i, p := range got {
		if i > 0 && !strings.HasPrefix(p, "application.") {
			t.Fatalf("%q is not a path of this form", p)
		}
		for j := i + 1; j < len(got); j++ {
			if strings.HasPrefix(got[j], p+".") {
				t.Errorf("%q contains %q: the list is not prefix-free", p,
					got[j])
			}
		}
	}
}

func TestARefusedBatchReportsNoAffectedPaths(t *testing.T) {
	// §9's no-persistent-effect claim covers the answer as well as the data: a
	// host that acted on a path list from a refused batch would refresh a
	// screen the engine left alone.
	c := openCase(t, affectedSrc, monshi.Actor{User: "elliot"})
	res, err := c.Update(ctx(), 0, monshi.Set("application.note",
		monshi.Text("not mine to write")))
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Fatalf("an edit by an actor with no role = %v, want ACCESS_DENIED",
			err)
	}
	if res.Affected != nil || res.Created != nil || res.Working != 0 {
		t.Errorf("a refused batch returned %+v, want the zero result", res)
	}
}

// commitSrc is the commit half of this file's claims (RUNTIME §5.4, §9). The
// guard pair is the same shape `affectedSrc` walks, so the batch and commit
// assertions read alike, and three declarations are added that no batch can
// demonstrate: a `verifiable` group whose approval a content commit erases, a
// signature field whose attestation one invalidates, and a role-typed field
// whose conferral moves the committer's own §3.5 fold as the commit lands.
// The reached form is a second one, not a subtree of the first: a whole-form
// `view` grant cascades, so visibility a commit can *add* has to live outside
// what the holder already sees — which is exactly the shape DESIGN §6.5's
// "host may notify users" paragraph assumes.
const commitSrc = `
roles {
    Clerk {
        allow view, edit, submit application;
    }
    Registrar {
        allow view all;
        allow review application.notes;
        allow sign application.counted;
    }
    Auditor {
        allow view audit;
    }
}
forms {
    application {
        kind     : string;
        province : string;
        referrer : Auditor;
        note     : string;

        region present[.province == "GJ"] {
            city : string;
        }
        district : string present[.region.city == "Central"];

        holders multiple {
            who : string required;
        }
        notes verifiable {
            claim : string;
        }
        counted : signature(Registrar);
    }

    audit {
        total : string;
    }
}
stages {
    draft { allow Clerk accept; accept filed; }
    filed { }
}
`

var (
	commitClerk = monshi.Actor{User: "dana", PrimaryRole: "Clerk"}
	commitSeal  = monshi.Actor{User: "elliot", PrimaryRole: "Registrar"}
)

// commitPair is one fresh case with the two handles the claims need: the actor
// who types and submits, and the Registrar who decides and attests, because an
// erasure is only news when a fact was standing to be erased.
func commitPair(t *testing.T) (monshi.Case, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "edit", commitSrc)
	c, err := rt.Create(ctx(), "edit", commitClerk)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	e, err := rt.Open(ctx(), c.ID(), commitSeal)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c, e
}

// lastCommit is the trail's account of one commit, for the assertions that
// read an `Affected` list against the entries the same act recorded.
func lastCommit(t *testing.T, c monshi.Case, rev uint64) monshi.Commit {
	t.Helper()
	h, err := c.History(ctx(), rev, 1)
	if err != nil || len(h.Commits) != 1 {
		t.Fatalf("History(%d, 1) = %d commits (%v), want one", rev,
			len(h.Commits), err)
	}
	return h.Commits[0]
}

func TestASubmitReachesWhatTheCommitMoved(t *testing.T) {
	// The batch reported these paths for the draft; the commit reports them
	// again from the entries the store actually recorded, so a client that
	// never saw the draft still learns the closure. Three paths from one
	// entry is the claim: the walk reaches what no entry names, because a
	// guard moved rather than a value.
	c, _ := commitPair(t)
	base, err := c.Update(ctx(), 0, monshi.Set("application.province",
		monshi.Text("GJ")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	want := []string{"application.province", "application.region",
		"application.district"}
	if !reflect.DeepEqual(res.Affected, want) {
		t.Fatal(paths(res.Affected, want...))
	}
	com := lastCommit(t, c, res.Revision)
	if len(com.Changes) != 1 ||
		com.Changes[0].Path != "application.province" {
		t.Errorf("the commit recorded %+v, want the one written field",
			com.Changes)
	}
}

func TestASubmitNamesTheCollectionNotTheInstance(t *testing.T) {
	// §5.3's Add/Remove rule at the commit end: the diff holds a row entry
	// and a member value, the walk answers with the declaration, and no
	// path in the list carries an instance number — a client refreshes
	// `application.holders` and re-reads the rows from there.
	c, _ := commitPair(t)
	base, err := c.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Text("PERSON")),
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Ada")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	want := []string{"application.kind", "application.holders"}
	if !reflect.DeepEqual(res.Affected, want) {
		t.Fatal(paths(res.Affected, want...))
	}
	// The trail beside it does name the instance, as a ROWS entry's row and
	// a VALUE entry's address: the two lists answer different questions,
	// what happened and what to refresh, and the collapse claim is that
	// only the second one ever speaks in instance numbers.
	com := lastCommit(t, c, res.Revision)
	if len(com.Changes) != 3 {
		t.Errorf("the commit recorded %d entries, want value, rows, member",
			len(com.Changes))
	}
}

func TestASubmitReportsTheDecisionItErased(t *testing.T) {
	// The asymmetry §5.6 and §5.4 make together: history keeps what stood
	// and writes nothing about what the commit removed, so the trail of the
	// act below shows one new value and the result shows the verdict that
	// died with it.
	c, e := commitPair(t)
	base, err := c.Update(ctx(), 0, monshi.Set("application.notes.claim",
		monshi.Text("first")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	sub, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	rv, err := e.Review(ctx(), "application.notes", monshi.ReviewApprove,
		sub.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	// A verdict's own answer is the unit and the subtree it renders — the
	// walk's collapse of one re-asked group, which is also the §5.9 claim
	// that approval is a fact about the unit, not the cell.
	if !reflect.DeepEqual(rv.Affected, []string{"application.notes"}) {
		t.Fatal(paths(rv.Affected, "application.notes"))
	}
	// §5.8's rule stands between the verdict and the edit: the draft descends
	// from the revision it was opened at, so the client rebases before
	// writing again, and the rebase's number is the next `Update`'s base.
	rb, err := c.Rebase(ctx())
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	base, err = c.Update(ctx(), rb.Working,
		monshi.Set("application.notes.claim", monshi.Text("moved")))
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	// The member's path is *absorbed* here: what the commit moved renders
	// as the group, and it would have anyway — the claim is the one entry
	// the trail does not carry, since a bare mark of the moved field would
	// answer with `claim` alone and never name the unit.
	if !reflect.DeepEqual(res.Affected, []string{"application.notes"}) {
		t.Fatal(paths(res.Affected, "application.notes"))
	}
	com := lastCommit(t, c, res.Revision)
	if len(com.Changes) != 1 || com.Changes[0].Kind != monshi.ChangeValue ||
		com.Changes[0].Path != "application.notes.claim" {
		t.Errorf("the commit recorded %+v, want the one moved value",
			com.Changes)
	}
	// And the verdict really is gone where the list says it is: the view
	// reads pending, so the list was not a fiction about the trail.
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if node(t, v, "application.notes").Review.State !=
		monshi.ReviewPending {
		t.Error("the erased approval still renders")
	}
	// A revoke moves the walk exactly as an approval did, and the pair
	// re-approving first shows the list survives the whole verdict cycle.
	rv, err = e.Review(ctx(), "application.notes", monshi.ReviewApprove,
		res.Revision, "")
	if err != nil {
		t.Fatalf("re-Review: %v", err)
	}
	back, err := e.Review(ctx(), "application.notes", monshi.ReviewRevoke,
		rv.Revision, "")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !reflect.DeepEqual(back.Affected, []string{"application.notes"}) {
		t.Fatal(paths(back.Affected, "application.notes"))
	}
}

func TestASubmitReportsTheSignatureItInvalidated(t *testing.T) {
	// §4.2's second erasure arrives on a commit's list the same way: the
	// attestation below stands over content, a later commit moves that
	// content, and the field's path appears beside the moved value with no
	// entry anywhere saying so.
	c, e := commitPair(t)
	base, err := c.Update(ctx(), 0, monshi.Set("application.notes.claim",
		monshi.Text("first")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	sub, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	rv, err := e.Review(ctx(), "application.notes", monshi.ReviewApprove,
		sub.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	sig, err := e.Sign(ctx(), "application.counted", rv.Revision, "seal")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !reflect.DeepEqual(sig.Affected, []string{"application.counted"}) {
		t.Fatal(paths(sig.Affected, "application.counted"))
	}
	rb, err := c.Rebase(ctx())
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	base, err = c.Update(ctx(), rb.Working, monshi.Set("application.kind",
		monshi.Text("OTHER")))
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	// Declaration order across the pair: the moved field is declared first,
	// the erased attestation last, and nothing between — the walk did not
	// reach the guard subtree, because this commit wrote `province` to
	// nothing. The exact list is the sortedness claim.
	want := []string{"application.kind", "application.counted"}
	if !reflect.DeepEqual(res.Affected, want) {
		t.Fatal(paths(res.Affected, want...))
	}
	com := lastCommit(t, c, res.Revision)
	if len(com.Changes) != 1 ||
		com.Changes[0].Path != "application.kind" {
		t.Errorf("the commit recorded %+v, want the one moved value",
			com.Changes)
	}
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if n := node(t, v, "application.counted"); n.Signature.By != "" {
		t.Errorf("the invalidated attestation still names %q",
			n.Signature.By)
	}
}

func TestACommitConfersTheViewTheResultNames(t *testing.T) {
	// §4.3's T4 rule read from the result side: a role-typed field confers
	// *with* the commit that lands it, so the committer's own fold moves at
	// dispatch and the list must name the subtree the actor could not see
	// a revision ago — the one term no adjacency edge reaches, since
	// §3.5's masks read the role set and no guard's operand does.
	c, _ := commitPair(t)
	base, err := c.Update(ctx(), 0, monshi.Set("application.referrer",
		monshi.User("dana")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	want := []string{"application.referrer", "audit"}
	if !reflect.DeepEqual(res.Affected, want) {
		t.Fatal(paths(res.Affected, want...))
	}
	// Not narrower than the actor's next read, either: the form the list
	// names is a form the same actor can now open.
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Roots) != 2 || v.Roots[1].Path != "audit" {
		t.Fatalf("the committed view has %d roots, want the conferring "+
			"commit's second form", len(v.Roots))
	}
	// The control is the same act naming somebody else: the conferral
	// lands on a user who is not the submitter, no fold moves, and the
	// list holds the field and nothing mask-shaped. A client caching the
	// blank `audit` form for dana keeps it blank — correctly.
	other, _ := commitPair(t)
	base, err = other.Update(ctx(), 0, monshi.Set("application.referrer",
		monshi.User("ferris")))
	if err != nil {
		t.Fatalf("control Update: %v", err)
	}
	res, err = other.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("control Submit: %v", err)
	}
	if !reflect.DeepEqual(res.Affected, []string{"application.referrer"}) {
		t.Fatal(paths(res.Affected, "application.referrer"))
	}
}

func TestTheQuietCommitsAnswerNoPaths(t *testing.T) {
	// The walk waits for the store's yes, exactly as the batch's closure
	// does: a no-op submit, a repeated verdict, an attestation already
	// standing and a refusal each answer with nothing, because there is no
	// commit for a list to describe.
	c, e := commitPair(t)
	base, err := c.Update(ctx(), 0, monshi.Set("application.note",
		monshi.Text("one")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	sub, err := c.Submit(ctx(), "application", base.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	again, err := c.Submit(ctx(), "application", base.Working)
	if err != nil || again.Revision != sub.Revision ||
		again.Affected != nil {
		t.Errorf("a no-op submit answered %+v (%v), want the same revision "+
			"and no list", again, err)
	}
	rv, err := e.Review(ctx(), "application.notes", monshi.ReviewApprove,
		sub.Revision, "")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	twice, err := e.Review(ctx(), "application.notes",
		monshi.ReviewApprove, rv.Revision, "")
	if err != nil || twice.Affected != nil {
		t.Errorf("a repeated verdict answered %+v (%v), want no list",
			twice, err)
	}
	// A stale revision is the refusal shape: the result is the zero value,
	// list included — the caller learns the case's number from the error,
	// not from a path set describing a commit that never happened.
	if res, err := e.Review(ctx(), "application.notes",
		monshi.ReviewReject, sub.Revision, "late"); !monshi.Failed(err,
		monshi.StaleRevision) || res.Affected != nil {
		t.Errorf("a stale verdict answered %+v (%v), want STALE_REVISION "+
			"and the zero result", res, err)
	}
}
