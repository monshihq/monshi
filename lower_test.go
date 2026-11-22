// Package monshi's lowering tests are in-package on purpose. The runtime
// image is not a public API (RUNTIME §9), so the only honest way to
// assert §3's shapes and §4's contract is to look at the image directly.
// Anything a host can observe gets external tests when the case
// operations land (RUNTIME §10).
//
// The conformance gate is Appendix A: the SYNTAX §7 example must compile
// clean, lower without problems, and produce the shapes that trace
// describes. The source is read from docs/SYNTAX.md, so the fixture cannot
// drift from the document it is traced against.
package monshi

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/schema"
	"github.com/shopspring/decimal"
)

// specExample returns the language document's complete example.
func specExample(tb testing.TB) string {
	tb.Helper()
	const marker = "## 7. Complete example"
	data, err := os.ReadFile("docs/SYNTAX.md")
	if err != nil {
		tb.Skip("docs/SYNTAX.md not shipped alongside the package")
	}
	text := string(data)
	i := strings.Index(text, marker)
	if i < 0 {
		tb.Fatalf("marker %q not found in SYNTAX.md", marker)
	}
	rest := text[i:]
	start := strings.Index(rest, "```")
	if start < 0 {
		tb.Fatal("no fenced example under §7")
	}
	rest = rest[start+3:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		tb.Fatal("unterminated fence under §7")
	}
	return strings.TrimPrefix(rest[:end+1], "\n")
}

// compile checks source through the real front end and lowers it. Errors
// fail the test; warnings are returned for the caller to judge.
func compile(t *testing.T, src string) (*image, []schema.Problem) {
	t.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		t.Fatalf("definition does not compile: %s", join(diagnostics(s.Errors)))
	}
	img, err := lower("test", s)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	return img, s.Warnings
}

func lowerExample(t *testing.T) *image {
	t.Helper()
	img, warnings := compile(t, specExample(t))
	if len(warnings) != 0 {
		t.Fatalf("the §7 example must compile clean, got %s", join(diagnostics(warnings)))
	}
	return img
}

func diagnostics(ps []schema.Problem) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Error())
	}
	return out
}

func join(list []string) string { return strings.Join(list, "; ") }

