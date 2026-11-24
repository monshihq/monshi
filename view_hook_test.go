package monshi

// The half of the view that needs a registered hook, and the walk's own
// mechanics (RUNTIME §5.7, §6; DESIGN §6.1, §7).
//
// `view_test.go` is the host's view of this operation. What a host cannot
// reach yet is the registry — binding an `Options` provider or a `Validate`
// hook still needs the seam §6 keeps unexported until `CaseContext` exists —
// so every claim about a hook is made here, and so is every claim that needs
// a narrower grant than a whole form. Both move out the day the seam opens:
// the split is the registry's, not the tests' preference.
//
// The claims worth the most here are the negative ones. A view is allowed to
// call host code, and a host that watches its own catalogue queried twice for
// a field with two hundred rows has no way to tell that from a bug in the
// engine's cache. So the counters below are the assertions, in both
// directions: what must be asked exactly once, and what must not be asked at
// all because the caller will never be shown the answer.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// viewHookSrc is the fixture for both kinds of binding and for the shapes the
// walk has to get right: an `options` field with a list to fetch, a repeated
// group whose member repeats that question across rows, a gated subtree, and
// two fields the viewer may edit but not see — one for each kind of hook,
// because the two bindings are asked at different moments and a single probe
// would let the other one through. Auditor's grant is one leaf, which is what
// makes `visible()`'s descendant half testable at all.
const viewHookSrc = `
types {
    legal : enum(PERSON, COMPANY);
    money : decimal(14, 2);
}
roles {
    Owner {
        allow view, edit, submit application;
        deny view application.hush;
        deny view application.owed;
    }
    Auditor {
        allow view application.fee;
    }
}
forms {
    application {
        kind : legal;
        city : options;
        fee  : money;
        hush : options;
        owed : money;
        gate : string;

        rows multiple {
            pick  : options;
            stake : money;
        }

        deep present[.gate == "y"] {
            far : options;
        }
    }
}
stages {
    draft { }
}
`

var (
	viewOwner   = Actor{User: "dana", PrimaryRole: "Owner"}
	viewAuditor = Actor{User: "dana", PrimaryRole: "Auditor"}
)

func viewHookCase(t *testing.T, actor Actor) (*Runtime, *image, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, viewHookSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

// mustView is the in-package twin of the external helper: a view that is
// expected to answer, so a refusal is a failure of the walk rather than a case
// the test meant to exercise.
func mustView(t *testing.T, c Case, paths ...string) ViewResult {
	t.Helper()
	res, err := c.View(ctx(), paths...)
	if err != nil {
		t.Fatalf("View(%s): %v", strings.Join(paths, ", "), err)
	}
	return res
}

// hookNode is a node the view is required to contain.
func hookNode(t *testing.T, res ViewResult, path string) *ViewNode {
	t.Helper()
	n := findNode(res.Roots, path)
	if n == nil {
		t.Fatalf("%q is not in the view: %s", path, renderNodes(res.Roots))
	}
	return n
}

func findNode(nodes []*ViewNode, path string) *ViewNode {
	for _, n := range nodes {
		if n.Path == path {
			return n
		}
		if got := findNode(n.Children, path); got != nil {
			return got
		}
	}
	return nil
}

func renderNodes(nodes []*ViewNode) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(n.Path + " ")
		b.WriteString(renderNodes(n.Children))
	}
	return b.String()
}

// catalogue binds an `Options` provider for one field path: it returns a fixed
// list, counts the calls, and refuses to answer a question about any other
// path, because a provider bound by §6's path key has no business being asked
// about a field it was not registered for.
func catalogue(rt *Runtime, path string, list []string, calls *int) {
	rt.RegisterOptions(path, func(_ context.Context, _ CaseContext, asked string) (
		[]string, error) {
		*calls++
		if asked != path {
			return nil, errors.New("asked about " + asked)
		}
		return list, nil
	})
}

