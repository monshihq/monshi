package monshi

// The edit operation's other half: what a batch did to the *data*.
//
// `edit_test.go` covers what a host can observe — the refusals, the working
// revision, the paths a batch created. What it cannot observe is a value,
// because the exported read of a case is milestone 3's `View`. So the claims
// below are the ones only this package can make, and they are the
// load-bearing ones: which copy a write lands in, what a cleared field *is*,
// that a not-present subtree kept what was written to it, that a
// normalization ran and what it produced, and that an edit conferred nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// editCaseSrc states each claim with one declaration: a custom decimal type a
// `Validate` hook can bind to, a role-typed field, a present-gated subtree, a
// repeated group with a `required` member and a defaulted one, and a field
// that carries a default. Manager may reach the gated subtree but not the
// field that opens it, and Applicant the reverse, so the two actors disagree
// about paths rather than about the fixture's honesty.
const editCaseSrc = `
types {
    legal : enum(PERSON, COMPANY);
    money : decimal(14, 2);
}
roles {
    Applicant {
        allow view, edit application;
        deny edit application.internal;
    }
    Manager {
        allow view all;
        allow edit application.internal;
        allow edit application.kind;
    }
}
forms {
    application {
        name     : string default "new";
        kind     : legal;
        fee      : money default 1.00;
        reviewer : Manager;

        internal present[.kind == COMPANY] {
            risk : integer;
        }

        holders multiple {
            who   : string required;
            since : date default "2026-01-01";
        }
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`

// danaEdit is Applicant, which owns `application` and is denied nothing here;
// danaManager is the same *identity* with a different role, so the two share
// one working copy and disagree only about authority.
var (
	danaEdit    = Actor{User: "dana", PrimaryRole: "Applicant"}
	danaManager = Actor{User: "dana", PrimaryRole: "Manager"}
)