// slotByPath finds a declaration by its absolute path. Tests use the
// readable path even though the image's own logic is index-only, which is
// what makes the assertion worth making: it proves the path was interned
// and attached to exactly one slot.
func slotByPath(t *testing.T, img *image, path string) nodeID {
	t.Helper()
	var found []nodeID
	for i, s := range img.slots {
		if s.path != 0 && img.name(s.path) == path {
			found = append(found, nodeID(i))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%q matches %d slots, want exactly 1", path, len(found))
	}
	return found[0]
}

// nestedSrc is the shape the §7 example cannot express: a repeated field, and
// a collection inside a collection. SYNTAX §4 allows `multiple` on a field and
// on a group, and nothing forbids a group inside a group, so the row model has
// to answer for all three at once.
const nestedSrc = `
roles {
    A { allow view, edit all; }
}
forms {
    f {
        tags : string multiple;
        rows multiple {
            n : integer default 7;
            meta {
                m : integer;
            }
            cells multiple {
                v : integer;
            }
        }
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`

func roleIDBy(t *testing.T, img *image, name string) roleID {
	t.Helper()
	for r := roleID(1); int(r) < len(img.roleIdx); r++ {
		if img.roleName(r) == name {
			return r
		}
	}
	t.Fatalf("role %q was not interned", name)
	return 0
}

func stageIDBy(t *testing.T, img *image, name string) stageID {
	t.Helper()
	for s := stageID(1); int(s) < len(img.stages); s++ {
		if img.name(img.stages[s].name) == name {
			return s
		}
	}
	t.Fatalf("stage %q was not interned", name)
	return 0
}

// --- Appendix A: shape of the compiled model ---

func TestSpecExampleLowers(t *testing.T) {
	img := lowerExample(t)

	if img.id != "test" {
		t.Errorf("image id = %q", img.id)
	}
	// Slot 0 is the case root, and its subtree is the whole image: "all"
	// is one grant on one slot.
	if s := img.slots[0]; s.kind != kindRoot || s.span != uint32(len(img.slots)) {
		t.Errorf("root = kind %d span %d over %d slots", s.kind, s.span, len(img.slots))
	}
	app := slotByPath(t, img, "application")
	arch := slotByPath(t, img, "archive")
	if img.slots[app].kind != kindForm || img.slots[arch].kind != kindForm {
		t.Error("application and archive must both be forms")
	}
	if len(img.forms) != 3 || img.forms[1] != app || img.forms[2] != arch {
		t.Errorf("forms = %v, want [0, %d, %d]", img.forms, app, arch)
	}
	if got := img.slots[app].span; got != uint32(arch-app) {
		t.Errorf("application subtree spans %d slots, want %d", got, arch-app)
	}

	// Types: the enum carries dense tags in declaration order, and
	// decimal(14, 2) arrives with its bounds test built once (§3.3).
	kind := slotByPath(t, img, "application.kind")
	kt := img.types[img.slots[kind].typ]
	if kt.base != baseEnum || len(kt.tags) != 2 {
		t.Fatalf("legal_form = base %d tags %v, want an enum with two tags",
			kt.base, kt.tags)
	}
	if img.tagName(kt.tags[0]) != "PERSON" || img.tagName(kt.tags[1]) != "COMPANY" {
		t.Errorf("tags = [%s, %s], want [PERSON, COMPANY]",
			img.tagName(kt.tags[0]), img.tagName(kt.tags[1]))
	}
	fee := slotByPath(t, img, "application.fee")
	ft := img.types[img.slots[fee].typ]
	if ft.base != baseDecimal || ft.fits == nil {
		t.Fatalf("amount = base %d fits %v, want a decimal with a bounds test",
			ft.base, ft.fits != nil)
	}
	if !ft.fits(decimal.RequireFromString("123456789012.34")) ||
		ft.fits(decimal.RequireFromString("1234567890123.40")) {
		t.Error("decimal(14, 2) bounds test accepts or rejects the wrong edge")
	}

	// Defaults are materialized constants, typed at compile time (§3.3):
	// an ISO 8601 default is an ordered instant, not text to parse.
	reg := img.constOf(img.slots[slotByPath(t, img, "application.registered")].def)
	if !reg.set || !reg.t.Equal(day(2026, 1, 1)) {
		t.Errorf("registered default = %+v, want date 2026-01-01", reg)
	}
	urg := img.constOf(img.slots[slotByPath(t, img, "application.urgent")].def)
	if !urg.set || urg.i != 0 {
		t.Errorf("urgent default = %+v, want boolean false", urg)
	}
	if d := img.constOf(img.slots[fee].def); !d.set || !d.d.Equal(decimal.Zero) {
		t.Errorf("fee default = %+v, want decimal zero", d)
	}

	// ST1: the first declared stage is the initial stage as a constant.
	if img.initial != stageIDBy(t, img, "draft") {
		t.Errorf("initial stage = %d, want draft", img.initial)
	}
	if len(img.perms.byRole) != 4 {
		t.Errorf("%d role tables, want three roles plus the no-role table",
			len(img.perms.byRole))
	}
	// No map is reachable from the image: every table is index-addressable
	// (§3.8). depOwners and effective are the only lookups, and both are
	// binary searches over sorted arrays.
	if img.depBound != uint32(len(img.slots)) {
		t.Errorf("fanout queue bound %d, want the slot count %d",
			img.depBound, len(img.slots))
	}
}

// day builds the UTC midnight a date literal names, which is how the
// compiler normalizes `"YYYY-MM-DD"` (§5.8).
func day(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

// --- Appendix A: the stage graph and transition authority ---

func TestSpecExampleStageTables(t *testing.T) {
	img := lowerExample(t)
	draft := stageIDBy(t, img, "draft")
	company := stageIDBy(t, img, "company_review")
	person := stageIDBy(t, img, "person_review")
	correction := stageIDBy(t, img, "correction")

	info := img.stages[draft]
	if len(info.edges) != 3 {
		t.Fatalf("draft has %d edges, want two accepts and one reject", len(info.edges))
	}
	kind := slotByPath(t, img, "application.kind")
	kt := img.types[img.slots[kind].typ]
	companyTag := kt.tags[1]

	// Both conditional accepts survive: ST3 bounds multiplicity per
	// destination, not per source stage (§3.7).
	eCompany, ok := info.edgeTo(company)
	if !ok || eCompany.reject || eCompany.guard == 0 {
		t.Fatalf("draft → company_review = %+v, want a guarded accept", eCompany)
	}
	ePerson, _ := info.edgeTo(person)
	if ePerson.guard == 0 || ePerson.guard == eCompany.guard {
		t.Errorf("draft → person_review guard = %d, want a distinct closure",
			ePerson.guard)
	}
	eFix, _ := info.edgeTo(correction)
	if !eFix.reject || eFix.guard != 0 {
		t.Errorf("draft → correction = %+v, want an unguarded reject", eFix)
	}
	if _, ok := info.edgeTo(draft); ok {
		t.Error("a stage must not transition to itself")
	}

	// The guard reads one dense slot and nothing else: PERSON admits the
	// person branch, COMPANY the company branch, and an unset kind admits
	// neither (SYNTAX Appendix C: absence satisfies nothing).
	frame := make(stateView, len(img.slots))
	if img.cond(eCompany.guard)(frame) || img.cond(ePerson.guard)(frame) {
		t.Error("an unset kind must not satisfy either accept guard")
	}
	frame[kind] = slotValue{set: true, i: int64(companyTag)}
	if !img.cond(eCompany.guard)(frame) || img.cond(ePerson.guard)(frame) {
		t.Error("COMPANY must satisfy exactly the company guard")
	}
	frame[kind] = slotValue{set: true, i: int64(kt.tags[0])}
	if img.cond(eCompany.guard)(frame) || !img.cond(ePerson.guard)(frame) {
		t.Error("PERSON must satisfy exactly the person guard")
	}

	// require is a bitset over top-level forms (ST4): application is
	// formID 1, so company_review holds exactly bit 0.
	if got := img.stages[company].requires; got != 1 {
		t.Errorf("company_review requires %#x, want %#x", got, 1)
	}
	if got := img.stages[stageIDBy(t, img, "closed")]; len(got.edges) != 0 ||
		got.requires != 0 {
		t.Errorf("closed = %d edges, requires %#x, want terminal and empty",
			len(got.edges), got.requires)
	}

	// P1 gave transitions no per-node representation: draft grants accept
	// to Manager alone, so an actor who is only an Applicant or only a
	// Registrar fails the mask, and so does one who holds neither.
	manager := roleIDBy(t, img, "Manager")
	applicant := roleIDBy(t, img, "Applicant")
	registrar := roleIDBy(t, img, "Registrar")
	if !img.stages[draft].mayAccept([]roleID{manager}) {
		t.Error("Manager must accept out of draft")
	}
	if !img.stages[draft].mayReject([]roleID{manager}) {
		t.Error("Manager must reject out of draft")
	}
	for _, r := range []roleID{applicant, registrar, 0} {
		if img.stages[draft].mayAccept([]roleID{r}) || img.stages[draft].mayReject([]roleID{r}) {
			t.Errorf("role %d must not transition out of draft", r)
		}
	}
	// An empty role set transitions nowhere: authority derives from roles
	// alone (DESIGN §4.3).
	if img.stages[draft].mayAccept(nil) {
		t.Error("no roles must not accept")
	}
	// Draft grants no accept through a role block: P1 keeps that shape
	// impossible, so the mask came only from the stage's own statements.
	if img.stages[draft].accept != 1<<(manager-1) {
		t.Errorf("draft accept mask = %#x, want the Manager bit alone",
			img.stages[draft].accept)
	}
}

// --- Appendix A: permission masks ---

func TestSpecExamplePermissions(t *testing.T) {
	img := lowerExample(t)
	risk := slotByPath(t, img, "application.internal.risk_score")
	internal := slotByPath(t, img, "application.internal")
	archive := slotByPath(t, img, "archive")
	company := stageIDBy(t, img, "company_review")

	applicant := roleIDBy(t, img, "Applicant")
	manager := roleIDBy(t, img, "Manager")
	registrar := roleIDBy(t, img, "Registrar")
	app := slotByPath(t, img, "application")

	// Applicant: `allow view, edit, submit application` then
	// `deny edit application.internal`, inherited down to the leaf.
	got := img.effective([]roleID{applicant}, noStage, risk)
	if got&permView == 0 || got&permEdit != 0 {
		t.Errorf("applicant on risk_score = %05b, want view without edit", got)
	}
	if img.effective([]roleID{applicant}, noStage, internal)&permEdit != 0 {
		t.Error("the denied group itself must not be editable")
	}
	// The deny was declared on the group, not the leaf: the fold put both
	// bits on every slot the scope chain reaches (§3.5).
	if img.perms.byRole[applicant].allow[risk]&permEdit == 0 {
		t.Error("edit must be allowed on the leaf before the denial applies")
	}
	if img.perms.byRole[applicant].deny[risk]&permEdit == 0 {
		t.Error("the group's denial must reach the leaf in the deny array")
	}
	// Manager holds the same group through its own allow, with no deny.
	if got := img.effective([]roleID{manager}, noStage, risk); got&permEdit == 0 {
		t.Errorf("manager on risk_score = %05b, want edit", got)
	}
	// One actor with both roles: deny overrides allow across all effective
	// roles, whoever granted what (DESIGN §4.3).
	if got := img.effective([]roleID{applicant, manager}, noStage, risk); got&permEdit != 0 {
		t.Errorf("a dual role still edits risk_score: %05b", got)
	}
	// Default-deny leaves nothing set for a role that was never granted:
	// nobody but Registrar may review, and no role may sign archive.
	reviewed := img.effective([]roleID{registrar}, noStage, slotByPath(t, img, "application"))
	if reviewed&permReview == 0 {
		t.Errorf("registrar on the form = %05b, want review", reviewed)
	}
	if got := img.effective([]roleID{registrar}, company, slotByPath(t, img, "archive.filed_at")); got&permSign != 0 {
		t.Errorf("nobody may sign archive: %05b", got)
	}

	// P3 made the target shapes true at compile time, so a sign bit lands on
	// a signature field, and review is Manager's too: `review application`
	// is granted to both Registrar and Manager in the example.
	if got := img.effective([]roleID{applicant}, noStage,
		slotByPath(t, img, "application")); got&permReview != 0 {
		t.Errorf("the Applicant was never granted review: %05b", got)
	}
	sign := slotByPath(t, img, "application.approved_by")
	if img.effective([]roleID{manager}, noStage, sign)&permSign == 0 {
		t.Error("Manager must sign approved_by")
	}
	if img.effective([]roleID{registrar}, noStage, sign)&permSign != 0 {
		t.Error("only Manager was granted that signature")
	}

	// A stage-targeted grant is visible only in its stage: the same
	// Registrar sees no edit or submit on archive while the case sits in
	// draft, and all three bits on arrival.
	for _, s := range []nodeID{archive, slotByPath(t, img, "archive.filed_at"),
		slotByPath(t, img, "archive.note")} {
		if got := img.effective([]roleID{registrar}, noStage, s); got&(permEdit|permSubmit) != 0 {
			t.Errorf("registrar outside company_review holds %05b on %s", got, img.path(s))
		}
		want := permView | permEdit | permSubmit
		if got := img.effective([]roleID{registrar}, company, s); got&want != want {
			t.Errorf("registrar in company_review holds %05b on %s, want %05b",
				got, img.path(s), want)
		}
	}
	// One overlay row per role and stage: company_review grants Registrar
	// `view all` and archive's three bits, so the row covers the whole
	// case, with the archive subtree carrying the extra bits.
	over := img.perms.byRole[registrar].over
	if len(over) != 1 || over[0].at != company {
		t.Fatalf("registrar overlays = %+v, want one for company_review", over)
	}
	if len(over[0].slots) != len(img.slots) {
		t.Errorf("the stage overlay touches %d slots, want all %d",
			len(over[0].slots), len(img.slots))
	}
	over2 := img.perms.byRole[applicant].over
	if len(over2) != 2 || over2[0].at != stageIDBy(t, img, "draft") ||
		over2[1].at != stageIDBy(t, img, "correction") {
		t.Fatalf("applicant overlays = %d rows at %d, %d, want draft then "+
			"correction in stage order", len(over2), over2[0].at, over2[1].at)
	}
	// Both layers hold the bit in correction: the stage's `allow Applicant
	// edit application` reaches the leaf, and so does the role block's
	// denial. Deny-override is then one AND-NOT at the end, so a stage
	// cannot outrun a denial (§3.5, DESIGN §4.3).
	allow, deny := img.perms.permAt(applicant, over2[1].at, risk)
	if allow&permEdit == 0 || deny&permEdit == 0 {
		t.Errorf("applicant in correction: allow %05b deny %05b, want edit in both",
			allow, deny)
	}
	if got := img.effective([]roleID{applicant}, over2[1].at, risk); got&permEdit != 0 {
		t.Errorf("a stage allow outran a role-block denial: %05b", got)
	}
	if got := img.effective([]roleID{applicant}, over2[1].at, app); got&permEdit == 0 {
		t.Errorf("correction must hold edit on the form itself: %05b", got)
	}
	// `allow Registrar view all` is the unconditional half: application
	// stays viewable in every stage.
	if img.effective([]roleID{registrar}, noStage, internal)&permView == 0 {
		t.Error("Registrar's role-block view must reach the whole case")
	}
	// The no-role table grants nothing, which is why ordinals and indexes
	// agree without a special case.
	for _, s := range []nodeID{0, app, risk} {
		if img.effective([]roleID{0}, company, s) != 0 {
			t.Errorf("role 0 holds bits on slot %d", s)
		}
	}
}

// --- Appendix A: conditions, presence and dependency fanout ---

func TestSpecExampleConditions(t *testing.T) {
	img := lowerExample(t)
	kind := slotByPath(t, img, "application.kind")
	kt := img.types[img.slots[kind].typ]
	company := kt.tags[1]

	internalSlot := slotByPath(t, img, "application.internal")
	internal := img.slots[internalSlot]
	if internal.condPres == 0 {
		t.Fatal("internal lost its present condition")
	}
	frame := make(stateView, len(img.slots))
	present := img.cond(internal.condPres)
	if present(frame) {
		t.Error("a group must be absent while its condition operand is unset")
	}
	frame[kind] = slotValue{set: true, i: int64(company)}
	if !present(frame) {
		t.Error("COMPANY must make internal present")
	}
	frame[kind] = slotValue{set: true, i: int64(kt.tags[0])}
	if present(frame) {
		t.Error("PERSON must not make internal present")
	}

	// Relative and absolute operands anchor at the same slot: the group's
	// own `.kind` and the stage guard's `application.kind` resolve to one
	// index, which the folded adjacency shows in one list (§5.3).
	owners := img.depOwners(kind)
	if len(owners) != 2 {
		t.Fatalf("kind is read by %v, want the group and the collection", owners)
	}
	for _, want := range []nodeID{slotByPath(t, img, "application.internal"),
		slotByPath(t, img, "application.shareholders")} {
		if !contains(owners, want) {
			t.Errorf("owners %v missing %s", owners, img.path(want))
		}
	}
	if len(img.depOwners(slotByPath(t, img, "application.full_name"))) != 0 {
		t.Error("an unread slot must own nothing")
	}

	// required[.kind == COMPANY] on a multiple collection is one closure
	// for the collection, not one per member (F6).
	shSlot := slotByPath(t, img, "application.shareholders")
	sh := img.slots[shSlot]
	if sh.condReq == 0 || sh.flags&flagMultiple == 0 {
		t.Fatalf("shareholders = %+v, want a required conditional collection", sh)
	}
	if sh.flags&flagRequired != 0 {
		t.Error("required and required[expr] must not both be set")
	}

	// Preorder numbering is what makes §3.2's dense ranges true: the
	// collection's span covers exactly its members, and members of a
	// collection are never read by a condition (E2).
	app := slotByPath(t, img, "application")
	holder := slotByPath(t, img, "application.shareholders.holder_name")
	shares := slotByPath(t, img, "application.shareholders.shares")
	if sh.span != 3 || holder != shSlot+1 || shares != shSlot+2 {
		t.Errorf("shareholders spans %d from %d, members %d and %d: want three "+
			"contiguous slots", sh.span, shSlot, holder, shares)
	}
	if len(img.depOwners(holder)) != 0 || len(img.depOwners(shares)) != 0 {
		t.Error("no condition may read a collection member") // E2
	}

	// Every declaration in the form is governed by the form itself, the
	// outermost verifiable unit (F3).
	for _, s := range []nodeID{app, shSlot, holder, shares, internalSlot} {
		if img.slots[s].verif != app {
			t.Errorf("%s verifiable unit = %d, want the form %d", img.path(s),
				img.slots[s].verif, app)
		}
	}
	if img.slots[shares].flags&flagRequired == 0 {
		t.Error("shares must carry the plain required flag")
	}
	if img.slots[holder].flags&flagRequired == 0 {
		t.Error("holder_name must carry the plain required flag")
	}

	// Signature entries read exactly as S2 resolved them (§3.6): one field
	// per entry, no fallback.
	sub := img.sigs[img.slots[slotByPath(t, img, "application.submitted")].sig]
	if sub.role != roleIDBy(t, img, "Applicant") || sub.assigned != 0 {
		t.Errorf("submitted = %+v, want the Applicant role only", sub)
	}
	appr := img.sigs[img.slots[slotByPath(t, img, "application.approved_by")].sig]
	if appr.role != 0 || appr.assigned != slotByPath(t, img, "application.reviewer") {
		t.Errorf("approved_by = %+v, want the reviewer slot only", appr)
	}
	// The role-typed field confers Manager and holds no value type (T4).
	rev := img.slots[slotByPath(t, img, "application.reviewer")]
	if rev.role != roleIDBy(t, img, "Manager") || rev.typ != 0 {
		t.Errorf("reviewer = role %d type %d, want Manager and no value type",
			rev.role, rev.typ)
	}
}

// --- folds, overlays and shapes the §7 example does not exercise ---

func TestConstantGuardsFold(t *testing.T) {
	const src = `
types { legal : enum(PERSON, COMPANY); }

roles {
    R { allow view all; }
}
forms {
    f {
        n : integer;
        a present[1 < 2]   { x : integer; }
        b present[2 < 1]   { y : integer; }
        c present["p" > "q"] { z : integer; }
    }
}
stages {
    s {
        allow R view all;
        accept[PERSON == COMPANY] t;
        accept[1 == 1] u;
    }
    t {}
    u {}
}
`
	img, warnings := compile(t, src)
	if len(warnings) == 0 {
		t.Error("a constant transition guard must draw ST6's warning")
	}
	// A folded guard is always() or never(): identical semantics, zero
	// evaluation (§3.4), so the closure's answer must not depend on data.
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"f.a", true}, {"f.b", false}, {"f.c", false},
	} {
		c := img.cond(img.slots[slotByPath(t, img, tc.path)].condPres)
		empty := make(stateView, len(img.slots))
		full := fullView(len(img.slots))
		if c(empty) != tc.want || c(full) != tc.want {
			t.Errorf("%s folded to %v, want %v", tc.path, c(empty), tc.want)
		}
	}
	s := stageIDBy(t, img, "s")
	n := len(img.slots)
	never := img.stages[s].edges[0]
	if never.guard == 0 || !isNever(img.cond(never.guard), n) {
		t.Error("a false constant guard must fold to never()")
	}
	e, _ := img.stages[s].edgeTo(stageIDBy(t, img, "u"))
	if e.guard == 0 || !isAlways(img.cond(e.guard), n) {
		t.Error("a true constant guard must fold to always()")
	}
}

