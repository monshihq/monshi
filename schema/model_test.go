package schema_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/testfixture"
	"github.com/monshihq/monshi/schema"
	"github.com/shopspring/decimal"
)

// --- helpers over the public model ---

func requireClean(t *testing.T, s *schema.Schema) {
	t.Helper()
	if len(s.Errors) != 0 || len(s.Warnings) != 0 {
		t.Fatalf("diagnostics = errors [%s] warnings [%s], want none",
			render(s.Errors), render(s.Warnings))
	}
}

func childByName(n *schema.Node, name string) *schema.Node {
	for _, c := range n.Nodes {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// nodeByPath resolves a form-rooted dotted path, e.g.
// "application.shareholders.holder_name".
func nodeByPath(t *testing.T, s *schema.Schema, path string) *schema.Node {
	t.Helper()
	segs := strings.Split(path, ".")
	for _, f := range s.Forms {
		if f.Name != segs[0] {
			continue
		}
		cur := f
		for _, seg := range segs[1:] {
			next := childByName(cur, seg)
			if next == nil {
				t.Fatalf("node %q not found under %q", seg, cur.Path)
			}
			cur = next
		}
		return cur
	}
	t.Fatalf("form %q not found", segs[0])
	return nil
}

// --- the §7 example ---

func TestSpecExampleCompiles(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)
}

func TestSpecExampleTopLevelShape(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	if got := namesOfTypes(s.Types); !reflect.DeepEqual(got, []string{"legal_form", "amount", "email"}) {
		t.Errorf("types = %v, want declaration order", got)
	}
	if got := namesOfRoles(s.Roles); !reflect.DeepEqual(got, []string{"Applicant", "Registrar", "Manager"}) {
		t.Errorf("roles = %v, want declaration order", got)
	}
	if got := namesOfNodes(s.Forms); !reflect.DeepEqual(got, []string{"application", "archive"}) {
		t.Errorf("forms = %v, want declaration order", got)
	}
	wantStages := []string{"draft", "person_review", "company_review", "correction", "closed"}
	if got := namesOfStages(s.Stages); !reflect.DeepEqual(got, wantStages) {
		t.Errorf("stages = %v, want %v", got, wantStages)
	}
	if s.Initial != s.Stages[0] || s.Initial.Name != "draft" {
		t.Errorf("initial stage = %v, want draft (first declared, ST1)", s.Initial)
	}
}

func namesOfTypes(ts []*schema.Type) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func namesOfRoles(rs []*schema.Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func namesOfNodes(ns []*schema.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

func namesOfStages(ss []*schema.Stage) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

func TestSpecExampleTypes(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	lf, amount, email := s.Types[0], s.Types[1], s.Types[2]
	if lf.Base != "enum" || !reflect.DeepEqual(lf.Tags, []string{"PERSON", "COMPANY"}) {
		t.Errorf("legal_form = %+v, want enum(PERSON, COMPANY)", lf)
	}
	if amount.Base != "decimal" || amount.Precision != 14 || amount.Scale != 2 {
		t.Errorf("amount = %+v, want decimal(14, 2)", amount)
	}
	if email.Base != "string" {
		t.Errorf("email base = %q, want string", email.Base)
	}

	// Custom types resolve to the same *Type instances by pointer.
	if got := nodeByPath(t, s, "application.kind").Type; got != lf {
		t.Errorf("kind.Type = %+v, want the legal_form instance", got)
	}
	if got := nodeByPath(t, s, "application.contact").Type; got != email {
		t.Errorf("contact.Type = %+v, want the email instance", got)
	}
}

func TestSpecExampleNodesAndAttributes(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	app := s.Forms[0]
	if app.Kind != "form" || app.Path != "application" || app.Parent != nil {
		t.Errorf("application = kind %q path %q parent %v", app.Kind, app.Path, app.Parent)
	}
	if !app.Verifiable {
		t.Error("application must be verifiable")
	}
	if len(app.Nodes) != 12 {
		t.Errorf("application members = %d, want 12: %v", len(app.Nodes), namesOfNodes(app.Nodes))
	}
	archive := s.Forms[1]
	if archive.Verifiable {
		t.Error("archive must not be verifiable")
	}

	internal := nodeByPath(t, s, "application.internal")
	if internal.Kind != "group" || internal.Parent != app {
		t.Errorf("internal = kind %q parent %v", internal.Kind, internal.Parent)
	}
	if internal.Verifiable {
		t.Error("verifiable is not inherited (F3): internal should not carry it")
	}

	sh := nodeByPath(t, s, "application.shareholders")
	if !sh.Multiple {
		t.Error("shareholders must be multiple")
	}
	if len(sh.Nodes) != 2 {
		t.Errorf("shareholders members = %v", namesOfNodes(sh.Nodes))
	}
	shares := childByName(sh, "shares")
	if shares == nil || shares.Type != s.Types[1] || !shares.Required {
		t.Errorf("shares = %+v, want required amount", shares)
	}
	if got := nodeByPath(t, s, "application.shareholders.holder_name").Path; got != "application.shareholders.holder_name" {
		t.Errorf("holder_name.Path = %q", got)
	}
	if !nodeByPath(t, s, "application.kind").Required {
		t.Error("kind must be required")
	}

	// Member names are container-scoped: two `note` fields coexist.
	appNote := nodeByPath(t, s, "application.note")
	archNote := nodeByPath(t, s, "archive.note")
	if appNote == archNote {
		t.Fatal("distinct members must be distinct nodes")
	}
	if appNote.Path != "application.note" || archNote.Path != "archive.note" {
		t.Errorf("note paths = %q / %q", appNote.Path, archNote.Path)
	}
}

// literalNormSrc pairs comparison literals with operands whose compiled
// representation differs from the literal's surface shape: an integer
// literal against decimal, quoted ISO 8601 against date/datetime.
const literalNormSrc = `
types {
    money : decimal(8, 2);
    day   : date;
    stamp : datetime;
}

roles {
    R { allow view all; }
}

forms {
    f {
        amount : money;
        when   : day;
        sent   : stamp;
        level  : integer;
        label  : string;

        big   present[.amount >= 5]                  { b : integer; }
        due   present[.when > "2026-01-01"]          { d : integer; }
        late  present[.sent < "2026-06-01T10:00:00"] { l : integer; }
        high  present[.level > 100]                  { n : integer; }
        named present[.label >= "z"]                 { m : integer; }
    }
}

stages {
    s { allow R view all; }
}
`

// condLiteral returns the literal operand of a group's present[cond],
// which the checker always builds as a comparison with the literal on
// the right (SYNTAX.md §5.7-E3).
func condLiteral(t *testing.T, s *schema.Schema, path string) *schema.Value {
	t.Helper()
	x := nodeByPath(t, s, path).PresentCond
	if x == nil {
		t.Fatalf("%s has no present condition", path)
	}
	if x.Right == nil || x.Right.Lit == nil {
		t.Fatalf("%s condition %+v: want a literal right operand", path, x)
	}
	return x.Right.Lit
}

// A compiled definition normalizes comparison literals to the type of
// the operand they pair with, so interpretation never re-parses or
// re-widens a literal (SYNTAX.md §5.8).
func TestCompiledLiteralsAreNormalized(t *testing.T) {
	s := mustParse(t, literalNormSrc)
	requireClean(t, s)

	if v := condLiteral(t, s, "f.big"); v.Kind != "decimal" ||
		!v.Dec.Equal(decimal.RequireFromString("5")) {
		t.Errorf(`.amount >= 5 literal = %+v, want decimal 5`, v)
	}
	if v := condLiteral(t, s, "f.due"); v.Kind != "date" ||
		!v.Time.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		v.Str != "2026-01-01" {
		t.Errorf(`.when > "2026-01-01" literal = %+v, want date 2026-01-01`, v)
	}
	if v := condLiteral(t, s, "f.late"); v.Kind != "datetime" ||
		!v.Time.Equal(time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)) ||
		v.Str != "2026-06-01T10:00:00" {
		t.Errorf(`.sent < "…" literal = %+v, want datetime 2026-06-01T10:00:00`, v)
	}
	if v := condLiteral(t, s, "f.high"); v.Kind != "integer" || v.Int != 100 {
		t.Errorf(".level > 100 literal = %+v, want integer 100", v)
	}
	if v := condLiteral(t, s, "f.named"); v.Kind != "string" || v.Str != "z" {
		t.Errorf(`.label >= "z" literal = %+v, want string "z"`, v)
	}
}

func TestSpecExampleDefaults(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	fee := nodeByPath(t, s, "application.fee").Default
	if fee == nil || fee.Kind != "decimal" || !fee.Dec.Equal(decimal.RequireFromString("0.00")) {
		t.Errorf("fee default = %+v, want decimal 0.00", fee)
	}
	registered := nodeByPath(t, s, "application.registered").Default
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if registered == nil || registered.Kind != "date" || registered.Str != "2026-01-01" ||
		!registered.Time.Equal(want) {
		t.Errorf("registered default = %+v, want date 2026-01-01", registered)
	}
	urgent := nodeByPath(t, s, "application.urgent").Default
	if urgent == nil || urgent.Kind != "boolean" || urgent.Bool {
		t.Errorf("urgent default = %+v, want boolean false", urgent)
	}
	if got := nodeByPath(t, s, "application.note").Default; got != nil {
		t.Errorf("note has no default in the source, got %+v", got)
	}
}

func TestSpecExampleRolesAndGrants(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	applicant := s.Roles[0]
	if len(applicant.Grants) != 2 {
		t.Fatalf("Applicant grants = %d, want 2", len(applicant.Grants))
	}
	g0 := applicant.Grants[0]
	if g0.Deny || !reflect.DeepEqual(g0.Perms, []string{"view", "edit", "submit"}) ||
		len(g0.Nodes) != 1 || g0.Nodes[0] != s.Forms[0] {
		t.Errorf("Applicant grant 0 = %+v", g0)
	}
	g1 := applicant.Grants[1]
	internal := nodeByPath(t, s, "application.internal")
	if !g1.Deny || len(g1.Nodes) != 1 || g1.Nodes[0] != internal {
		t.Errorf("Applicant grant 1 = %+v, want deny edit application.internal", g1)
	}
	// Role-block grants resolve their subject to the enclosing role.
	if g0.Role != applicant || g1.Role != applicant {
		t.Error("role-block grants must carry the enclosing role")
	}

	registrar := s.Roles[1]
	all := registrar.Grants[0]
	if !all.All || len(all.Nodes) != 0 || !reflect.DeepEqual(all.Perms, []string{"view"}) {
		t.Errorf("Registrar grant 0 = %+v, want view all", all)
	}

	manager := s.Roles[2]
	if len(manager.Grants) != 4 {
		t.Fatalf("Manager grants = %d, want 4", len(manager.Grants))
	}
	sign := manager.Grants[2]
	signed := nodeByPath(t, s, "application.approved_by")
	if !reflect.DeepEqual(sign.Perms, []string{"sign"}) || len(sign.Nodes) != 1 || sign.Nodes[0] != signed {
		t.Errorf("Manager sign grant = %+v, want sign application.approved_by", sign)
	}
}

func TestSpecExampleSignatureResolution(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	reviewer := nodeByPath(t, s, "application.reviewer")
	if reviewer.Type != nil || reviewer.Role != s.Roles[2] {
		t.Errorf("reviewer = type %+v role %+v, want the Manager role only", reviewer.Type, reviewer.Role)
	}

	submitted := nodeByPath(t, s, "application.submitted")
	if submitted.Type == nil || submitted.Type.Base != "signature" {
		t.Fatalf("submitted type = %+v, want signature", submitted.Type)
	}
	if submitted.Signer != s.Roles[0] || submitted.SignField != nil {
		t.Errorf("submitted: signer %+v field %+v, want Applicant role and no field",
			submitted.Signer, submitted.SignField)
	}

	approved := nodeByPath(t, s, "application.approved_by")
	if approved.SignField != reviewer || approved.Signer != nil {
		t.Errorf("approved_by: signer %+v field %+v, want the reviewer field and no role",
			approved.Signer, approved.SignField)
	}
}

func TestSpecExampleConditionTrees(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	kind := nodeByPath(t, s, "application.kind")
	for _, tc := range []struct {
		label string
		e     *schema.Expr
		tag   string
	}{
		{"internal.present", nodeByPath(t, s, "application.internal").PresentCond, "COMPANY"},
		{"shareholders.required", nodeByPath(t, s, "application.shareholders").RequiredCond, "COMPANY"},
	} {
		e := tc.e
		if e == nil || e.Op != "==" {
			t.Errorf("%s = %+v, want a == comparison", tc.label, e)
			continue
		}
		if e.Left == nil || e.Left.Node != kind {
			t.Errorf("%s left = %+v, want the application.kind node", tc.label, e.Left)
		}
		if e.Right == nil || e.Right.Tag != tc.tag || e.Right.Node != nil || e.Right.Lit != nil {
			t.Errorf("%s right = %+v, want bare tag %s", tc.label, e.Right, tc.tag)
		}
	}
	if got := nodeByPath(t, s, "application.note").PresentCond; got != nil {
		t.Errorf("note.PresentCond = %+v, want nil", got)
	}
}

func TestSpecExampleStageGraph(t *testing.T) {
	s := mustParse(t, testfixture.Example(t))
	requireClean(t, s)

	draft := s.Stages[0]
	if len(draft.Grants) != 2 {
		t.Errorf("draft grants = %d, want 2", len(draft.Grants))
	}
	if draft.Grants[0].Role != s.Roles[0] || draft.Grants[1].Role != s.Roles[2] {
		t.Error("stage grants must resolve their named roles")
	}
	trans := draft.Grants[1]
	if !reflect.DeepEqual(trans.Perms, []string{"accept", "reject"}) || len(trans.Nodes) != 0 {
		t.Errorf("transition grant = %+v, want sole target-free accept, reject", trans)
	}

	if len(draft.Edges) != 3 {
		t.Fatalf("draft edges = %d, want 3", len(draft.Edges))
	}
	e0, e1, e2 := draft.Edges[0], draft.Edges[1], draft.Edges[2]
	if e0.Reject || e0.Dest != s.Stages[1] {
		t.Errorf("edge 0 = %+v, want accept person_review", e0)
	}
	if e0.Cond == nil || e0.Cond.Op != "==" || e0.Cond.Right == nil || e0.Cond.Right.Tag != "PERSON" {
		t.Errorf("edge 0 cond = %+v, want == PERSON", e0.Cond)
	}
	if e1.Dest != s.Stages[2] || e1.Cond == nil || e1.Cond.Right.Tag != "COMPANY" {
		t.Errorf("edge 1 = %+v, want accept[COMPANY] company_review", e1)
	}
	if !e2.Reject || e2.Dest != s.Stages[3] || e2.Cond != nil {
		t.Errorf("edge 2 = %+v, want unconditional reject correction", e2)
	}

	person := s.Stages[1]
	if len(person.Requires) != 1 || person.Requires[0] != s.Forms[0] {
		t.Errorf("person_review requires = %+v, want the application form", person.Requires)
	}
	if len(person.Edges) != 2 || person.Edges[0].Dest != s.Stages[4] ||
		!person.Edges[1].Reject || person.Edges[1].Dest != s.Stages[3] {
		t.Errorf("person_review edges = %+v, want accept closed; reject correction", person.Edges)
	}

	correction := s.Stages[3]
	if len(correction.Edges) != 1 || correction.Edges[0].Dest != s.Stages[0] {
		t.Errorf("correction must cycle back to draft, edges = %+v", correction.Edges)
	}

	closed := s.Stages[4]
	if len(closed.Edges) != 0 || len(closed.Grants) != 0 || len(closed.Requires) != 0 {
		t.Errorf("closed is terminal and empty: %+v", closed)
	}
}

// --- small focused model checks beyond the big example ---

func TestStageGrantRoleNilWhenUndeclared(t *testing.T) {
	s := mustParse(t, `forms { f { a : string; } } stages { d { allow Nobody view f; } }`)
	if !hasCode(codes(s.Errors), schema.CodeUnknownRole) {
		t.Fatalf("want UNKNOWN_ROLE, got [%s]", render(s.Errors))
	}
	st := s.Stages[0]
	if len(st.Grants) != 1 || st.Grants[0].Role != nil {
		t.Errorf("grants = %+v, want one grant with a nil (unresolved) role", st.Grants)
	}
}

func TestValueTypeInstances(t *testing.T) {
	const src = `types {
    money : decimal(9, 3);
    color : enum(RED, GREEN);
}
forms {
    paint {
        price : money required;
        tone  : color default RED;
        when  : datetime;
    }
}
stages { d { } }`
	s := mustParse(t, src)
	requireClean(t, s)

	money := s.Types[0]
	if money.Base != "decimal" || money.Precision != 9 || money.Scale != 3 {
		t.Errorf("money = %+v, want decimal(9, 3)", money)
	}
	nodes := s.Forms[0].Nodes
	if nodes[0].Type != money {
		t.Error("price must reference the money type instance")
	}
	def := nodes[1].Default
	if def == nil || def.Kind != "enum" || def.Str != "RED" {
		t.Errorf("tone default = %+v, want enum tag RED", def)
	}
	if nodes[2].Type == nil || nodes[2].Type.Base != "datetime" {
		t.Errorf("when type = %+v, want datetime", nodes[2].Type)
	}
}
