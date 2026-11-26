package engine

// The boundary's value half, tested where it is defined: which payload a
// class lands in, what the type table says about it, and what a refusal
// about it says. Nothing here needs a runtime or a case — `img.store` is a
// function from a declared field and a `Value` to a row payload or a
// sentence — so this file is the in-package half that `edit_test.go` cannot
// observe from outside.

import (
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// valueSrc declares one field per class, two enums so a tag's owner is a
// question with a possible wrong answer, a role-typed field, a signature, a
// fixed group, and a repeated group whose member is only addressable through
// an instance. `money` is decimal(6, 2) so both bounds are small enough to
// test by hand.
const valueSrc = `
types {
    legal : enum(PERSON, COMPANY);
    phase : enum(DRAFT, SIGNED);
    money : decimal(6, 2);
}
roles {
    Applicant { allow view, edit application; }
    Manager   { allow view all; }
}
forms {
    application {
        text     : string;
        count    : integer;
        fee      : money;
        urgent   : boolean;
        on       : date;
        at       : datetime;
        doc      : binary;
        kind     : legal;
        state    : phase;
        grade    : options;
        reviewer : Manager;
        signed   : signature(Applicant);

        sub {
            leaf : string;
        }

        rows multiple {
            note : string;
        }

        tags : string multiple;
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`

func valueImage(t *testing.T) *program.Program {
	t.Helper()
	return mustLowerSource(t, valueSrc)
}

func TestStoreSettlesEachClassIntoItsPayload(t *testing.T) {
	img := valueImage(t)
	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	when := day.Add(9 * time.Hour)

	cases := []struct {
		name string
		path string
		in   Value
		want func(program.Cell) bool
	}{
		{"text", "application.text", Text("Monshi"),
			func(v program.Cell) bool { return v.Text == "Monshi" }},
		{"an empty string is a value", "application.text", Text(""),
			func(v program.Cell) bool { return v.Text == "" }},
		{"integer", "application.count", Int(-7),
			func(v program.Cell) bool { return v.Int == -7 }},
		{"decimal", "application.fee", Decimal(decimal.NewFromInt(1250)),
			func(v program.Cell) bool {
				return v.Decimal.Equal(decimal.NewFromInt(1250))
			}},
		{"false is a value too", "application.urgent", Bool(false),
			func(v program.Cell) bool { return v.Int == 0 }},
		{"true", "application.urgent", Bool(true),
			func(v program.Cell) bool { return v.Int == 1 }},
		{"date", "application.on", Date(day),
			func(v program.Cell) bool { return v.Time.Equal(day) }},
		{"datetime", "application.at", DateTime(when),
			func(v program.Cell) bool { return v.Time.Equal(when) }},
		{"binary is a handle", "application.doc", Binary(program.BinaryRef("blob-7")),
			func(v program.Cell) bool { return string(v.Binary) == "blob-7" }},
		{"options holds its text", "application.grade", Text("A"),
			func(v program.Cell) bool { return v.Text == "A" }},
		{"enum holds an ordinal", "application.kind", Tag("COMPANY"),
			func(v program.Cell) bool {
				return img.TagName(program.TagID(v.Int)) == "COMPANY"
			}},
		{"an identity", "application.reviewer", User("dana"),
			func(v program.Cell) bool { return v.Text == "dana" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := store(img, slotByPath(t, img, tc.path), tc.in)
			if bad != "" {
				t.Fatalf("store(%s) refused: %s", tc.path, bad)
			}
			// Every accepted value is *set*: the distinction between a
			// value and no value is the payload's `set` bit, and an empty
			// string, a false and a zero are values.
			if !got.Set {
				t.Fatal("an accepted value came back unset")
			}
			if !tc.want(got) {
				t.Errorf("stored %+v", got)
			}
		})
	}
}

func TestStoreRefusesWhatTheFieldDoesNotTake(t *testing.T) {
	img := valueImage(t)
	cases := []struct {
		name string
		path string
		in   Value
		says string
	}{
		{"text into an integer", "application.count", Text("7"),
			"holds integer"},
		{"an integer into text", "application.text", Int(7), "holds string"},
		{"a number into a date", "application.on", Int(7), "holds date"},
		{"a value never built", "application.text", Value{},
			"no value at all"},
		{"a decimal into a text field", "application.text",
			Decimal(decimal.NewFromInt(1)), "holds string"},
		{"text into an enum", "application.kind", Text("COMPANY"),
			"holds enum"},
		{"an identity into text", "application.text", User("dana"),
			"holds string"},
		{"text into a role field", "application.reviewer", Text("dana"),
			"holds an identity"},
		{"an identity into a group", "application.sub", User("dana"),
			"is structure"},
		{"anything into a signature", "application.signed", Text("ok"),
			"only sign writes one"},
		{"a number into a signature", "application.signed", Int(1),
			"only sign writes one"},
		{"a tag nobody declared", "application.kind", Tag("TRUST"),
			`no tag "TRUST" is declared`},
		{"a tag from the other enum", "application.kind", Tag("DRAFT"),
			"not a member of"},
		{"an empty identity", "application.reviewer", User(""),
			"names nobody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := store(img, slotByPath(t, img, tc.path), tc.in)
			if bad == "" {
				t.Fatalf("store(%s, %v) was accepted", tc.path, tc.in)
			}
			if !strings.Contains(bad, tc.says) {
				t.Errorf("refusal says %q, want it to mention %q", bad, tc.says)
			}
			// A refusal hands back nothing usable. The caller's atomicity
			// argument is that a refused batch saves nothing, and a
			// half-filled payload is how that becomes a lie.
			if got.Set {
				t.Errorf("a refused value still came back set: %+v", got)
			}
		})
	}
}