// isNever and isAlways identify folded guards by behaviour: a guard whose
// answer does not move between an empty case and a case where every slot
// carries a value is a constant (§3.4).
func isNever(c condFn, n int) bool {
	return !c(make(stateView, n)) && !c(fullView(n))
}

func isAlways(c condFn, n int) bool {
	return c(make(stateView, n)) && c(fullView(n))
}

// fullView is the probe for a folded guard: every slot present and every
// payload zero. A guard whose answer moves under it reads data, so it is
// not the constant §3.4 claims it is.
func fullView(n int) stateView {
	v := make(stateView, n)
	for i := range v {
		v[i] = slotValue{set: true}
	}
	return v
}

func TestTransitionDenyOverridesAllow(t *testing.T) {
	const src = `
roles {
    Boss   { allow view all; }
    Helper { allow view all; }
}
forms {
    f { x : integer; }
}
stages {
    a {
        allow Boss accept;
        accept b;
    }
    b {
        allow Boss accept;
        deny Helper accept;
        accept c;
    }
    c {}
}
`
	img, _ := compile(t, src)
	boss := roleIDBy(t, img, "Boss")
	helper := roleIDBy(t, img, "Helper")
	a, b := stageIDBy(t, img, "a"), stageIDBy(t, img, "b")

	if img.stages[a].mayAccept([]roleID{helper}) {
		t.Error("default-deny: Helper holds no accept anywhere")
	}
	if !img.stages[a].mayAccept([]roleID{boss}) {
		t.Error("Boss must accept out of a")
	}
	// P6: the deny is kept as its own mask, because the actor's roles are
	// only known per request (§3.7).
	if !img.stages[b].mayAccept([]roleID{boss}) {
		t.Error("Boss must still accept out of b on its own")
	}
	if img.stages[b].mayAccept([]roleID{boss, helper}) {
		t.Error("a deny held through Helper must strip the Boss grant")
	}
	if img.stages[b].acceptNo != 1<<(helper-1) {
		t.Errorf("b's deny mask = %#x, want the Helper bit alone", img.stages[b].acceptNo)
	}
	if img.stages[b].reject != 0 || img.stages[b].rejectNo != 0 {
		t.Error("reject was never granted or denied here")
	}
}

