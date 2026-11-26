package monshi_test

// The snapshot a host function is handed, seen from
// outside the module: what a hook is told about the case it was called for,
// what it may read, and which words its signature carries.
//
// These tests exercise case reads from inside hooks, including late
// registration, actor-specific visibility, and reads from a retained context.
//
// What is asserted in-package instead is the plumbing: that the view's option
// fetch is still one fetch per binding and not one per row, and that an
// unbound assignment costs the map lookup and nothing else.
//
// The fixture is one case, three hands, and one custom type used at three
// addresses. `Clerk` is the actor that makes the masking test mean something:
// it may see one field and one member of a row, which is the shape that tells
// a permission rule from a reachability rule. Its granted member's *sibling*
// sits under a group the clerk can render, so a rule that asked visibility of
// the ancestor would hand a host function the value its caller was refused.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

const ctxSrc = `
types {
    legal : enum(PERSON, COMPANY);
    money : decimal(14,2);
    name  : string;
}
roles {
    Applicant {
        allow view, edit application, ledger;
        allow submit application, ledger;
        allow sign application.submitted;
    }
    Clerk {
        allow view application.fee, application.holders.who;
        allow edit application.fee, application.title;
    }
    Registrar {
        allow view all;
        allow review application;
    }
}
forms {
    application verifiable {
        title      : string;
        owner_name : name;
        kind       : legal required;
        fee   : money;
        note  : options;

        holders multiple {
            who  : string;
            paid : money;
            pick : options;
        }

        submitted : signature(Applicant);
    }

    ledger {
        total : money;
    }
}
stages {
    draft {
        allow Registrar accept;
        accept filed;
    }
    filed {}
}
`

var (
	ctxOwner = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}
	ctxClerk = monshi.Actor{User: "bo", PrimaryRole: "Clerk"}
	ctxRuler = monshi.Actor{User: "elliot", PrimaryRole: "Registrar"}
)

// cents is a value of the fixture's custom `money` type, which is the type
// every hook in this file is bound to.
func cents(text string) monshi.Value {
	return monshi.Decimal(decimal.RequireFromString(text))
}

