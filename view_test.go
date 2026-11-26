package monshi_test

// The view seen from outside the module: what a
// host gets when it asks what one actor sees — the selected subtrees, the
// draft's values at instance addresses, the permissions the operation will
// itself apply, and the refusals that say nothing about what is behind them.
//
// Two of the facts are missing here on purpose, and both are missing for
// the same reason: the `Options` list a field shows and the `Validate` sentence
// a node carries each need a hook to be *registered*, and the seam that
// registers one is not exported until `CaseContext` exists. So the
// hook half of the view lives in `view_hook_test.go`, in-package, and moves here
// the day a host can bind a function.
//
// The fixture states each claim with one declaration. Manager is the actor whose
// view is a *subset* — one form in full, one form's contents only, and edit
// rights that arrive with the stage rather than with the role — because a second
// role with every grant would test nothing about filtering. `stranger` is
// edit_test.go's actor with no role at all.
//
// The two role-typed fields are not redundancy.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

const viewSrc = `
types {
    legal : enum(PERSON, COMPANY);
    money : decimal(14, 2);
}
roles {
    Applicant {
        allow view, edit, submit application;
    }
    Manager {
        allow view application;
        allow view, edit risk;
    }
}
forms {
    application {
        kind   : legal required;
        name   : string;
        amount : money;
        count  : integer;
        flag   : boolean;
        due    : date;
        sent   : datetime;
        blob   : binary;
        owner  : Applicant;
        reviewer : Manager;
        signed : signature(Applicant);
        note   : string required[.amount > 1000];

        detail present[.kind == COMPANY] {
            reg : string required;
            ref : string;
        }

        holders multiple required {
            who : string required;
            homes multiple {
                where : string;
            }
        }

        tags : string multiple;
    }

    risk {
        internal : string;
        checks multiple {
            label : string;
        }
    }
}
stages {
    draft {
        allow Applicant accept;
        accept filed;
    }

    filed {
        allow Manager edit, submit application;
    }
}
`

// manager is the subset actor. A distinct identity from `dana` matters: two
// actors of one user share one working copy, and half the claims
// below are about a draft that is not the viewer's.
var manager = monshi.Actor{User: "myra", PrimaryRole: "Manager"}

// viewCase is one case of the fixture, created by — and open to — the actor the
// test reads it as.
func viewCase(t *testing.T, actor monshi.Actor) (
	*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "view", viewSrc)
	c, err := rt.Create(ctx(), "view", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// look is a view that is expected to succeed, so every refusal in these tests is
// one the test did not ask for. The paths are printed because a refusal about a
// path is the one refusal whose cause is the argument.
func look(t *testing.T, c monshi.Case, paths ...string) monshi.ViewResult {
	t.Helper()
	res, err := c.View(ctx(), paths...)
	if err != nil {
		t.Fatalf("View(%s): %v", strings.Join(paths, ", "), err)
	}
	return res
}

// find returns the node at one address, or nil. It searches every subtree, so a
// test states which address it expects rather than which route the view chose.
func find(nodes []*monshi.ViewNode, path string) *monshi.ViewNode {
	for _, n := range nodes {
		if n.Path == path {
			return n
		}
		if got := find(n.Children, path); got != nil {
			return got
		}
	}
	return nil
}

// node is `find` for an address the view is required to contain, which is the
// assertion half of these tests: a missing node is a bug, not a nil.
func node(t *testing.T, res monshi.ViewResult, path string) *monshi.ViewNode {
	t.Helper()
	n := find(res.Roots, path)
	if n == nil {
		t.Fatalf("%q is not in the view:\n%s", path, render(res.Roots, ""))
	}
	return n
}

// absent is the same claim the other way, stated as a failure of the view rather
// than a silent nil a later field access would panic on.
func absent(t *testing.T, res monshi.ViewResult, path string) {
	t.Helper()
	if n := find(res.Roots, path); n != nil {
		t.Errorf("%q is in the view (kind %q, required %v), want it pruned",
			path, n.Kind, n.Required)
	}
}

// render prints a view's shape, for the failure messages above: a pruning bug is
// about which nodes arrived, and a list of field names cannot say so.
func render(nodes []*monshi.ViewNode, pad string) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(pad + n.Path + " [" + n.Kind + "]\n")
		b.WriteString(render(n.Children, pad+"  "))
	}
	return b.String()
}