func TestStageDenyStripsRoleAllow(t *testing.T) {
	const src = `
roles {
    Keeper { allow view, edit secret; }
}
forms {
    secret { value : string; }
}
stages {
    open {
        deny Keeper edit secret;
        accept locked;
    }
    locked {
        allow Keeper edit secret;
    }
}
`
	img, _ := compile(t, src)
	keeper := roleIDBy(t, img, "Keeper")
	value := slotByPath(t, img, "secret.value")

	// The role block allows edit unconditionally; the stage's overlay adds
	// a denial, and deny-override is one AND-NOT at the end (§3.5).
	if img.effective([]roleID{keeper}, noStage, value)&permEdit == 0 {
		t.Error("the role grant must hold outside the denying stage")
	}
	open := stageIDBy(t, img, "open")
	if got := img.effective([]roleID{keeper}, open, value); got&permEdit != 0 {
		t.Errorf("stage deny did not strip edit: %05b", got)
	}
	if got := img.effective([]roleID{keeper}, open, value); got&permView == 0 {
		t.Error("the denial named edit only, so view must survive")
	}
	locked := stageIDBy(t, img, "locked")
	if got := img.effective([]roleID{keeper}, locked, value); got&permEdit == 0 {
		t.Error("the stage allow must not leak a denial into another stage")
	}
}

func TestBooleanAndNestedConditions(t *testing.T) {
	const src = `
roles {
    R { allow view all; }
}
forms {
    f {
        urgent : boolean;
        count  : integer;
        fee    : decimal(6, 2);
        when   : date;
        label  : string;
        level  : integer;

        g1 present[.urgent]                        { a : integer; }
        g2 present[!.urgent && .count > 10]        { b : integer; }
        g3 present[.count > .level && .label != "x"] { c : integer; }
        g4 present[.fee >= 5 || .when < "2026-01-01"] { d : integer; }
    }
}
stages {
    s { allow R view all; }
}
`
	img, _ := compile(t, src)
	slots := map[string]nodeID{}
	for _, name := range []string{"urgent", "count", "fee", "when", "label", "level"} {
		slots[name] = slotByPath(t, img, "f."+name)
	}
	set := func(v stateView, name string, sv slotValue) { v[slots[name]] = sv }
	eval := func(path string) func(stateView) bool {
		return img.cond(img.slots[slotByPath(t, img, path)].condPres)
	}

	g1 := eval("f.g1")
	v := make(stateView, len(img.slots))
	if g1(v) {
		t.Error("an unset boolean satisfies nothing")
	}
	set(v, "urgent", slotValue{set: true, i: 0})
	if g1(v) {
		t.Error("false must not satisfy present[.urgent]")
	}
	set(v, "urgent", slotValue{set: true, i: 1})
	if !g1(v) {
		t.Error("true must satisfy present[.urgent]")
	}

	g2 := eval("f.g2")
	v = make(stateView, len(img.slots))
	if g2(v) {
		t.Error("absent operands must make the conjunction false")
	}
	set(v, "urgent", slotValue{set: true, i: 0})
	set(v, "count", slotValue{set: true, i: 11})
	if !g2(v) {
		t.Error("!false && 11 > 10 must be true")
	}
	set(v, "count", slotValue{set: true, i: 10})
	if g2(v) {
		t.Error("10 > 10 must be false")
	}

	// Slot-versus-slot comparison (E3: identical types).
	g3 := eval("f.g3")
	v = make(stateView, len(img.slots))
	set(v, "count", slotValue{set: true, i: 3})
	set(v, "level", slotValue{set: true, i: 2})
	set(v, "label", slotValue{set: true, s: "y"})
	if !g3(v) {
		t.Error("3 > 2 && \"y\" != \"x\" must be true")
	}
	set(v, "label", slotValue{set: true, s: "x"})
	if g3(v) {
		t.Error("an equal string must fail the conjunction")
	}

	// The disjunction pairs a decimal with an integer literal normalized
	// to decimal, and a date with an ISO string normalized to an instant
	// (SYNTAX §5.8), so both branches compare typed values.
	g4 := eval("f.g4")
	v = make(stateView, len(img.slots))
	set(v, "fee", slotValue{set: true, d: decimal.RequireFromString("5.00")})
	if !g4(v) {
		t.Error("decimal 5.00 >= integer literal 5 must be true")
	}
	v = make(stateView, len(img.slots))
	set(v, "fee", slotValue{set: true, d: decimal.RequireFromString("4.99")})
	set(v, "when", slotValue{set: true, t: day(2025, 12, 31)})
	if !g4(v) {
		t.Error("a 2025 date must satisfy < \"2026-01-01\"")
	}
	v = make(stateView, len(img.slots))
	set(v, "fee", slotValue{set: true, d: decimal.RequireFromString("4.99")})
	set(v, "when", slotValue{set: true, t: day(2026, 1, 1)})
	if g4(v) {
		t.Error("neither branch holds at the boundary")
	}

	// Both guards read exactly the slots they name (§5.3's adjacency).
	if got := img.depOwners(slots["urgent"]); len(got) != 2 {
		t.Errorf("urgent is read by %d owners, want g1 and g2", len(got))
	}
	if got := img.depOwners(slots["level"]); len(got) != 1 ||
		got[0] != slotByPath(t, img, "f.g3") {
		t.Errorf("level owners = %v, want g3 alone", got)
	}
}

