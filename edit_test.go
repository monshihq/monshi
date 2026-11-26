package monshi_test

// The edit operation seen from outside the module: what a host can observe
// without reaching into a state, which is refusals, the working revision, the
// paths a batch created, and what an abandoned batch did *not* do.
//
// Reading a value back is not in this list.

import (
	"strings"
	"sync"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

// editSrc is the smallest definition that states each claim below with one
// line: a form an Applicant owns, a subtree the same role is denied, a
// role-typed field, an enum, a decimal with a scale, a repeated group, and a
// signature field the engine will not let `Set` write at all.
const editSrc = `
types {
    legal : enum(PERSON, COMPANY);
    money : decimal(14, 2);
}
roles {
    Applicant {
        allow view, edit, submit application;
        deny edit application.internal;
    }
    Manager   { allow view all; allow edit application.internal; }
}
forms {
    application {
        kind       : legal required;
        name       : string required;
        fee        : money default 0.00;
        reviewer   : Manager;
        signed     : signature(Applicant);

        internal present[.kind == COMPANY] {
            risk : integer;
        }

        holders multiple {
            who  : string required;
            what : money required;
        }
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`

// applicant is the role the fixture hands the whole form to and denies one
// subtree of it; watchman holds the other half; stranger holds no role.
var (
	applicant = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}
	watchman  = monshi.Actor{User: "elliot", PrimaryRole: "Manager"}
	stranger  = monshi.Actor{User: "elliot"}
)

func openCase(t *testing.T, src string, actor monshi.Actor) monshi.Case {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "edit", src)
	c, err := rt.Create(ctx(), "edit", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return c
}

func TestUpdateEditsTheWorkingCopy(t *testing.T) {
	c := openCase(t, editSrc, applicant)

	res, err := c.Update(ctx(), 0,
		monshi.Set("application.name", monshi.Text("Monshi Ltd")),
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("1250.05"))))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// One batch is one step of the copy's revision, not three: the atomicity is
	// what makes "province and city are evaluated together" mean the pair
	// arrived as one state.
	if res.Working != 1 {
		t.Errorf("Working = %d, want 1 for a single accepted batch", res.Working)
	}
	// Nothing about the *case* moved: an edit is not a commit,
	// so the committed revision the handle reports is still 0.
	if rev, err := c.Revision(ctx()); err != nil || rev != 0 {
		t.Errorf("Revision = %d, %v, want an edit to leave it at 0", rev, err)
	}

	// The next batch presents what this one reported. A host that kept the
	// number from the response, not from its own counting, is the one that
	// survives a second session.
	if _, err := c.Update(ctx(), res.Working,
		monshi.Empty("application.name")); err != nil {
		t.Errorf("Update at the reported revision: %v", err)
	}
}

