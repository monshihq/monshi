package engine

// In-package tests for the role sources and the store seam (DESIGN §4.1,
// §4.3, §5; RUNTIME §5.1). What a host observes — that a case opens, that a
// reload is accepted or refused — is asserted externally in runtime_test.go.
// This file asserts the parts a host cannot reach: which ordinals a role
// set contains, what the store's revision guards refuse, and which
// definition changes count as structural.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/monshihq/monshi/schema"
)

// mustLowerSource compiles a small definition for the store and role tests.
// The §7 example covers the permission shape; these sources exist to make a
// *change* to a definition legible in three lines.
func mustLowerSource(tb testing.TB, src string) *program.Program {
	tb.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		tb.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		tb.Fatalf("definition does not compile: %s", join(diagnostics(s.Errors)))
	}
	img, err := program.Compile("test", s)
	if err != nil {
		tb.Fatalf("lower: %v", err)
	}
	return img
}

// put writes one base-row value the way an edit primitive will (§5.3).
// Tests use it to give a role-typed field a holder before there is an
// editor to do it.
func put(t *testing.T, img *program.Program, s *state, path string, v program.Cell) {
	t.Helper()
	c, ok := s.cellOf(slotByPath(t, img, path))
	if !ok {
		t.Fatalf("%s has no cell in the base row", path)
	}
	c.set(v)
}

func held(t *testing.T, img *program.Program, path string) program.NodeID {
	t.Helper()
	return slotByPath(t, img, path)
}

func TestRolesHeldCombinesPrimaryAndCommittedField(t *testing.T) {
	img := lowerExample(t)
	manager, ok := img.RoleByName("Manager")
	if !ok {
		t.Fatal("the §7 example declares no Manager role")
	}
	applicant, ok := img.RoleByName("Applicant")
	if !ok {
		t.Fatal("the §7 example declares no Applicant role")
	}

	s := newState(img)
	s.applyDefaults()

	// Nothing assigned: the primary role is the only source, and an
	// unresolvable name confers nothing rather than failing (P1).
	if got := rolesHeld(img, Actor{User: "dana"}, s); len(got) != 0 {
		t.Errorf("with no value and no primary role, rolesHeld = %v, want none", got)
	}
	ghost := Actor{User: "dana", PrimaryRole: "Ghost"}
	if got := rolesHeld(img, ghost, s); len(got) != 0 {
		t.Errorf("an undeclared primary role conferred %v, want nothing", got)
	}

	// The committed value of the role-typed field names dana, who is
	// therefore a Manager (T4) with no primary role asserted.
	put(t, img, &s, "application.reviewer", program.Cell{Set: true, Text: "dana"})
	dana := Actor{User: "dana"}
	if got := rolesHeld(img, dana, s); len(got) != 1 || got[0] != manager {
		t.Errorf("dana via the committed field: rolesHeld = %v, want [%v]",
			got, manager)
	}

	// Another user's identity confers nothing on dana.
	if got := rolesHeld(img, Actor{User: "elliot"}, s); len(got) != 0 {
		t.Errorf("a name that does not match conferred %v, want none", got)
	}

	// Both sources, and a role held twice is one role: the mask fold would
	// not mind a duplicate, but this list is what §8's permission resolve
	// walks, so it must not grow per request.
	both := rolesHeld(img, Actor{User: "dana", PrimaryRole: "Applicant"}, s)
	if len(both) != 2 || both[0] != applicant || both[1] != manager {
		t.Errorf("primary plus field: rolesHeld = %v, want [Applicant Manager]", both)
	}
	twice := rolesHeld(img, Actor{User: "dana", PrimaryRole: "Manager"}, s)
	if len(twice) != 1 || twice[0] != manager {
		t.Errorf("the same role from both sources: rolesHeld = %v, want one entry", twice)
	}

	// An empty value is not an identity, so it matches nobody — including an
	// actor whose UserID is the empty string, which is "nobody".
	put(t, img, &s, "application.reviewer", program.Cell{Set: true, Text: ""})
	if got := rolesHeld(img, Actor{User: ""}, s); len(got) != 0 {
		t.Errorf("a stored empty identity conferred %v, want none", got)
	}

	// A role-typed field inside a collection confers nothing: it has no
	// single value to compare, and guessing "any instance" would make
	// authority depend on a row the actor never named (§5.1, E2).
	nested := mustLowerSource(t, `
roles {
    Applicant { allow view application; }
    Lead      { allow view, edit application; }
}
forms {
    application {
        owner : Lead;
        team multiple {
            member : Lead;
        }
    }
}
stages { start { accept finish; } finish { } }
`)
	ns := newState(nested)
	ns.applyDefaults()
	if got := rolesHeld(nested, Actor{User: "dana"}, ns); len(got) != 0 {
		t.Errorf("the base-row owner field confers %v before anything is set", got)
	}
	// The member field is unreachable as a cell, which is the property the
	// roles loop relies on; §5.1's addressing test says the same thing about
	// rows, so this only has to hold here.
	if _, ok := ns.cellOf(held(t, nested, "application.team.member")); ok {
		t.Error("a collection member has a base-row cell, so the role scan would " +
			"read one value for many instances")
	}
}