func TestOptionsAndCustomTypeHooks(t *testing.T) {
	const src = `
types {
    money : decimal(8, 2);
}
roles {
    R { allow view all; }
}
forms {
    f {
        amount : money;
        city   : options;
        other  : options;
    }
}
stages {
    s { allow R view all; }
}
`
	img, _ := compile(t, src)
	money := img.types[img.slots[slotByPath(t, img, "f.amount")].typ]
	if money.hook == 0 || img.hooks[money.hook].kind != hookByType ||
		img.name(img.hooks[money.hook].name) != "money" {
		t.Errorf("money Validate binding = %+v, want a type key", img.hooks[money.hook])
	}
	// Built-in types bind no hook: only declared custom types are keys.
	builtin := img.types[img.slots[slotByPath(t, img, "f.other")].typ]
	if builtin.hook != 0 {
		t.Errorf("options built-in carries hook %d", builtin.hook)
	}
	city := img.slots[slotByPath(t, img, "f.city")]
	if city.hook == 0 || img.hooks[city.hook].kind != hookByPath ||
		img.name(img.hooks[city.hook].name) != "f.city" {
		t.Errorf("city Options binding = %+v, want a path key", img.hooks[city.hook])
	}
	// Two options fields are two keys, because the binding is per path.
	other := img.slots[slotByPath(t, img, "f.other")]
	if other.hook == city.hook {
		t.Error("options fields must not share one path key")
	}
}

// --- §3.4's closed constructor set, one guard per shape ---

// condShapesSrc exercises every comparator the emitter can produce: slot
// against literal, literal against slot, slot against slot, negation,
// conjunction, disjunction, truth, and each value class. The assertions
// below are the semantics table §4's E1–E4 rows promise, so this is also
// the test that catches a comparator wired to the wrong operand class.
const condShapesSrc = `
types {
    money : decimal(8, 2);
    stamp : datetime;
    state : enum(OPEN, SHUT);
}

roles {
    R { allow view all; }
}

forms {
    f {
        flag  : boolean;
        other : boolean;
        n     : integer;
        m     : integer;
        label : string;
        note  : string;
        fee   : money;
        cost  : money;
        at    : stamp;
        later : stamp;
        st    : state;
        st2   : state;

        tTrue  present[.flag]                         { a : integer; }
        tNeg   present[!.flag]                        { b : integer; }
        tAnd   present[.flag && .n > 3]               { c : integer; }
        tOr    present[.n == 4 || .label != "x"]      { d : integer; }
        tRev   present[10 < .n]                       { e : integer; }
        tRevLe present[10 <= .n]                      { e1 : integer; }
        tRevGt present[10 > .n]                       { e2 : integer; }
        tRevGe present[10 >= .n]                      { e3 : integer; }
        tStr   present[.label >= "abc"]               { g : integer; }
        tDec   present[.fee >= 5.5]                   { h : integer; }
        tDecI  present[.fee >= 5]                     { s : integer; }
        tTime  present[.at < "2026-01-01T00:00:00Z"]  { i : integer; }
        tTag   present[.st == OPEN]                   { j : integer; }
        tBoolC present[.flag != true]                 { k : integer; }
        tSb    present[.flag == .other]               { l : integer; }
        tSi    present[.n != .m]                      { m1 : integer; }
        tSd    present[.fee <= .cost]                 { n1 : integer; }
        tSt    present[.at == .later]                 { o : integer; }
        tSe    present[.st != .st2]                   { p : integer; }
        tSs    present[.label < .note]                { q : integer; }
        tNot   present[!(.n > 3)]                     { t : integer; }
        tNotOr present[!(.label == "x" || .flag)]      { u : integer; }
        tFold  present[!(1 < 2)]                      { r : integer; }
    }
}

stages {
    s { allow R view all; }
}
`

func TestConditionShapes(t *testing.T) {
	img, _ := compile(t, condShapesSrc)
	at := func(path string) nodeID { return slotByPath(t, img, "f."+path) }
	guard := func(group string) condFn {
		return img.cond(img.slots[at(group)].condPres)
	}
	iv := func(n int) slotValue { return slotValue{set: true, i: int64(n)} }
	sv := func(s string) slotValue { return slotValue{set: true, s: s} }
	money := func(text string) slotValue {
		return slotValue{set: true, d: decimal.RequireFromString(text)}
	}
	stamp := func(y, mo, d, hh, mm, ss int) slotValue {
		return slotValue{set: true,
			t: time.Date(y, time.Month(mo), d, hh, mm, ss, 0, time.UTC)}
	}
	// state's tags are OPEN then SHUT, dense within the type (§3.1).
	state := img.types[img.slots[at("st")].typ].tags
	on := slotValue{set: true, i: int64(state[0])}
	off := slotValue{set: true, i: int64(state[1])}
	fill := func(patches ...map[string]slotValue) stateView {
		f := make(stateView, len(img.slots))
		for _, one := range patches {
			for path, value := range one {
				f[at(path)] = value
			}
		}
		return f
	}
	yes := map[string]slotValue{"flag": iv(1)}
	no := map[string]slotValue{"flag": iv(0)}

	// Absence satisfies nothing, in either direction: every guard here
	// reads data, so an empty case answers false for all of them (SYNTAX
	// Appendix C). !(1 < 2) folds to never() and agrees by construction.
	empty := make(stateView, len(img.slots))
	for _, group := range []string{"tTrue", "tNeg", "tAnd", "tOr", "tRev",
		"tRevLe", "tRevGt", "tRevGe",
		"tStr", "tDec", "tDecI", "tTime", "tTag", "tBoolC", "tSb", "tSi",
		"tSd", "tSt", "tSe", "tSs", "tFold"} {
		if guard(group)(empty) {
			t.Errorf("%s is satisfied by an empty case", group)
		}
	}
	if !isNever(guard("tFold"), len(img.slots)) {
		t.Error("!(1 < 2) must fold to never(), reading no slot (§3.4)")
	}

	cases := []struct {
		group string
		fill  stateView
		want  bool
		note  string
	}{
		{"tTrue", fill(yes), true, "true is truth"},
		{"tTrue", fill(no), false, "false is not"},
		{"tNeg", fill(no), true, "negation of a value"},
		{"tAnd", fill(yes, map[string]slotValue{"n": iv(4)}),
			true, "both halves of &&"},
		{"tAnd", fill(yes, map[string]slotValue{"n": iv(3)}), false, "3 is not > 3"},
		{"tOr", fill(map[string]slotValue{"n": iv(4)}), true, "left branch of ||"},
		{"tOr", fill(map[string]slotValue{"label": sv("y")}), true, "right branch"},
		{"tOr", fill(map[string]slotValue{"n": iv(5), "label": sv("x")}),
			false, "neither branch"},
		{"tRev", fill(map[string]slotValue{"n": iv(11)}), true, "10 < 11"},
		{"tRev", fill(map[string]slotValue{"n": iv(10)}), false, "10 < 10"},
		// A literal on the left reverses the operator it is read with
		// (§3.4), so each direction needs its own case.
		{"tRevLe", fill(map[string]slotValue{"n": iv(10)}), true, "10 <= 10"},
		{"tRevLe", fill(map[string]slotValue{"n": iv(9)}), false, "10 <= 9"},
		{"tRevGt", fill(map[string]slotValue{"n": iv(9)}), true, "10 > 9"},
		{"tRevGt", fill(map[string]slotValue{"n": iv(10)}), false, "10 > 10"},
		{"tRevGe", fill(map[string]slotValue{"n": iv(10)}), true, "10 >= 10"},
		{"tRevGe", fill(map[string]slotValue{"n": iv(11)}), false, "10 >= 11"},
		{"tStr", fill(map[string]slotValue{"label": sv("abc")}), true, ">= at equal"},
		{"tStr", fill(map[string]slotValue{"label": sv("abb")}), false, "lex order"},
		{"tDec", fill(map[string]slotValue{"fee": money("5.50")}), true,
			"the boundary itself"},
		{"tDec", fill(map[string]slotValue{"fee": money("5.49")}), false, "below it"},
		// An integer literal against a decimal slot: §5.8 normalizes 5 to
		// decimal 5, so 5.00 satisfies >= 5 and 4.99 does not.
		{"tDecI", fill(map[string]slotValue{"fee": money("5.00")}), true,
			"decimal against an integer literal"},
		{"tDecI", fill(map[string]slotValue{"fee": money("4.99")}), false,
			"one cent below"},
		{"tTime", fill(map[string]slotValue{"at": stamp(2025, 12, 31, 23, 59, 59)}),
			true, "before the instant"},
		{"tTime", fill(map[string]slotValue{"at": stamp(2026, 1, 1, 0, 0, 0)}),
			false, "ISO 8601 normalized to an instant (§5.8)"},
		{"tTag", fill(map[string]slotValue{"st": on}), true, "OPEN"},
		{"tTag", fill(map[string]slotValue{"st": off}), false, "SHUT"},
		{"tBoolC", fill(map[string]slotValue{"flag": iv(0)}), true, "false != true"},
		{"tBoolC", fill(yes), false, "true != true"},
		{"tSb", fill(yes, map[string]slotValue{"other": iv(1)}),
			true, "booleans, slot against slot"},
		{"tSb", fill(yes, no), false, "and it moves"},
		{"tNot", fill(map[string]slotValue{"n": iv(5)}), false,
			"! of a true comparison"},
		{"tNot", fill(map[string]slotValue{"n": iv(2)}), true,
			"! of a false one"},
		{"tNotOr", fill(map[string]slotValue{"label": sv("y"), "flag": iv(0)}),
			true, "neither inner branch holds"},
		{"tNotOr", fill(map[string]slotValue{"label": sv("x"), "flag": iv(1)}),
			false, "one inner branch holds"},
		{"tSi", fill(map[string]slotValue{"n": iv(1), "m": iv(2)}),
			true, "integers differ"},
		{"tSi", fill(map[string]slotValue{"n": iv(2), "m": iv(2)}),
			false, "integers equal"},
		{"tSd", fill(map[string]slotValue{"fee": money("1.00"),
			"cost": money("1.00")}), true, "<= at equal"},
		{"tSd", fill(map[string]slotValue{"fee": money("1.01"),
			"cost": money("1.00")}), false, "decimal order"},
		{"tSt", fill(map[string]slotValue{"at": stamp(2026, 3, 1, 0, 0, 0),
			"later": stamp(2026, 3, 1, 0, 0, 0)}), true, "the same instant"},
		{"tSt", fill(map[string]slotValue{"at": stamp(2026, 3, 1, 0, 0, 0),
			"later": stamp(2026, 3, 1, 0, 0, 1)}), false, "a second apart"},
		{"tSe", fill(map[string]slotValue{"st": on, "st2": off}),
			true, "different tags"},
		{"tSe", fill(map[string]slotValue{"st": off, "st2": off}),
			false, "the same tag"},
		{"tSs", fill(map[string]slotValue{"label": sv("a"), "note": sv("b")}),
			true, "strings compare"},
	}
	for _, tc := range cases {
		if got := guard(tc.group)(tc.fill); got != tc.want {
			t.Errorf("%s: %s = %v, want %v", tc.group, tc.note, got, tc.want)
		}
	}

	// The folded adjacency lists exactly the slots each guard reads, so
	// §5.3's fanout can never visit a path no operand mentioned.
	for group, want := range map[string][]string{
		"tTrue":  {"f.flag"},
		"tRev":   {"f.n"},
		"tRevLe": {"f.n"},
		"tRevGt": {"f.n"},
		"tRevGe": {"f.n"},
		"tOr":    {"f.n", "f.label"},
		"tSt":    {"f.at", "f.later"},
		"tNot":   {"f.n"},
		"tNotOr": {"f.flag", "f.label"},
		"tFold":  {},
	} {
		got := readBy(img, at(group))
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s reads %v, want %v", group, got, want)
		}
	}
}