// brokenProvider counts too, and is the sharper probe of the two: one call is
// not a slow answer, it is a failed view.
func brokenProvider(rt *Runtime, path string, calls *int) {
	rt.RegisterOptions(path, func(_ context.Context, _ CaseContext, _ string) (
		[]string, error) {
		*calls++
		return nil, errors.New("the catalogue is closed")
	})
}

// refusesBinds the fixture's `Validate` hook for `money`, refusing any value
// under `min` and counting what it was asked about.
func refusesValues(rt *Runtime, min int64, calls *int) {
	rt.RegisterValidate("money", func(_ context.Context, _ CaseContext,
		_ string, v Value) (Value, error) {
		*calls++
		if v.d.LessThan(decimal.NewFromInt(min)) {
			return Value{}, errors.New("below the floor")
		}
		return v, nil
	})
}

func TestViewCarriesTheOptionsListItsProviderSupplies(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	var city int
	catalogue(rt, "application.city", []string{"APLA", "BPLA", "OTHER"},
		&city)

	mustUpdate(t, c, 0, Set("application.city", Text("APLA")))
	res := mustView(t, c, "application")

	n := hookNode(t, res, "application.city")
	if got := strings.Join(n.Options, ","); got != "APLA,BPLA,OTHER" {
		t.Errorf("city shows %q, want the list its provider returned", got)
	}
	if city != 1 {
		t.Errorf("the provider was called %d times for one field, want 1",
			city)
	}
	// The two vocabularies §6.1 keeps apart, on one node: the declaration
	// says what kind of field this is, the value says what the row holds, and
	// for an options field those are different words — §3.3's options value
	// *is* the chosen option's text, and the list of choices is not data the
	// row holds.
	if n.Kind != "options" {
		t.Errorf("city is kind %q, want options", n.Kind)
	}
	if got := n.Value.Kind(); got != KindText {
		t.Errorf("city's value is kind %q, want text", got)
	}
	if got := n.Value.Text(); got != "APLA" {
		t.Errorf("city holds %q, want APLA", got)
	}
	// An enum's choices are declared data and need nobody to fetch them:
	// `kind` is here with no list, and the walk asked no host about it.
	if got := hookNode(t, res, "application.kind").Options; got != nil {
		t.Errorf("an enum's choices arrived as a fetched list: %v", got)
	}
}

func TestViewFetchesOneListForEveryInstance(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	var pick int
	catalogue(rt, "application.rows.pick", []string{"X", "Y"}, &pick)

	// Only the selection of `rows` is walked, so the fixture's other options
	// field is not asked about either — and that is the cache's own evidence.
	mustUpdate(t, c, 0,
		Add("application.rows"), Add("application.rows"),
		Add("application.rows"))
	res := mustView(t, c, "application.rows")
	rows := hookNode(t, res, "application.rows")
	if len(rows.Children) != 3 {
		t.Fatalf("rows rendered %d instances, want 3", len(rows.Children))
	}
	if pick != 1 {
		t.Errorf("three rows fetched one list %d times, want once: a "+
			"provider is bound by field path and cannot see a row", pick)
	}
	for i, inst := range rows.Children {
		n := findNode(inst.Children, inst.Path+".pick")
		if n == nil {
			t.Fatalf("instance %d has no pick", i)
		}
		if got := strings.Join(n.Options, ","); got != "X,Y" {
			t.Errorf("row %d shows %q, want the same list", i, got)
		}
	}
	// The cache is per call and not per case, so a host that changed its
	// catalogue between two views is asked again rather than showing a stale
	// list until something else invalidates it.
	mustView(t, c, "application.rows")
	if pick != 2 {
		t.Errorf("two views called the provider %d times in total, want 2",
			pick)
	}
}

