package engine

// The §5.4 validation pass, and the four ways a hook can be involved in a
// commit.
//
// These are in package because binding a `Validate` hook still needs the
// seam RUNTIME §6 keeps unexported, and because the claims are about a
// function the host wrote: whether Submit asks it again at commit time, what
// it does when the answer has changed since the value was written, which
// cells it declines to ask about, and which half of the case it never
// mentions. `submit_test.go` covers what a host can see without any of that.
//
// One distinction runs through all of it and is the reason the pass exists in
// two shapes. A hook that *refuses* a value has answered a question about the
// content, so the answer belongs in the field-level list beside a missing
// field (§9). A hook that returns something its type cannot hold, or that
// rewrites what the row already says, has broken the contract this package
// stores on, and that is HOOK_FAILURE with no list at all: there is no field
// for the actor to go and fix.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// submitHookSrc is the fixture every claim below makes with one line: a
// custom decimal type for a type binding to attach to, two forms so a scope
// claim has something outside the submitted subtree, a required money field
// in each, a repeated group with a money member for the per-instance
// addresses, and a gated subtree whose field can hold a value nobody is
// currently asking about.
const submitHookSrc = `
types {
    money : decimal(14, 2);
}
roles {
    A {
        allow view, edit, submit application;
        allow view, edit, submit archive;
    }
}
forms {
    application {
        fee  : money required;
        gate : string;

        deep present[.gate == "y"] {
            owed : money;
        }

        rows multiple {
            stake : money required;
        }
    }

    archive {
        paid : money required;
    }
}
stages {
    draft { }
}
`

// submitHookCase is the fixture in the shapes the tests need: the runtime is
// what a hook is bound to, and the image is what a draft is read through.
func submitHookCase(t *testing.T) (*Runtime, *program.Program, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, submitHookSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), Actor{User: "dana", PrimaryRole: "A"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

// floor is the refusal a hook that changed its mind returns, and the sentence
// a `FieldProblem` is expected to carry through from host code.
var floor = errors.New("below the floor")

// refusesBinds a hook that refuses a money value below `min` and accepts
// every other one, which is the shape of the real thing: a rule the host can
// tighten without touching a definition.
func refuses(rt *Runtime, min int64) {
	rt.RegisterValidate("money",
		func(_ context.Context, _ CaseContext, _ string, v Value) (Value, error) {
			if v.d.LessThan(decimal.NewFromInt(min)) {
				return Value{}, floor
			}
			return v, nil
		})
}

// acceptsBinds a hook that records how often it was called and never refuses.
func accepts(rt *Runtime, seen *int) {
	rt.RegisterValidate("money",
		func(_ context.Context, _ CaseContext, _ string, v Value) (Value, error) {
			*seen++
			return v, nil
		})
}

// wholeDraft is the batch that makes the fixture's `application` form
// submittable: the required field, and one row whose required member has a
// value.
func wholeDraft(t *testing.T, c Case) uint64 {
	t.Helper()
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(3))),
		Add("application.rows"),
		Set("application.rows.1.stake", Decimal(decimal.NewFromInt(50))))
	return base
}

