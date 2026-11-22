package monshi_test

// External tests for the exported half of milestone 2 (RUNTIME §10): the
// registry, the handles, and what a host observes when a definition is
// reloaded under a name cases were already created against. Everything here
// sees only exported names — no `schema` import, no image, no ordinal —
// which is the claim §10 makes about the seam.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/monshihq/monshi"
)

// The §7 example's shape is asserted elsewhere; these sources are the
// smallest definitions that express one claim each.
const hostDef = `
roles {
    Applicant { allow view, edit application; }
    Manager   { allow view, edit application; }
}
forms {
    application {
        name  : string;
        score : integer default 3;
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`

// widened is hostDef with one permission word changed: a semantic edit, so
// existing cases stay readable.
var widened = strings.Replace(hostDef,
	"Applicant { allow view, edit application; }",
	"Applicant { allow view application; }", 1)

// grown is hostDef with a field added: a structural edit, so existing cases
// must refuse rather than be renumbered.
var grown = strings.Replace(hostDef, "score : integer default 3;",
	"score : integer default 3;\n        extra : string;", 1)

// ctx is the caller's context. The in-memory store has nothing to cancel
// yet, and DESIGN §6 says an operation that will do I/O takes one anyway, so
// a host writes the call the same way in both milestones.
func ctx() context.Context { return context.Background() }

// dana is the actor most of these cases are created and opened by: an
// identity plus the role the host says it authenticated (DESIGN §4.3).
var dana = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}

func define(t *testing.T, rt *monshi.Runtime, id, src string) *monshi.Definition {
	t.Helper()
	d, err := rt.Define(id, strings.NewReader(src))
	if err != nil {
		t.Fatalf("Define(%q): %v", id, err)
	}
	return d
}

func TestDefineCreateAndOpenReadTheCase(t *testing.T) {
	rt := monshi.NewRuntime()
	d := define(t, rt, "host", hostDef)

	c, err := rt.Create(ctx(), "host", dana)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID() == 0 {
		t.Error("Create returned the zero CaseID; 0 is \"no such case\"")
	}
	if got := c.Actor().User; got != "dana" {
		t.Errorf("Actor().User = %q, want dana", got)
	}

	// A new case is at the initial stage and revision 0, and creating it
	// appended no commit (DESIGN §5, invariant 2).
	stage, err := c.Stage(ctx())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if stage != "draft" {
		t.Errorf("Stage = %q, want the initial stage %q", stage, "draft")
	}
	if rev, err := c.Revision(ctx()); err != nil || rev != 0 {
		t.Errorf("Revision = %d, %v, want 0 and no error", rev, err)
	}

	// Open resolves the definition through the case's own record (DESIGN §3),
	// so a host that only kept the CaseID still gets the right schema.
	got, err := rt.Open(ctx(), c.ID(), monshi.Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Actor().User != "elliot" {
		t.Error("Open did not carry the actor it was given")
	}
	resolved, ok := got.Definition()
	if !ok {
		t.Fatal("the opened case resolves to no definition")
	}
	if resolved != d {
		t.Error("Open resolved a different definition than the one created from")
	}
	if resolved.ID() != "host" {
		t.Errorf("resolved definition ID = %q, want %q", resolved.ID(), "host")
	}
}

// DESIGN §3: duplicate IDs are rejected, and the definition already
// registered stays in place. Two Loads of one source are independent and
// both valid — naming them the same is the host's mistake to hear about.
func TestRegisterRefusesADuplicateID(t *testing.T) {
	rt := monshi.NewRuntime()
	first := define(t, rt, "dup", hostDef)

	second, err := monshi.Load("dup", strings.NewReader(widened))
	if err != nil {
		t.Fatalf("Load of the second copy: %v", err)
	}
	err = rt.Register(second)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("Register of a duplicate ID = %v, want INVALID_OPERATION", err)
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate refusal says %q", err)
	}
	// The first is still what the registry answers with, and a case created
	// afterwards is created from it: a refused registration changed nothing.
	if got, ok := rt.Definition("dup"); !ok || got != first {
		t.Error("the refused registration replaced the definition in the registry")
	}
	c, err := rt.Create(ctx(), "dup", monshi.Actor{User: "dana"})
	if err != nil {
		t.Fatalf("Create after a refused registration: %v", err)
	}
	if _, err := c.Stage(ctx()); err != nil {
		t.Errorf("Stage after a refused registration: %v", err)
	}

	// Registering nothing is the same kind of mistake, answered the same way.
	if err := rt.Register(nil); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("Register(nil) = %v, want INVALID_OPERATION", err)
	}
	// Define is Load and Register in one call, so it refuses the second half
	// the same way — and the source it was given is never installed, which is
	// the point of a host's startup being able to Define every file in a
	// directory and hear about a collision rather than silently keep one of
	// the two.
	_, err = rt.Define("dup", strings.NewReader(widened))
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("Define of a registered ID = %v, want INVALID_OPERATION", err)
	}
	if got, ok := rt.Definition("dup"); !ok || got != first {
		t.Error("the refused Define replaced the definition in the registry")
	}
}