// readBy inverts the fanout table: the paths whose affected set names this
// declaration as a condition owner, in slot order. It reads only §3.8's
// arrays, so it proves what Update will actually walk rather than what the
// emitter meant.
func readBy(img *image, owner nodeID) []string {
	var out []string
	for s := range img.depFrom {
		if contains(img.depOwners(nodeID(s)), owner) {
			out = append(out, img.path(nodeID(s)))
		}
	}
	return out
}

func TestConstantFoldsEveryClass(t *testing.T) {
	const src = `
types {
    kind : enum(OPEN, SHUT);
}

roles {
    R { allow view all; }
}

forms {
    f {
        k : kind;
        a present[3 > 2]            { x : integer; }
        b present["p" < "q"]        { y : integer; }
        c present[1.5 >= 1.2]       { z : integer; }
        d present[2 == 2 && 1 != 2] { w : integer; }
        e present[1 > 2 || 3 > 2]   { u : integer; }
        g present[.k == OPEN]       { v : integer; }
        h present[1 < 2.5]          { p : integer; }
        i present[3 > 2.5 && 1 != 2] { q : integer; }
    }
}

stages {
    s { allow R view all; }
}
`
	img, _ := compile(t, src)
	// A folded guard answers without reading data, so it owns no fanout
	// either (§5.3): the walk never enqueues anything for it.
	for _, tc := range []struct {
		group string
		want  bool
		reads int
	}{
		{"a", true, 0}, {"b", true, 0}, {"c", true, 0}, {"d", true, 0},
		{"e", true, 0}, {"g", false, 1},
		// Two number literals of different widths are one class (§3.4).
		{"h", true, 0}, {"i", true, 0},
	} {
		id := img.slots[slotByPath(t, img, "f."+tc.group)].condPres
		if id == 0 {
			t.Fatalf("%s lost its condition", tc.group)
		}
		c := img.cond(id)
		if got := c(make(stateView, len(img.slots))); got != tc.want {
			t.Errorf("%s on an empty case = %v, want %v", tc.group, got, tc.want)
		}
		if got := c(fullView(len(img.slots))); got != tc.want {
			t.Errorf("%s on a full case = %v, want %v", tc.group, got, tc.want)
		}
		if got := len(readBy(img, slotByPath(t, img, "f."+tc.group))); got != tc.reads {
			t.Errorf("%s reads %d slots, want %d", tc.group, got, tc.reads)
		}
	}
}

// --- §10's refusal path: a violated assumption is reported, not repaired ---