// TestCanEditFollowsTheCommittedRoleAssignment re-derives Appendix A's
// permission case through the path the operations will use: Manager may edit
// `application.internal`, Applicant may not, and an actor holding both roles
// is denied — "deny overrides allow across all effective roles".
func TestCanEditFollowsTheCommittedRoleAssignment(t *testing.T) {
	img := lowerExample(t)
	risk := held(t, img, "application.internal.risk_score")
	note := held(t, img, "application.note")
	draft, ok := img.StageByName("draft")
	if !ok {
		t.Fatal("the §7 example declares no draft stage")
	}

	s := newState(img)
	s.applyDefaults()
	dana := Actor{User: "dana"}
	applicant := Actor{User: "dana", PrimaryRole: "Applicant"}

	// A plain applicant may edit a note and may not reach the internal
	// subtree: the §5.6-P5 override, from the precomputed masks.
	if !canEdit(img, applicant, s, draft, note) {
		t.Error("Applicant cannot edit application.note, which §7 allows")
	}
	if canEdit(img, applicant, s, draft, risk) {
		t.Error("Applicant edits application.internal.risk_score, which §7 denies")
	}

	// A Manager holds the same allow with no deny, so the identical slot is
	// writable — and it is writable *because of a committed value*, not a
	// draft one.
	put(t, img, &s, "application.reviewer", program.Cell{Set: true, Text: "dana"})
	if !canEdit(img, dana, s, draft, risk) {
		t.Error("the committed reviewer does not reach the field their role grants")
	}

	// Both roles, both answers carried, and the denial wins.
	if canEdit(img, applicant, s, draft, risk) {
		t.Error("holding Manager erased Applicant's denial on risk_score")
	}
}

// The working copy is a candidate: §6.2's evaluation reads it, and
// invariant 6 says access control derives from committed assignments. The
// two states are the same shape, so the only thing keeping a draft from
// granting authority is *which state the operation hands this function*.
// That is the line milestone 2's edits must not cross, so it is asserted as
// a property of the pair rather than left to a comment in authority.go.
func TestDraftValueDoesNotReachUntilCommitted(t *testing.T) {
	img := lowerExample(t)
	risk := held(t, img, "application.internal.risk_score")
	draft, _ := img.StageByName("draft")

	commit := newState(img)
	commit.applyDefaults()
	work := commit.clone()

	// dana's draft names them reviewer. Asked about the *committed* state,
	// the answer is what it was before the draft existed.
	put(t, img, &work, "application.reviewer", program.Cell{Set: true, Text: "dana"})
	if canEdit(img, Actor{User: "dana"}, commit, draft, risk) {
		t.Error("a committed state that never held the value granted the role")
	}
	// The candidate state does hold it, which is why operations take the
	// other state for authority and this one for content.
	if !canEdit(img, Actor{User: "dana"}, work, draft, risk) {
		t.Fatal("the candidate state grants nothing, so this test cannot claim " +
			"the committed one is what operations must ask")
	}
}