// The decimal bounds are the prebuilt `fits`, and `money: decimal(6, 2)`
// makes both edges reachable: four integer digits and two fractional.
func TestStoreChecksADecimalAgainstItsType(t *testing.T) {
	img := valueImage(t)
	n := slotByPath(t, img, "application.fee")

	for _, text := range []string{"0.00", "9999.99", "-9999.99", "7", "0.1",
		"9999.90"} {
		v, bad := store(img, n, Decimal(decimal.RequireFromString(text)))
		if bad != "" {
			t.Errorf("%s was refused: %s", text, bad)
			continue
		}
		// The value is stored as written. `fits` is a bounds test, not a
		// rounding rule: a host that assigned 0.10 reads 0.10 back, with its
		// scale, and the commit diff says what the user typed rather than what
		// the engine normalized away.
		if got := decText(v.Decimal); got != text {
			t.Errorf("stored %s for %s, want it kept as written", got, text)
		}
	}

	// Past scale, past precision, and past both. The refusal names the value:
	// a rejected update whose message says only "decimal" is a bug report
	// nobody can act on.
	for _, over := range []string{"0.001", "9999.999", "10000.00", "99999.99",
		"-12345.67"} {
		_, bad := store(img, n, Decimal(decimal.RequireFromString(over)))
		if bad == "" {
			t.Errorf("%s was accepted into decimal(6, 2)", over)
			continue
		}
		if !strings.Contains(bad, "does not hold") ||
			!strings.Contains(bad, over) {
			t.Errorf("refusal for %s says %q", over, bad)
		}
	}

	// Bounds belong to one class: an integer field never consults `fits`, and
	// a text field has none.
	if _, bad := store(img, slotByPath(t, img, "application.count"),
		Int(1<<50)); bad != "" {
		t.Errorf("an integer outside a decimal's bounds was refused: %s", bad)
	}
}