func TestViewWithoutPathsIsTheWholeVisibleCase(t *testing.T) {
	rt, dana := viewCase(t, applicant)
	res := look(t, dana)

	// One root per top-level form the actor may see, in declaration order.
	if len(res.Roots) != 1 || res.Roots[0].Path != "application" {
		t.Fatalf("the applicant sees %s, want application alone",
			render(res.Roots, ""))
	}
	if res.Roots[0].Kind != "form" {
		t.Errorf("application is kind %q, want form", res.Roots[0].Kind)
	}
	if res.Stage != "draft" || res.Revision != 0 {
		t.Errorf("the view says stage %q at revision %d, want draft at 0",
			res.Stage, res.Revision)
	}
	// A case nobody has edited has no draft.
	if res.Working != 0 {
		t.Errorf("Working = %d for an actor who never wrote, want 0",
			res.Working)
	}

	// The same case, the second actor, both forms — and the order is the
	// definition's, not a map's.
	myra, err := rt.Open(ctx(), dana.ID(), manager)
	if err != nil {
		t.Fatalf("Open as manager: %v", err)
	}
	theirs := look(t, myra)
	if len(theirs.Roots) != 2 || theirs.Roots[0].Path != "application" ||
		theirs.Roots[1].Path != "risk" {
		t.Fatalf("the manager sees %s, want application then risk",
			render(theirs.Roots, ""))
	}
}

func TestViewSelectsSubtreesAndMergesOverlaps(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// One path, one subtree, descendants included: asking for a collection is
	// not asking for its members' names, it is asking for their nodes.
	one := look(t, dana, "application.holders")
	if len(one.Roots) != 1 || one.Roots[0].Path != "application.holders" {
		t.Fatalf("one selection = %s, want holders alone",
			render(one.Roots, ""))
	}
	if len(one.Roots[0].Children) != 0 {
		t.Errorf("holders has %d children with no rows added, want none",
			len(one.Roots[0].Children))
	}
	if got := look(t, dana, "application.detail"); len(got.Roots) != 0 {
		t.Errorf("selecting an absent node returned %s, want nothing",
			render(got.Roots, ""))
	}

	// Overlapping selections merge: the wider one renders, and the
	// narrower one adds nothing beside it. Order is the caller's, so the
	// merged root keeps the position it was named at.
	merged := look(t, dana, "application.holders", "application")
	if len(merged.Roots) != 1 || merged.Roots[0].Path != "application" {
		t.Fatalf("merged = %s, want application alone",
			render(merged.Roots, ""))
	}
	merged = look(t, dana, "application", "application.holders")
	if len(merged.Roots) != 1 || merged.Roots[0].Path != "application" {
		t.Fatalf("merged the other way = %s, want application alone",
			render(merged.Roots, ""))
	}
	// Twice is once.
	twice := look(t, dana, "application.name", "application.name")
	if len(twice.Roots) != 1 {
		t.Errorf("one path named twice selected %d roots, want 1",
			len(twice.Roots))
	}
	// Disjoint selections stay separate and keep the order they were given in
	// — which is not declaration order, and is the point.
	split := look(t, dana, "application.name", "application.holders")
	if len(split.Roots) != 2 || split.Roots[0].Path != "application.name" ||
		split.Roots[1].Path != "application.holders" {
		t.Fatalf("split = %s, want name then holders",
			render(split.Roots, ""))
	}
	// And a sibling of a selected collection is not absorbed by it: `all` and
	// the paths beneath one member are different questions. The dot rule that
	// decides it — "holders.1" does not lie under "holders.12" — is a property
	// of the address text, so `view_walk_test.go` checks it there.
	siblings := look(t, dana, "application.holders", "application.tags")
	if len(siblings.Roots) != 2 ||
		siblings.Roots[0].Path != "application.holders" ||
		siblings.Roots[1].Path != "application.tags" {
		t.Fatalf("siblings = %s, want holders then tags",
			render(siblings.Roots, ""))
	}
}