func TestViewRefusesAnOptionsFieldWithNoProvider(t *testing.T) {
	_, _, c := viewHookCase(t, viewOwner)

	// DESIGN §7's asymmetry, and the one place §6's two bindings disagree on
	// purpose: `Validate` absent is identity, `Options` absent is no answer.
	// The whole form refuses, because the alternative is a form on screen
	// with a dropdown that cannot open — the bug the rule exists to prevent.
	_, err := c.View(ctx(), "application")
	if !Failed(err, HookFailure) {
		t.Fatalf("a view with no provider = %v, want HOOK_FAILURE", err)
	}
	var ce *CaseError
	if !errors.As(err, &ce) {
		t.Fatalf("refusal %v is not a CaseError", err)
	}
	if ce.Op != "view" {
		t.Errorf("the refusal is about %q, want view", ce.Op)
	}
	if !strings.Contains(ce.Detail, `"application.city"`) {
		t.Errorf("the refusal says %q, want the binding it could not find",
			ce.Detail)
	}
	// The same for a whole-case view: nothing about `city` is withheld, so
	// the walk reaches it and the contract breaks the same way.
	if _, err := c.View(ctx()); !Failed(err, HookFailure) {
		t.Errorf("the whole-case view = %v, want HOOK_FAILURE", err)
	}
	// A selection that never reaches an options field asks nothing, so a
	// definition with a broken half still answers about the other: this is
	// the walk's scope rule rather than §7's, and it is what keeps a view of
	// one field hostage to a provider it never mentioned.
	if _, err := c.View(ctx(), "application.kind"); err != nil {
		t.Errorf("View(application.kind) = %v, want it to answer", err)
	}
}

func TestViewRefusesWhenTheProviderRefuses(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	var called int
	broken := errors.New("the catalogue is closed")
	rt.RegisterOptions("application.city",
		func(_ context.Context, _ CaseContext, _ string) ([]string, error) {
			called++
			return nil, broken
		})

	_, err := c.View(ctx(), "application.city")
	if !Failed(err, HookFailure) {
		t.Fatalf("a refusing provider = %v, want HOOK_FAILURE", err)
	}
	if !errors.Is(err, broken) {
		t.Errorf("the provider's own error is lost: %v", err)
	}
	// An execution failure is an error and not silent success (DESIGN §7),
	// and this is the call site where that sentence has teeth.
	if called != 1 {
		t.Errorf("the provider was called %d times, want 1", called)
	}
}

func TestViewAsksNoProviderForContentThatIsAbsent(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	catalogue(rt, "application.city", []string{"APLA"}, new(int))
	var far int
	catalogue(rt, "application.deep.far", []string{"1"}, &far)

	// `deep` is gated on `gate == "y"` and nobody has written `gate`, so the
	// subtree is not part of the effective state (§5.2) — and a host's
	// catalogue is not consulted about content the caller cannot be shown.
	res := mustView(t, c, "application")
	if findNode(res.Roots, "application.deep") != nil {
		t.Error("a closed gate rendered its subtree")
	}
	if far != 0 {
		t.Errorf("the provider behind a closed gate was called %d times, "+
			"want never: %s", far, renderNodes(res.Roots))
	}
	// A selection addressed *into* the gated subtree is the same question
	// asked earlier, at the door rather than after the walk.
	if got := mustView(t, c, "application.deep.far"); len(got.Roots) != 0 {
		t.Errorf("a selection inside a closed gate returned %s, want "+
			"nothing: §5.2", renderNodes(got.Roots))
	}
	if far != 0 {
		t.Errorf("a closed gate fetched %d lists, want none", far)
	}

	// The gate opening is the only thing that changes the answer, and it
	// changes it by making the node exist rather than by making the walk
	// filter better.
	base := mustUpdate(t, c, res.Working,
		Set("application.gate", Text("y")))
	res = mustView(t, c, "application")
	if got := hookNode(t, res, "application.deep.far").Options; len(got) != 1 {
		t.Errorf("an open gate shows %v, want the list behind it", got)
	}
	if far != 1 {
		t.Errorf("the open gate called the provider %d times, want 1", far)
	}
	_ = base
}