func TestWorkingCopyIsPerActorAndSharedBySessions(t *testing.T) {
	img := lowerExample(t)
	st := newCaseStore()
	id, err := st.create(ctx(), newRefuser(open_, 0),
		&Definition{img: img})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	note := held(t, img, "application.note")

	// First access creates the copy from the committed state, at the case's
	// current revision (DESIGN §5: a working state is created when the user
	// opens the case).
	first, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	if first.base != 0 || first.rev != 0 {
		t.Errorf("new copy: base %d rev %d, want 0 and 0", first.base, first.rev)
	}

	// An edit that was never saved is nobody's business but the caller's:
	// what `working` handed out is a candidate, so the second session still
	// sees the field empty. This is invariant 1's sibling — nothing an
	// operation discarded becomes visible to another one.
	c, ok := first.s.cellOf(note)
	if !ok {
		t.Fatal("application.note has no cell")
	}
	c.set(program.Cell{Set: true, Text: "drafted by session one"})
	unsaved, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil {
		t.Fatalf("working for a second session: %v", err)
	}
	if got, _ := unsaved.s.cellOf(note); got.get().Set {
		t.Error("an edit that was never saved is visible to another session")
	}

	// Saving under the guard publishes it, and every later session of that
	// actor gets the same copy: invariant 4 expressed as one stored value.
	_, err = st.saveWorking(ctx(), newRefuser(update, id), "dana", 0, first, 0)
	if err != nil {
		t.Fatalf("saveWorking: %v", err)
	}
	second, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil {
		t.Fatalf("working after a save: %v", err)
	}
	cell, ok := second.s.cellOf(note)
	if !ok {
		t.Fatal("application.note has no cell")
	}
	if got := cell.get(); !got.Set || got.Text != "drafted by session one" {
		t.Errorf("second session sees %+v, want the saved draft", got)
	}

	// A different actor gets their own copy, which does not carry it.
	other, err := st.working(ctx(), newRefuser(open_, id), "elliot", img)
	if err != nil {
		t.Fatalf("working for another actor: %v", err)
	}
	if got, _ := other.s.cellOf(note); got.get().Set {
		t.Error("one actor's draft leaked into another actor's working copy")
	}

	// Writing to the value a caller holds after saving it cannot reach the
	// store: what was stored is a copy of what was saved.
	cell.set(program.Cell{Set: true, Text: "edited after the save"})
	again, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil {
		t.Fatalf("working after a post-save edit: %v", err)
	}
	if got, _ := again.s.cellOf(note); got.get().Text == "edited after the save" {
		t.Error("the store kept a live reference to a caller's candidate")
	}
}

