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
