package schema_test

import (
	"testing"

	"github.com/monshihq/monshi/schema"
)

// ruleCase is one definition snippet and the diagnostics it must
// trigger. Case names carry the SYNTAX.md rule ID they verify. The
// table asserts presence: listed codes MUST appear in Errors /
// Warnings; recovery cascades may add further diagnostics. When errs is
// empty the case additionally asserts the compile is error-free, so
// warning-only cases prove warnings never leak into Errors.
type ruleCase struct {
	name  string
	src   string
	errs  []schema.Code
	warns []schema.Code
	clean bool // no errors AND no warnings
}

var ruleCases = []ruleCase{
	// §2.4 / §5.1-N1 — reserved words as names.
	{"N1-ReservedFormName", `forms { default { } } stages { d { } }`,
		[]schema.Code{schema.CodeReservedName}, nil, false},
	{"N1-ReservedFieldName", `forms { f { view : string; } } stages { d { } }`,
		[]schema.Code{schema.CodeReservedName}, nil, false},

	// §5.1-N2 — casing.
	{"N2-LowercaseRole", `roles { applicant { allow view f; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeNameCase}, nil, false},
	{"N2-UppercaseField", `forms { f { X : string; } } stages { d { } }`,
		[]schema.Code{schema.CodeNameCase}, nil, false},
	{"N2-UppercaseForm", `forms { Application { } } stages { d { } }`,
		[]schema.Code{schema.CodeNameCase}, []schema.Code{schema.CodeEmptyForm}, false},
	{"N2-UppercaseStage", `forms { f { } } stages { Draft { } }`,
		[]schema.Code{schema.CodeNameCase}, nil, false},
	{"N2-LowercaseTag", `types { e : enum(red); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeNameCase}, nil, false},
	{"N2-UppercaseType", `types { T : string; } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeNameCase}, nil, false},

	// §5.1-N3 — namespaces.
	{"N3-FormVsTypeCollision", `types { x : string; } forms { x { } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-StageVsFormCollision", `forms { f { } } stages { f { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-DuplicateTagAcrossEnums", `types { e1 : enum(PERSON); e2 : enum(PERSON); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-TagVsRoleCollision", `roles { PERSON { allow view f; } } types { e1 : enum(PERSON); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-DuplicateMembers", `forms { f { a : string; a : integer; } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-DuplicateRoles", `roles { R { allow view f; } R { allow view f; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},
	{"N3-DuplicateStages", `forms { f { } } stages { d { } d { } }`,
		[]schema.Code{schema.CodeDuplicateName}, nil, false},

	// §5.2-T1/T2 — type bases and decimal arguments.
	{"T1-CustomOverCustom", `types { a : string; b : a; } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidTypeBase}, nil, false},
	{"T1-OptionsBase", `types { b : options; } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidTypeBase}, nil, false},
	{"T1-SignatureBase", `types { b : signature(R); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidTypeBase}, nil, false},
	{"T1-RoleBase", `roles { R { allow view f; } } types { t : R; } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidTypeBase}, nil, false},
	{"T1-UnknownBaseIsUnknownType", `forms { f { a : Nope; } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownRole}, nil, false},
	{"T2-DecimalArgs", `types { a : decimal(0, 1); b : decimal(40, 0); c : decimal(4, 5); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDecimalArgs}, nil, false},
	{"T2-DecimalNonNumericArgs", `types { a : decimal(x, 1); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},

	// §5.3 — forms, fields, groups.
	{"F1-EmptyGroup", `forms { f { g { } } } stages { d { } }`,
		[]schema.Code{schema.CodeEmptyGroup}, nil, false},
	{"F1-EmptyFormWarns", `forms { f { } } stages { d { } }`,
		nil, []schema.Code{schema.CodeEmptyForm}, false},
	{"F3-RedundantVerifiable", `forms { f verifiable { g verifiable { x : string; } } } stages { d { } }`,
		nil, []schema.Code{schema.CodeRedundantVerifiable}, false},
	{"F5-BinaryDefault", `forms { f { x : binary default "z"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-GroupDefault", `forms { f { g default 1 { x : string; } } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidAttribute}, nil, false},
	{"F5-BadDateDefault", `forms { f { d : date default "2026-13-01"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-DecimalDefaultTooWide", `forms { f { a : decimal(4, 2) default 1234.50; b : decimal(4, 2) default 1.234; } } stages { s { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-RoleFieldDefault", `roles { R { allow view f; } } forms { f { who : R default "x"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-FloatDefaultOnInteger", `forms { f { n : integer default 1.5; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-RelativePathDefault", `forms { f { a : boolean; b : string default .a; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F5-OptionsDefault", `forms { f { o : options default "x"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidDefault}, nil, false},
	{"F7-SelfReference", `forms { f { a : integer present[.a == 1]; } } stages { d { } }`,
		[]schema.Code{schema.CodeSelfReference}, nil, false},
	{"F7-CyclicConditions", `forms { f { a : integer present[.b == 1]; b : integer present[.a == 2]; } } stages { d { } }`,
		[]schema.Code{schema.CodeCyclicCondition}, nil, false},
	{"F7-ConditionIntoOwnSubtree", `forms { f { g { x : string; } g2 : string present[.g.x == "v"]; } } stages { d { } }`,
		nil, nil, true}, // sibling subtree is not a descendant: legal
	{"F7-GroupConditionIntoItsOwnMember", `forms { f { g present[.g.x == "v"] { x : string; } } } stages { d { } }`,
		[]schema.Code{schema.CodeSelfReference}, nil, false},

	// §5.4 — signature references (S1–S3).
	{"S1-SignatureInGroup", `roles { R { allow view f; } } forms { f { g { s : signature(R); } } } stages { d { } }`,
		[]schema.Code{schema.CodeSignatureInGroup}, nil, false},
	{"S2-SignatureBareLowerName", `roles { Manager { allow view f; } } forms { f { reviewer : Manager; s : signature(reviewer); } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidSignatureRef}, nil, false},
	{"S2-SignatureCrossFormField", `roles { Manager { allow view f; } } forms { f { reviewer : Manager; } g { s : signature(f.reviewer); } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidSignatureRef}, nil, false},
	{"S2-SignatureLiteral", `forms { f { s : signature("x"); } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidSignatureRef}, nil, false},
	{"S2-SignatureSelf", `roles { R { allow view f; } } forms { f { who : R; s : signature(.s); } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidSignatureRef}, nil, false},
	{"S2-SignatureNonRoleField", `forms { f { who : string; s : signature(.who); } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidSignatureRef}, nil, false},
	{"S2-SignatureUnknownRole", `forms { f { s : signature(Nobody); } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownRole}, nil, false},
	{"S2-SignatureEnumTagIsNotRole", `forms { f { k : enum(PERSON); s : signature(PERSON); } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownRole}, nil, false},

	// §5.5-P1 — grant statement shapes.
	{"P1-AcceptInRoleBlock", `roles { R { allow view, accept f; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidGrant}, nil, false},
	{"P1-RoleGrantWithoutTargets", `roles { R { allow view; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidGrant}, nil, false},
	{"P1-TransitionMixedWithOthers", `roles { R { allow view f; } } forms { f { } } stages { d { allow R view, accept; } }`,
		[]schema.Code{schema.CodeInvalidGrant}, nil, false},
	{"P1-TransitionWithTargets", `roles { R { allow view f; } } forms { f { } } stages { d { allow R accept, reject f; } }`,
		[]schema.Code{schema.CodeInvalidGrant}, nil, false},
	{"P1-StageGrantWithoutTargets", `roles { R { allow view f; } } forms { f { } } stages { d { allow R view; } }`,
		[]schema.Code{schema.CodeInvalidGrant}, nil, false},

	// §5.5-P2 — grant target resolution.
	{"P2-RelativeGrantTargetUnparsable", `roles { R { allow view .f; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},
	{"P2-UnknownGrantTarget", `roles { R { allow view f.nope; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownTarget}, nil, false},
	{"P2-TargetNamingAStage", `roles { R { allow view nope; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownTarget}, nil, false},

	// §5.5-P3 — permission/target compatibility.
	{"P3-ReviewNonVerifiable", `roles { R { allow review f; } } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeIncompatiblePermission}, nil, false},
	{"P3-SignNonSignature", `roles { R { allow sign f; } } forms { f verifiable { } } stages { d { } }`,
		[]schema.Code{schema.CodeIncompatiblePermission}, nil, false},
	{"P3-SubmitNonForm", `roles { R { allow submit f.a; } } forms { f { a : string; } } stages { d { } }`,
		[]schema.Code{schema.CodeIncompatiblePermission}, nil, false},
	{"P3-EditSignatureField", `roles { R { allow edit f.s; sign f.s; } } forms { f { s : signature(Applicant); } } stages { d { } }`,
		[]schema.Code{schema.CodeIncompatiblePermission}, nil, false},

	// §5.5-P4/P5 — role usage and contradictions.
	{"P4-StageGrantUnknownRole", `forms { f { } } stages { d { allow Nobody view f; } }`,
		[]schema.Code{schema.CodeUnknownRole}, nil, false},
	{"P5-UnusedDeclarations", `types { unused : string; } roles { Ghost { allow view f; } } forms { f { a : string; } } stages { d { } orphan { } }`,
		nil, []schema.Code{schema.CodeUnusedRole, schema.CodeUnusedType, schema.CodeUnreachableStage}, false},
	{"P5-OwnBlockGrantsDontCount", `roles { Lone { allow view all; } } forms { f { a : string; } } stages { d { } }`,
		nil, []schema.Code{schema.CodeUnusedRole}, false},
	{"P5-ContradictoryGrants", `roles { R { allow view f; deny view f; } } forms { f { a : string; } } stages { d { } }`,
		nil, []schema.Code{schema.CodeContradictoryGrants}, false},

	// §5.6 — stages and transitions.
	{"ST2-UnknownTransitionDestination", `roles { R { allow view f; } } forms { f { } } stages { d { accept nowhere; } }`,
		[]schema.Code{schema.CodeUnknownStage}, nil, false},
	{"ST2-UnknownRequiredForm", `forms { f { } } stages { d { require nope; } }`,
		[]schema.Code{schema.CodeUnknownForm}, nil, false},
	{"ST3-SelfTransition", `roles { R { allow view f; } } forms { f { } } stages { d { accept d; } }`,
		[]schema.Code{schema.CodeSelfTransition}, nil, false},
	{"ST3-DuplicateTransition", `roles { R { allow view f; } } forms { f { } } stages { d { accept e; reject e; } e { } }`,
		[]schema.Code{schema.CodeDuplicateTransition}, nil, false},
	{"ST4-RequireGroup", `forms { f { g { x : string; } } } stages { d { require g; } }`,
		[]schema.Code{schema.CodeUnknownForm}, nil, false},
	{"ST6-RelativePathInTransition", `roles { R { allow view f; } } forms { f { k : enum(A); } } stages { d { accept[.k == A] e; } e { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"ST6-ConstantCondition", `roles { R { allow view f; } } forms { f { a : string; } } stages { d { accept[1 == 1] e; } e { } }`,
		nil, []schema.Code{schema.CodeConstantCondition}, false},

	// §5.7-E1 — name-shaped operand classification.
	{"E1-UnknownEnumTag", `types { e : enum(PERSON); } forms { f { a : e present[.a == NOPE]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E1-RoleIsNotAValue", `roles { R { allow view f; } } forms { f { a : string present[.a == R]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E1-BareFormReference", `forms { f { a : string present[f]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E1-UppercaseRootPath", `roles { R { allow view f; } } forms { f { a : string present[R.x == 1]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},

	// §5.7-E2 — operand shapes that hold no single value.
	{"E2-MultipleFieldOperand", `forms { f { m : string multiple; b : boolean; c : string present[.b && .m == "x"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-MultipleTraversal", `forms { f { g multiple { x : string; } b : string present[.g.x == "y"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-GroupOperand", `forms { f { g { x : string; } b : string present[.g == .g]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-OptionsOperand", `forms { f { a : options; b : string present[.a == .a]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-BinaryOperand", `forms { f { b : binary; s : boolean; c : string present[.s && .b == .b]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-RoleTypedFieldOperand", `roles { R { allow view f; } } forms { f { who : R; a : string present[.who == .who]; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidOperand}, nil, false},
	{"E2-UnknownAbsolutePath", `forms { f { a : string; b : string present[g.a == "x"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeUnknownTarget}, nil, false},

	// §5.7-E3 — comparison typing.
	{"E3-CrossEnumTags", `types { e1 : enum(PERSON); e2 : enum(RED); } forms { f { a : e1 present[PERSON == RED]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},
	{"E3-OrderOnBoolean", `forms { f { a : boolean; b : boolean; c : string present[.a < .b]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},
	{"E3-OrderOnEnum", `types { e1 : enum(PERSON); } forms { f { a : e1 present[.a > .a]; } } stages { d { } }`,
		[]schema.Code{schema.CodeSelfReference, schema.CodeTypeMismatch}, nil, false},
	{"E3-StringLiteralVsInteger", `forms { f { n : integer present[.n == "x"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},
	{"E3-BoolLiteralVsString", `forms { f { a : string present[true == "y"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},
	{"E3-IntegerLiteralOverflow", `forms { f { n : integer present[.n == 99999999999999999999]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},
	{"E3-BadDateLiteralCompare", `forms { f { d : date present[.d == "yesterday"]; } } stages { d { } }`,
		[]schema.Code{schema.CodeTypeMismatch}, nil, false},

	// §5.7-E4 — boolean expression requirements.
	{"E4-StandaloneNumber", `forms { f { a : string present[42]; } } stages { d { } }`,
		[]schema.Code{schema.CodeNotBoolean}, nil, false},
	{"E4-StandaloneTag", `types { e1 : enum(PERSON); } forms { f { a : e1 present[PERSON]; } } stages { d { } }`,
		[]schema.Code{schema.CodeNotBoolean}, nil, false},
	{"E4-NonBoolPath", `forms { f { a : string; b : string present[.a]; } } stages { d { } }`,
		[]schema.Code{schema.CodeNotBoolean}, nil, false},
	{"E4-NegatedNonBool", `forms { f { a : string present[!.a]; } } stages { d { } }`,
		[]schema.Code{schema.CodeNotBoolean}, nil, false},

	// §3.1 — top-level block structure.
	{"Blocks-OutOfOrder", `stages { d { } } forms { f { } }`,
		[]schema.Code{schema.CodeBlockOrder}, nil, false},
	{"Blocks-DuplicateForms", `forms { f { } } forms { g { } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateBlock}, nil, false},
	{"Blocks-MissingBoth", `types { }`,
		[]schema.Code{schema.CodeMissingBlock}, nil, false},
	{"Blocks-EmptyStages", `forms { f { } } stages { }`,
		[]schema.Code{schema.CodeEmptyBlock}, nil, false},
	{"Blocks-EmptyForms", `forms { } stages { d { } }`,
		[]schema.Code{schema.CodeEmptyBlock}, nil, false},

	// §4 — attribute validity matrix.
	{"Attr-RequiredOnForm", `forms { f required { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidAttribute}, nil, false},
	{"Attr-MultipleOnForm", `forms { f multiple { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidAttribute}, nil, false},
	{"Attr-PresentOnForm", `forms { f present[1 == 1] { } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidAttribute}, nil, false},
	{"Attr-DuplicateRequired", `forms { f { a : string required required; } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateAttribute}, nil, false},
	{"Attr-RequiredBothVariants", `forms { f { a : string required required[1 == 1]; } } stages { d { } }`,
		[]schema.Code{schema.CodeDuplicateAttribute}, nil, false},
	{"Attr-SignatureRestrictions", `roles { R { allow view f; } } forms { f { s : signature(R) verifiable multiple default "x"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidAttribute}, nil, false},

	// §2/§3 — lexical and parse failures.
	{"Lex-BadCharacter", "forms { f { a : string; } } stages { d { } }\n# extra",
		[]schema.Code{schema.CodeInvalidCharacter}, nil, false},
	{"Lex-InvalidEscape", `forms { f { a : string default "x\q"; } } stages { d { } }`,
		[]schema.Code{schema.CodeInvalidEscape}, nil, false},
	{"Lex-UnterminatedString", `forms { f { a : string default "oops } }`,
		[]schema.Code{schema.CodeUnterminatedString, schema.CodeMissingBlock}, nil, false},
	{"Lex-UnterminatedComment", `forms { f { a : string; } } stages { d { } } /* dangling`,
		[]schema.Code{schema.CodeUnterminatedComment}, nil, false},
	{"Parse-MissingSemicolon", `forms { f { a : string } } stages { d { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},
	{"Parse-EmptyEnumTagList", `types { e : enum(); } forms { f { } } stages { d { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},
	{"Parse-DecimalWithoutArgs", `types { d : decimal; } forms { f { } } stages { d2 { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},
	{"Parse-PresentWithoutCondition", `forms { f { a : string present; } } stages { d { } }`,
		[]schema.Code{schema.CodeUnexpectedToken}, nil, false},

	// Positive shapes that MUST compile with zero diagnostics.
	{"OK-Minimal", minimalSrc, nil, nil, true},
	{"OK-TrailingZerosFitDecimal", `forms { f { a : decimal(4, 2) default 1.200; b : decimal(4, 2) default -12.50; c : decimal(14, 2) default 0.00; } } stages { d { } }`,
		nil, nil, true},
	{"OK-StandaloneBoolPath", `forms { f { u : boolean; a : string present[.u]; } } stages { d { } }`,
		nil, nil, true},
	{"OK-NegatedBoolPath", `forms { f { u : boolean; a : string present[!.u]; } } stages { d { } }`,
		nil, nil, true},
	{"OK-AbsolutePathInsideForm", `forms { f { u : boolean; } g { a : string present[f.u]; } } stages { d { } }`,
		nil, nil, true},
	{"OK-EscapeInDefault", `forms { f { a : string default "x\"y\n"; } } stages { d { } }`,
		nil, nil, true},
	{"OK-InlineEnumTagCompare", `forms { f { a : enum(RED, BLUE) required; b : string present[.a == RED]; } } stages { d { } }`,
		nil, nil, true},
	{"OK-MemberNameReuseAcrossForms", `forms { f { n : string; } g { n : string; } } stages { d { } }`,
		nil, nil, true},
	{"OK-RelativePathFromNestedGroup", `forms { f { k : enum(A); g { x : string present[.k == A]; } } } stages { d { } }`,
		nil, nil, true},
	{"OK-ScalarDefaults", `forms { f { d : date default "2026-02-28"; t : datetime default "2026-02-28T09:30:00"; n : integer default -7; b : boolean default true; s : string default "hi"; } } stages { d { } }`,
		nil, nil, true},
	{"OK-EnumAliasDefault", `types { k : enum(A, B); } forms { f { s : k default A; } } stages { d { } }`,
		nil, nil, true},
	{"OK-SignatureAssignedField", `roles { Manager { allow view f; } } forms { f { reviewer : Manager; s : signature(.reviewer); } } stages { d { } }`,
		nil, nil, true},
	{"OK-SignatureRoleAndSignGrant", `roles { Applicant { allow view, sign f.s; } } forms { f { s : signature(Applicant); } } stages { d { } }`,
		nil, nil, true},
	{"OK-StageGrantAll", `roles { R { allow view all; } } forms { f { a : string; } } stages { d { allow R view all; } }`,
		nil, nil, true},
	{"OK-SameEnumTagPair", `forms { f { a : enum(X, Y) required; b : string present[X == Y]; } } stages { d { } }`,
		nil, nil, true},
}

func TestRuleTable(t *testing.T) {
	for _, tc := range ruleCases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustParse(t, tc.src)
			ec, wc := codes(s.Errors), codes(s.Warnings)

			if tc.clean {
				if len(s.Errors) != 0 || len(s.Warnings) != 0 {
					t.Fatalf("want zero diagnostics, got errors [%s] warnings [%s]",
						render(s.Errors), render(s.Warnings))
				}
				return
			}
			for _, want := range tc.errs {
				if !hasCode(ec, want) {
					t.Errorf("missing error %s; got [%s]", want, render(s.Errors))
				}
			}
			for _, want := range tc.warns {
				if !hasCode(wc, want) {
					t.Errorf("missing warning %s; got [%s]", want, render(s.Warnings))
				}
			}
			// Isolation: a case that expects no error codes must be
			// error-free — warnings alone never invalidate (SYNTAX.md §5).
			if len(tc.errs) == 0 && len(s.Errors) != 0 {
				t.Errorf("warning-only case has errors: [%s]", render(s.Errors))
			}
		})
	}
}

func TestDiagnosticPositions(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		code      schema.Code
		line, col int
	}{
		{
			"unknown-type-at-type-token",
			`forms { f { a : nope; } } stages { d { } }`,
			schema.CodeUnknownType, 1, 17,
		},
		{
			"stage-grant-at-role-token",
			"forms { f { a : string; } }\nstages { d { allow Nobody view f; } }",
			schema.CodeUnknownRole, 2, 20,
		},
		{
			"unknown-transition-at-destination-token",
			`forms { f { } } stages { d { accept nowhere; } }`,
			schema.CodeUnknownStage, 1, 37,
		},
		{
			"unterminated-string-at-opening-quote",
			`forms { f { a : string default "oops } }`,
			schema.CodeUnterminatedString, 1, 32,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustParse(t, tc.src)
			for _, p := range s.Errors {
				if p.Code == tc.code {
					if p.Line != tc.line || p.Col != tc.col {
						t.Errorf("%s reported at %d:%d, want %d:%d",
							tc.code, p.Line, p.Col, tc.line, tc.col)
					}
					return
				}
			}
			t.Fatalf("no %s diagnostic in [%s]", tc.code, render(s.Errors))
		})
	}
}

func TestMemberLevelParseRecovery(t *testing.T) {
	s := mustParse(t, `forms { f { bad : ; a : string; } } stages { d { } }`)
	if !hasCode(codes(s.Errors), schema.CodeUnexpectedToken) {
		t.Fatalf("want UNEXPECTED_TOKEN, got [%s]", render(s.Errors))
	}
	if len(s.Forms) != 1 {
		t.Fatalf("forms = %d, want the form to survive recovery", len(s.Forms))
	}
	f := s.Forms[0]
	if len(f.Nodes) != 1 || f.Nodes[0].Name != "a" {
		names := make([]string, len(f.Nodes))
		for i, n := range f.Nodes {
			names[i] = n.Name
		}
		t.Errorf("members after recovery = %v, want [a]", names)
	}
}

func TestTopLevelResync(t *testing.T) {
	s := mustParse(t, `bogus stuff forms { f { a : string; } } stages { d { } }`)
	if !hasCode(codes(s.Errors), schema.CodeUnexpectedToken) {
		t.Errorf("want UNEXPECTED_TOKEN for the stray prefix, got [%s]", render(s.Errors))
	}
	if len(s.Forms) != 1 || len(s.Stages) != 1 {
		t.Errorf("blocks after resync: forms=%d stages=%d, want 1/1", len(s.Forms), len(s.Stages))
	}
	for _, p := range s.Errors {
		if p.Code == schema.CodeMissingBlock {
			t.Errorf("MISSING_BLOCK despite recoverable source: %v", p)
		}
	}
}

func TestProblemRendersAsDiagnostics(t *testing.T) {
	var err error = schema.Problem{} // Problem implements error
	_ = err

	cases := []struct {
		p    schema.Problem
		want string
	}{
		{
			schema.Problem{Line: 3, Col: 7, Code: schema.CodeUnknownType, Context: "nope"},
			"3:7: unknown type: nope [UNKNOWN_TYPE]",
		},
		{
			schema.Problem{Line: 2, Col: 4, Code: schema.CodeUnterminatedString},
			"2:4: unterminated string [UNTERMINATED_STRING]",
		},
		{
			schema.Problem{Line: 1, Col: 1, Code: schema.CodeMissingBlock, Context: "forms"},
			"1:1: required block missing: forms [MISSING_BLOCK]",
		},
	}
	for _, c := range cases {
		if got := c.p.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

// allCodes lists every Code constant exported by the package. A new
// diagnostic must be added here with a description (model.go), or this
// test fails.
var allCodes = []schema.Code{
	schema.CodeUnexpectedToken, schema.CodeUnterminatedString,
	schema.CodeUnterminatedComment, schema.CodeInvalidEscape,
	schema.CodeInvalidCharacter, schema.CodeReservedName,
	schema.CodeMissingBlock, schema.CodeDuplicateBlock,
	schema.CodeBlockOrder, schema.CodeEmptyBlock,
	schema.CodeNameCase, schema.CodeDuplicateName, schema.CodeUnknownType,
	schema.CodeUnknownRole, schema.CodeInvalidTypeBase,
	schema.CodeInvalidDecimalArgs, schema.CodeEmptyEnum, schema.CodeEmptyGroup,
	schema.CodeInvalidAttribute, schema.CodeDuplicateAttribute,
	schema.CodeInvalidDefault, schema.CodeInvalidCondition,
	schema.CodeSelfReference, schema.CodeCyclicCondition,
	schema.CodeSignatureInGroup, schema.CodeInvalidSignatureRef,
	schema.CodeInvalidGrant, schema.CodeUnknownTarget, schema.CodeUnknownForm,
	schema.CodeIncompatiblePermission, schema.CodeUnknownStage,
	schema.CodeSelfTransition, schema.CodeDuplicateTransition,
	schema.CodeInvalidOperand, schema.CodeTypeMismatch, schema.CodeNotBoolean,
	schema.CodeEmptyForm, schema.CodeRedundantVerifiable, schema.CodeUnusedRole,
	schema.CodeUnusedType, schema.CodeContradictoryGrants,
	schema.CodeUnreachableStage, schema.CodeConstantCondition,
}

func TestEveryCodeHasDescriptionAndRenders(t *testing.T) {
	seen := map[schema.Code]bool{}
	for _, c := range allCodes {
		if seen[c] {
			t.Errorf("duplicate code %s in test list", c)
		}
		seen[c] = true
		p := schema.Problem{Line: 1, Col: 1, Code: c}
		got := p.Error()
		want := "1:1: [" + string(c) + "]"
		if got == want { // description slot empty: "1:1:  [CODE]"
			t.Errorf("code %s has no description: %q", c, got)
		}
		if len(got) <= len(string(c))+9 {
			t.Errorf("code %s renders too short to carry a description: %q", c, got)
		}
	}
}