func TestWorkingAndCommitGuardsRefuseStaleWrites(t *testing.T) {
	img := lowerExample(t)
	st := newCaseStore()
	id, err := st.create(ctx(), newRefuser(open_, 0),
		&Definition{img: img})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	// A second actor is reading the same case, so the commit below has
	// somebody to be stale for. Opening is what records the base revision a
	// copy descends from, and nothing else here touches this one.
	if _, err := st.working(ctx(), newRefuser(open_, id), "nina", img); err != nil {
		t.Fatalf("working: %v", err)
	}
	// The copy arrives at revision 0 and the store is what moves it: a save
	// reports the number the copy now carries, and the fields the caller
	// holds on the state it was handed are not authority (§3.1's division,
	// drawn about a working revision rather than a case ID).
	w.rev = 41
	w.base = 41
	rev, err := st.saveWorking(ctx(), newRefuser(update, id), "dana", 0, w, 0)
	if err != nil {
		t.Fatalf("first guarded save: %v", err)
	}
	if rev != 1 {
		t.Errorf("save reported working revision %d, want 1", rev)
	}
	back, err := st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil || back.rev != 1 || back.base != 0 {
		t.Errorf("stored copy is rev %d base %d, want 1 and 0 (said %v)",
			back.rev, back.base, err)
	}
	// Presenting the revision the copy was read at, after it moved, is what
	// §6.2 refuses rather than resolving.
	_, err = st.saveWorking(ctx(), newRefuser(update, id), "dana", 0, w, 0)
	if !Failed(err, StaleRevision) {
		t.Fatalf("stale working save = %v, want STALE_REVISION", err)
	}
	if !strings.Contains(err.Error(), "at revision 1, not 0") {
		t.Errorf("stale refusal says %q, want both revisions named", err)
	}
	// The store's guards report in the voice of the operation that asked,
	// not the store's own: a host reading a log line for a refused commit
	// has to learn which of its calls produced it, and `check` is one
	// function with four callers.
	var ce *CaseError
	if !errors.As(err, &ce) || ce.Op != update {
		t.Errorf("a stale save refused with op %q, want %q", ce.Op, update)
	}

	// Writing to a copy nobody opened is a caller bug.
	if _, err := st.saveWorking(ctx(), newRefuser(update, id), "nobody", 0, w,
		0); !Failed(err,
		InvalidOp) {
		t.Errorf("save to an unopened copy = %v, want INVALID_OPERATION", err)
	}

	// The committed snapshot moves under the same kind of guard, and a
	// successful commit is the one thing that advances the revision
	// (invariant 2: a new case is at 0, so the first commit is revision 1).
	rec, err := st.load(ctx(), newRefuser(open_, id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.revision != 0 {
		t.Fatalf("a created case is at revision %d, want 0", rec.revision)
	}
	by := committedBy{by: "dana", op: opSubmit, changes: []commitChange{
		{kind: chgValue, slot: 1, v: program.Cell{Set: true, Text: "x"}},
	}}
	_, err = st.commit(ctx(), newRefuser(submit, id), img, 7, rec.commit, by)
	if !Failed(err, StaleRevision) {
		t.Errorf("commit over revision 0 as revision 7 = %v, want STALE_REVISION", err)
	} else if errors.As(err, &ce) && ce.Op != submit {
		t.Errorf("a stale commit refused with op %q, want %q", ce.Op, submit)
	}
	// A refused commit appended nothing: the history's length is what a
	// revision *is*, so an entry written by an operation that did not happen
	// would be a case counting a commit it never made.
	h, err := st.history(ctx(), newRefuser(open_, id), 1, 0)
	if err != nil || len(h) != 0 {
		t.Errorf("history after a refused commit = %d entries (%v), want 0",
			len(h), err)
	}
	next, err := st.commit(ctx(), newRefuser(submit, id), img, 0, rec.commit, by)
	if err != nil {
		t.Fatalf("guarded commit: %v", err)
	}
	if next.rec.revision != 1 {
		t.Errorf("committed revision = %d, want 1", next.rec.revision)
	}

	// The other two writes §6.3 requires to be consistent with the snapshot
	// swap: the entry that says what landed, and the submitter's copy rebased
	// onto it. The entry carries the caller's own diff and identity — the
	// store numbers it, it does not invent it.
	h, err = st.history(ctx(), newRefuser(open_, id), 1, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h) != 1 || h[0].revision != 1 || h[0].by != "dana" ||
		h[0].op != opSubmit || len(h[0].changes) != 1 {
		t.Errorf("history = %+v, want one entry at revision 1 by dana", h)
	}
	// The submitter's content is not rewritten by its own commit — §6.3 is
	// explicit that a submit of one form leaves other drafts alone — so only
	// `base` moves, and `rev` stays where the client left it.
	back, err = st.working(ctx(), newRefuser(open_, id), "dana", img)
	if err != nil || back.base != 1 || back.rev != 1 {
		t.Errorf("submitter's copy is base %d rev %d, want 1 and 1 (said %v)",
			back.base, back.rev, err)
	}
	// And a copy opened before the commit, and not submitted by it, is left
	// at the revision it read — which is exactly what makes that actor's next
	// submit a STALE_REVISION rather than a surprise (the exported half is
	// TestSubmitRefusesACopyAnotherCommitLeftBehind).
	other, err := st.working(ctx(), newRefuser(open_, id), "nina", img)
	if err != nil || other.base != 0 {
		t.Errorf("another actor's copy is based at %d, want 0 (said %v)",
			other.base, err)
	}

	// A snapshot an operation already read never changed underneath it: the
	// commit replaced the stored field, and the copy the reader holds still
	// reports what it was given (invariant 3's rule, stated about the
	// present).
	if rec.revision != 0 {
		t.Errorf("the earlier read now reports revision %d, want 0", rec.revision)
	}

	// Every way to name a case the store does not have is the same refusal,
	// and the guard runs before the revision check: a caller cannot tell an
	// unissued ID from an issued one by which error it gets, and a stale
	// number attached to a case that does not exist is NOT_FOUND, because
	// the case is the thing that is missing.
	for _, err := range []error{
		func() error {
			_, err := st.saveWorking(ctx(), newRefuser(update, 9999), "dana",
				0, w, 0)
			return err
		}(),
		func() error {
			_, err := st.commit(ctx(), newRefuser(submit, 9999), img, 0,
				rec.commit, by)
			return err
		}(),
		func() error {
			_, err := st.working(ctx(), newRefuser(open_, 9999), "dana", img)
			return err
		}(),
		func() error {
			_, err := st.load(ctx(), newRefuser(open_, 9999))
			return err
		}(),
	} {
		if !Failed(err, NotFound) {
			t.Errorf("an unknown case answered %v, want NOT_FOUND", err)
		}
	}
}

func TestRegistryRefusesADuplicateID(t *testing.T) {
	g := newRegistry()
	d := &Definition{img: mustLowerSource(t, `
roles { Applicant { allow view application; } }
forms { application { name : string; } }
stages { start { accept done; } done { } }
`)}
	if err := g.add(newRefuser("register", 0), d, false); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := g.add(newRefuser("register", 0), d, false)
	if !Failed(err, InvalidOp) {
		t.Fatalf("duplicate add = %v, want INVALID_OPERATION", err)
	}
	// The refusal names the operation that asked, so a host reading a log
	// line knows whether it was registering or replacing.
	if !strings.Contains(err.Error(), "register") {
		t.Errorf("duplicate refusal says %q, want it to name the operation", err)
	}
	if err := g.add(newRefuser("replace", 0), d, true); err != nil {
		t.Errorf("replace of the same ID = %v, want it accepted", err)
	}
	if got, ok := g.get(d.ID()); !ok || got != d {
		t.Error("the registry does not answer its own ID")
	}
	if _, ok := g.get("no such name"); ok {
		t.Error("the registry answered a name that was never added")
	}
}

// The store's lookup is one function with two callers, and the refusal it
// produces is the operation's, not the store's: `load` says NOT_FOUND for an
// unissued ID and the caller's Op names the request that asked.
func TestCaseLookupSaysNotFoundOnce(t *testing.T) {
	st := newCaseStore()
	if _, err := st.load(ctx(), newRefuser(open_, 0)); !Failed(err, NotFound) {
		t.Errorf("load(0) = %v, want NOT_FOUND", err)
	}
	if _, err := st.load(ctx(), newRefuser(open_, 1)); !Failed(err, NotFound) {
		t.Errorf("load(1) = %v, want NOT_FOUND", err)
	}
}

// A case whose record names a definition nobody registered is
// SCHEMA_MISMATCH rather than NOT_FOUND: the case exists, and what is
// missing is the schema that says what its bytes mean (DESIGN §3). Building
// the state directly is how a host reaches this today — a persisted record
// read before its definition is loaded, which is milestone 4's restart — and
// the refusal is the same either way.
func TestLookRefusesACaseWhoseDefinitionIsMissing(t *testing.T) {
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, `
roles { Applicant { allow view application; } }
forms { application { name : string; } }
stages { start { accept done; } done { } }
`)}
	id, cerr := rt.w.cases.create(ctx(), newRefuser("create", 0), d)
	if cerr != nil {
		t.Fatalf("create: %v", cerr)
	}
	c := Case{id: id, def: d.ID(), actor: Actor{User: "dana"}, w: rt.w}

	_, err := c.Stage(ctx())
	if !Failed(err, SchemaMismatch) {
		t.Fatalf("Stage with an unregistered definition = %v, want SCHEMA_MISMATCH", err)
	}
	// Registering the definition the record already names makes the case
	// readable, which is the point: nothing about the data was wrong.
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if stage, err := c.Stage(ctx()); err != nil || stage != "start" {
		t.Errorf("Stage after registering = %q, %v, want start", stage, err)
	}
}