func TestViewPrunesAnAbsentSubtree(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// `detail` is gated on kind == COMPANY and kind is unset, so the guard is
	// false and the subtree is not part of the effective state —
	// including its required member, which is therefore not a gap.
	res := look(t, dana)
	absent(t, res, "application.detail")
	absent(t, res, "application.detail.reg")

	// The gate opens by writing the field it reads, and the view follows the
	// draft rather than the commit: nothing here was submitted.
	mustEdit(t, dana, 0, monshi.Set("application.kind",
		monshi.Tag("COMPANY")))
	res = look(t, dana)
	if got := node(t, res, "application.detail").Kind; got != "group" {
		t.Errorf("detail is kind %q, want group", got)
	}
	reg := node(t, res, "application.detail.reg")
	if !reg.Required {
		t.Error("detail.reg arrived but is not reported required")
	}
	if reg.Value.Kind() != monshi.KindNone {
		t.Errorf("detail.reg holds kind %q, want none: presence is not content",
			reg.Value.Kind())
	}
	if !reg.CanEdit {
		t.Error("the applicant owns application and cannot edit its member")
	}
	// The sibling that opens on the same guard is here too, and the group's
	// own node is not a leaf pretending to hold data.
	node(t, res, "application.detail.ref")
	if len(node(t, res, "application.detail").Children) != 2 {
		t.Error("an open gate should show both members")
	}
}

func TestViewReportsBlankFields(t *testing.T) {
	_, dana := viewCase(t, applicant)
	res := look(t, dana)

	// The one place this walk does not prune is an unset cell. `kind` is
	// required and empty, and a view that hid it would report what has been
	// typed rather than what is left to do.
	kind := node(t, res, "application.kind")
	if !kind.Required || kind.Value.Kind() != monshi.KindNone {
		t.Errorf("kind = required %v, value kind %q, want required and none",
			kind.Required, kind.Value.Kind())
	}
	// And a field nobody declared required is here too, with no demand on it.
	name := node(t, res, "application.name")
	if name.Required {
		t.Error("application.name is not required and is reported so")
	}
	if !name.CanEdit || !name.CanSubmit {
		t.Errorf("name is editable %v, submittable %v: the applicant granted "+
			"all three on the form", name.CanEdit, name.CanSubmit)
	}
	// A form's own node reports its structure and no content of its own: forms
	// and groups are not data.
	form := res.Roots[0]
	if form.Value.Kind() != monshi.KindNone || form.Multiple {
		t.Errorf("application = value kind %q, multiple %v, want neither",
			form.Value.Kind(), form.Multiple)
	}
	// `CanEdit` is the mask at the node it is reported on. A container's flag
	// is therefore the grant's shape rather than a promise that a path to the
	// container holds a value.
	if !form.CanEdit {
		t.Error("edit was granted on the form, so its own mask should say so")
	}
	if !form.CanSubmit {
		t.Error("submit targets top-level forms, and this one is granted")
	}
	// Every node of a form answers the submit question the same way, because
	// there is one answer and the client holding a field is asking about a
	// button.
	for _, path := range []string{"application.kind", "application.name"} {
		if got := node(t, res, path).CanSubmit; got != form.CanSubmit {
			t.Errorf("%s reports CanSubmit %v, want the form's %v",
				path, got, form.CanSubmit)
		}
	}

	// `required[cond]` is the other half, and the condition is read from
	// the same draft the values are — no demand while the amount is small,
	// and a demand the moment the draft makes it a big one. Nothing was
	// committed to get here, which is the point: a form is completed before
	// it is filed, and the client is completing it.
	note := node(t, res, "application.note")
	if note.Required {
		t.Errorf("note is required with no amount entered, want the guard to"+
			" hold it back: %+v", *note)
	}
	big := mustEdit(t, dana, 0, monshi.Set("application.amount",
		monshi.Decimal(decimal.RequireFromString("1500.00"))))
	after := node(t, look(t, dana, "application"), "application.note")
	if !after.Required {
		t.Error("a draft over the threshold did not make note required")
	}
	if after.Value.Kind() != monshi.KindNone {
		t.Errorf("the demanded note holds kind %q, want no value: asked for "+
			"is not the same as filled in", after.Value.Kind())
	}
	// And the demand belongs to this actor's draft alone: the copy's number
	// moved with the write, and the case's did not.
	own := look(t, dana)
	if own.Working != big || own.Revision != 0 {
		t.Errorf("the write left the view at %d/%d, want %d/0", own.Revision,
			own.Working, big)
	}
}