func TestUpdateRefusesWhatItCannotCarryOut(t *testing.T) {
	// Each case is one claim about the boundary, and the actor it names is
	// part of the claim: the same path is allowed to one actor and refused
	// to another, and refused rather than silently dropped.
	cases := []struct {
		name  string
		actor monshi.Actor
		base  uint64
		edits []monshi.Edit
		want  monshi.Category
		says  string
	}{
		{"a denied subtree", applicant, 0,
			[]monshi.Edit{monshi.Set("application.internal.risk", monshi.Int(7))},
			monshi.AccessDenied, "not granted"},
		{"a field no role reaches", stranger, 0,
			[]monshi.Edit{monshi.Set("application.name", monshi.Text("x"))},
			monshi.AccessDenied, "not granted"},
		{"the wrong class", applicant, 0,
			[]monshi.Edit{monshi.Set("application.name", monshi.Int(3))},
			monshi.InvalidOp, "holds string"},
		{"a value with no constructor", applicant, 0,
			[]monshi.Edit{monshi.Set("application.name", monshi.Value{})},
			monshi.InvalidOp, "no value at all"},
		{"a tag from no enum", applicant, 0,
			[]monshi.Edit{monshi.Set("application.kind", monshi.Tag("TRUST"))},
			monshi.InvalidOp, "is declared"},
		{"a signature written by hand", applicant, 0,
			[]monshi.Edit{monshi.Set("application.signed", monshi.Text("ok"))},
			monshi.InvalidOp, "only sign writes one"},
		{"an identity in a text field", applicant, 0,
			[]monshi.Edit{monshi.Set("application.name", monshi.User("dana"))},
			monshi.InvalidOp, "holds string"},
		{"a text in a role field", applicant, 0,
			[]monshi.Edit{monshi.Set("application.reviewer", monshi.Text("dana"))},
			monshi.InvalidOp, "holds an identity"},
		{"nobody as an identity", applicant, 0,
			[]monshi.Edit{monshi.Set("application.reviewer", monshi.User(""))},
			monshi.InvalidOp, "names nobody"},
		{"a group", applicant, 0,
			[]monshi.Edit{monshi.Set("application.internal", monshi.Text("x"))},
			monshi.InvalidOp, "is a group"},
		{"a collection without an instance", applicant, 0,
			[]monshi.Edit{monshi.Set("application.holders.1.who",
				monshi.Text("no such row"))},
			monshi.NotFound, "reached by path"},
		{"no path at all", applicant, 0,
			[]monshi.Edit{monshi.Set("application.nope", monshi.Text("x"))},
			monshi.NotFound, "reached by path"},
		{"an edit that names nothing", applicant, 0,
			[]monshi.Edit{monshi.Empty("")}, monshi.InvalidOp, "no path"},
		{"an empty batch", applicant, 0, nil,
			monshi.InvalidOp, "names no edit"},
		{"add to a fixed group", applicant, 0,
			[]monshi.Edit{monshi.Add("application.internal")},
			monshi.InvalidOp, "not a multiple declaration"},
		{"add to a form", applicant, 0,
			[]monshi.Edit{monshi.Add("application")},
			monshi.InvalidOp, "not a multiple declaration"},
		{"remove a collection itself", applicant, 0,
			[]monshi.Edit{monshi.Remove("application.holders")},
			monshi.InvalidOp, "not an instance"},
		{"remove a declaration", applicant, 0,
			[]monshi.Edit{monshi.Remove("application.name")},
			monshi.InvalidOp, "not a multiple declaration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := openCase(t, editSrc, tc.actor)
			_, err := c.Update(ctx(), tc.base, tc.edits...)
			if !monshi.Failed(err, tc.want) {
				t.Fatalf("Update = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("refusal says %q, want it to mention %q", err, tc.says)
			}
		})
	}

	t.Run("a decimal past its scale", func(t *testing.T) {
		// The bounds belong to the type.
		c := openCase(t, editSrc, applicant)
		_, err := c.Update(ctx(), 0, monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("1.2345"))))
		if !monshi.Failed(err, monshi.InvalidOp) {
			t.Fatalf("an over-scaled decimal = %v, want INVALID_OPERATION", err)
		}
		if !strings.Contains(err.Error(), "does not hold") {
			t.Errorf("refusal says %q", err)
		}
		// Two decimals inside the bounds, including a scale of exactly the
		// declared one and a value that needs none. Each lands in its own
		// accepted batch, so each moves the copy's revision: presenting the
		// number the last batch reported is the only way a host is allowed to
		// stay current, and it is what makes this loop honest.
		base := uint64(0)
		for _, d := range []string{"0.00", "-999999999999.99", "7", "7.00"} {
			res, err := c.Update(ctx(), base, monshi.Set("application.fee",
				monshi.Decimal(decimal.RequireFromString(d))))
			if err != nil {
				t.Errorf("a legal decimal %s was refused: %v", d, err)
				continue
			}
			base = res.Working
		}
	})
}

// An abandoned batch is not a partial batch: the guarantee is that a
// refused operation has no persistent effect, and the way a host can see
// that without a view is that its revision did not move and its next
// presentation of the same number is still accepted.
func TestRefusedUpdateSavesNothing(t *testing.T) {
	c := openCase(t, editSrc, applicant)

	// The second edit of this batch is the one that is denied, so the first
	// is already applied to the candidate when the batch fails.
	_, err := c.Update(ctx(), 0,
		monshi.Set("application.name", monshi.Text("kept?")),
		monshi.Set("application.internal.risk", monshi.Int(9)))
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Fatalf("a denied edit = %v, want ACCESS_DENIED", err)
	}

	// Same base, accepted: had the batch saved anything, the copy would be at
	// revision 1 and this would be STALE_REVISION.
	if _, err := c.Update(ctx(), 0,
		monshi.Set("application.name", monshi.Text("Monshi Ltd"))); err != nil {
		t.Fatalf("the batch after a refused one: %v", err)
	}

	// A whole batch of creations is abandoned the same way: the instance this
	// `Add` made is gone with it, because the state it lived in was a copy.
	_, err = c.Update(ctx(), 1,
		monshi.Add("application.holders"),
		monshi.Set("application.signed", monshi.Text("forged")))
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("a batch ending in a signature = %v, want INVALID_OPERATION",
			err)
	}
	res, err := c.Update(ctx(), 1, monshi.Add("application.holders"))
	if err != nil {
		t.Fatalf("the add after an abandoned batch: %v", err)
	}
	// One new row, and the ID continues where the abandoned batch would have
	// left it: the allocator is part of the state that was discarded, so a
	// host never sees a gap it has to explain — and never sees the same ID
	// twice either.
	if len(res.Created) != 1 {
		t.Fatalf("Created = %v, want one path", res.Created)
	}
	if !strings.HasPrefix(res.Created[0], "application.holders.") {
		t.Errorf("Created[0] = %q, want the caller's path plus a new ID",
			res.Created[0])
	}
}