func ctx() context.Context { return context.Background() }

// The three words a store method can be asked with, spelled as the exported
// operations spell themselves, and used as the name a `refuser` carries into
// the seam. A test naming them as variables is deliberate: the point is that a
// refusal carries the *caller's* word — `CaseError.Op` is asserted against
// these same constants below — and a literal inside a `newRefuser(…)` call
// would prove nothing about which argument produced it.
const (
	open_  = "open"
	update = "update"
	submit = "submit"
)

func TestDataDriftDistinguishesShapeFromMeaning(t *testing.T) {
	// The base definition: types, one form with a plain group, two
	// stages. Every variant below is this source with one line changed, which
	// is what makes the verdict readable as a claim about that line.
	const base = `
types {
    money : decimal(14, 2);
}
roles { Applicant { allow view, edit application; } }
forms {
    application {
        name  : string;
        score : integer default 3;
        fee   : money default 1.00;

        note {
            text : string;
        }
    }
}
stages {
    start { allow Applicant submit application; accept done; }
    done  { }
}
`
	// A rename changes which path names a stored ordinal, and committed
	// diffs are keyed by path (§5.1), so it is a structural change.
	renamed := strings.Replace(base, "name  : string;", "label : string;", 1)

	// Adding a field renumbers every ordinal after it.
	inserted := strings.Replace(base, "score : integer default 3;",
		"score : integer default 3;\n        extra : string;", 1)

	// `multiple` on a group is what changes which slots one instance covers.
	multipleGroup := strings.Replace(base, "        note {",
		"        note multiple {", 1)

	// Reordering stages keeps the count and renumbers the ordinal every
	// stored case holds.
	reorderedStages := `
types {
    money : decimal(14, 2);
}
roles { Applicant { allow view, edit application; } }
forms {
    application {
        name  : string;
        score : integer default 3;
        fee   : money default 1.00;

        note {
            text : string;
        }
    }
}
stages {
    done  { }
    start { allow Applicant submit application; accept done; }
}
`
	// A permission change alters what the engine answers, not what the data
	// is, so it is compatible by §3's definition.
	repermitted := strings.Replace(base,
		"Applicant { allow view, edit application; }",
		"Applicant { allow view application; }", 1)

	// So is a default, and so is a decimal whose scale moved within the same
	// value class: §3's "incomplete, not incompatible" is this case.
	redefaulted := strings.Replace(base, "score : integer default 3;",
		"score : integer default 5;", 1)
	rescaled := strings.Replace(base, "money : decimal(14, 2);",
		"money : decimal(14, 4);", 1)

	// Retyping across value classes moves the payload to another field of
	// the row.
	retyped := strings.Replace(base, "name  : string;", "name  : integer;", 1)

	// One more stage, reachable and referenced, so the source is legal and
	// only the count of declarations moved. The stored ordinals still name
	// the same stages, and §3's answer is that a definition which changes
	// how many there are cannot be trusted to mean the same number.
	extendedStages := strings.Replace(base,
		"    done  { }\n",
		"    done { accept closed; }\n    closed { }\n", 1)

	old := mustLowerSource(t, base)
	cases := []struct {
		name    string
		src     string
		wantOK  bool
		wantSay string
	}{
		{"reword permissions", repermitted, true, ""},
		{"change a default", redefaulted, true, ""},
		{"rescale a decimal", rescaled, true, ""},
		{"rename a field", renamed, false, "there"},
		{"add a field", inserted, false, "slots"},
		{"make a group multiple", multipleGroup, false, "became"},
		{"reorder stages", reorderedStages, false, "stage 1"},
		{"add a reachable stage", extendedStages, false, "stages"},
		{"retype across classes", retyped, false, "value class"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := mustLowerSource(t, tc.src)
			detail, ok := program.DataDrift(old, next)
			if ok != tc.wantOK {
				t.Fatalf("dataDrift ok = %v (%q), want %v", ok, detail, tc.wantOK)
			}
			// The verdict alone is not useful to a host: a refusal has to
			// say which part of the definition moved, which is the whole
			// difference between SCHEMA_MISMATCH here and a wrong answer
			// from §5.1's addressing.
			if !tc.wantOK && !strings.Contains(detail, tc.wantSay) {
				t.Errorf("drift detail = %q, want it to mention %q", detail, tc.wantSay)
			}
			if tc.wantOK && detail != "" {
				t.Errorf("a compatible change reported %q, want the empty string", detail)
			}
		})
	}

	// The comparison is worth its cost only because the common case is never
	// compared: one image against itself agrees, and §7.4's immutability is
	// what lets a pointer answer "same schema?" before this runs at all.
	same := mustLowerSource(t, base)
	if _, ok := program.DataDrift(same, same); !ok {
		t.Error("two images of one source disagree with themselves")
	}
}