func TestViewEnforcesViewPermissionAtEveryNode(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// Nothing under an ungranted form is in the whole-case view.
	res := look(t, dana)
	absent(t, res, "risk")
	absent(t, res, "risk.internal")

	// A path named explicitly and not granted is a refusal, and it is the same
	// refusal whether the caller got the name right or wrong: an actor with no
	// grant anywhere under `risk` is not a directory service.
	for _, path := range []string{"risk", "risk.internal", "risk.nope"} {
		if _, err := dana.View(ctx(), path); !monshi.Failed(err,
			monshi.AccessDenied) {
			t.Errorf("View(%q) = %v, want ACCESS_DENIED", path, err)
		}
	}

	// The manager sees `risk` because the definition granted it, and `risk`'s
	// own node carries no more authority than the grant that named it: view
	// and edit were granted here, submit was not, so one node answers yes and
	// no to the two questions side by side.
	_, myra := viewCase(t, manager)
	res = look(t, myra, "risk")
	if len(res.Roots) != 1 || res.Roots[0].Path != "risk" {
		t.Fatalf("the manager sees %s, want risk", render(res.Roots, ""))
	}
	internal := node(t, res, "risk.internal")
	if !internal.CanEdit || internal.CanSubmit {
		t.Errorf("internal is editable %v, submittable %v: view and edit were "+
			"granted here, submit was not", internal.CanEdit,
			internal.CanSubmit)
	}
	// The same actor's other form is a *path* to content and nothing more: the
	// container is rendered because something under it is granted, and its own
	// flags are the honest "no".
	app := look(t, myra, "application").Roots[0]
	if app.CanEdit || app.CanSubmit {
		t.Errorf("application is editable %v, submittable %v: the manager has "+
			"view alone at the stage the case is at", app.CanEdit,
			app.CanSubmit)
	}
	if len(app.Children) == 0 {
		t.Fatal("a view of a form the actor may see has no children at all")
	}
	for _, kid := range app.Children {
		if kid.CanEdit {
			t.Errorf("%s reports CanEdit for an actor granted only view",
				kid.Path)
		}
	}

	// And an actor with no role sees nothing, without being told why: the empty
	// view is the answer, and there is no error to leak a cause.
	_, elliot := viewCase(t, stranger)
	if roots := look(t, elliot).Roots; len(roots) != 0 {
		t.Errorf("a roleless actor sees %s, want nothing", render(roots, ""))
	}
	if _, err := elliot.View(ctx(), "application.kind"); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("a roleless actor naming a path got %v, want ACCESS_DENIED",
			err)
	}
}

func TestViewDoesNotRevealWhatItWithheld(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// The two refusals a failed selection can earn are told apart by whether
	// case data was consulted. A name that is not in the definition is
	// NOT_FOUND for anyone, because the definition is the caller's own copy.
	// And a path whose failure depends on somebody's rows is answered with the
	// permission refusal instead, so probing instance IDs of a collection the
	// caller cannot see learns nothing — `risk.checks` is that collection. For
	// a caller who may see it, the same probe is a plain NOT_FOUND: those rows
	// are then theirs to ask about.
	for _, path := range []string{"risk.checks.1", "risk.checks.9",
		"risk.checks.1.label"} {
		if _, err := dana.View(ctx(), path); !monshi.Failed(err,
			monshi.AccessDenied) {
			t.Errorf("probing %q got %v, want ACCESS_DENIED", path, err)
		}
	}
	if _, err := dana.View(ctx(), "application.nope"); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("a mistyped path got %v, want NOT_FOUND", err)
	}
	if _, err := dana.View(ctx(), "application.holders.1"); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("an unseen instance of a visible collection got %v, want "+
			"NOT_FOUND", err)
	}
	// The manager may see `risk.checks` and is likewise told NOT_FOUND, which
	// is the difference: the answer is safe once the rows are in scope.
	_, myra := viewCase(t, manager)
	if _, err := myra.View(ctx(), "risk.checks.1"); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("a visible collection probed for a row got %v, want "+
			"NOT_FOUND", err)
	}
}