// editCase makes a runtime, its definition's image, and one case of it, open
// for the actor — which is also where that actor's working copy appears.
func editCase(t *testing.T, actor Actor) (*Runtime, *image, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, editCaseSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

// draftState is the handle's own actor's copy, read the way an operation
// reads it: through the store, interpreted through the image the case's
// definition resolves to.
func draftState(t *testing.T, c Case, img *image) state {
	t.Helper()
	w, err := c.w.cases.working(newRefuser("open", c.id), c.actor.User, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	return w.s
}

// draftValue reads one path's payload out of that copy. What it reads is a
// clone, which is what the store hands every operation: a test that wrote
// through what it read would be testing something no operation can do.
func draftValue(t *testing.T, c Case, img *image, path string) slotValue {
	t.Helper()
	s := draftState(t, c, img)
	r, ok := s.resolve(path)
	if !ok {
		t.Fatalf("%q resolves to nothing in the working copy", path)
	}
	if !r.hasValue {
		t.Fatalf("%q has no value cell in the working copy", path)
	}
	return r.cell.get()
}

// mustUpdate applies a batch and fails the test if it is refused, returning
// the revision the copy moved to so the next batch presents the right number.
// Threading it explicitly is part of what these tests check: §6.2's guard
// means a host that keeps its own count gets refused, and a helper that
// ignored the number would hide that in every test but one.
func mustUpdate(t *testing.T, c Case, base uint64, edits ...Edit) uint64 {
	t.Helper()
	res, err := c.Update(ctx(), base, edits...)
	if err != nil {
		t.Fatalf("Update(%d, %d edits): %v", base, len(edits), err)
	}
	return res.Working
}

func TestUpdateWritesTheWorkingCopyAndNotTheCase(t *testing.T) {
	_, img, c := editCase(t, danaEdit)
	base := mustUpdate(t, c, 0, Set("application.name", Text("Monshi Ltd")))

	// The candidate holds what the batch wrote.
	if got := draftValue(t, c, img, "application.name"); !got.set ||
		got.s != "Monshi Ltd" {
		t.Errorf("draft holds %+v, want the value the batch wrote", got)
	}

	// The committed snapshot does not. An edit is not a commit (invariant 2),
	// and the split is what §6.3's Submit diff is computed against, so if
	// this assertion ever fails the symptom is a commit reporting an empty
	// diff on content a user typed an hour ago.
	rec, err := c.w.cases.load(newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r, ok := rec.commit.s.resolve("application.name")
	if !ok || !r.hasValue {
		t.Fatal("application.name is unreadable in the committed snapshot")
	}
	if v := r.cell.get(); v.s != "new" {
		t.Errorf("committed name = %q, want the default create materialized, "+
			"which is what a diff is computed against", v.s)
	}

	// And the copy's base is the committed revision it diverged from, not a
	// number the caller asserted: §6.5's rebase compares that pair, and §3.1's
	// division says the store does the numbering.
	w, err := c.w.cases.working(newRefuser("open", c.id), c.actor.User, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if w.base != 0 || w.rev != 1 || base != 1 {
		t.Errorf("copy is base %d rev %d, batch reported %d: want 0, 1, 1",
			w.base, w.rev, base)
	}
}

func TestEmptyClearsTheValueAndKeepsTheField(t *testing.T) {
	_, img, c := editCase(t, danaEdit)

	res, err := c.Update(ctx(), 0, Add("application.holders"))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	row := res.Created[0]
	base := mustUpdate(t, c, res.Working, Set(row+".who", Text("Dana")))
	base = mustUpdate(t, c, base, Empty(row+".who"))

	s := draftState(t, c, img)
	v := draftValue(t, c, img, row+".who")

	// `Empty` is §5.1's "clear an existing field's value, preserving the
	// field": the value is gone and the field is not. The row already has an
	// exact spelling for a slot with no value, so that is what a cleared one
	// is — and the preserved half is that the field stays in the structure,
	// still reachable, so its `required` is asked and answered.
	if v.set {
		t.Errorf("a cleared field still holds %+v", v)
	}
	n := slotByPath(t, img, "application.holders.who")
	if !s.reached(n) {
		t.Error("a member of an existing instance is unreachable: `Empty` " +
			"preserves the field, and presence-driven pruning is a different " +
			"thing (DESIGN §5.1)")
	}
	if s.satisfied(n, v) {
		t.Error("a required field cleared to empty satisfied its rule")
	}

	// What `Empty` is *for*, then: a default nobody re-applies. §5.1 says a
	// field a host cleared stays cleared, because defaults materialize when
	// data is created and nothing rewrites what a case holds (invariant 8). A
	// default reappearing under a cleared field is the engine undoing a user's
	// edit, which is the shape of bug this assertion exists to catch.
	base = mustUpdate(t, c, base, Empty("application.fee"))
	if back := draftValue(t, c, img, "application.fee"); back.set ||
		!back.d.Equal(decimal.Zero) {
		t.Errorf("a cleared decimal is %+v, want no value and the zero "+
			"payload, not the declared default", back)
	}

	// And the field is still writable: clearing leaves a cell, not a hole.
	mustUpdate(t, c, base, Set("application.fee", Decimal(decimal.NewFromInt(5))))
	if back := draftValue(t, c, img, "application.fee"); !back.d.Equal(
		decimal.NewFromInt(5)) {
		t.Errorf("writing over a cleared field left %+v", back)
	}
}

func TestAddMaterializesDefaultsAndNeverReusesAnID(t *testing.T) {
	_, img, c := editCase(t, danaEdit)
	res, err := c.Update(ctx(), 0, Add("application.holders"))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	first := res.Created[0]

	// A row `Add` created holds the defaults its declaration names and no
	// other content: §5.1's `Add` takes no value, so anything in a new row is
	// the definition's doing. `who` has no default, so it is genuinely absent
	// — which is also why a fresh instance with a `required` member is an
	// invalid draft rather than an empty one (§6.2).
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if since := draftValue(t, c, img, first+".since"); !since.set ||
		!since.t.Equal(day) {
		t.Errorf("a new row's defaulted field is %+v, want the declared day",
			since)
	}
	if who := draftValue(t, c, img, first+".who"); who.set {
		t.Errorf("a row nobody wrote to holds %q, want it absent until set",
			who.s)
	}

	// Dropping an instance never hands its number to the next one: the
	// allocator is part of the case's data and does not move backwards, which
	// is what lets a host cache a path and a stored diff name a row forever
	// (§5.1).
	base := mustUpdate(t, c, res.Working, Remove(first))
	second, err := c.Update(ctx(), base, Add("application.holders"))
	if err != nil {
		t.Fatalf("Add after a removal: %v", err)
	}
	if second.Created[0] == first {
		t.Errorf("a new instance reused the ID %q", first)
	}

	// And the removed row is gone rather than skipped: a member of it no
	// longer resolves, so a host holding that path learns it is stale instead
	// of writing into a row the state stopped counting.
	s := draftState(t, c, img)
	if _, ok := s.resolve(first + ".who"); ok {
		t.Errorf("%q still resolves after its instance was removed", first)
	}
	if _, ok := s.resolve(second.Created[0] + ".who"); !ok {
		t.Errorf("%q does not resolve: the state lost the wrong row",
			second.Created[0])
	}
}

func TestPresenceDoesNotGateAWrite(t *testing.T) {
	// `internal` is gated on `.kind == COMPANY` and nothing is set, so the
	// subtree is not part of the effective state. §5.2 says pruning is
	// *derived, never stored*, so the write is accepted and its data waits in
	// the row: refusing it would turn the engine's own dependency order into
	// the caller's problem, and §6.2's "changes such as province and city are
	// evaluated together" is precisely the batch that both opens a subtree and
	// fills it in.
	_, img, c := editCase(t, danaManager)
	base := mustUpdate(t, c, 0, Set("application.internal.risk", Int(7)))

	n := slotByPath(t, img, "application.internal")
	if got := draftValue(t, c, img, "application.internal.risk"); !got.set ||
		got.i != 7 {
		t.Errorf("the gated write is %+v, want 7 retained until it is reached",
			got)
	}

	// Retained, and not shown: the guard's answer is what a view prunes by,
	// and it is still false.
	if before := draftState(t, c, img); before.reached(n) {
		t.Error("a subtree whose guard does not hold is reached: §5.2's " +
			"pruning is derived, and here it was not applied")
	}

	// Naming the tag that opens it makes the retained value visible with no
	// write to the subtree at all — the same fact as above read from the other
	// side, and the reason the first batch was not refused.
	base = mustUpdate(t, c, base, Set("application.kind", Tag("COMPANY")))
	if after := draftState(t, c, img); !after.reached(n) {
		t.Fatal("a guard the draft satisfies still prunes its subtree")
	}

	// And clearing the guard again hides it, with the data still in the row:
	// presence is an answer about the *view*, never a deletion (§4.1).
	mustUpdate(t, c, base, Empty("application.kind"))
	s := draftState(t, c, img)
	if s.reached(n) {
		t.Error("a cleared guard still lets its subtree be reached")
	}
	r, ok := s.resolve("application.internal.risk")
	if !ok || !r.hasValue {
		t.Fatal("pruning took the subtree's slots out of the state")
	}
	if got := r.cell.get(); !got.set || got.i != 7 {
		t.Errorf("pruning deleted the value it hid: %+v", got)
	}
}

func TestAnEditConfersNothingUntilItIsCommitted(t *testing.T) {
	// SYNTAX §5.2-T4 and DESIGN §4.3: a role-typed field's value confers the
	// role, and the state that says so is the committed one. dana writes
	// herself into `Manager` and asks, in the same batch, for what Manager
	// owns — and is refused. Asserting it as one batch is the point: the
	// assignment is in the candidate the edit is about to write, so an
	// operation that folded roles from the candidate would grant itself the
	// second edit here.
	_, img, c := editCase(t, danaEdit)
	_, err := c.Update(ctx(), 0,
		Set("application.reviewer", User("dana")),
		Set("application.internal.risk", Int(9)))
	if !Failed(err, AccessDenied) {
		t.Fatalf("the batch that assigned the role = %v, want ACCESS_DENIED",
			err)
	}

	// The assignment alone is a permitted edit, and it still confers nothing
	// while it is a draft.
	base := mustUpdate(t, c, 0, Set("application.reviewer", User("dana")))
	rec, err := c.w.cases.load(newRefuser("open", c.id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	commit := rec.commit.s
	internal := slotByPath(t, img, "application.internal")
	held := func(s state) []roleID {
		return img.rolesHeld(Actor{User: "dana"}, s)
	}

	// Two folds of the same actor against two snapshots, and the pair is what
	// §4.3 means: the draft's value is a fact about the data that a later read
	// of the draft sees, and it is not a source of authority.
	if got := held(commit); len(got) != 0 {
		t.Errorf("committed roles = %v, want none: the assignment is a draft",
			got)
	}
	if got := held(draftState(t, c, img)); len(got) != 1 {
		t.Fatalf("roles derived from the draft = %v, want the one the drafted "+
			"field names", got)
	}
	if img.mayEdit(held(commit), rec.commit.stage, internal) {
		t.Error("committed roles already grant the denied edit, so the " +
			"refusal above tested the fixture rather than the rule")
	}
	if !img.mayEdit(held(draftState(t, c, img)), rec.commit.stage, internal) {
		t.Error("the draft's roles grant nothing even in principle, so which " +
			"snapshot is consulted would not matter")
	}

	// Still refused at the next batch, with the draft in place: authority is
	// not something an edit accumulates.
	if _, err := c.Update(ctx(), base,
		Set("application.internal.risk", Int(9))); !Failed(err, AccessDenied) {
		t.Errorf("a later batch by the same draft = %v, want ACCESS_DENIED",
			err)
	}
}

// An `Edit` the engine did not build is a defect in whatever produced it, and
// the answer is a refusal, not a no-op. A zero `Value` gets the same treatment
// for the same reason: an argument nobody constructed is a mistake, and §5.1's
// four verbs are the only instructions there are.
func TestAVerblessEditIsRefusedRatherThanIgnored(t *testing.T) {
	_, img, c := editCase(t, danaEdit)
	_, err := c.Update(ctx(), 0,
		Set("application.name", Text("written first")),
		Edit{path: "application.kind"})
	if !Failed(err, InvalidOp) {
		t.Fatalf("a verb-less edit = %v, want INVALID_OPERATION", err)
	}
	if !strings.Contains(err.Error(), "no verb") {
		t.Errorf("refusal says %q", err)
	}

	// Refused as a whole batch: the earlier edit in it landed nowhere, the
	// field still holds what create materialized, and the same base is still
	// the current one — which is the claim §9's no-persistent-effect sentence
	// makes, checked against an edit that cannot even be described.
	if got := draftValue(t, c, img, "application.name"); got.s != "new" {
		t.Errorf("a batch refused for its verb wrote %q", got.s)
	}
	if _, err := c.Update(ctx(), 0,
		Set("application.name", Text("still"))); err != nil {
		t.Errorf("presenting the same base after that refusal: %v", err)
	}
}

// The normalization seam, which is the part of §6 a host cannot exercise
// until `CaseContext` is exported.
func TestValidateNormalizesTheValueItIsGiven(t *testing.T) {
	rt, img, c := editCase(t, danaEdit)

	// A custom type's binding is the type's declared name (§6's lookup rule,
	// interned at lowering from `money`), and what the hook returns is what
	// the row holds.
	var given Value
	rt.RegisterValidate("money", func(_ context.Context, _ CaseContext,
		_ string, v Value) (Value, error) {
		given = v
		return Decimal(v.d.Mul(decimal.NewFromInt(100))), nil
	})
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(3))))

	got := draftValue(t, c, img, "application.fee")
	if want := decimal.NewFromInt(300); !got.d.Equal(want) {
		t.Errorf("stored %s, want the normalized %s", got.d.String(),
			want.String())
	}
	if want := decimal.NewFromInt(3); !given.d.Equal(want) {
		t.Errorf("the hook was given %s, want the value the host assigned: "+
			"normalizing is not re-reading the row", given.d.String())
	}

	// A hook that refuses is HOOK_FAILURE, and its own error stays reachable
	// (§9 keeps a hook's or a backend's cause for the host to find). The
	// refusal also names the binding, because a host knows its function only
	// by the words its definition wrote.
	sentinel := errors.New("that amount is not a payable one")
	rt.RegisterValidate("money", func(_ context.Context, _ CaseContext,
		_ string, v Value) (Value, error) {
		return Value{}, sentinel
	})
	_, err := c.Update(ctx(), base,
		Set("application.fee", Decimal(decimal.NewFromInt(1))))
	if !Failed(err, HookFailure) {
		t.Fatalf("a refusing hook = %v, want HOOK_FAILURE", err)
	}
	if !errors.Is(err, sentinel) {
		t.Error("a hook's own error is unreachable through errors.Is")
	}
	if !strings.Contains(err.Error(), `"money"`) {
		t.Errorf("the refusal does not name the binding it is about: %v", err)
	}

	// The batch is abandoned with it: what the last accepted batch wrote is
	// still there, and the copy did not move — a failing hook is a refusal,
	// not a partial write.
	if again := draftValue(t, c, img, "application.fee"); !again.d.Equal(
		decimal.NewFromInt(300)) {
		t.Errorf("a failed hook left %s in the row, want the earlier value",
			again.d.String())
	}
	w, err := c.w.cases.working(newRefuser("open", c.id), c.actor.User, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if w.rev != base {
		t.Errorf("a refused batch left the copy at %d, want %d", w.rev, base)
	}

	// A hook that returns something the field's type cannot hold is *also*
	// HOOK_FAILURE, and not INVALID_OPERATION, because the mistake is the
	// function's rather than the caller's. This is what makes hooks safe to
	// have at all: a normalizer cannot put a value in a row that §3.3's own
	// bounds test would refuse, so the type is the last word about a payload
	// whoever produced it.
	rt.RegisterValidate("money", func(_ context.Context, _ CaseContext,
		_ string, v Value) (Value, error) {
		return Text("not a decimal"), nil
	})
	_, err = c.Update(ctx(), base,
		Set("application.fee", Decimal(decimal.NewFromInt(2))))
	if !Failed(err, HookFailure) {
		t.Fatalf("a normalizer returning the wrong class = %v, want "+
			"HOOK_FAILURE", err)
	}
	if !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("the refusal does not say what the hook broke: %v", err)
	}
}

// A binding is keyed by kind as well as name: `Validate` by custom type and
// `Options` by field path are two questions, and one table entry for both
// would mean a dropdown's option list being run as a normalizer.
func TestAHookBindingIsKeyedByKindAndName(t *testing.T) {
	rt, img, c := editCase(t, danaEdit)
	hook := img.types[img.slots[slotByPath(t, img, "application.fee")].typ].hook
	if hook == 0 {
		t.Fatal("a custom type carried no Validate binding, so §6's lookup " +
			"is untested")
	}
	// What a host registers against, and what a refusal about a hook quotes:
	// the name the definition wrote, read back out of the image's own table.
	// A binding that does not exist renders as nothing, because the sentence
	// about it is not a question a host has.
	if got := img.bindingName(hook); got != "money" {
		t.Errorf("bindingName(%d) = %q, want the declared type's name",
			hook, got)
	}
	if got := img.bindingName(0); got != "" {
		t.Errorf("no binding rendered as %q, want nothing", got)
	}

	rt.RegisterOptions("money", func(_ context.Context, _ CaseContext, _ string) (
		[]string, error) {
		return []string{"an option list is not a normalizer"}, nil
	})
	if _, have := rt.w.hooks.normalizer(img, hook); have {
		t.Fatal("a path binding answered for a type binding")
	}
	if _, have := rt.w.hooks.provider(img, hook); have {
		t.Fatal("a type binding answered for a path binding")
	}
	// An unbound field is identity rather than a refusal: a named type
	// obliges nobody to write a hook, which is what lets a definition use
	// `money` for documentation alone.
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(9))))
	if got := draftValue(t, c, img, "application.fee"); !got.d.Equal(
		decimal.NewFromInt(9)) {
		t.Errorf("an unhooked custom type stored %s", got.d.String())
	}

	// The key a host would register against is the name the definition
	// spelled, which `bindingName` reads back out of the image's own table.
	rt.RegisterValidate(img.bindingName(hook),
		func(_ context.Context, _ CaseContext, _ string, v Value) (Value, error) {
			return Decimal(v.d.Add(decimal.NewFromInt(1))), nil
		})
	mustUpdate(t, c, base, Set("application.fee",
		Decimal(decimal.NewFromInt(9))))
	if got := draftValue(t, c, img, "application.fee"); !got.d.Equal(
		decimal.NewFromInt(10)) {
		t.Errorf("the name-keyed binding stored %s, want the normalized 10",
			got.d.String())
	}
}