func TestViewAsksNoProviderForAFieldTheCallerMayNotSee(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	catalogue(rt, "application.city", []string{"APLA"}, new(int))
	var hush int
	brokenProvider(rt, "application.hush", &hush)

	// `hush` is denied view and granted edit, so the field is in the form,
	// the actor owns it, and the walk still has to leave it alone.
	res := mustView(t, c, "application")
	if n := findNode(res.Roots, "application.hush"); n != nil {
		t.Errorf("a field denied view is in the view as %q", n.Path)
	}
	if hush != 0 {
		t.Errorf("the withheld field's provider was called %d times, want "+
			"never", hush)
	}
	// Naming it is a refusal, and the refusal comes before the fetch: an
	// actor probing whether a provider exists for a field they may not see
	// learns only that they may not see it.
	if _, err := c.View(ctx(), "application.hush"); !Failed(err,
		AccessDenied) {
		t.Errorf("View of a denied field = %v, want ACCESS_DENIED", err)
	}
	if hush != 0 {
		t.Errorf("a denied path fetched %d lists, want none", hush)
	}
}

func TestViewCarriesValidationProblemsToTheirNodes(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	catalogue(rt, "application.city", []string{"APLA"}, new(int))
	catalogue(rt, "application.rows.pick", []string{"p"}, new(int))
	brokenProvider(rt, "application.deep.far", new(int))

	// One visible fee the host will come to refuse, and one in a field this
	// actor may edit but not see. Binding the rule *after* the write is the
	// point: §6.3's re-check exists because rules are host code, and the host
	// may have tightened them since the value arrived.
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(3))),
		Set("application.owed", Decimal(decimal.NewFromInt(3))))
	var asked int
	refusesValues(rt, 10, &asked)

	res := mustView(t, c, "application")
	fee := hookNode(t, res, "application.fee")
	if len(fee.Problems) != 1 {
		t.Fatalf("fee carries %d problems, want the hook's one: %v",
			len(fee.Problems), fee.Problems)
	}
	pb := fee.Problems[0]
	if pb.Path != "application.fee" || pb.Code != invalidValue {
		t.Errorf("problem = %+v, want it about this field's own path", pb)
	}
	if !strings.Contains(pb.Msg, "below the floor") {
		t.Errorf("the hook's sentence did not arrive: %q", pb.Msg)
	}
	// The one-line rendering belongs to the same promise: a host prints a
	// view's problems as a list, and the half a list must not lose is which
	// field the sentence is about.
	want := `application.fee: the Validate hook for "money" refused this ` +
		`value: below the floor`
	if got := pb.String(); got != want {
		t.Errorf("the one-line render = %q, want %q", got, want)
	}
	// A refusal is a report and not a failure: the view renders the form
	// beside the bad value, which is the only way a client can put the
	// message where the actor will see it.
	if len(res.Roots) != 1 || res.Roots[0].Path != "application" {
		t.Errorf("the view refused rather than rendered: %s",
			renderNodes(res.Roots))
	}
	// And the withheld field was never asked about. `asked` is the whole
	// claim: a filter that collected the problem and dropped it on the way
	// out would cost the same call and leave the same sentence in memory,
	// and §6.1's per-actor view is why the walk is filtered instead.
	if asked != 1 {
		t.Errorf("the rule ran %d times for a view of one visible value, "+
			"want 1", asked)
	}
	if findProblem(res.Roots[0], "application.owed") {
		t.Error("a problem about a withheld field reached the view")
	}
	_ = base
}