func TestLowerRefusesViolatedInvariants(t *testing.T) {
	// Built by hand, the way the front end would, with one guarantee
	// broken each time. Nothing here is reachable from legal source: each
	// shape the subtests assemble is one the checker rejects first.
	base := func() *schema.Schema {
		boolean := &schema.Type{Name: "boolean", Base: "boolean"}
		dec := &schema.Type{Name: "amount", Base: "decimal", Precision: 6, Scale: 2}
		x := &schema.Node{Kind: "field", Name: "x", Path: "f.x", Type: boolean}
		y := &schema.Node{Kind: "field", Name: "y", Path: "f.y", Type: boolean}
		z := &schema.Node{Kind: "field", Name: "z", Path: "f.z", Type: dec}
		f := &schema.Node{Kind: "form", Name: "f", Path: "f",
			Nodes: []*schema.Node{x, y, z}}
		st := &schema.Stage{Name: "s"}
		return &schema.Schema{
			Roles:   []*schema.Role{{Name: "R"}},
			Forms:   []*schema.Node{f},
			Stages:  []*schema.Stage{st},
			Initial: st,
		}
	}

	t.Run("required and required[expr] are one attribute", func(t *testing.T) {
		s := base()
		x := s.Forms[0].Nodes[0]
		x.Required = true
		// A legal condition on a sibling: the violation is the flag pair,
		// not the expression (§5.2).
		x.RequiredCond = &schema.Expr{Op: "", Node: s.Forms[0].Nodes[1]}
		_, err := lower("test", s)
		requireProblem(t, err, "required and required[expr]")
	})

	t.Run("unnormalized decimal literal", func(t *testing.T) {
		s := base()
		x, z := s.Forms[0].Nodes[0], s.Forms[0].Nodes[2]
		x.PresentCond = &schema.Expr{
			Op:    ">",
			Left:  &schema.Expr{Node: z},
			Right: &schema.Expr{Lit: &schema.Value{Kind: "integer", Int: 5}},
		}
		_, err := lower("test", s)
		requireProblem(t, err, "not normalized")
	})

	t.Run("signature resolves to neither", func(t *testing.T) {
		s := base()
		sig := &schema.Node{Kind: "field", Name: "sig", Path: "f.sig",
			Type: &schema.Type{Name: "signature", Base: "signature"}}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, sig)
		_, err := lower("test", s)
		requireProblem(t, err, "signature reference is unresolved")
	})

	t.Run("edge destination unnumbered", func(t *testing.T) {
		s := base()
		// The destination stage exists as a value but is not in Stages, so
		// no ordinal was ever assigned to it (ST2's shape).
		s.Stages[0].Edges = []schema.Edge{{Dest: &schema.Stage{Name: "t"}}}
		_, err := lower("test", s)
		requireProblem(t, err, "was not numbered")
	})

	// The emitter's anti-drift branches (§4's table, read as assertions).
	// Each shape below is one the checker rejects, so the message is the
	// runtime saying "the front end and I disagree about E2, E3, E4 or F7",
	// and the definition is refused rather than compiled on a guess.

	t.Run("ordering a boolean pair", func(t *testing.T) {
		s := base()
		x, y, z := s.Forms[0].Nodes[0], s.Forms[0].Nodes[1], s.Forms[0].Nodes[2]
		z.PresentCond = &schema.Expr{Op: "<",
			Left:  &schema.Expr{Node: x},
			Right: &schema.Expr{Node: y}}
		_, err := lower("test", s)
		requireProblem(t, err, "cannot order") // E3
	})

	t.Run("standalone literal is not a condition", func(t *testing.T) {
		s := base()
		z := s.Forms[0].Nodes[2]
		z.PresentCond = &schema.Expr{Lit: &schema.Value{Kind: "integer", Int: 5}}
		_, err := lower("test", s)
		requireProblem(t, err, "not a condition") // E4
	})

	t.Run("constants of unrelated classes", func(t *testing.T) {
		s := base()
		z := s.Forms[0].Nodes[2]
		z.PresentCond = &schema.Expr{Op: "==",
			Left:  &schema.Expr{Lit: &schema.Value{Kind: "string", Str: "a"}},
			Right: &schema.Expr{Lit: &schema.Value{Kind: "integer", Int: 1}}}
		_, err := lower("test", s)
		requireProblem(t, err, "cannot be compared") // E3
	})

	t.Run("operand is not a declaration", func(t *testing.T) {
		s := base()
		z := s.Forms[0].Nodes[2]
		foreign := &schema.Node{Kind: "field", Name: "q", Path: "other.q",
			Type: &schema.Type{Name: "integer", Base: "integer"}}
		z.PresentCond = &schema.Expr{Node: foreign}
		_, err := lower("test", s)
		requireProblem(t, err, "not a declaration")
	})

	t.Run("role-typed field is not a value", func(t *testing.T) {
		s := base()
		z := s.Forms[0].Nodes[2]
		holder := &schema.Node{Kind: "field", Name: "who", Path: "f.who",
			Role: s.Roles[0]}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, holder)
		z.PresentCond = &schema.Expr{Node: holder}
		_, err := lower("test", s)
		requireProblem(t, err, "is not a value") // E2, T4
	})

	t.Run("condition reads inside a collection", func(t *testing.T) {
		s := base()
		z := s.Forms[0].Nodes[2]
		inner := &schema.Node{Kind: "field", Name: "amt", Path: "f.rows.amt",
			Type: &schema.Type{Name: "integer", Base: "integer"}}
		rows := &schema.Node{Kind: "group", Name: "rows", Path: "f.rows",
			Multiple: true, Nodes: []*schema.Node{inner}}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, rows)
		z.PresentCond = &schema.Expr{Node: inner}
		_, err := lower("test", s)
		requireProblem(t, err, "inside a multiple collection") // E2
	})

	t.Run("twoSlots of two different types", func(t *testing.T) {
		s := base()
		// The owner is a third field: a condition that names itself is
		// F7's self-reference, and that check answers first.
		y, z := s.Forms[0].Nodes[1], s.Forms[0].Nodes[2]
		x := s.Forms[0].Nodes[0]
		x.PresentCond = &schema.Expr{Op: "==",
			Left:  &schema.Expr{Node: y},
			Right: &schema.Expr{Node: z}}
		_, err := lower("test", s)
		requireProblem(t, err, "not the same type") // E3
	})

	t.Run("twoSlots of two different enums", func(t *testing.T) {
		s := base()
		e1 := &schema.Type{Name: "one", Base: "enum", Tags: []string{"A1", "A2"}}
		e2 := &schema.Type{Name: "two", Base: "enum", Tags: []string{"B1", "B2"}}
		s.Types = []*schema.Type{e1, e2}
		p := &schema.Node{Kind: "field", Name: "p", Path: "f.p", Type: e1}
		q := &schema.Node{Kind: "field", Name: "q", Path: "f.q", Type: e2}
		host := &schema.Node{Kind: "field", Name: "w", Path: "f.w",
			Type: &schema.Type{Name: "integer", Base: "integer"}}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, p, q, host)
		host.PresentCond = &schema.Expr{Op: "==",
			Left:  &schema.Expr{Node: p},
			Right: &schema.Expr{Node: q}}
		_, err := lower("test", s)
		requireProblem(t, err, "different enums") // E3
	})

	t.Run("signature target is not role-typed", func(t *testing.T) {
		s := base()
		x := s.Forms[0].Nodes[0]
		sig := &schema.Node{Kind: "field", Name: "sig", Path: "f.sig",
			Type:      &schema.Type{Name: "signature", Base: "signature"},
			SignField: x}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, sig)
		_, err := lower("test", s)
		requireProblem(t, err, "not a role-typed field") // S2
	})

	t.Run("a group carries a value type", func(t *testing.T) {
		s := base()
		integer := &schema.Type{Name: "integer", Base: "integer"}
		kid := &schema.Node{Kind: "field", Name: "q", Path: "f.g.q", Type: integer}
		g := &schema.Node{Kind: "group", Name: "g", Path: "f.g",
			Type: integer, Nodes: []*schema.Node{kid}}
		s.Forms[0].Nodes = append(s.Forms[0].Nodes, g)
		_, err := lower("test", s)
		requireProblem(t, err, "carries a value type")
	})
}

func requireProblem(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("lower succeeded, want a problem mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("problem = %v, want it to mention %q", err, want)
	}
	// The refusal must be the internal-defect case, never a source
	// problem dressed up as one.
	verr, ok := err.(*Error)
	if !ok || !verr.Mismatch() {
		t.Fatalf("error = %#v, want a *Error with Mismatch true", err)
	}
}

// --- helpers ---

func (img *image) depOwners(s nodeID) []nodeID {
	r := img.depFrom[s]
	out := make([]nodeID, 0, r.n)
	for i := r.first; i < r.first+r.n; i++ {
		out = append(out, img.depOwn[i])
	}
	return out
}

func contains(list []nodeID, want nodeID) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// --- the two semantic edges the language leaves sharp ---

// A definition may grant and deny the same permission to the same role in
// the same stage. The front end warns (CONTRADICTORY_GRANTS) and lowering
// keeps both bits, because the §3.5 algebra is what answers a request:
// the warning is not a defect, so the image must still be built.
func TestContradictoryGrantsAreCarriedNotRefused(t *testing.T) {
	const src = `
roles {
    R { allow view all; }
}
forms {
    f { x : integer; }
}
stages {
    s {
        allow R edit f;
        deny R edit f;
    }
}
`
	img, warnings := compile(t, src)
	if len(warnings) != 1 {
		t.Fatalf("%d warnings, want the contradiction reported once: %v",
			len(warnings), diagnostics(warnings))
	}
	r := roleIDBy(t, img, "R")
	s := stageIDBy(t, img, "s")
	x := slotByPath(t, img, "f.x")
	allow, deny := img.perms.permAt(r, s, x)
	if allow&permEdit == 0 || deny&permEdit == 0 {
		t.Errorf("overlay bits = %05b/%05b, want edit in both", allow, deny)
	}
	// The stage's own contradiction strips edit, and nothing else: view
	// came from the role block and is untouched.
	if got := img.effective([]roleID{r}, s, x); got != permView {
		t.Errorf("in s, R holds %05b on f.x, want view alone", got)
	}
	if got := img.effective([]roleID{r}, noStage, x); got != permView {
		t.Errorf("outside s, R holds %05b on f.x, want view alone", got)
	}
}

