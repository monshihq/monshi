package monshi

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

func valueImage(t *testing.T) *image {
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
		want func(slotValue) bool
	}{
		{"text", "application.text", Text("Monshi"),
			func(v slotValue) bool { return v.s == "Monshi" }},
		{"an empty string is a value", "application.text", Text(""),
			func(v slotValue) bool { return v.s == "" }},
		{"integer", "application.count", Int(-7),
			func(v slotValue) bool { return v.i == -7 }},
		{"decimal", "application.fee", Decimal(decimal.NewFromInt(1250)),
			func(v slotValue) bool {
				return v.d.Equal(decimal.NewFromInt(1250))
			}},
		{"false is a value too", "application.urgent", Bool(false),
			func(v slotValue) bool { return v.i == 0 }},
		{"true", "application.urgent", Bool(true),
			func(v slotValue) bool { return v.i == 1 }},
		{"date", "application.on", Date(day),
			func(v slotValue) bool { return v.t.Equal(day) }},
		{"datetime", "application.at", DateTime(when),
			func(v slotValue) bool { return v.t.Equal(when) }},
		{"binary is a handle", "application.doc", Binary(BinaryRef("blob-7")),
			func(v slotValue) bool { return string(v.bin) == "blob-7" }},
		{"options holds its text", "application.grade", Text("A"),
			func(v slotValue) bool { return v.s == "A" }},
		{"enum holds an ordinal", "application.kind", Tag("COMPANY"),
			func(v slotValue) bool {
				return img.tagName(tagID(v.i)) == "COMPANY"
			}},
		{"an identity", "application.reviewer", User("dana"),
			func(v slotValue) bool { return v.s == "dana" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := img.store(slotByPath(t, img, tc.path), tc.in)
			if bad != "" {
				t.Fatalf("store(%s) refused: %s", tc.path, bad)
			}
			// Every accepted value is *set*: §5.2's distinction between a
			// value and no value is the payload's `set` bit, and an empty
			// string, a false and a zero are values.
			if !got.set {
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
			got, bad := img.store(slotByPath(t, img, tc.path), tc.in)
			if bad == "" {
				t.Fatalf("store(%s, %v) was accepted", tc.path, tc.in)
			}
			if !strings.Contains(bad, tc.says) {
				t.Errorf("refusal says %q, want it to mention %q", bad, tc.says)
			}
			// A refusal hands back nothing usable. The caller's atomicity
			// argument is that a refused batch saves nothing, and a
			// half-filled payload is how that becomes a lie.
			if got.set {
				t.Errorf("a refused value still came back set: %+v", got)
			}
		})
	}
}