func TestViewAddressesInstancesByIds(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// The model, read back: a collection as declared holds no value and its
	// children are its instances, each addressed by the stable ID `Add` handed
	// out, and each instance node's children are the members. The IDs are read
	// out of the batch rather than guessed, because the counter belongs to the
	// case and not to the collection: a nested collection's first row is
	// numbered after every row that came before it anywhere.
	added, err := dana.Update(ctx(), 0,
		monshi.Add("application.holders"),
		monshi.Add("application.holders"),
		monshi.Add("application.tags"),
		monshi.Add("application.tags"))
	if err != nil {
		t.Fatalf("adding rows: %v", err)
	}
	first, second := added.Created[0], added.Created[1]
	tag1, tag2 := added.Created[2], added.Created[3]
	homes, err := dana.Update(ctx(), added.Working,
		monshi.Add(first+".homes"), monshi.Add(first+".homes"))
	if err != nil {
		t.Fatalf("adding homes: %v", err)
	}
	home1, home2 := homes.Created[0], homes.Created[1]
	mustEdit(t, dana, homes.Working,
		monshi.Set(first+".who", monshi.Text("Monshi Ltd")),
		monshi.Set(second+".who", monshi.Text("Dana Inc")),
		monshi.Set(home1+".where", monshi.Text("Gandhinagar")),
		monshi.Set(tag1, monshi.Text("urgent")))

	res := look(t, dana, "application.holders", "application.tags")
	holders := node(t, res, "application.holders")
	if !holders.Multiple {
		t.Error("holders is declared multiple and the view does not say so")
	}
	if holders.Value.Kind() != monshi.KindNone {
		t.Error("a collection as declared holds no value")
	}
	if len(holders.Children) != 2 {
		t.Fatalf("holders has %d instances, want 2:\n%s",
			len(holders.Children), render(holders.Children, ""))
	}
	// Row order, and the ID inside the path: the same text `Update` accepted.
	for i, want := range []struct {
		path string
		who  string
	}{{first, "Monshi Ltd"}, {second, "Dana Inc"}} {
		got := holders.Children[i]
		if got.Path != want.path {
			t.Errorf("instance %d is at %q, want %q", i, got.Path, want.path)
		}
		n := find(got.Children, want.path+".who")
		if n == nil {
			t.Fatalf("%s.who is missing from its instance:\n%s", want.path,
				render(got.Children, ""))
		}
		if n.Value.Text() != want.who {
			t.Errorf("%s.who = %q, want %q", want.path, n.Value.Text(),
				want.who)
		}
	}

	// A collection inside an instance is addressed by both IDs, outermost
	// first, and only inside the instance it belongs to: the second holder has
	// no homes at all, and its own node is there without borrowing the first
	// holder's rows.
	inner := node(t, res, first+".homes")
	if len(inner.Children) != 2 {
		t.Fatalf("%s.homes has %d instances, want 2", first,
			len(inner.Children))
	}
	where := find(inner.Children, home1+".where")
	if where == nil {
		t.Fatalf("%s.where is missing:\n%s", home1, render(inner.Children, ""))
	}
	if where.Value.Text() != "Gandhinagar" {
		t.Errorf("where = %q, want Gandhinagar", where.Value.Text())
	}
	// And the row that was added and never filled is here, empty: the instance
	// exists from the moment `Add` gave it an ID, which is the fact a client
	// needs in order to put a blank box in front of somebody.
	if find(inner.Children, home2+".where") == nil {
		t.Errorf("%s is an instance with no value and is not in the view:\n%s",
			home2, render(inner.Children, ""))
	}
	other := node(t, res, second+".homes")
	if len(other.Children) != 0 {
		t.Errorf("%s.homes has %d instances, want none of its own", second,
			len(other.Children))
	}

	// A repeated *field* is the other shape: the same slot appears once per
	// row, and the value lives on the instance node rather than the
	// declaration's. The second element was added and never set.
	tags := node(t, res, "application.tags")
	if !tags.Multiple || tags.Kind != "string" {
		t.Errorf("tags = kind %q, multiple %v, want string and true",
			tags.Kind, tags.Multiple)
	}
	if tags.Value.Kind() != monshi.KindNone {
		t.Error("the declaration node of a repeated field holds no value")
	}
	if len(tags.Children) != 2 {
		t.Fatalf("tags has %d instances, want 2: an element with no value is "+
			"still an element", len(tags.Children))
	}
	// The instance nodes are addressed by the IDs the batch handed back, and
	// requiredness is the declaration's: an instance is asked for what its
	// field was declared to ask for, no more and no less.
	for i, want := range []struct {
		path string
		text string
	}{{tag1, "urgent"}, {tag2, ""}} {
		got := tags.Children[i]
		if got.Path != want.path {
			t.Errorf("instance %d is at %q, want %q", i, got.Path, want.path)
		}
		if got.Required {
			t.Errorf("%s is reported required; tags is not declared so",
				got.Path)
		}
		if got.Value.Text() != want.text {
			t.Errorf("%s = %q, want %q", got.Path, got.Value.Text(),
				want.text)
		}
	}
}