// A definition that did not compile never reaches the registry (§7.5): a
// host cannot half-install a schema and then open cases against it.
func TestDefineRefusesSourceThatDoesNotCompile(t *testing.T) {
	rt := monshi.NewRuntime()
	const bad = hostDef + `
forms {
    other {
        n : not_a_type;
    }
}
`
	_, err := rt.Define("bad", strings.NewReader(bad))
	var derr *monshi.Error
	if !errors.As(err, &derr) {
		t.Fatalf("Define of bad source = %v, want *monshi.Error", err)
	}
	if derr.Mismatch() {
		t.Errorf("the front end's problem was reported as a runtime defect: %v", err)
	}
	if _, ok := rt.Definition("bad"); ok {
		t.Error("a definition that did not compile is in the registry")
	}
	// And Create says so by name, not by a later surprise inside an
	// operation: the host asked for a case of a schema that is not there.
	_, err = rt.Create(ctx(), "bad", monshi.Actor{User: "dana"})
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("Create of an unregistered definition = %v, want INVALID_OPERATION", err)
	}
}

func TestOpenRefusesWhatItCannotResolve(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "host", hostDef)

	// The zero CaseID and an ID this store never issued are both NOT_FOUND.
	// There is no fallback that reads a similar-looking case instead, and no
	// zero record to mistake for a real one: revision 0 at the initial stage
	// is a state a case can genuinely be in (DESIGN §5).
	for _, id := range []monshi.CaseID{0, 1, 4242} {
		_, err := rt.Open(ctx(), id, monshi.Actor{User: "dana"})
		if !monshi.Failed(err, monshi.NotFound) {
			t.Errorf("Open(%d) = %v, want NOT_FOUND", id, err)
		}
	}

	// A case created in one runtime does not exist in another: separate
	// runtimes share nothing, which is what lets a test build a fresh
	// universe without a package-level reset. (IDs from two in-memory
	// stores happen to start at the same number; they are not comparable,
	// because the store that issued one owns what it addresses.)
	other := monshi.NewRuntime()
	define(t, other, "host", hostDef)
	c, err := other.Create(ctx(), "host", monshi.Actor{User: "dana"})
	if err != nil {
		t.Fatalf("Create in the second runtime: %v", err)
	}
	if _, ok := c.Definition(); !ok {
		t.Error("a case created in its own runtime does not resolve its definition there")
	}
	_, err = rt.Open(ctx(), c.ID(), dana)
	if !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("another runtime's case opened here = %v, want NOT_FOUND", err)
	}
}

// A handle is four words and no mutable state, so a copy is the same
// handle. What a zero one is — the value a host holds if it ignored
// Create's error — is a refusal, not a nil dereference.
func TestHandlesAreValuesNotCaches(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "host", hostDef)
	c, err := rt.Create(ctx(), "host", dana)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	copied := c
	if copied.ID() != c.ID() || copied.Actor() != c.Actor() {
		t.Error("a copied handle addresses a different case or actor")
	}
	// A second handle for the same case, made by Open rather than by copy,
	// reports the same revision: there is one stored state and no session
	// map to keep in step (invariant 4).
	again, err := rt.Open(ctx(), c.ID(), monshi.Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first, err := c.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	second, err := again.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision on the second handle: %v", err)
	}
	if first != second {
		t.Errorf("two handles report %d and %d, want one stored state",
			first, second)
	}

	// The zero handle refuses. Every method on it goes through the same
	// lookup, so one guard covers them all — and the answer is the one the
	// store gives for an ID it never issued, because that is what it is.
	var zero monshi.Case
	if _, ok := zero.Definition(); ok {
		t.Error("the zero Case resolved a definition")
	}
	if _, err := zero.Stage(ctx()); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("the zero Case's Stage = %v, want NOT_FOUND", err)
	}
	if _, err := zero.Revision(ctx()); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("the zero Case's Revision = %v, want NOT_FOUND", err)
	}
	if got := zero.ID(); got != 0 {
		t.Errorf("the zero Case's ID = %d, want the unissued one", got)
	}
}