func TestViewAsksNoRuleThatCouldRefuseTheWholeView(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	catalogue(rt, "application.city", []string{"APLA"}, new(int))
	catalogue(rt, "application.rows.pick", []string{"p"}, new(int))

	// The nastier half of the same rule. A hook that is not a fixed point on
	// its own output breaks §6's contract, and the engine's answer is
	// HOOK_FAILURE for the whole operation — so a view that asked it about
	// withheld content would let one actor's unviewable field refuse another
	// actor's form. `owed` is the only money cell with a value, and `fee` is
	// left unset, which is what makes the two outcomes differ.
	base := mustUpdate(t, c, 0,
		Set("application.owed", Decimal(decimal.NewFromInt(3))))
	var asked int
	rt.RegisterValidate("money", func(_ context.Context, _ CaseContext,
		_ string, v Value) (Value, error) {
		asked++
		return Decimal(v.d.Add(decimal.NewFromInt(1))), nil
	})

	if asked != 0 {
		t.Fatalf("the hook ran %d times before anything asked it", asked)
	}
	res := mustView(t, c, "application")
	if findNode(res.Roots, "application.owed") != nil {
		t.Error("the withheld field rendered")
	}
	if asked != 0 {
		t.Errorf("the view asked a rule about withheld content %d times,"+
			" want never: %s", asked, renderNodes(res.Roots))
	}

	// The same case, the same hook, and the operation that is entitled to
	// find out: Submit's walk is unfiltered, so the defect is the host's to
	// fix and not a field the actor can correct.
	if _, err := c.Submit(ctx(), "application", base); !Failed(err,
		HookFailure) {
		t.Fatalf("committing a non-fixed-point hook = %v, want "+
			"HOOK_FAILURE", err)
	}
	if asked == 0 {
		t.Error("Submit refused without asking the hook anything")
	}
}

func TestViewSelectionOfOneInstanceAsksItsSiblingsAndReportsOnlyIt(t *testing.T) {
	rt, _, c := viewHookCase(t, viewOwner)
	catalogue(rt, "application.rows.pick", []string{"p"}, new(int))

	rows := mustUpdate(t, c, 0, Add("application.rows"), Add("application.rows"))
	base := mustUpdate(t, c, rows,
		Set("application.rows.1.stake", Decimal(decimal.NewFromInt(3))),
		Set("application.rows.2.stake", Decimal(decimal.NewFromInt(4))))
	var asked int
	refusesValues(rt, 4, &asked)

	// The report is asked once per selected root as a subtree walk, so a
	// selection addressed to one row does hear about the other — and the
	// answer about it is dropped by never being looked up, because the only
	// nodes built are the caller's. `asked == 2` is the walk, and the empty
	// problem list is the drop.
	one := mustView(t, c, "application.rows.2.stake")
	if len(one.Roots) != 1 {
		t.Fatalf("a one-row selection returned %d roots", len(one.Roots))
	}
	if got := len(one.Roots[0].Problems); got != 0 {
		t.Errorf("the selected row carries %d problems, want none: %v",
			got, one.Roots[0].Problems)
	}
	if asked != 2 {
		t.Errorf("the subtree walk asked about %d values, want both rows",
			asked)
	}
	whole := mustView(t, c, "application.rows")
	bad := findNode(whole.Roots, "application.rows.1.stake")
	if bad == nil || len(bad.Problems) != 1 {
		t.Fatalf("the other row's problem is missing: %v", bad)
	}
	if bad.Problems[0].Path != "application.rows.1.stake" {
		t.Errorf("problem = %+v, want it on the instance's own address",
			bad.Problems[0])
	}
	_ = base
}

// findProblem asks whether any rendered node carries a problem about one path,
// which is the shape the claim needs: what is absent is a *message*.
func findProblem(n *ViewNode, path string) bool {
	for _, pb := range n.Problems {
		if pb.Path == path {
			return true
		}
	}
	for _, c := range n.Children {
		if findProblem(c, path) {
			return true
		}
	}
	return false
}

// --- the walk's own mechanics, which no host can observe separately ---