func TestViewReportsARequiredEmptyCollection(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// Pruning it would hide the one thing the actor has to do.
	holders := node(t, look(t, dana), "application.holders")
	if !holders.Required || len(holders.Children) != 0 {
		t.Errorf("holders = required %v with %d instances, want required and "+
			"empty", holders.Required, len(holders.Children))
	}
	// The optional collection beside it says the same thing about itself, and
	// the difference between the two is `Required` alone.
	tags := node(t, look(t, dana), "application.tags")
	if tags.Required || len(tags.Children) != 0 {
		t.Errorf("tags = required %v with %d instances, want neither",
			tags.Required, len(tags.Children))
	}
}

func TestViewReadsTheDraftAndLeavesTheCommitAlone(t *testing.T) {
	rt, dana := viewCase(t, applicant)

	// A draft is per actor, so this claim needs three views and two handles:
	// dana's before the write, dana's after it, and a second actor's of the
	// same case throughout. The committed number never moves, because nothing
	// here submitted.
	before := look(t, dana)
	if before.Revision != 0 || before.Working != 0 {
		t.Fatalf("a new case views as %d/%d, want 0/0", before.Revision,
			before.Working)
	}
	base := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("Monshi Ltd")))

	after := look(t, dana)
	if after.Revision != 0 {
		t.Errorf("an edit left the case at revision %d, want 0",
			after.Revision)
	}
	if after.Working != base {
		t.Errorf("Working = %d, want the copy's %d", after.Working, base)
	}
	if got := node(t, after, "application.name").Value.Text(); got !=
		"Monshi Ltd" {
		t.Errorf("the view of a draft says %q, want what was typed", got)
	}

	// The same actor on a second handle sees the same copy.
	again, err := rt.Open(ctx(), dana.ID(), applicant)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := node(t, look(t, again), "application.name").Value.Text(); got !=
		"Monshi Ltd" {
		t.Errorf("a second session of one actor sees %q", got)
	}

	// Another actor's view of the same case sees the committed content, which
	// is nothing: a draft is not a preview.
	myra, err := rt.Open(ctx(), dana.ID(), manager)
	if err != nil {
		t.Fatalf("Open as manager: %v", err)
	}
	theirs := look(t, myra, "application.name")
	if theirs.Working != 0 {
		t.Errorf("the manager's Working = %d, want 0 for a copy not made",
			theirs.Working)
	}
	if got := node(t, theirs, "application.name").Value.Kind(); got !=
		monshi.KindNone {
		t.Errorf("the manager sees dana's draft (%q), want no value", got)
	}
}

func TestViewCreatesNoWorkingCopy(t *testing.T) {
	_, dana := viewCase(t, applicant)

	// A read is not an edit. The claim is not that nothing is allocated — it
	// is that the actor still has no copy, which is observable: the guard
	// accepts `base` 0 only for a copy that has never been written, so a view
	// that silently made one would make the first edit stale on its face.
	look(t, dana)
	look(t, dana, "application.holders")
	base := mustEdit(t, dana, 0,
		monshi.Set("application.name", monshi.Text("still my first write")))
	if base != 1 {
		t.Errorf("the first batch left the copy at %d, want 1", base)
	}
}

func TestViewPermissionsComeFromTheCommittedStage(t *testing.T) {
	rt, dana := viewCase(t, applicant)
	myra, err := rt.Open(ctx(), dana.ID(), manager)
	if err != nil {
		t.Fatalf("Open as manager: %v", err)
	}

	// `filed` is where the manager is granted edit and submit on the form, so
	// one node answers two different questions at two stages and the role
	// alone decides neither.
	name := node(t, look(t, myra, "application"), "application.name")
	if name.CanEdit || name.CanSubmit {
		t.Fatalf("at draft the manager edits %v and submits %v, want neither",
			name.CanEdit, name.CanSubmit)
	}

	// Complete the case as its author, commit it, and move the stage.
	base := mustEdit(t, dana, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Monshi Ltd")))
	committed, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v (%s)", err, render(look(t, dana).Roots, ""))
	}
	moved, err := dana.Transition(ctx(), "filed", committed.Revision)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if moved.Stage != "filed" {
		t.Fatalf("the case is at %q, want filed", moved.Stage)
	}

	// The stage moved, so the manager's masks moved with it — with no value
	// having changed. The view is where a client finds out without asking.
	after := node(t, look(t, myra, "application"), "application.name")
	if !after.CanEdit || !after.CanSubmit {
		t.Errorf("at %q the manager edits %v and submits %v, want both",
			moved.Stage, after.CanEdit, after.CanSubmit)
	}

	// The two numbers the same view reports belong to two different things,
	// and only one of them moved: the case advanced when its author
	// committed, and this actor still has no copy at all. A view that
	// answered `Working` with the case's revision would tell the manager to
	// present a base they were never handed, and the guard would refuse
	// their first edit as stale — which is why 0 here is a fact about the
	// copy and not a placeholder.
	theirs := look(t, myra)
	if theirs.Revision != moved.Revision {
		t.Errorf("the manager's view reports case %d, want the committed %d",
			theirs.Revision, moved.Revision)
	}
	if theirs.Working != 0 {
		t.Errorf("the manager's Working = %d for a copy never made, want 0",
			theirs.Working)
	}

	// And the applicant's own view of the same field is unchanged: an actor's
	// permissions move only where the definition staged them.
	own := node(t, look(t, dana, "application"), "application.name")
	if !own.CanEdit {
		t.Error("the applicant lost edit when the stage moved")
	}
	// The view says which stage it answered at, because the same nodes mean
	// something else at the next one.
	if got := look(t, myra).Stage; got != "filed" {
		t.Errorf("the view reports stage %q, want filed", got)
	}
}