func TestACommitRecordsTheMomentItHappened(t *testing.T) {
	img := lowerExample(t)
	st := newCaseStore()
	id, err := st.create(ctx(), newRefuser(open_, 0),
		&Definition{img: img})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// DESIGN §5.2's determinism rule in one field: a replay cannot ask what
	// the clock said at the time, so the stamp belongs to the entry and is
	// never recomputed from it. Making the clock a seam the store owns is also
	// what lets a test say *when* a commit happened, which a `time.Now` call
	// inside the store could only be asserted about by waiting for it to
	// disagree with itself.
	first := time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return first }
	rec, err := st.load(ctx(), newRefuser(open_, id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	by := committedBy{by: "dana", op: opSubmit}
	if _, err := st.commit(ctx(), newRefuser(submit, id), img, 0, rec.commit,
		by); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The entry carries the moment, the revision and the stage the caller
	// committed *at*, because a history a replay cannot locate in a stage is a
	// list of changes to the wrong rules. It carries no changes, and that is
	// the store's half of the division: §5.4's operation decides what a commit
	// means and hands the diff over, and the store records what it is given
	// rather than re-deriving any of it.
	h, err := st.history(ctx(), newRefuser(open_, id), 1, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h) != 1 || !h[0].at.Equal(first) {
		t.Fatalf("history = %+v, want one entry stamped %s", h,
			first.Format(time.RFC3339))
	}
	if h[0].stage != rec.commit.stage || h[0].revision != 1 {
		t.Errorf("the entry says revision %d stage %d, want 1 at the stage "+
			"the snapshot was at (%d)", h[0].revision, h[0].stage,
			rec.commit.stage)
	}

	// A second commit under a moved clock gets its own moment, and the first
	// entry keeps the one it was given: the history is a record and not a view
	// recomputed at read time. The numbering is dense — index 0 is revision 1,
	// index 1 is revision 2 — which is what makes "everything between 4 and 7"
	// a slice rather than a search, and what §5.1's rule that a revision is
	// the *count* of commits buys.
	later := first.Add(time.Hour)
	st.now = func() time.Time { return later }
	rec, err = st.load(ctx(), newRefuser(open_, id))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := st.commit(ctx(), newRefuser(submit, id), img, 1, rec.commit,
		by); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	h, err = st.history(ctx(), newRefuser(open_, id), 1, 0)
	if err != nil || len(h) != 2 {
		t.Fatalf("history = %d entries (%v), want 2", len(h), err)
	}
	if !h[0].at.Equal(first) || !h[1].at.Equal(later) {
		t.Errorf("the entries are stamped %s and %s, want the two moments "+
			"the clock was set to", h[0].at.Format(time.RFC3339),
			h[1].at.Format(time.RFC3339))
	}
	if h[0].revision != 1 || h[1].revision != 2 {
		t.Errorf("the entries are numbered %d and %d, want 1 and 2",
			h[0].revision, h[1].revision)
	}

	// A store that was never given a clock still stamps, from the system
	// time: the field is a seam for a test and for a replay, not a
	// requirement either operation has to satisfy.
	plain := newCaseStore()
	pid, perr := plain.create(ctx(), newRefuser(open_, 0),
		&Definition{img: img})
	if perr != nil {
		t.Fatalf("create: %v", perr)
	}
	before := time.Now()
	if _, err := plain.commit(ctx(), newRefuser(submit, pid), img, 0,
		rec.commit, by); err != nil {
		t.Fatalf("commit on a default clock: %v", err)
	}
	if h, err := plain.history(ctx(), newRefuser(open_, pid), 1,
		0); err != nil || len(h) != 1 ||
		h[0].at.Before(before) {
		t.Errorf("a store with no clock set stamped %v (%v), want the time "+
			"the commit happened at", h, err)
	}
}