func TestSubmitAsksTheRulesAsTheyAreToday(t *testing.T) {
	rt, img, c := submitHookCase(t)
	accepts(rt, new(int))
	base := wholeDraft(t, c)

	// The host changes its mind: no new definition, no `Replace`, one function
	// with a stricter body. The value in the row was legal when it arrived and
	// is not now, which is DESIGN §3's "old content becomes invalid, not
	// incompatible" — and Submit is the only operation that acts on it, since
	// §5.2's `required` cannot see a value that is present and merely wrong.
	refuses(rt, 10)
	res, err := c.Submit(ctx(), "application", base)
	if !Failed(err, InvalidOp) {
		t.Fatalf("a submit of a value the rules now refuse = %v, want "+
			"INVALID_OPERATION", err)
	}
	if len(res.Invalid) != 1 {
		t.Fatalf("Invalid = %+v, want the one refused value", res.Invalid)
	}
	p := res.Invalid[0]
	if p.Path != "application.fee" || p.Code != invalidValue {
		t.Errorf("the problem is %q with code %q, want application.fee and "+
			"%q", p.Path, p.Code, invalidValue)
	}
	// The host's own sentence comes through, and so does the binding it was
	// declared against, because a host knows its function only by the words
	// its definition wrote (§6's reason a refusal quotes a name).
	if !strings.Contains(p.Msg, `"money"`) ||
		!strings.Contains(p.Msg, floor.Error()) {
		t.Errorf("the problem says %q, want the binding and the hook's own "+
			"reason", p.Msg)
	}

	// A value a hook refused is not a field nobody filled in, so the two
	// lists stay apart rather than one field appearing under both headings
	// and a client guessing which fix to offer.
	if len(res.Missing) != 0 {
		t.Errorf("Missing = %q beside an invalid value, want nothing",
			res.Missing)
	}
	if res.Revision != 0 {
		t.Errorf("an invalid submit reached revision %d, want 0",
			res.Revision)
	}
	// Nothing durable happened: no commit, and the draft exactly as it was
	// presented — a refused submit does not normalize, clear or re-validate
	// its way into the copy (§9's no persistent effect).
	hist, err := c.w.cases.history(ctx(), newRefuser("open", c.id), 1, 0)
	if err != nil || len(hist) != 0 {
		t.Errorf("the refused submit left %d history entries (%v), want 0",
			len(hist), err)
	}
	if got := draftValue(t, c, img, "application.fee"); !got.Decimal.Equal(
		decimal.NewFromInt(3)) {
		t.Errorf("the draft holds %s after the refusal, want the value the "+
			"actor wrote", got.Decimal.String())
	}

	// The same draft, under rules the value satisfies, commits — which is the
	// half of this test that says the refusal was about the *rules* and not
	// about a copy the previous call had disturbed.
	refuses(rt, 1)
	if out, err := c.Submit(ctx(), "application", base); err != nil ||
		out.Revision != 1 {
		t.Errorf("submitting the same draft under looser rules = %+v (%v), "+
			"want revision 1", out, err)
	}
}

func TestSubmitNeverAsksAHookAboutNothing(t *testing.T) {
	rt, _, c := submitHookCase(t)
	var seen int
	accepts(rt, &seen)
	// One money cell and no rows, so the count below is about this field
	// alone: a fixture with a second value in it would prove nothing about
	// the one that has none.
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(3))))

	// Clearing the field is a change to make deliberately, and §5.2 counts it
	// as empty rather than absent-but-known. The walk has an answer for it
	// already; the hook does not, because E6 makes a question about no value
	// unsatisfied rather than invalid.
	mustUpdate(t, c, base, Empty("application.fee"))
	seen = 0

	res, err := c.Submit(ctx(), "application", base+1)
	if !Failed(err, InvalidOp) {
		t.Fatalf("a submit with a cleared required field = %v, want a "+
			"refusal", err)
	}
	if seen != 0 {
		t.Errorf("the hook ran %d times over an unset value, want 0: an "+
			"empty field reported as both missing and invalid is one field "+
			"a client cannot render", seen)
	}
	if strings.Join(res.Missing, " ") != "application.fee" {
		t.Errorf("Missing = %q, want the one cleared field", res.Missing)
	}
	if len(res.Invalid) != 0 {
		t.Errorf("Invalid = %+v, want the cleared field reported once, as a "+
			"gap", res.Invalid)
	}
}

func TestSubmitRefusesAHookThatRewroteAStoredValue(t *testing.T) {
	rt, _, c := submitHookCase(t)
	base := wholeDraft(t, c)

	// A normalizer is a function the engine already applied when the value
	// arrived, so the stored value is its own output. Run it again and it
	// must say the same thing: a hook that adds one every time it is asked
	// has a bug, and the alternative to refusing it is committing a value the
	// actor never presented.
	rt.RegisterValidate("money",
		func(_ context.Context, _ CaseContext, _ string, v Value) (Value, error) {
			return Decimal(v.d.Add(decimal.NewFromInt(1))), nil
		})
	_, err := c.Submit(ctx(), "application", base)
	if !Failed(err, HookFailure) {
		t.Fatalf("a hook that is not a fixed point = %v, want HOOK_FAILURE",
			err)
	}
	if !strings.Contains(err.Error(), "rewrote") {
		t.Errorf("the refusal says %q, want it to name what the hook did",
			err)
	}
	// A defect in host code is not a problem about a field: there is nothing
	// for the actor to fix, so the result carries no list to mislead with,
	// and the caller gets the zero value beside the error.
	if !strings.Contains(err.Error(), `"money"`) {
		t.Error("the refusal did not name the binding it is about")
	}
	hist, err := c.w.cases.history(ctx(), newRefuser("open", c.id), 1, 0)
	if err != nil || len(hist) != 0 {
		t.Errorf("a hook failure left %d history entries (%v), want 0",
			len(hist), err)
	}
}