func TestViewGrantsNoRoleThatIsNotCommitted(t *testing.T) {
	rt, dana := viewCase(t, applicant)

	// A role-typed field is how a case hands out a role. The view is where that
	// rule could be read two ways: values come from the draft, so a careless
	// fold would take the roles from the draft too, and an applicant who writes
	// their own user into `reviewer` would see the risk form before anyone
	// committed the grant. `risk` is the only thing the Manager role sees that
	// the Applicant role does not, so one node answers the whole question.
	base := mustEdit(t, dana, 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.owner", monshi.User("dana")),
		monshi.Set("application.reviewer", monshi.User("dana")),
		monshi.Add("application.holders"),
		monshi.Set("application.holders.1.who", monshi.Text("Monshi Ltd")))

	// Dana is now a manager in her own draft, and the view she gets is the one
	// she had before the edit. That closes the self-grant a client would
	// otherwise be shown as already true: the row says the value is hers, the
	// permissions beside it say it has not taken effect.
	if _, err := dana.View(ctx(), "risk"); !monshi.Failed(err,
		monshi.AccessDenied) {
		t.Errorf("a pending grant widened its author's view: %v", err)
	}

	// The same edit, committed, does confer it: the grant was the draft's and
	// the permission is the commit's, and this is the one line between them.
	// A reopen is what proves it is the *stored* state being folded and not
	// the handle remembering the edit — the mask is derived, never cached.
	_, err := dana.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v (%s)", err, render(look(t, dana).Roots, ""))
	}
	fresh, err := rt.Open(ctx(), dana.ID(), applicant)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	seen := look(t, fresh, "risk")
	if len(seen.Roots) != 1 || seen.Roots[0].Path != "risk" {
		t.Errorf("after the commit dana sees %s, want risk",
			render(seen.Roots, ""))
	}

	rv := node(t, look(t, dana, "application"), "application.reviewer")
	if rv.Kind != "role" {
		t.Errorf("reviewer is kind %q, want role", rv.Kind)
	}
	if got := rv.Value.User(); got != "dana" {
		t.Errorf("reviewer holds user %q, want dana", got)
	}
}