// The decimal bounds are §3.3's prebuilt `fits`, and `money : decimal(6, 2)`
// makes both edges reachable: four integer digits and two fractional.
func TestStoreChecksADecimalAgainstItsType(t *testing.T) {
	img := valueImage(t)
	n := slotByPath(t, img, "application.fee")

	for _, text := range []string{"0.00", "9999.99", "-9999.99", "7", "0.1",
		"9999.90"} {
		v, bad := img.store(n, Decimal(decimal.RequireFromString(text)))
		if bad != "" {
			t.Errorf("%s was refused: %s", text, bad)
			continue
		}
		// The value is stored as written. `fits` is a bounds test, not a
		// rounding rule: a host that assigned 0.10 reads 0.10 back, with its
		// scale, and the commit diff says what the user typed rather than what
		// the engine normalized away.
		if got := decText(v.d); got != text {
			t.Errorf("stored %s for %s, want it kept as written", got, text)
		}
	}

	// Past scale, past precision, and past both. The refusal names the value:
	// a rejected update whose message says only "decimal" is a bug report
	// nobody can act on.
	for _, over := range []string{"0.001", "9999.999", "10000.00", "99999.99",
		"-12345.67"} {
		_, bad := img.store(n, Decimal(decimal.RequireFromString(over)))
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
	if _, bad := img.store(slotByPath(t, img, "application.count"),
		Int(1<<50)); bad != "" {
		t.Errorf("an integer outside a decimal's bounds was refused: %s", bad)
	}
}

// An enum's ordinal is what the row holds, and it is the same ordinal a
// literal compiles to (E3), which is what lets a stored value and a stored
// default mean one thing.
func TestStoreResolvesATagToItsOwnEnumsOrdinal(t *testing.T) {
	img := valueImage(t)
	kind := slotByPath(t, img, "application.kind")
	state := slotByPath(t, img, "application.state")
	if img.slots[kind].typ == img.slots[state].typ {
		t.Fatal("the fixture's two fields share an enum")
	}

	for _, name := range []string{"PERSON", "COMPANY"} {
		v, bad := img.store(kind, Tag(name))
		if bad != "" {
			t.Fatalf("%s: %s", name, bad)
		}
		if img.tagType[tagID(v.i)] != img.slots[kind].typ {
			t.Errorf("%s resolved to a tag of another enum", name)
		}
	}

	// The two enums' ordinals overlap as *numbers* and never as meanings,
	// which is the whole reason `Tag` takes a name and resolves it here rather
	// than trusting an ordinal a host computed somewhere else.
	person, _ := img.store(kind, Tag("PERSON"))
	draft, bad := img.store(state, Tag("DRAFT"))
	if bad != "" {
		t.Fatalf("DRAFT: %s", bad)
	}
	if img.tagType[tagID(person.i)] == img.tagType[tagID(draft.i)] {
		t.Error("PERSON and DRAFT resolved to one enum")
	}
	if person.i == draft.i && img.tagName(tagID(person.i)) ==
		img.tagName(tagID(draft.i)) {
		t.Errorf("both resolved to the tag %q", img.tagName(tagID(person.i)))
	}
}

// The `options` class is the one soft spot at the boundary — its value is
// text, and `Text` is the only spelling text has — and an options field's
// hook binding is by *path*, which milestone 3's `View` consults and a
// normalizer must not.
func TestCarriesIsSoftForOptionsOnly(t *testing.T) {
	img := valueImage(t)
	if !carries(baseOptions, classText) || !carries(baseString, classText) {
		t.Error("carries refused the class its own field takes")
	}
	if carries(baseOptions, classInt) || carries(baseString, classTag) ||
		carries(baseDecimal, classInt) || carries(classUser, classText) ||
		carries(baseString, classUser) {
		t.Error("carries accepted a second class somewhere it should not")
	}

	grade := slotByPath(t, img, "application.grade")
	sl := &img.slots[grade]
	if sl.hook == 0 {
		t.Fatal("an options field carries no binding, so §6's path key is " +
			"untested")
	}
	if img.types[sl.typ].hook != 0 {
		t.Fatal("the options *type* carried a binding: settle would consult " +
			"a path binding as if it were a type one")
	}
	if got := valueName(0); got != "no value at all" {
		t.Errorf("the zero class is %q", got)
	}
	if got := valueName(classUser); got != "an identity" {
		t.Errorf("the identity class is %q, which is not a type", got)
	}
	if got := valueName(baseString); got != "string" {
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
		sl := &img.slots[slotByPath(t, img, tc.path)]
		if got := whyNotInstance(sl); !strings.Contains(got, tc.says) {
			t.Errorf("add to %s is described as %q, want %q", tc.path, got,
				tc.says)
		}
	}
	// Both shapes `multiple` is legal on have instances to add, and say
	// nothing about it — including the repeated field, whose rows are values
	// rather than containers of them (DESIGN §5.1). A message here would be
	// the bug this table exists to keep: the verb refused for the shape that
	// needs it.
	for _, path := range []string{"application.rows", "application.tags"} {
		sl := &img.slots[slotByPath(t, img, path)]
		if got := whyNotInstance(sl); got != "" {
			t.Errorf("add to %s was described as %q, want nothing", path, got)
		}
	}
}