// Add allocates the ID. Fill the row in a subsequent batch using its returned
// path.
func TestAddThenSetByTheIDItReports(t *testing.T) {
	c := openCase(t, editSrc, applicant)

	res, err := c.Update(ctx(), 0, monshi.Add("application.holders"),
		monshi.Add("application.holders"))
	if err != nil {
		t.Fatalf("two adds: %v", err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("Created = %v, want two paths", res.Created)
	}
	first, second := res.Created[0], res.Created[1]
	if first == second {
		t.Fatalf("both adds reported %q: instance IDs are never reused or shared",
			first)
	}
	if res.Working != 1 {
		t.Errorf("Working = %d, want one revision for two instances", res.Working)
	}

	// Now each row is addressable in the language's own path syntax.
	who := first + ".who"
	what := first + ".what"
	if _, err := c.Update(ctx(), res.Working,
		monshi.Set(who, monshi.Text("Dana")),
		monshi.Set(what, monshi.Decimal(decimal.NewFromInt(500)))); err != nil {
		t.Fatalf("filling the instance in: %v", err)
	}
	// A member of an instance that was never created is the unknown path it
	// is, not a near miss on the row that was.
	if _, err := c.Update(ctx(), 2,
		monshi.Set(second+".nonsense", monshi.Text("x"))); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("a member that is not declared = %v, want NOT_FOUND", err)
	}
	// Dropping the other row works, and dropping it twice is NOT_FOUND the
	// second time: stable IDs are not reused, so a host cannot be told
	// it removed a different instance.
	if _, err := c.Update(ctx(), 2, monshi.Remove(second)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := c.Update(ctx(), 3, monshi.Remove(second)); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("removing an instance twice = %v, want NOT_FOUND", err)
	}
}

// The reason the guard exists: two sessions of one actor are one working copy,
// and the second must not silently overwrite the first.
func TestConcurrentSessionsShareOneGuard(t *testing.T) {
	c := openCase(t, editSrc, applicant)

	const racers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[uint64]int{}
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := c.Update(ctx(), 0,
				monshi.Set("application.name", monshi.Text("session")))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !monshi.Failed(err, monshi.StaleRevision) {
					t.Errorf("session %d: %v, want STALE_REVISION or success",
						i, err)
				}
				seen[0]++
				return
			}
			// Every winner would report revision 1 only if it saved at base 0,
			// which the store's guard allows exactly once.
			seen[1]++
		}(i)
	}
	wg.Wait()

	if seen[0]+seen[1] != racers {
		t.Fatalf("%d of %d sessions reported", seen[0]+seen[1], racers)
	}
	if seen[0] == racers {
		t.Error("no session of eight got its edit in: the guard refuses everything")
	}
	// The number that moved is the point: one copy advanced by one, not by
	// however many sessions happened to arrive.
	rev, err := c.Update(ctx(), 1, monshi.Empty("application.name"))
	if err != nil {
		t.Fatalf("updating at the revision the guard left: %v", err)
	}
	if rev.Working != 2 {
		t.Errorf("Working = %d, want 2 after the copy moved once and this moved again",
			rev.Working)
	}
}

// A handle opened by `Open` edits the same copy the creator made (the one
// working state per case and user), and the revision travels with the copy
// rather than with the handle.
func TestSessionsOfOneActorShareOneWorkingCopy(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "edit", editSrc)
	first, err := rt.Create(ctx(), "edit", applicant)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := first.Update(ctx(), 0,
		monshi.Set("application.name", monshi.Text("saved by session one"))); err != nil {
		t.Fatalf("Update: %v", err)
	}

	second, err := rt.Open(ctx(), first.ID(), applicant)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Presenting 0 here is a stale claim, and that is the assertion: the copy
	// this actor's first session wrote is the copy this handle reads, so the
	// revision it must present is 1.
	if _, err := second.Update(ctx(), 0,
		monshi.Set("application.name", monshi.Text("overwritten"))); !monshi.Failed(err,
		monshi.StaleRevision) {
		t.Errorf("a second session at the first's revision = %v, want STALE_REVISION",
			err)
	}
	if _, err := second.Update(ctx(), 1,
		monshi.Set("application.name", monshi.Text("continued"))); err != nil {
		t.Errorf("a second session at the shared revision: %v", err)
	}

	// A second actor gets a copy of its own.
	mgr, err := rt.Open(ctx(), first.ID(), watchman)
	if err != nil {
		t.Fatalf("Open as Manager: %v", err)
	}
	res, err := mgr.Update(ctx(), 0,
		monshi.Set("application.internal.risk", monshi.Int(4)))
	if err != nil {
		t.Fatalf("Manager editing what the roles block grants it: %v", err)
	}
	if res.Working != 1 {
		t.Errorf("Manager's fresh copy reported revision %d, want 1", res.Working)
	}
	// And `dana`'s copy is still where dana left it: one actor's save does
	// not move another's numbering, because it does not touch their data.
	if _, err := first.Update(ctx(), 2,
		monshi.Set("application.name", monshi.Text("still mine"))); err != nil {
		t.Errorf("the Applicant at its own revision: %v", err)
	}
}