// E6's absence rule, both directions. A comparison whose operand has no
// value is unsatisfied, and negation does not rescue it: the runtime must
// never make structure appear, or demand a value, because nothing was
// entered. Defaults are materialized before evaluation (§5.1), so an
// operand below is genuinely unset.
func TestAbsenceIsNotSatisfactionUnderNegation(t *testing.T) {
	const src = `
roles {
    R { allow view all; }
}
forms {
    f {
        n    : integer;
        flag : boolean;
        a present[!(.n > 3)]    { x : integer; }
        b present[!.flag]       { y : integer; }
        c required[!(.n > 3)] { z : integer; }
        d present[!(.flag || .n > 3)] { w : integer; }
    }
}
stages {
    s { allow R view all; }
}
`
	img, _ := compile(t, src)
	n := len(img.slots)
	nSlot := slotByPath(t, img, "f.n")
	flag := slotByPath(t, img, "f.flag")
	empty := make(stateView, n)
	with := func(path string, sv slotValue) stateView {
		v := make(stateView, n)
		v[slotByPath(t, img, "f."+path)] = sv
		return v
	}
	for _, g := range []string{"a", "b", "d"} {
		c := img.cond(img.slots[slotByPath(t, img, "f."+g)].condPres)
		if c(empty) {
			t.Errorf("%s is present while its operand has no value", g)
		}
	}
	// With a value, negation behaves: 5 > 3 so !(5 > 3) is false, and
	// 2 > 3 is false so the group is present.
	a := img.cond(img.slots[slotByPath(t, img, "f.a")].condPres)
	if a(with("n", slotValue{set: true, i: 5})) {
		t.Error("!(5 > 3) must not be satisfied")
	}
	if !a(with("n", slotValue{set: true, i: 2})) {
		t.Error("!(2 > 3) must be satisfied")
	}
	// !.flag needs flag to hold false, not merely to be non-true.
	b := img.cond(img.slots[slotByPath(t, img, "f.b")].condPres)
	if b(with("flag", slotValue{set: true, i: 1})) {
		t.Error("!true must be unsatisfied")
	}
	if !b(with("flag", slotValue{set: true, i: 0})) {
		t.Error("!false must be satisfied")
	}
	// required[!(.n > 3)] must not demand a value from an empty case:
	// condReq is consulted only when the node is present (§5.2), and here
	// it is unsatisfied, so z stays not required.
	c := img.slots[slotByPath(t, img, "f.c")]
	if c.condReq == 0 {
		t.Fatal("c lost its required condition")
	}
	if img.cond(c.condReq)(empty) {
		t.Error("an empty case must not be forced to fill c.z")
	}
	// A negated disjunction needs every operand it reads, and the fanout
	// still names all of them: d must be re-evaluated when either moves.
	if got := readBy(img, slotByPath(t, img, "f.d")); len(got) != 2 {
		t.Errorf("d reads %v, want flag and n", got)
	}
	d := img.cond(img.slots[slotByPath(t, img, "f.d")].condPres)
	v := with("flag", slotValue{set: true, i: 1})
	v[nSlot] = slotValue{set: true, i: 2}
	if d(v) {
		t.Error("!(true || 2 > 3) must be unsatisfied")
	}
	v[flag] = slotValue{set: true, i: 0}
	if !d(v) {
		t.Error("!(false || 2 > 3) must be satisfied")
	}
}

// Appendix A's gate, clause (a) and (b), reached through the exported
// seam rather than around it: Load is what a host actually calls, and §1
// promises source in and definition out. The two routes must agree.
func TestSpecExampleLoads(t *testing.T) {
	d, err := Load("example", strings.NewReader(specExample(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.ID() != "example" {
		t.Errorf("ID = %q, want example", d.ID())
	}
	if w := d.Warnings(); len(w) != 0 {
		t.Fatalf("the §7 example must load without warnings, got %v", w)
	}
	if got, want := len(d.img.slots), len(lowerExample(t).slots); got != want {
		t.Errorf("Load built %d slots, lower built %d", got, want)
	}
}

// --- §3.2's two dense ranges, checked against the tree they claim to be ---

// The accessors are what View, Submit and §5.3's walk will use, and
// Appendix A's "one contiguous interval" claim is only true of the arrays
// if the numbering really is preorder. This walks the §7 example both ways
// and requires the same set: children by declaration order, subtree by
// index range.
func TestImageAccessorsWalkTheTree(t *testing.T) {
	img := lowerExample(t)

	// descendants is the tree answer, from childrenOf alone.
	var descendants func(n nodeID) []nodeID
	descendants = func(n nodeID) []nodeID {
		out := []nodeID{n}
		for _, c := range img.childrenOf(n) {
			out = append(out, descendants(c)...)
		}
		return out
	}

	for n := range img.slots {
		id := nodeID(n)
		if img.slot(id) != img.slots[n] {
			t.Fatalf("slot(%d) is not the row", n)
		}
		lo, hi := img.subtree(id)
		if lo != id {
			t.Errorf("subtree(%d) starts at %d", n, lo)
		}
		want := descendants(id)
		if got := int(hi - lo); got != len(want) {
			t.Errorf("subtree %s is %d slots, the tree says %d",
				img.path(id), got, len(want))
			continue
		}
		// Contiguity: the range holds exactly the descendants, in the
		// order a preorder walk produces.
		for i, d := range want {
			if lo+nodeID(i) != d {
				t.Errorf("%s slot %d: preorder walk says %d, range says %d",
					img.path(id), i, d, lo+nodeID(i))
			}
		}
		// Every child names its parent, and sits inside its range: that
		// is the fold §3.5's inheritance and §5.4's walks both assume.
		for _, c := range img.childrenOf(id) {
			if img.slots[c].parent != id {
				t.Errorf("%s is %s's child but names %d as parent",
					img.path(c), img.path(id), img.slots[c].parent)
			}
			if c < lo || c >= hi {
				t.Errorf("%s is outside %s's subtree", img.path(c), img.path(id))
			}
		}
	}

	// The root is the whole image, which is what makes `all` one grant.
	lo, hi := img.subtree(0)
	if lo != 0 || int(hi) != len(img.slots) {
		t.Errorf("case root subtree is [%d,%d) over %d slots", lo, hi, len(img.slots))
	}

	// Names render from the interned table only.
	for path, want := range map[string]string{
		"application":                     "application",
		"application.internal":            "application.internal",
		"application.internal.risk_score": "application.internal.risk_score",
		"application.shareholders":        "application.shareholders",
		"archive":                         "archive",
	} {
		n := slotByPath(t, img, path)
		if got := img.path(n); got != want {
			t.Errorf("path(%s) = %q", path, got)
		}
		if got := img.name(img.slots[n].name); got != path[strings.LastIndex(path, ".")+1:] {
			t.Errorf("name(%s) = %q, want the last segment", path, got)
		}
	}
	// The tables index by ordinal, index 0 is "none" everywhere, and no
	// accessor has to search for anything (§3.1).
	if img.tagName(0) != "" || img.roleName(0) != "" {
		t.Error("ordinal 0 should have no name")
	}
	if img.cond(0) == nil || img.cond(0)(make(stateView, len(img.slots))) {
		t.Error("condID 0 must answer false without reading data (§3.4)")
	}
	if sv := img.constOf(0); sv.set || sv.i != 0 || sv.s != "" || !sv.d.IsZero() {
		t.Errorf("valueID 0 = %+v, want the no-value placeholder", sv)
	}
}