func TestSubmitRefusesAHookThatReturnedAnotherType(t *testing.T) {
	rt, _, c := submitHookCase(t)
	base := wholeDraft(t, c)

	// The other misbehaviour: a value the field's type cannot hold at all.
	// The row would have to store something §3.3's bounds test would refuse,
	// so the type is the last word about a payload whoever produced it — and
	// the blame is the host's rather than the caller's, which is why this is
	// HOOK_FAILURE and not the INVALID_OPERATION a refused value gets.
	rt.RegisterValidate("money",
		func(_ context.Context, _ CaseContext, _ string, _ Value) (Value, error) {
			return Text("an option list is not an amount"), nil
		})
	res, err := c.Submit(ctx(), "application", base)
	if !Failed(err, HookFailure) {
		t.Fatalf("a hook returning the wrong class = %v, want HOOK_FAILURE",
			err)
	}
	if !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("the refusal says %q, want it to say what broke", err)
	}
	if len(res.Invalid) != 0 || res.Revision != 0 {
		t.Errorf("a hook failure reported %+v, want no lists and no "+
			"revision", res)
	}
}

func TestSubmitAsksTheSubmittedFormAndNothingElse(t *testing.T) {
	rt, img, c := submitHookCase(t)
	// The rules as they were when the values arrived: every one of them was
	// legal at the moment it was written, which is the whole premise of
	// re-asking at commit time rather than trusting what the row holds.
	refuses(rt, 0)

	// Four money cells hold a value, in three different shapes: the form's
	// own field, two rows of a repeated group, and a field behind a guard
	// that does not hold. The fifth is in another form entirely.
	base := mustUpdate(t, c, 0,
		Set("application.fee", Decimal(decimal.NewFromInt(3))),
		Add("application.rows"),
		Set("application.rows.1.stake", Decimal(decimal.NewFromInt(1))),
		Add("application.rows"),
		Set("application.rows.2.stake", Decimal(decimal.NewFromInt(2))),
		Set("application.deep.owed", Decimal(decimal.NewFromInt(7))),
		Set("archive.paid", Decimal(decimal.NewFromInt(9))))

	// The rules as they are now.
	refuses(rt, 100)

	// §6.3's scope is one form's subtree, and this is where it costs
	// something to get wrong: a pass over the whole working state would
	// refuse a submit of `application` because of a draft the actor was
	// committing nothing, and would report a field its own definition has
	// stopped asking about.
	res, err := c.Submit(ctx(), "application", base)
	if !Failed(err, InvalidOp) {
		t.Fatalf("a submit of refused values = %v, want INVALID_OPERATION",
			err)
	}
	want := "application.fee application.rows.1.stake " +
		"application.rows.2.stake"
	if got := problemPaths(res.Invalid); got != want {
		t.Errorf("Invalid = %q, want %q: the pruned subtree and the other "+
			"form's draft do not belong to this commit", got, want)
	}

	// The same draft, committed form by form: `archive`'s refusal names its
	// own field and none of the ones that just failed on the other side of
	// the case.
	res, err = c.Submit(ctx(), "archive", base)
	if !Failed(err, InvalidOp) {
		t.Fatalf("submitting the other form = %v, want a refusal of its own",
			err)
	}
	if got := problemPaths(res.Invalid); got != "archive.paid" {
		t.Errorf("Invalid = %q, want only archive.paid", got)
	}

	// And the pruned cell is still there, unasked about and unwritten: the
	// pass neither refused it nor repaired it, because a guard's answer is
	// not a delete (§5.1's reason data behind a false guard survives).
	if got := draftValue(t, c, img, "application.deep.owed"); !got.Decimal.Equal(
		decimal.NewFromInt(7)) {
		t.Errorf("the subtree outside the submitted form holds %s, want the "+
			"value it was given", got.Decimal.String())
	}
}

// problemPaths renders a list the way a missing list is rendered in
// `submit_test.go`: one string per entry, so a failure says what was in the
// result rather than what its fields are.
func problemPaths(ps []FieldProblem) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Path)
	}
	return strings.Join(out, " ")
}