// An enum's ordinal is what the row holds, and it is the same ordinal a
// literal compiles to, which is what lets a stored value and a stored
// default mean one thing.
func TestStoreResolvesATagToItsOwnEnumsOrdinal(t *testing.T) {
	img := valueImage(t)
	kind := slotByPath(t, img, "application.kind")
	state := slotByPath(t, img, "application.state")
	if img.Slot(kind).Type == img.Slot(state).Type {
		t.Fatal("the fixture's two fields share an enum")
	}

	for _, name := range []string{"PERSON", "COMPANY"} {
		v, bad := store(img, kind, Tag(name))
		if bad != "" {
			t.Fatalf("%s: %s", name, bad)
		}
		if !img.TagMatches(uint64(v.Int), img.Slot(kind).Type) {
			t.Errorf("%s resolved to a tag of another enum", name)
		}
	}

	// The two enums' ordinals overlap as *numbers* and never as meanings,
	// which is the whole reason `Tag` takes a name and resolves it here rather
	// than trusting an ordinal a host computed somewhere else.
	person, _ := store(img, kind, Tag("PERSON"))
	draft, bad := store(img, state, Tag("DRAFT"))
	if bad != "" {
		t.Fatalf("DRAFT: %s", bad)
	}
	if !img.TagMatches(uint64(draft.Int), img.Slot(state).Type) ||
		img.TagMatches(uint64(person.Int), img.Slot(state).Type) {
		t.Error("PERSON and DRAFT resolved to one enum")
	}
	if person.Int == draft.Int && img.TagName(program.TagID(person.Int)) ==
		img.TagName(program.TagID(draft.Int)) {
		t.Errorf("both resolved to the tag %q", img.TagName(program.TagID(person.Int)))
	}
}

// The `options` class is the one soft spot at the boundary — its value is text,
// and `Text` is the only spelling text has — and an options field's hook
// binding is by *path*.
func TestCarriesIsSoftForOptionsOnly(t *testing.T) {
	img := valueImage(t)
	if !carries(program.BaseOptions, classText) || !carries(program.BaseString, classText) {
		t.Error("carries refused the class its own field takes")
	}
	if carries(program.BaseOptions, classInt) || carries(program.BaseString, classTag) ||
		carries(program.BaseDecimal, classInt) || carries(classUser, classText) ||
		carries(program.BaseString, classUser) {
		t.Error("carries accepted a second class somewhere it should not")
	}

	grade := slotByPath(t, img, "application.grade")
	sl := img.Slot(grade)
	if sl.Hook == 0 {
		t.Fatal("an options field carries no binding, so the path key is " +
			"untested")
	}
	if img.Type(sl.Type).Hook != 0 {
		t.Fatal("the options *type* carried a binding: settle would consult " +
			"a path binding as if it were a type one")
	}
	if got := valueName(0); got != "no value at all" {
		t.Errorf("the zero class is %q", got)
	}
	if got := valueName(classUser); got != "an identity" {
		t.Errorf("the identity class is %q, which is not a type", got)
	}
	if got := valueName(program.BaseString); got != "string" {
		t.Errorf("a class the type table numbers is %q, want the language's "+
			"word for it", got)
	}
}

// The two structural sentences `apply` builds are the difference between a
// refusal a host can act on and one it has to debug.
func TestStructuralSentencesNameTheShape(t *testing.T) {
	img := valueImage(t)
	fixed := slotByPath(t, img, "application.sub")
	if got := whyNoValue(img, fixed); !strings.Contains(got, "group") {
		t.Errorf("a fixed group is described as %q", got)
	}
	// A collection's own slot has no value either, and the answer there is
	// the verb that makes one.
	rows := slotByPath(t, img, "application.rows")
	if got := whyNoValue(img, rows); !strings.Contains(got, "add one") {
		t.Errorf("a collection is described as %q", got)
	}
	// And a member of one, addressed without an instance, needs the instance
	// named — three shapes, three remedies, one category.
	member := slotByPath(t, img, "application.rows.note")
	if got := whyNoValue(img, member); !strings.Contains(got, "instance") {
		t.Errorf("a member is described as %q", got)
	}

	for _, tc := range []struct {
		path string
		says string
	}{
		{"application.text", "not a multiple declaration"},
		{"application.sub", "not a multiple declaration"},
		{"application.count", "not a multiple declaration"},
	} {
		sl := img.Slot(slotByPath(t, img, tc.path))
		if got := whyNotInstance(sl); !strings.Contains(got, tc.says) {
			t.Errorf("add to %s is described as %q, want %q", tc.path, got,
				tc.says)
		}
	}
	// Both shapes `multiple` is legal on have instances to add, and say
	// nothing about it — including the repeated field, whose rows are values
	// rather than containers of them. A message here would be
	// the bug this table exists to keep: the verb refused for the shape that
	// needs it.
	for _, path := range []string{"application.rows", "application.tags"} {
		sl := img.Slot(slotByPath(t, img, path))
		if got := whyNotInstance(sl); got != "" {
			t.Errorf("add to %s was described as %q, want nothing", path, got)
		}
	}
}
