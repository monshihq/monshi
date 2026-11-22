package monshi_test

// External tests for the host-facing seam (RUNTIME §1): everything here
// sees only exported names, which is the point. The image below Load is
// asserted in-package, because it is not a host's business.

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/monshihq/monshi/schema"
)

const tinyDef = `
roles {
    Applicant { allow view, edit application; }
}
forms {
    application {
        name : string;
    }
}
stages {
    start { allow Applicant submit application; accept done; }
    done  { }
}
`

func TestLoadBuildsDefinitionFromSource(t *testing.T) {
	d, err := monshi.Load("tiny", strings.NewReader(tinyDef))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.ID() != "tiny" {
		t.Errorf("ID = %q, want %q", d.ID(), "tiny")
	}
	if w := d.Warnings(); len(w) != 0 {
		t.Errorf("warnings = %v, want none", w)
	}
}

// A definition that does not compile is the author's problem, not the
// runtime's: the front end's codes and positions come back unchanged, and
// nothing was lowered (§1).
func TestLoadRefusesSourceThatDoesNotCompile(t *testing.T) {
	const bad = tinyDef + `
forms {
    other {
        n : not_a_type;
    }
}
`
	_, err := monshi.Load("bad", strings.NewReader(bad))
	var verr *monshi.Error
	if !errors.As(err, &verr) {
		t.Fatalf("error type %T (%v), want *monshi.Error", err, err)
	}
	if verr.Mismatch() {
		t.Errorf("Mismatch = true, want a source problem: %v", err)
	}
	if verr.ID != "bad" {
		t.Errorf("ID = %q, want %q", verr.ID, "bad")
	}
	if len(verr.Problems) == 0 {
		t.Fatal("no Problems; a refused definition must say why")
	}
	var codes []schema.Code
	for _, p := range verr.Problems {
		codes = append(codes, p.Code)
	}
	if !containsCode(codes, schema.CodeDuplicateBlock) {
		t.Errorf("Problems = %v, want the duplicated forms block reported",
			verr.Problems)
	}
	if !containsCode(codes, schema.CodeUnknownType) {
		t.Errorf("Problems = %v, want every front-end diagnostic kept", verr.Problems)
	}
	for _, p := range verr.Problems {
		if p.Line == 0 || p.Col == 0 {
			t.Errorf("problem %v lost the position the author must fix", p)
		}
	}
	if !strings.Contains(err.Error(), string(schema.CodeDuplicateBlock)) {
		t.Errorf("message %q does not render the stable code", err)
	}
}

func containsCode(cs []schema.Code, want schema.Code) bool {
	for _, c := range cs {
		if c == want {
			return true
		}
	}
	return false
}

// Warnings never invalidate a definition (SYNTAX §5.8), and the handle is
// where the author finds out about them: Load succeeds and reports.
func TestLoadKeepsWarningsWithoutRefusing(t *testing.T) {
	const unusedRole = `
roles {
    Applicant { allow view, edit application; }
    Auditor   { allow view all; }
}
forms {
    application {
        name : string;
    }
}
stages {
    start { allow Applicant submit application; accept done; }
    done  { }
}
`
	d, err := monshi.Load("unused", strings.NewReader(unusedRole))
	if err != nil {
		t.Fatalf("a warning is not an error: %v", err)
	}
	var found bool
	for _, w := range d.Warnings() {
		if w.Code == schema.CodeUnusedRole && w.Context == "Auditor" {
			found = true
		}
		if w.Line == 0 || w.Col == 0 {
			t.Errorf("warning %v lost its position", w)
		}
	}
	if !found {
		t.Errorf("warnings = %v, want Auditor reported unused", d.Warnings())
	}
	// The host cannot corrupt the handle by writing into what it was given.
	before := d.Warnings()
	if len(before) == 0 {
		t.Fatal("no warnings to check the copy against")
	}
	want := before[0].Code
	before[0] = schema.Problem{}
	if got := d.Warnings()[0].Code; got != want {
		t.Errorf("Warnings handed out the definition's own slice: %v then %v", want, got)
	}
}

// A reader that fails is not a definition problem: it never reaches the
// front end's diagnostics, and the I/O cause is preserved.
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestLoadPropagatesSourceFailure(t *testing.T) {
	_, err := monshi.Load("unreadable", io.Reader(brokenReader{}))
	if err == nil {
		t.Fatal("Load accepted a reader that could not be read")
	}
	var verr *monshi.Error
	if errors.As(err, &verr) {
		t.Errorf("an I/O failure became a definition refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("error %q lost the underlying cause", err)
	}
}

// An unnamed definition can never be selected by Create, so the seam
// refuses it rather than storing something unreachable (DESIGN §3).
func TestLoadNeedsAnID(t *testing.T) {
	if _, err := monshi.Load("", strings.NewReader(tinyDef)); err == nil {
		t.Fatal("Load accepted an empty definition id")
	}
}

func TestDefinitionsAreIndependent(t *testing.T) {
	a, err := monshi.Load("same", strings.NewReader(tinyDef))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := monshi.Load("same", strings.NewReader(tinyDef))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a == b {
		t.Fatal("two Loads of one source returned the same handle")
	}
	// DESIGN §3: duplicate IDs are the host's registry's problem, and the
	// runtime's answer is an equally valid, independent definition.
	if a.ID() != b.ID() {
		t.Errorf("IDs %q and %q differ", a.ID(), b.ID())
	}
}