// The reload test, in both directions: a semantic change reinterprets the
// same data (DESIGN §3), and a structural change is refused instead of
// silently renumbering it (§9's SCHEMA_MISMATCH, invariant 8).
func TestReplaceReinterpretsCompatibleCases(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "host", hostDef)
	c, err := rt.Create(ctx(), "host", dana)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	compatible, err := monshi.Load("host", strings.NewReader(widened))
	if err != nil {
		t.Fatalf("Load of the compatible revision: %v", err)
	}
	if err := rt.Replace(compatible); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// The handle opened before the reload now reads through the new
	// definition, and nothing about the case was rewritten: same stage, same
	// revision, same ID.
	if got, ok := c.Definition(); !ok || got != compatible {
		t.Error("a handle opened before the reload still resolves the old definition")
	}
	if stage, err := c.Stage(ctx()); err != nil || stage != "draft" {
		t.Errorf("after a compatible reload, Stage = %q, %v, want draft", stage, err)
	}
	if rev, err := c.Revision(ctx()); err != nil || rev != 0 {
		t.Errorf("after a compatible reload, Revision = %d, %v, want 0", rev, err)
	}

	// An incompatible replacement refuses, naming what moved, and the case
	// is untouched rather than half-read: every operation on this handle
	// answers the same way until the host fixes the registry.
	structural, err := monshi.Load("host", strings.NewReader(grown))
	if err != nil {
		t.Fatalf("Load of the structural revision: %v", err)
	}
	if err := rt.Replace(structural); err != nil {
		t.Fatalf("Replace with a wider definition: %v", err)
	}
	_, err = c.Stage(ctx())
	if !monshi.Failed(err, monshi.SchemaMismatch) {
		t.Fatalf("Stage after an incompatible reload = %v, want SCHEMA_MISMATCH", err)
	}
	if !strings.Contains(err.Error(), "slots") {
		t.Errorf("the refusal does not say what moved: %v", err)
	}
	if _, err := c.Revision(ctx()); !monshi.Failed(err, monshi.SchemaMismatch) {
		t.Errorf("Revision after an incompatible reload = %v, want SCHEMA_MISMATCH", err)
	}
	_, err = rt.Open(ctx(), c.ID(), dana)
	if !monshi.Failed(err, monshi.SchemaMismatch) {
		t.Error("Open accepted a case it cannot interpret")
	}
	// The refusal is about the stored data, not the definition: a new case
	// of the same registered definition is created and read normally,
	// because it is written into the ordinal space the definition has now.
	fresh, err := rt.Create(ctx(), "host", monshi.Actor{User: "elliot"})
	if err != nil {
		t.Fatalf("Create after an incompatible reload: %v", err)
	}
	if _, err := fresh.Stage(ctx()); err != nil {
		t.Errorf("a new case of the reloaded definition fails: %v", err)
	}
	// And rolling the registry back to the definition the case was written
	// under makes the old case readable again — which is the whole point of
	// the data never having been rewritten.
	if err := rt.Replace(compatible); err != nil {
		t.Fatalf("Replace back: %v", err)
	}
	if _, err := c.Stage(ctx()); err != nil {
		t.Errorf("Stage after rolling the reload back: %v", err)
	}
}

// The registry and the store are the two things a runtime owns, and both
// are shared state: hosts hit them from request handlers.
func TestRuntimeIsSafeForConcurrentUse(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "host", hostDef)

	const actors = 8
	var wg sync.WaitGroup
	ids := make([]monshi.CaseID, actors)
	for i := 0; i < actors; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := rt.Create(ctx(), "host", monshi.Actor{User: monshi.UserID("user")})
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			ids[i] = c.ID()
			// Reads and a reopen, all against the same shared state.
			if _, err := c.Stage(ctx()); err != nil {
				t.Errorf("Stage: %v", err)
			}
			if _, err := rt.Open(ctx(), c.ID(), monshi.Actor{User: "user"}); err != nil {
				t.Errorf("Open: %v", err)
			}
			// Concurrent registration of the same ID must be refused for
			// every racer but one, and never corrupt the map.
			d, err := monshi.Load("host", strings.NewReader(hostDef))
			if err != nil {
				t.Errorf("Load: %v", err)
				return
			}
			// Every racer finds the name taken, because the definition was
			// registered before the goroutines started: the refusal has to
			// be total, and the map must not be corrupted by the race.
			if err := rt.Register(d); !monshi.Failed(err, monshi.InvalidOp) {
				t.Errorf("concurrent duplicate Register = %v, want INVALID_OPERATION", err)
			}
		}(i)
	}
	wg.Wait()

	// Every goroutine got its own case: IDs are issued by the store and are
	// never derived from content, so two creates cannot collide (§3.1).
	seen := map[monshi.CaseID]bool{}
	for i, id := range ids {
		if id == 0 || seen[id] {
			t.Fatalf("case %d was issued ID %d, want a fresh non-zero one", i, id)
		}
		seen[id] = true
	}
	// The registration race left exactly one definition under the name.
	if _, ok := rt.Definition("host"); !ok {
		t.Error("the racing registrations lost the definition")
	}
}