func TestValueReadersAnswerOnlyTheirOwnClass(t *testing.T) {
	_, dana := viewCase(t, applicant)
	sent := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	mustEdit(t, dana, 0,
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.name", monshi.Text("Monshi Ltd")),
		monshi.Set("application.amount",
			monshi.Decimal(decimal.RequireFromString("12.50"))),
		monshi.Set("application.count", monshi.Int(42)),
		monshi.Set("application.flag", monshi.Bool(false)),
		monshi.Set("application.due", monshi.Date(due)),
		monshi.Set("application.sent", monshi.DateTime(sent)),
		monshi.Set("application.blob",
			monshi.Binary(monshi.BinaryRef("sha256:1f"))),
		monshi.Set("application.owner", monshi.User("dana")))
	res := look(t, dana)

	// One table, three claims per row: the getter the constructor wrote reads
	// back the value, the word `Kind` answers with is one of the closed set the
	// boundary dispatches on, and a getter for a different class answers its
	// zero rather than guessing.
	cases := []struct {
		path string
		kind monshi.ValueKind
		want string
		got  func(*monshi.ViewNode) string
	}{
		{"application.kind", monshi.KindTag, "COMPANY",
			func(n *monshi.ViewNode) string { return n.Value.Tag() }},
		{"application.name", monshi.KindText, "Monshi Ltd",
			func(n *monshi.ViewNode) string { return n.Value.Text() }},
		{"application.amount", monshi.KindDecimal, "12.5",
			func(n *monshi.ViewNode) string {
				return n.Value.Decimal().String()
			}},
		{"application.count", monshi.KindInteger, "42",
			func(n *monshi.ViewNode) string {
				return strconv.FormatInt(n.Value.Int(), 10)
			}},
		{"application.flag", monshi.KindBoolean, "false",
			func(n *monshi.ViewNode) string {
				return strconv.FormatBool(n.Value.Bool())
			}},
		{"application.due", monshi.KindDate, "2026-12-01",
			func(n *monshi.ViewNode) string {
				return n.Value.Date().Format("2006-01-02")
			}},
		{"application.sent", monshi.KindDatetime, "2026-11-23T09:30:00Z",
			func(n *monshi.ViewNode) string {
				return n.Value.DateTime().Format(time.RFC3339)
			}},
		{"application.blob", monshi.KindBinary, "sha256:1f",
			func(n *monshi.ViewNode) string {
				return string(n.Value.Binary())
			}},
		{"application.owner", monshi.KindUser, "dana",
			func(n *monshi.ViewNode) string {
				return string(n.Value.User())
			}},
	}
	for _, tc := range cases {
		n := node(t, res, tc.path)
		if got := n.Value.Kind(); got != tc.kind {
			t.Errorf("%s is kind %q, want %q", tc.path, got, tc.kind)
		}
		if got := tc.got(n); got != tc.want {
			t.Errorf("%s reads back %q, want %q", tc.path, got, tc.want)
		}
		// The wrong getter is not a failure, and not a guess: it is the zero.
		if got := n.Value.Int(); got != 0 && tc.kind != monshi.KindInteger {
			t.Errorf("%s answered Int %d from a %s value", tc.path, got,
				tc.kind)
		}
		if got := n.Value.Text(); got != "" && tc.kind != monshi.KindText {
			t.Errorf("%s answered Text %q from a %s value", tc.path, got,
				tc.kind)
		}
	}
	// A stored `false` is a value and not an absence, which is the one place a
	// zero-returning getter would lose information — so the test for content is
	// `Kind`, and `Kind` says content is here.
	flag := node(t, res, "application.flag")
	if flag.Value.Kind() != monshi.KindBoolean || flag.Value.Bool() {
		t.Errorf("a stored false reads as kind %q, value %v",
			flag.Value.Kind(), flag.Value.Bool())
	}
	// The unset cell is the one `Empty` leaves behind too, so every
	// reader has to answer its zero rather than guess: the test for content is
	// `Kind`, and `Kind` says there is none.
	blank := node(t, res, "application.detail.ref")
	if blank.Value.Kind() != monshi.KindNone {
		t.Fatalf("an unset cell is kind %q, want none", blank.Value.Kind())
	}
	if blank.Value.Text() != "" || blank.Value.Int() != 0 ||
		blank.Value.Bool() || blank.Value.Tag() != "" ||
		!blank.Value.Date().IsZero() || !blank.Value.DateTime().IsZero() ||
		blank.Value.User() != "" || blank.Value.Binary() != nil ||
		!blank.Value.Decimal().IsZero() {
		t.Errorf("a value with no class answered something: %+v", blank)
	}
}

func TestViewNamesTheKindTheDeclarationUsed(t *testing.T) {
	_, dana := viewCase(t, applicant)
	res := look(t, dana)

	kinds := map[string]string{
		"application":         "form",
		"application.kind":    "enum",
		"application.amount":  "decimal",
		"application.blob":    "binary",
		"application.owner":   "role",
		"application.signed":  "signature",
		"application.holders": "group",
		"application.tags":    "string",
		"application.detail":  "group",
	}
	for path, want := range kinds {
		if path == "application.detail" {
			// The gated group is absent with its guard false, and the point
			// about kinds is not a licence to render a pruned node.
			absent(t, res, path)
			continue
		}
		if got := node(t, res, path).Kind; got != want {
			t.Errorf("%s is declared %q, want %q", path, got, want)
		}
	}
	// A custom type reads as its base class, because the name the definition
	// gave it is documentation the engine holds in its own table and the row
	// keeps only what the value is. A host that needs the declared name for a
	// label has it in its own source; the view's job is what to render.
	if got := node(t, res, "application.amount").Kind; got != "decimal" {
		t.Errorf("money reads as %q, want decimal", got)
	}
}