// ctxCase is a runtime with the fixture loaded and one case at revision 0.
// Registration order is the point of three of these tests, so the helper
// hands back the runtime: the hooks arrive after the case exists.
func ctxCase(t *testing.T, actor monshi.Actor) (*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "ctx.app", ctxSrc)
	c, err := rt.Create(ctx(), "ctx.app", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// ctxFilled is a case whose application was committed: the form's own fee, a
// row with both of its money-bearing fields, and a field of a second form, so
// a later read has a value at every address these tests name. The row's
// address comes back because it is a number the engine chose, and the claims
// about a member of a row are the ones that cannot be made without it.
func ctxFilled(t *testing.T) (
	*monshi.Runtime, monshi.Case, string, uint64) {
	t.Helper()
	rt, c := ctxCase(t, ctxOwner)
	res, err := c.Update(ctx(), 0,
		monshi.Set("application.title", monshi.Text("Holding")),
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.fee", cents("20.00")))
	if err != nil {
		t.Fatalf("filling the form: %v", err)
	}
	res, err = c.Update(ctx(), res.Working, monshi.Add("application.holders"))
	if err != nil {
		t.Fatalf("adding a row: %v", err)
	}
	row := res.Created[0]
	res, err = c.Update(ctx(), res.Working,
		monshi.Set(row+".who", monshi.Text("Ada")),
		monshi.Set(row+".paid", cents("7.50")),
		monshi.Set("ledger.total", cents("100.00")))
	if err != nil {
		t.Fatalf("filling the row: %v", err)
	}
	if _, err := c.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// The second form is published as well. A probe that expects a reader to
	// see `ledger.total` is a probe about committed content, and a value that
	// only ever lived in one actor's draft answers absence for everyone else
	// — true, but not the fact this file's tables are about.
	if _, err := c.Submit(ctx(), "ledger", res.Working); err != nil {
		t.Fatalf("Submit ledger: %v", err)
	}
	// Publishing the copy leaves it where it stood, so the number a later
	// batch presents is the one the last batch got and not the case's new
	// head: the two numbers, kept apart here because four of these tests
	// write after the submit.
	return rt, c, row, res.Working
}

// ctxSilent registers the providers a walk over this fixture reaches, so a
// test that is not about absence does not spend its assertions on a dropdown
// nobody filled in. It is the rule that a missing provider is a failure
// standing behind the convenience, and the test for that rule is the one that
// registers nothing.
func ctxSilent(rt *monshi.Runtime) {
	rt.RegisterOptions("application.note", func(_ context.Context,
		_ monshi.CaseContext, _ string) ([]string, error) {
		return []string{"red", "blue"}, nil
	})
	rt.RegisterOptions("application.holders.pick", func(_ context.Context,
		_ monshi.CaseContext, _ string) ([]string, error) {
		return []string{"left", "right"}, nil
	})
}

func TestARegisteredHookReachesAHandleOpenedBeforeIt(t *testing.T) {
	// The registry belongs to the runtime and not to a case or a handle, so a
	// host that wires its hooks after it created a case is not editing past the
	// handles it already holds. The one contract per deployment is what this
	// buys: register once, and every case the server has ever opened speaks
	// through the same function.
	rt, c := ctxCase(t, ctxOwner)
	var calls int
	name := func(
		_ context.Context, _ monshi.CaseContext, _ string,
		v monshi.Value) (monshi.Value, error) {
		calls++
		return monshi.Text(strings.Title(v.Text())), nil
	}
	rt.RegisterValidate("name", name)

	if _, err := c.Update(ctx(), 0, monshi.Set("application.owner_name",
		monshi.Text("ada"))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the hook created after the case was called %v times, want"+
			" 1", calls)
	}
	res, err := c.View(ctx(), "application.owner_name")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := res.Roots[0].Value.Text(); got != "Ada" {
		t.Errorf("stored value = %q, want the hook's Ada", got)
	}

	// Replacing the binding is picked up by the same handle, which is the
	// other half of a registry a host can fix without a new runtime: the
	// lookup goes through the runtime's maps on every call rather than
	// through a table a handle copied when it was opened.
	rt.RegisterValidate("name", func(_ context.Context,
		_ monshi.CaseContext, _ string,
		v monshi.Value) (monshi.Value, error) {
		calls++
		return monshi.Text(strings.ToUpper(v.Text())), nil
	})
	if _, err := c.Update(ctx(), res.Working, monshi.Set(
		"application.owner_name", monshi.Text("ben"))); err != nil {
		t.Fatalf("Update after the replacement: %v", err)
	}
	if calls != 3 {
		t.Errorf("the replacement was called %v more times, want 2 (one "+
			"write, one re-ask)", calls-1)
	}
	res, err = c.View(ctx(), "application.owner_name")
	if err != nil {
		t.Fatalf("View after the replacement: %v", err)
	}
	if got := res.Roots[0].Value.Text(); got != "BEN" {
		t.Errorf("stored value = %q, want the new hook's BEN", got)
	}
}

func TestAValidateHookIsHandedTheAddressItsValueIsAt(t *testing.T) {
	// One binding, three addresses: a form's own field, a member of a row, and
	// a field of a different form.
	rt, c, row, working := ctxFilled(t)
	var asked []string
	var atWork []uint64
	rt.RegisterValidate("money", func(_ context.Context,
		at monshi.CaseContext, path string,
		v monshi.Value) (monshi.Value, error) {
		asked = append(asked, path)
		atWork = append(atWork, at.Working())
		if _, ok := at.Value(path); !ok {
			return monshi.Value{}, fmt.Errorf(
				"the snapshot does not answer for %s", path)
		}
		return v, nil
	})

	res, err := c.Update(ctx(), working,
		monshi.Set("application.fee", cents("3")),
		monshi.Set(row+".paid", cents("4")),
		monshi.Set("ledger.total", cents("5")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	want := []string{"application.fee", row + ".paid", "ledger.total"}
	if !reflect.DeepEqual(asked, want) {
		t.Fatalf("the binding was asked about %v, want %v", asked, want)
	}

	// `Working` is the revision the batch started at, three times over: this
	// case's copy had already moved, and a hook that read the *next* number
	// would be reading a fact the store has not agreed to yet. It is the one
	// field of the snapshot whose value a host is likely to write back into
	// `Update`, so the number it sees is the number it presented.
	for _, w := range atWork {
		if w != working {
			t.Errorf("the hook reported working revision %v, want the %v it"+
				" was called under", w, working)
		}
	}

	// The address round-trips: what the hook was handed goes back into
	// `Update` and reaches the same cell, which is the difference between an
	// address and a description.
	if _, err := c.Update(ctx(), res.Working, monshi.Set(asked[1],
		cents("6"))); err != nil {
		t.Errorf("the address %q does not reach the cell it came from: %v",
			asked[1], err)
	}
}

// ctxRead is one path a probing hook asks about, with the answer each of the
// two masked actors should get and the fact that decides it. Listing them as
// data is what keeps the test honest about *why* an answer is what it is: a
// table that cannot state the reason is a table that can pass for the wrong
// one.
type ctxRead struct {
	path  string
	clerk bool
	ruler bool
	why   string
}

// ctxProbe binds the fixture's `money` type to a normalizer that changes
// nothing and asks the snapshot about every path in the table, then runs one
// whole-case view as `actor`. The view is what calls the hook: the re-ask
// walks the stored values this actor may see, so a path that answers here is
// one a host rule was really told about.
func ctxProbe(t *testing.T, rt *monshi.Runtime, actor monshi.Actor,
	id monshi.CaseID, table []ctxRead) map[string]bool {
	t.Helper()
	ctxSilent(rt)
	got := map[string]bool{}
	rt.RegisterValidate("money", func(_ context.Context,
		at monshi.CaseContext, _ string,
		v monshi.Value) (monshi.Value, error) {
		for _, r := range table {
			if _, ok := at.Value(r.path); ok {
				got[r.path] = true
			}
		}
		return v, nil
	})
	c, err := rt.Open(ctx(), id, actor)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := c.View(ctx()); err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the whole-case view asked the money binding nothing")
	}
	return got
}

func TestAContextCannotReadWhatTheCallerMayNotSee(t *testing.T) {
	// The rule about a hook's *output* — a host rule is not entitled to run
	// over content the caller could not render — asked of a hook's input. A
	// context that answered an unfiltered path would be a disclosure channel
	// wearing a host's function, and a wider one than the trail ever was,
	// because it takes arbitrary text.
	rt, c, row, _ := ctxFilled(t)
	table := []ctxRead{
		{"application.fee", true, true, "granted to the clerk by name"},
		{row + ".who", true, true, "granted as one member of a row"},
		{row + ".paid", false, true, "the sibling of that member"},
		{"application.kind", false, true, "no grant reaches it"},
		{"application.holders", false, false,
			"a container, so no value answers at all"},
		{"ledger.total", false, true, "a form the clerk was never in"},
		{"application.nowhere", false, false,
			"a path no declaration is reached by"},
		{"Application.fee", false, false,
			"a path spelled with the wrong case"},
		{"application.note", false, false,
			"a cell the case has no value in"},
		{"application.title", false, true,
			"granted to the clerk to write, and not to read"},
	}

	clerk := ctxProbe(t, rt, ctxClerk, c.ID(), table)
	for _, r := range table {
		if clerk[r.path] != r.clerk {
			t.Errorf("the clerk's hook read %s = %v, want %v: %s", r.path,
				clerk[r.path], r.clerk, r.why)
		}
	}

	// The sibling is the whole point. A rule that asked permission of the row's
	// *group* — which this clerk may render.
	if clerk[row+".paid"] {
		t.Fatal("a grant on one member of a row let a hook read another")
	}

	// The same hook, the same paths, one actor with a grant for all of the
	// case: what filtered the first answer was the caller and not the
	// machinery, and nothing in the path itself decided. The two containers
	// and the mistyped path are the only refusals left, and they are refusals
	// about *shape*, which is why the table expects them for both actors.
	ruler := ctxProbe(t, rt, ctxRuler, c.ID(), table)
	for _, r := range table {
		if ruler[r.path] != r.ruler {
			t.Errorf("the registrar's hook read %s = %v, want %v: %s",
				r.path, ruler[r.path], r.ruler, r.why)
		}
	}
}

func TestAReadAnswersTheSameInAnEditAndInAView(t *testing.T) {
	// A view carries a folded visibility array because it is about to render
	// every node; an edit has no reason to compute one, so its snapshot asks
	// the same question by walking the node's own range. The two are not
	// allowed to disagree, because a host's rule would otherwise answer one
	// way when the actor writes a value and another when it shows one, and
	// the second half of that is a disclosure the first half refuses.
	//
	// The verdict reads are what make this a test rather than a tautology. A
	// *value* cannot tell the two rules apart, because the inheritance
	// runs downwards only and a leaf's reachability is its own bit. A
	// container's decision can: `application` is a container this clerk
	// renders only through the field granted inside it, and the unit's
	// verdict rides on that node in the view, so a hook reading it has to be
	// told the same thing whichever way the read was reached.
	rt, c, row, _ := ctxFilled(t)
	ctxSilent(rt)
	asked := []string{"application.fee", row + ".who", row + ".paid",
		"application.kind", "application.title", "application",
		"application.submitted", "application.note", "ledger.total",
		"application.nowhere"}
	record := func(into map[string]string) monshi.ValidateFunc {
		return func(_ context.Context, at monshi.CaseContext,
			path string, v monshi.Value) (monshi.Value, error) {
			if path != "application.fee" {
				return v, nil
			}
			for _, p := range asked {
				got := "nothing"
				if _, ok := at.Value(p); ok {
					got = "value"
				}
				if rv, ok := at.Review(p); ok {
					got += "/review " + string(rv.State)
				}
				if sg, ok := at.Signature(p); ok {
					got += "/hand " + string(sg.By)
				}
				into[p] = got
			}
			return v, nil
		}
	}

	fromEdit, fromView := map[string]string{}, map[string]string{}
	clerk, err := rt.Open(ctx(), c.ID(), ctxClerk)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rt.RegisterValidate("money", record(fromEdit))
	if _, err := clerk.Update(ctx(), 0, monshi.Set("application.fee",
		cents("9")), monshi.Set("application.title",
		monshi.Text("Renamed"))); err != nil {
		t.Fatalf("the clerk edits the two fields it may edit: %v", err)
	}
	if len(fromEdit) != len(asked) {
		t.Fatalf("the edit answered %v probes, want %v", len(fromEdit),
			len(asked))
	}

	// The second read is the same actor's whole-case view: the fold is built
	// there and the range is scanned here, so any difference between the two
	// maps is a difference in the rule rather than in the data.
	rt.RegisterValidate("money", record(fromView))
	if _, err := clerk.View(ctx()); err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(fromView) != len(asked) {
		t.Fatalf("the view answered %v probes, want %v", len(fromView),
			len(asked))
	}
	for p, got := range fromView {
		if fromEdit[p] != got {
			t.Errorf("%s answered %q in an edit and %q in a view", p,
				fromEdit[p], got)
		}
	}

	// And four named facts, so the agreement cannot be an agreement between
	// two ways of answering nothing at all. The fee is the clerk's own field
	// and carries the unit's state beside its value, exactly as the rendered
	// node does; the sibling of a granted member and a field of the other
	// form answer nothing at all, in either reading.
	for path, want := range map[string]string{
		"application.fee":       "value/review PENDING",
		"application.kind":      "nothing",
		"application.title":     "nothing",
		row + ".paid":           "nothing",
		"application.submitted": "nothing",
		"ledger.total":          "nothing",
	} {
		if got := fromEdit[path]; got != want {
			t.Errorf("an edit read %s as %q, want %q", path, got, want)
		}
	}
	if got := fromEdit["application"]; !strings.HasPrefix(got,
		"nothing/review ") {
		t.Errorf("the unit's verdict on a container the clerk reaches through"+
			" a member reads %q, want the decision the view also shows", got)
	}
}

// ctxVerdict is what a hook was told about one path: the decision standing
// over the unit that governs it, and the attestation on the field itself.
type ctxVerdict struct {
	unit  bool
	state monshi.ReviewState
	hand  bool
	by    monshi.UserID
}

func TestAContextReportsTheVerdictsItsCallerWasShown(t *testing.T) {
	// A decision and an attestation are facts about the case, so a hook that
	// reads them must read what the caller's own view rendered, at the
	// caller's permission. This is one step further out from a hole than the
	// value rule: the verdict a *unit* carries shows on every node of it the
	// caller may see, so a grant on one field entitles a host rule to the
	// unit's decision and to nothing else in the subtree.
	rt, c, row, _ := ctxFilled(t)
	ctxSilent(rt)
	owner, err := rt.Open(ctx(), c.ID(), ctxOwner)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ruler, err := rt.Open(ctx(), c.ID(), ctxRuler)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rev, err := ruler.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if _, err := ruler.Review(ctx(), "application", monshi.ReviewApprove,
		rev, ""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// An attestation is made against the head the decision moved, which is
	// the reason the hand and the verdict cannot be stamped with one
	// number: the hand attests to content, and the verdict is about it.
	after, err := owner.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if _, err := owner.Sign(ctx(), "application.submitted", after,
		"envelope-7"); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	var (
		mu     sync.Mutex
		byPath = map[string]ctxVerdict{}
		probed = []string{"application", "application.fee", row + ".who",
			"application.submitted", "application.title", "ledger",
			"ledger.total", "application.nowhere"}
	)
	rt.RegisterValidate("money", func(_ context.Context,
		at monshi.CaseContext, _ string,
		v monshi.Value) (monshi.Value, error) {
		mu.Lock()
		defer mu.Unlock()
		// One entry per path, overwritten by the last call of the walk: every
		// call reads the same snapshot, so the last answer is the walk's, and
		// a hook that answered differently between two cells of one view is a
		// bug this line would show as a difference.
		for _, p := range probed {
			rv, okUnit := at.Review(p)
			sg, okHand := at.Signature(p)
			byPath[p] = ctxVerdict{unit: okUnit, state: rv.State,
				hand: okHand, by: sg.By}
		}
		return v, nil
	})

	check := func(actor monshi.Actor, want map[string]ctxVerdict) {
		t.Helper()
		mu.Lock()
		byPath = map[string]ctxVerdict{}
		mu.Unlock()
		as, err := rt.Open(ctx(), c.ID(), actor)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := as.View(ctx()); err != nil {
			t.Fatalf("View: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(byPath) != len(probed) {
			t.Fatalf("%s: the probe answered %v paths, want %v",
				actor.User, len(byPath), len(probed))
		}
		for p, w := range want {
			if got := byPath[p]; got != w {
				t.Errorf("%s was told %+v about %s, want %+v", actor.User,
					got, p, w)
			}
		}
	}

	approved := ctxVerdict{unit: true, state: monshi.ReviewApproved}
	// The registrar may see the whole case, so it is told what its own view
	// renders: the application's approval on every node of that unit, the
	// applicant's hand on the signature field, and nothing where no
	// `verifiable` declaration governs — `ledger` is a plain form, so a
	// verdict asked there is answered with absence and not with PENDING,
	// which is the rule that a unit has a state and a non-unit has none.
	check(ctxRuler, map[string]ctxVerdict{
		"application":       approved,
		"application.fee":   approved,
		row + ".who":        approved,
		"application.title": approved,
		"application.submitted": {unit: true, state: monshi.ReviewApproved,
			hand: true, by: "dana"},
		"ledger":              {},
		"ledger.total":        {},
		"application.nowhere": {},
	})

	// The clerk holds one field of the unit and one member of a row. Its view
	// renders the form node with the unit's decision beside it, so a hook
	// reading that path is told the same thing. A path the clerk may not
	// render.
	check(ctxClerk, map[string]ctxVerdict{
		"application":           approved,
		"application.fee":       approved,
		row + ".who":            approved,
		"application.submitted": {},
		"application.title":     {},
		"ledger":                {},
		"ledger.total":          {},
		"application.nowhere":   {},
	})
}

func TestAContextIsTheSnapshotItsCallWasCutFrom(t *testing.T) {
	// The sentence this test exists for: "no hook result may be cached across a
	// commit: the context it saw is the snapshot, not a lease". The store is
	// what makes it true rather than the doc — a working copy is cloned on the
	// way out and the candidate is cloned on the way in — so a context retained
	// past its call reads a *finished* state, and a goroutine reading it is
	// reading bytes nobody writes.
	rt, c := ctxCase(t, ctxOwner)
	var (
		mu   sync.Mutex
		kept monshi.CaseContext
		held bool
	)
	rt.RegisterValidate("money", func(_ context.Context,
		at monshi.CaseContext, path string,
		v monshi.Value) (monshi.Value, error) {
		if path != "application.fee" {
			return v, nil
		}
		mu.Lock()
		// Once only: a later batch calls this same hook, and a retained
		// context that kept being replaced would prove nothing about
		// retention. What is kept is the first call's snapshot, and every
		// read below is asked of it after two more operations moved the case.
		if !held {
			kept, held = at, true
		}
		mu.Unlock()
		return v, nil
	})

	res, err := c.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.fee", cents("3")),
		monshi.Set("application.title", monshi.Text("First")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !held {
		t.Fatal("nothing was retained")
	}

	// The identity of the call, and the numbers it was cut at. `Working` is the
	// revision the batch started from.
	if kept.Case() != c.ID() {
		t.Errorf("Case() = %v, want %v", kept.Case(), c.ID())
	}
	if kept.Definition() != "ctx.app" {
		t.Errorf("Definition() = %q, want ctx.app", kept.Definition())
	}
	if kept.Actor() != ctxOwner {
		t.Errorf("Actor() = %+v, want the actor the handle was opened as",
			kept.Actor())
	}
	if kept.Stage() != "draft" {
		t.Errorf("Stage() = %q, want draft", kept.Stage())
	}
	if kept.Revision() != 0 || kept.Working() != 0 {
		t.Errorf("Revision,Working = %v,%v, want 0,0", kept.Revision(),
			kept.Working())
	}
	if got := kept.Roles(); !reflect.DeepEqual(got, []string{"Applicant"}) {
		t.Errorf("Roles() = %v, want the primary role alone", got)
	}

	// The batch's own candidate is what the snapshot reads, including the write
	// that landed *after* this hook returned.
	if v, ok := kept.Value("application.title"); !ok ||
		v.Text() != "First" {
		t.Errorf("the later write of the same batch reads %q, %v",
			v.Text(), ok)
	}

	// A second batch is a different state. The store kept its own copy of the
	// first candidate and clones again for the next, so nothing the retained
	// snapshot answers has moved: not the value the hook was called about, and
	// not the one its own batch wrote beside it.
	if _, err := c.Update(ctx(), res.Working, monshi.Set("application.fee",
		cents("8")), monshi.Set("application.title",
		monshi.Text("Second"))); err != nil {
		t.Fatalf("the second batch: %v", err)
	}
	if v, _ := kept.Value("application.fee"); !v.Decimal().Equal(
		decimal.RequireFromString("3")) {
		t.Errorf("the retained snapshot reads fee %s, want the 3 it saw",
			v.Decimal())
	}
	if v, ok := kept.Value("application.title"); ok &&
		v.Text() == "Second" {
		t.Fatal("a retained context is reading a later batch's draft")
	}

	// A commit moves the case and not the snapshot. `Revision` stays at the
	// head the call read, which is the number a host would find stale if it
	// cached, and the reason the rule is stated as a property rather than as
	// advice.
	if _, err := c.Submit(ctx(), "application", res.Working+1); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if kept.Revision() != 0 {
		t.Errorf("the retained snapshot reports revision %v, want the 0 it"+
			" was cut at", kept.Revision())
	}

	// And reading it from elsewhere is safe precisely because it is inert.
	// This is the assertion that says a snapshot may outlive its call without
	// becoming a lease on somebody else's state, and it is the reason the
	// type is pointers rather than a copy: run the file under -race.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := kept.Value("application.fee"); !ok {
				t.Error("a goroutine could not read the retained snapshot")
			}
			_ = kept.Roles()
			_ = kept.Stage()
			_ = kept.Working()
		}()
	}
	wg.Wait()
}

func TestAProblemNamesTheAddressTheHookWasHanded(t *testing.T) {
	// One vocabulary for one cell. A normalizer is handed a path when a value
	// is written, and a `FieldProblem` names a path when the same stored
	// value is later refused; if those two texts ever disagreed, a host could
	// not line its own rule up with what the engine reported about it. They
	// are built in different places — `settle` for the write, `validate` for
	// the re-ask — which is what makes the equality worth a test instead of a
	// comment.
	rt, c, row, _ := ctxFilled(t)
	ctxSilent(rt)
	var (
		asked    []string
		no       = errors.New("this value is no longer acceptable")
		refusing bool
	)
	rt.RegisterValidate("money", func(_ context.Context,
		_ monshi.CaseContext, path string,
		v monshi.Value) (monshi.Value, error) {
		if !refusing {
			return v, nil
		}
		asked = append(asked, path)
		return v, no
	})

	// Nothing is written from here on: the case was filled before the hook
	// turned against its own output, which is the shape of a host rule that
	// changed its mind, and the only way to see a re-ask refuse a value the
	// write accepted.
	refusing = true
	owner, err := rt.Open(ctx(), c.ID(), ctxOwner)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := owner.View(ctx(), "application")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	reported := walkProblems(res.Roots)
	if len(asked) < 2 {
		t.Fatalf("the re-ask called the hook %v times, want the form's two"+
			" money fields", len(asked))
	}
	sort.Strings(asked)
	sort.Strings(reported)
	if !reflect.DeepEqual(asked, reported) {
		t.Errorf("the hook was handed %v and the report named %v", asked,
			reported)
	}

	// The row's member is in both lists under one text, which is the half
	// nothing else guarantees: an address built for a problem and an address
	// built for a hook call are the same function's answer, and the row's ID
	// appears in both.
	if !strings.Contains(strings.Join(asked, " "), row+".paid") {
		t.Errorf("the re-ask skipped the row's field: %v", asked)
	}
	for _, p := range reported {
		if p == "application.paid" || !strings.Contains(p, ".") {
			t.Errorf("the report named %q, which is not an address", p)
		}
	}
}

// walkProblems collects every reported path in a rendered tree, sorted, so a
// test compares the set of addresses rather than two walk orders that happen
// to agree.
func walkProblems(roots []*monshi.ViewNode) []string {
	var out []string
	var walk func(n *monshi.ViewNode)
	walk = func(n *monshi.ViewNode) {
		for _, p := range n.Problems {
			out = append(out, p.Path)
		}
		for _, k := range n.Children {
			walk(k)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	sort.Strings(out)
	return out
}

func TestAMissingOptionsProviderRefusesAndAMissingNormalizerDoesNot(t *testing.T) {
	// A missing validator leaves the value unchanged. A missing options
	// provider cannot supply any choices.
	rt, c := ctxCase(t, ctxOwner)
	if _, err := c.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.fee", cents("20.00"))); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// No normalizer anywhere: the value that comes back is the value that was
	// sent, in the same text, and nothing was heard about the absence.
	res, err := c.View(ctx(), "application.kind")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := res.Roots[0].Value.Tag(); got != "COMPANY" {
		t.Errorf("an unbound normalizer turned the tag into %q", got)
	}

	// No provider: the whole-case view refuses, and names the binding it
	// could not find, which is the only name the host's own log has.
	_, err = c.View(ctx())
	if !monshi.Failed(err, monshi.HookFailure) {
		t.Fatalf("a view with no provider = %v, want HOOK_FAILURE", err)
	}
	var ce *monshi.CaseError
	if !errors.As(err, &ce) {
		t.Fatalf("refusal %v is not a CaseError", err)
	}
	if !strings.Contains(ce.Detail, `"application.note"`) {
		t.Errorf("the refusal says %q, want the binding it could not find",
			ce.Detail)
	}

	// A scope that reaches no options field asks nothing.
	if _, err := c.View(ctx(), "application.fee"); err != nil {
		t.Errorf("a scope that reaches no provider = %v, want the answer",
			err)
	}

	// The one consequence of keying a hook by a *binding* rather than by a
	// symbol, and the asymmetry this slice inherits rather than invents: a
	// mistyped normalizer name is quiet, because an unbound `Validate` is
	// identity anyway, and a mistyped provider name is loud, because the
	// binding the definition declared is still unbound. Both answers arrive
	// where the mistake is visible, and neither is a refusal the runtime
	// could have made: a runtime holds no definition when a host registers,
	// so it cannot know that "mon" names no type.
	rt.RegisterValidate("mon", func(_ context.Context,
		_ monshi.CaseContext, _ string, v monshi.Value) (monshi.Value,
		error) {
		return monshi.Decimal(v.Decimal().Neg()), nil
	})
	rt.RegisterOptions("application.note2", func(_ context.Context,
		_ monshi.CaseContext, _ string) ([]string, error) {
		return []string{"red", "blue"}, nil
	})
	if _, err := c.View(ctx(), "application.fee"); err != nil {
		t.Errorf("a mistyped Validate key refused a read, want silence: %v",
			err)
	}
	if _, err := c.View(ctx()); !monshi.Failed(err, monshi.HookFailure) {
		t.Errorf("a mistyped Options key = %v, want the still-missing"+
			" binding", err)
	}

	// And the right names end it.
	ctxSilent(rt)
	if _, err := c.View(ctx()); err != nil {
		t.Fatalf("with both providers registered: %v", err)
	}
}

func TestAnOptionsProviderIsAskedOnceForTwoHundredRows(t *testing.T) {
	// The fetch rule, priced where it matters. A provider is bound to a
	// *declaration*, so its answer is the field's and not a row's, and a
	// collection of two hundred instances is one question with two hundred
	// renderings of the answer. The alternative would be a host function called
	// once per row for a list the definition says is the field's, and a client
	// that added rows would find its own server pricing the form.
	rt, c := ctxCase(t, ctxOwner)
	edits := []monshi.Edit{monshi.Set("application.kind",
		monshi.Tag("PERSON"))}
	for i := 0; i < 200; i++ {
		edits = append(edits, monshi.Add("application.holders"))
	}
	res, err := c.Update(ctx(), 0, edits...)
	if err != nil {
		t.Fatalf("adding 200 rows: %v", err)
	}
	filled := make([]monshi.Edit, 0, len(res.Created))
	for _, row := range res.Created {
		filled = append(filled, monshi.Set(row+".who",
			monshi.Text("holder")))
	}
	if _, err := c.Update(ctx(), res.Working, filled...); err != nil {
		t.Fatalf("naming the rows: %v", err)
	}
	if _, err := c.Submit(ctx(), "application", res.Working+1); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var notes, picks int
	rt.RegisterOptions("application.note", func(_ context.Context,
		_ monshi.CaseContext, _ string) ([]string, error) {
		notes++
		return []string{"red", "blue"}, nil
	})
	rt.RegisterOptions("application.holders.pick", func(_ context.Context,
		at monshi.CaseContext, asked string) ([]string, error) {
		picks++
		// The address a provider is handed is its own binding, with no
		// instance in it: the decision that a per-row answer would be a
		// per-row fetch, stated as the text the host actually receives.
		if asked != "application.holders.pick" {
			return nil, fmt.Errorf("a provider was asked about %s", asked)
		}
		// And the snapshot beside it is the caller's, not a placeholder: a
		// provider that hands its list to a server needs to say who is
		// asking, and an empty context would be a silent bug in the fetch
		// rather than a refusal anyone hears about.
		if at.Case() != c.ID() || at.Actor().User != ctxRuler.User ||
			at.Stage() != "draft" || at.Revision() != 1 {
			return nil, fmt.Errorf("the provider was handed case %v as %q"+
				" at stage %q revision %v", at.Case(), at.Actor().User,
				at.Stage(), at.Revision())
		}
		if _, ok := at.Value("application.kind"); !ok {
			return nil, errors.New("the provider could not read the form")
		}
		return []string{"left", "right"}, nil
	})

	ruler, err := rt.Open(ctx(), c.ID(), ctxRuler)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	view, err := ruler.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if notes != 1 || picks != 1 {
		t.Errorf("the two bindings were asked %v and %v times, want 1 and 1"+
			" for %v rows", notes, picks, len(res.Created))
	}

	// One fetch, and every instance shows the same list, which is what
	// `ViewNode.Options` promises for a repeated field and the reason a
	// provider that needed the row to answer is being asked a question this
	// language does not have. The member is found by its own address rather
	// than by an index, because declaration order is the definition's choice
	// and a test that hard-codes it breaks when somebody adds a field.
	rows := findNode(view.Roots, "application.holders")
	if rows == nil {
		t.Fatal("the collection is not in the answer")
	}
	if len(rows.Children) != 200 {
		t.Fatalf("the collection rendered %v instances", len(rows.Children))
	}
	same := 0
	for _, inst := range rows.Children {
		pick := findNode(inst.Children, inst.Path+".pick")
		if pick == nil {
			t.Fatalf("%s rendered no choice list", inst.Path)
		}
		if reflect.DeepEqual(pick.Options, []string{"left", "right"}) {
			same++
		}
	}
	if same != 200 {
		t.Errorf("%v of 200 instances show the field's list", same)
	}
}

// findNode walks rendered roots for one address. It exists so that a test
// names the node it means instead of counting children, which is the
// difference between an assertion about the answer and an assertion about the
// order the definition happened to be written in.
func findNode(roots []*monshi.ViewNode, path string) *monshi.ViewNode {
	for _, n := range roots {
		if n.Path == path {
			return n
		}
		if got := findNode(n.Children, path); got != nil {
			return got
		}
	}
	return nil
}

func TestAZeroContextIsInert(t *testing.T) {
	// A zero CaseContext must be safe to read and reveal no data.
	var at monshi.CaseContext
	if _, ok := at.Value("application.fee"); ok {
		t.Error("Value answered true")
	}
	if rv, ok := at.Review("application"); ok || rv.State != "" {
		t.Errorf("Review = %+v, %v, want the zero info", rv, ok)
	}
	if sg, ok := at.Signature("application.submitted"); ok ||
		sg.By != "" {
		t.Errorf("Signature = %+v, %v, want the zero info", sg, ok)
	}
	if got := at.Roles(); len(got) != 0 {
		t.Errorf("Roles = %v, want none", got)
	}
	if at.Case() != 0 || at.Definition() != "" || at.Stage() != "" ||
		at.Revision() != 0 || at.Working() != 0 {
		t.Errorf("the identity reads are %+v, %q, %q, %v, %v, want zero",
			at.Case(), at.Definition(), at.Stage(), at.Revision(),
			at.Working())
	}
	if at.Actor().User != "" || at.Actor().PrimaryRole != "" {
		t.Errorf("Actor = %+v, want nobody", at.Actor())
	}
}