func TestCaseErrorCarriesABackendsError(t *testing.T) {
	// A host builds no CaseError today — the refusals come from this
	// package — but milestone 4's backend does, by wrapping its own
	// sentinel in Cause. §9's contract is that the category still answers
	// `Failed` and the backend's error still answers `errors.Is`, and this
	// is the shape that makes both true.
	sentinel := errors.New("connection reset")
	err := &monshi.CaseError{Op: "commit", Reason: monshi.StorageFailure,
		Case: 7, Detail: "the snapshot did not land", Cause: sentinel}
	if !monshi.Failed(err, monshi.StorageFailure) {
		t.Errorf("Failed = false for a storage refusal: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Error("a backend's sentinel is unreachable through Unwrap")
	}
	for _, want := range []string{"STORAGE_FAILURE", "commit", "case 7",
		"the snapshot did not land", "connection reset"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q: %v", want, err)
		}
	}

	// Two things `Failed` must not do: call a nil error a refusal, or claim
	// a category for somebody else's error type.
	if monshi.Failed(nil, monshi.NotFound) {
		t.Error("no error is a refusal of something")
	}
	if monshi.Failed(io.EOF, monshi.StorageFailure) {
		t.Error("a foreign error was read as a refusal of this package")
	}
	// And a registry refusal, which is about no case, renders without one.
	if got := (&monshi.CaseError{Op: "register", Reason: monshi.InvalidOp,
		Detail: "duplicate"}).Error(); strings.Contains(got, "case ") {
		t.Errorf("a refusal about no case renders a case: %q", got)
	}
}

// The Op field is the part of a refusal a host logs, so it has to name the
// call the host made and not the internal lookup that noticed the problem:
// the registry check, the store lookup and the compatibility check are three
// shared paths serving four operations.
func TestRefusalsNameTheOperationThatRefused(t *testing.T) {
	rt := monshi.NewRuntime()
	define(t, rt, "host", hostDef)

	haveOp := func(err error, want string) bool {
		t.Helper()
		var ce *monshi.CaseError
		if !errors.As(err, &ce) {
			t.Errorf("refusal is %T, want *monshi.CaseError", err)
			return false
		}
		if ce.Op != want {
			t.Errorf("%v refused with Op %q, want %q", want, ce.Op, want)
			return false
		}
		return true
	}

	_, err := rt.Open(ctx(), 4242, dana)
	if !monshi.Failed(err, monshi.NotFound) || !haveOp(err, "open") {
		t.Errorf("Open of an unissued ID = %v", err)
	}

	// The zero handle answers through the same lookup as every other
	// accessor, and each accessor says which it is.
	var zero monshi.Case
	if _, err := zero.Stage(ctx()); !haveOp(err, "stage") {
		t.Errorf("the zero handle's Stage = %v", err)
	}
	if _, err := zero.Revision(ctx()); !haveOp(err, "revision") {
		t.Errorf("the zero handle's Revision = %v", err)
	}

	// A registry refusal names the act, too, and says so about no case.
	dup, err := monshi.Load("host", strings.NewReader(hostDef))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := rt.Register(dup); !haveOp(err, "register") {
		t.Errorf("a duplicate Register = %v", err)
	}
	if err := rt.Replace(dup); err != nil {
		t.Errorf("Replace of the same definition = %v, want it accepted", err)
	}
}

func TestIdentifiersRenderForLogs(t *testing.T) {
	// A refusal is read by a human in a log line, so the two identifiers a
	// host carries say what they are — including the empty one, which is
	// exactly the mistake worth being able to see.
	if got := monshi.UserID("").String(); got != `""` {
		t.Errorf("UserID(\"\").String() = %s, want an empty quoted string", got)
	}
	if got := monshi.UserID("dana").String(); got != `"dana"` {
		t.Errorf("UserID.String() = %s", got)
	}
	if got := monshi.CaseID(17).String(); got != "case 17" {
		t.Errorf("CaseID.String() = %s, want case 17", got)
	}
}