func TestContainedComparesAddressesAndNotPrefixes(t *testing.T) {
	for _, tc := range []struct {
		outer, inner string
		want         bool
	}{
		{"application.holders", "application.holders", true},
		{"application.holders", "application.holders.who", true},
		{"application.holders", "application.holders.1.who", true},
		// The instance ID ends where the dot says it does: row 1 is not row
		// 12's ancestor, and merging them would drop a selection.
		{"application.holders.1", "application.holders.12", false},
		{"application.holders.1", "application.holders.1.who", true},
		{"application.holders.12", "application.holders.1", false},
		// A name that merely starts the same way is a sibling.
		{"application.part", "application.party", false},
		{"application", "audit", false},
		{"", "application", false},
	} {
		if got := contained(tc.outer, tc.inner); got != tc.want {
			t.Errorf("contained(%q, %q) = %v, want %v", tc.outer, tc.inner,
				got, tc.want)
		}
	}
}

func TestAddSelectionKeepsTheWidestSubtreeAtTheEarliestPlace(t *testing.T) {
	sel := func(path string) selection { return selection{addr: path} }

	// A path under one already kept adds nothing.
	kept := addSelection(nil, sel("a"))
	kept = addSelection(kept, sel("a.b"))
	if len(kept) != 1 || kept[0].addr != "a" {
		t.Fatalf("a nested selection kept %v, want a alone", kept)
	}
	// A path that contains earlier ones absorbs them, and takes its place
	// after the roots that remain: caller order, widest subtree, no path
	// rendered twice.
	kept = addSelection(kept, sel("c"))
	kept = addSelection(kept, sel("a.b.c"))
	if len(kept) != 2 || kept[0].addr != "a" || kept[1].addr != "c" {
		t.Fatalf("absorbing kept %v, want a then c", kept)
	}
	kept = addSelection([]selection{sel("a.b")}, sel("a"))
	if len(kept) != 1 || kept[0].addr != "a" {
		t.Fatalf("absorbing = %v, want a alone", kept)
	}
	// Disjoint paths stay separate.
	kept = addSelection([]selection{sel("a")}, sel("b"))
	if len(kept) != 2 {
		t.Fatalf("siblings merged into %v, want two roots", kept)
	}
	// The first answer must not have shortened the list it returns: with `a`
	// kept and `a.b` named, the untouched slice is the right answer, and a
	// single-pass version would hand back a slice it had already rewritten.
	kept = addSelection([]selection{sel("a"), sel("z")}, sel("a.b"))
	if len(kept) != 2 || kept[1].addr != "z" {
		t.Fatalf("an already-covered path gave %v, want a then z untouched",
			kept)
	}
}

func TestWithStepCopiesAndEndsAtReadsTheInnermostCollection(t *testing.T) {
	outer := instPath{{coll: 7, id: 1}}
	inner := withStep(outer, instStep{coll: 9, id: 2})
	if len(outer) != 1 || len(inner) != 2 {
		t.Fatalf("withStep changed the caller's prefix: %v then %v", outer,
			inner)
	}
	// Copying is not cosmetic: a shared array would let one sibling's row
	// overwrite another's address on its way out of the walk, and the blast
	// radius is a path a host will send back in an `Update`.
	other := withStep(outer, instStep{coll: 9, id: 3})
	if inner[1] != (instStep{coll: 9, id: 2}) ||
		other[1] != (instStep{coll: 9, id: 3}) {
		t.Errorf("two instances of one collection share %v and %v", inner,
			other)
	}
	// A node is being built as one row when the *last* step is its
	// collection: the outer step says which holder, and only the inner one
	// says which homes row this is.
	if !endsAt(inner, 9) {
		t.Error("a prefix that ends at the collection does not know it")
	}
	if endsAt(inner, 7) {
		t.Error("an outer instance makes the declaration node a row")
	}
	if endsAt(nil, 9) || endsAt(outer, 9) {
		t.Error("a prefix that does not reach the collection ended there")
	}
}