func TestUpdateRefusesACaseItCannotInterpret(t *testing.T) {
	// The reload rule is the same for an edit as for a read, and stating it
	// twice is the point: writing into an ordinal space the caller's definition
	// no longer describes is worse than reading one, because it makes the
	// nonsense durable.
	rt, _, c := editCase(t, danaEdit)
	grown := strings.Replace(editCaseSrc,
		"        fee      : money default 1.00;",
		"        fee      : money default 1.00;\n        extra : string;", 1)
	next := &Definition{img: mustLowerSource(t, grown)}
	if err := rt.Replace(next); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, err := c.Update(ctx(), 0,
		Set("application.name", Text("x"))); !Failed(err, SchemaMismatch) {
		t.Errorf("an edit after a structural reload = %v, want "+
			"SCHEMA_MISMATCH", err)
	}

	// A compatible reload — one field's *default* changed, which moves no
	// ordinal and no path — and the write goes through the new definition.
	wider := strings.Replace(editCaseSrc,
		"        fee      : money default 1.00;",
		"        fee      : money default 2.00;", 1)
	compat := &Definition{img: mustLowerSource(t, wider)}
	if err := rt.Replace(compat); err != nil {
		t.Fatalf("Replace with a compatible definition: %v", err)
	}
	base := mustUpdate(t, c, 0, Set("application.name", Text("x")))

	// And the draft that outlived the reload is read under the definition the
	// case resolves to *now*, which is §3's reinterpretation applied to
	// uncommitted bytes: the same value, a new pair of eyes. §3.3 makes the
	// changed default a fact about *future* data, so the field still holds
	// what create put in it until an edit says otherwise.
	w, err := rt.w.cases.working(newRefuser("open", c.id), "dana", compat.img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if w.s.img != compat.img {
		t.Error("a draft that outlived a reload is interpreted through the " +
			"definition it was written under, not the one the case names")
	}
	r, ok := w.s.resolve("application.name")
	if !ok || !r.hasValue {
		t.Fatal("the reloaded image cannot reach what the old one wrote")
	}
	if v := r.cell.get(); v.s != "x" {
		t.Errorf("the reinterpreted draft holds %q, want the value the batch "+
			"wrote under the previous definition", v.s)
	}
	if w.rev != base {
		t.Errorf("the copy is at %d and the batch reported %d, want one number",
			w.rev, base)
	}
}