func TestSubmitMaskIsTheFormsMaskEverywhere(t *testing.T) {
	// The equivalence `ViewNode.CanSubmit` reads a node's own mask on: P3
	// lets a `submit` grant be written only at a top-level form, and §3.5's
	// fold is total, so a member carries its form's bit and no bit of its
	// own. Nothing here needs a case — the claim is about the table lowering
	// builds — and if it ever fails, the symptom is a view offering or
	// withholding a submit button that `Submit` then contradicts.
	img := mustLowerSource(t, `
roles {
    Keeper { allow view, edit, submit box; }
    Wide   { allow view, edit, submit all; }
    Stranger { allow view, edit box; }
}
forms {
    box {
        note : string;
        part multiple {
            bit  : string;
            deep {
                deeper : string;
            }
        }
    }
    other {
        hide : string;
    }
}
stages {
    draft {
        allow Keeper view other;
    }
}
`)
	s := newState(img)
	s.applyDefaults()
	stage, ok := img.stageByName("draft")
	if !ok {
		t.Fatal("the fixture declares no draft stage")
	}
	box := slotByPath(t, img, "box")
	other := slotByPath(t, img, "other")

	// Every slot of every form, asked the two questions the view compares.
	for _, form := range []nodeID{box, other} {
		lo, hi := img.subtree(form)
		for n := lo; n < hi; n++ {
			for _, role := range []string{"Keeper", "Wide", "Stranger"} {
				roles := img.rolesHeld(
					Actor{User: "dana", PrimaryRole: role}, s)
				at := img.maySubmit(roles, stage, n)
				on := img.maySubmit(roles, stage, form)
				if at != on {
					t.Errorf("%s's submit at %q is %v, at its form %q %v",
						role, img.path(n), at, img.path(form), on)
				}
			}
		}
	}

	// And the grants are the ones the fixture says, so the loop above is not
	// comparing two answers that are both false everywhere.
	keep := img.rolesHeld(Actor{User: "dana", PrimaryRole: "Keeper"}, s)
	if !img.maySubmit(keep, stage, box) || img.maySubmit(keep, stage, other) {
		t.Error("Keeper's submit grant is not the one the definition wrote")
	}
	wide := img.rolesHeld(Actor{User: "dana", PrimaryRole: "Wide"}, s)
	if !img.maySubmit(wide, stage, box) || !img.maySubmit(wide, stage, other) {
		t.Error("`submit all` did not reach both forms")
	}
	none := img.rolesHeld(Actor{User: "dana", PrimaryRole: "Stranger"}, s)
	if img.maySubmit(none, stage, box) {
		t.Error("a role granted view and edit can submit")
	}
}

func TestVisibleMarksAContainerByWhatItHolds(t *testing.T) {
	_, img, _ := viewHookCase(t, viewOwner)
	s := newState(img)
	s.applyDefaults()
	stage, ok := img.stageByName("draft")
	if !ok {
		t.Fatal("the fixture declares no draft stage")
	}

	// Auditor's only grant in the case is one field, so every other true flag
	// below is the descendant half of the rule: the path to content is
	// rendered because content is on the other side of it, and nothing else
	// is.
	vis := viewable(img, img.rolesHeld(viewAuditor, s), stage)
	on := func(path string) bool {
		return vis[slotByPath(t, img, path)]
	}
	if !on("application.fee") {
		t.Error("the granted field is not visible")
	}
	if !on("application") {
		t.Error("a container holding something granted is not visible")
	}
	if !vis[0] {
		t.Error("the case root is not visible to an actor with a grant")
	}
	for _, path := range []string{"application.kind", "application.city",
		"application.rows", "application.rows.stake", "application.deep",
		"application.deep.far", "application.hush", "application.owed"} {
		if on(path) {
			t.Errorf("%s is visible to an actor granted only one field", path)
		}
	}

	// An actor with no grant anywhere has no visible slot at all, including
	// the root — which is what lets a view of nothing be an empty answer
	// rather than a walk that asks a host about content nobody will see.
	for i, ok := range viewable(img,
		img.rolesHeld(Actor{User: "nobody"}, s), stage) {
		if ok {
			t.Fatalf("slot %d is visible to an actor with no role", i)
		}
	}
}
