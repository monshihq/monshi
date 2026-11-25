package engine

import (
	"slices"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// Value is a field's value at the boundary. The zero Value carries no
// class, and `Set` refuses it: `Empty` is what clears a field, so a value
// a caller never built is a mistake rather than an instruction.
type Value struct {
	class uint8
	i     int64
	d     decimal.Decimal
	s     string
	t     time.Time
	bin   program.BinaryRef
}

// The class a Value carries: the image's base classes, under the boundary's
// names, plus the identity class. A host never sees these words — the
// constructors below are the vocabulary — but the codes that say what they
// expect are these.
const (
	classText = program.BaseString
	classInt  = program.BaseInteger
	classDec  = program.BaseDecimal
	classBool = program.BaseBoolean
	classDate = program.BaseDate
	classDT   = program.BaseDatetime
	classBin  = program.BaseBinary
	classTag  = program.BaseEnum
	classOpt  = program.BaseOptions

	// classUser is one past every class a *type* can have, which is the
	// honest place for it: `baseOf` answers 0 for a role-typed field
	// precisely because there is no type behind it. Stated relative to
	// `baseSignature` so it cannot collide with a class added later.
	classUser = program.BaseSignature + 1
)

// Text carries a `string` field's value. An empty string is a value: it is
// what a host stores with `Set(path, Text(""))`, which is not what `Empty`
// does. The two differ in whether the field *has* a value at all — a
// comparison over an empty string is answered and satisfied, one over an
// absent value is neither (SYNTAX E6) — and §5.2's `required` fails them the
// same way, which is why the distinction is a host's to make deliberately.
func Text(s string) Value { return Value{class: classText, s: s} }

// Int carries an `integer` field's value. There is no float at the boundary:
// T2 makes scale explicit in `decimal`, so a number with fractions arrives
// as one or not at all.
func Int(i int64) Value { return Value{class: classInt, i: i} }

// Decimal carries a `decimal` field's value. Whether it fits the precision
// and scale the definition declared is checked at assignment — §3.3's
// `fits`, built once per type — because a value the type forbids is not a
// value the row may hold.
func Decimal(d decimal.Decimal) Value { return Value{class: classDec, d: d} }

// Bool carries a `boolean` field's value.
func Bool(b bool) Value { return Value{class: classBool, i: b2i(b)} }

// Date carries a `date` field's value. What the instant's zone means is not
// this package's question: §5.8 settled the literal form of a date at
// compile time and the row stores the time it compiled to.
func Date(t time.Time) Value { return Value{class: classDate, t: t} }

// DateTime carries a `datetime` field's value.
func DateTime(t time.Time) Value { return Value{class: classDT, t: t} }

// Tag names one member of an `enum` field's set by its declared name, which
// is the language's only spelling for an enum value. Which enum owns the
// name is checked against the field's type, so a tag from a different enum
// is refused rather than stored as an ordinal that means something else —
// the runtime echo of the pairing E3 makes at compile time.
func Tag(name string) Value { return Value{class: classTag, s: name} }

// Binary carries a `binary` field's content handle. Nothing in this package
// reads or resolves one: DESIGN §5.2's store of content is milestone 4's,
// and §5.1's row reserves the field now so a value never changes shape when
// it arrives.
func Binary(ref program.BinaryRef) Value { return Value{class: classBin, bin: slices.Clone(ref)} }

// User carries the value of a role-typed field, which *is* an actor's
// identity (SYNTAX §5.2-T4) rather than a role name. Assigning one is how a
// definition hands out a role; whether the assignee holds it is a committed
// question, and authority reads the committed snapshot (DESIGN §4.3).
func User(id UserID) Value { return Value{class: classUser, s: string(id)} }

// ValueKind names the class a `Value` carries. The words are the boundary's
// — the classes a host chose constructors from — and one is missing from the
// list on purpose: `options`. An options field's value is the chosen option's
// *text* (§3.3), so it reads as `KindText`; what distinguishes it from a
// string field is the view's `Options` list, not its value's class.
type ValueKind string

const (
	// KindNone is a zero `Value`: no class, nothing carried. `Empty` leaves
	// a cell with no value and the view renders that cell with a `KindNone`
	// `Value` — the same distinction value.go's header draws between an
	// empty string and no string at all.
	KindNone     ValueKind = "none"
	KindText     ValueKind = "text"
	KindInteger  ValueKind = "integer"
	KindDecimal  ValueKind = "decimal"
	KindBoolean  ValueKind = "boolean"
	KindDate     ValueKind = "date"
	KindDatetime ValueKind = "datetime"
	KindBinary   ValueKind = "binary"
	KindTag      ValueKind = "tag"
	KindUser     ValueKind = "user"
)

// Kind is the switch a read starts at: which getter returns something, and
// which word a log line should use.
func (v Value) Kind() ValueKind {
	switch v.class {
	case classText:
		return KindText
	case classInt:
		return KindInteger
	case classDec:
		return KindDecimal
	case classBool:
		return KindBoolean
	case classDate:
		return KindDate
	case classDT:
		return KindDatetime
	case classBin:
		return KindBinary
	case classTag:
		return KindTag
	case classUser:
		return KindUser
	}
	return KindNone
}

// Text reads a `Text` value; it is also how an options field's stored choice
// reads, since that is the class `load` gives it (§3.3).
func (v Value) Text() string {
	if v.class == classText {
		return v.s
	}
	return ""
}

// Int reads an `Int` value.
func (v Value) Int() int64 {
	if v.class == classInt {
		return v.i
	}
	return 0
}

// Decimal reads a `Decimal` value, at the scale the row kept it at.
func (v Value) Decimal() decimal.Decimal {
	if v.class == classDec {
		return v.d
	}
	return decimal.Decimal{}
}

// Bool reads a `Bool` value. A zero `Value` answers false, which is why a
// host that must tell a stored `false` from no value at all switches on Kind
// first — the distinction `Set` and `Empty` draw, held open on this side too.
func (v Value) Bool() bool {
	return v.class == classBool && v.i != 0
}

// Date reads a `Date` value.
func (v Value) Date() time.Time {
	if v.class == classDate {
		return v.t
	}
	return time.Time{}
}

// DateTime reads a `DateTime` value.
func (v Value) DateTime() time.Time {
	if v.class == classDT {
		return v.t
	}
	return time.Time{}
}

// Binary reads a `Binary` handle. Nothing resolves it yet (DESIGN §5.2); the
// value is opaque content identity, which is all the language says it is.
func (v Value) Binary() program.BinaryRef {
	if v.class == classBin {
		return slices.Clone(v.bin)
	}
	return nil
}

// Tag reads a `Tag` value's declared name — the enum member's text, which is
// what both sides of the seam agree on (§5.2's E3 pairing).
func (v Value) Tag() string {
	if v.class == classTag {
		return v.s
	}
	return ""
}

// User reads a `User` identity.
func (v Value) User() UserID {
	if v.class == classUser {
		return UserID(v.s)
	}
	return ""
}

// b2i is the boolean class's storage convention: the payload lives in the
// row's integer field, 1 or 0, which is what `nonEmpty`'s default branch and
// §3.4's `boolCmp` already assume — a stored false is a value, not an
// absence.
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// valueName says what class a Value carries, in the boundary's words. The
// identity is spelled here rather than through `className` because it is
// not a type: the one place a refusal about a role-typed field has to name
// what it wanted.
func valueName(class uint8) string {
	switch class {
	case classUser:
		return "an identity"
	case 0:
		return "no value at all"
	}
	return program.ClassName(class)
}

// carries is the one soft spot in an otherwise exact class match: an
// `options` field's value is the selected option's stored text, and `Text`
// is the only spelling text has at the boundary. Everything else matches
// the field's class exactly.
func carries(want, got uint8) bool {
	return got == want || (want == classOpt && got == classText)
}

// store settles one boundary value into the payload a row holds for slot n,
// or says in one sentence why it is not a value for that field. The second
// answer is the empty string when the first is usable — the same shape
// `dataDrift` uses, because the caller already knows the operation and the
// case and needs only the words for the refusal.
//
// The rest is two questions and no more: which class this field takes, and
// whether this value is one. What is *not* checked here is content — a
// `required` field cleared to empty, a length, a business rule — because
// §5.2 and §5.4 answer those when the case is read and submitted. DESIGN
// §6.2's split is the reason for the line: a wrong class is a structural
// error that rejects the whole update, an invalid value is ordinary feedback
// that stays with the draft.
func store(img *program.Program, n program.NodeID, v Value) (program.Cell, string) {
	sl := img.Slot(n)
	if sl.Kind != program.KindField {
		// `apply` refuses this shape before it gets here, in the longer
		// sentence `whyNoValue` gives. Stating it again is the function's own
		// contract: a group has no type and no role, so falling through would
		// ask the type table about structure.
		return program.Cell{}, quote(img.Path(n)) + " is structure and has no value"
	}
	if sl.Role != 0 {
		return storeRole(img, n, v)
	}
	info := img.Type(sl.Type)
	if info.Base == program.BaseSignature {
		// A signature is not a value a caller assigns. It is the record of
		// a `Sign` that checked authority, content and the observed revision
		// (DESIGN §6.4), so accepting one here would be the engine letting a
		// host forge it — which is why this answers before the class test:
		// "you cannot write a signature" is the truth about *any* value here,
		// and "that is not a signature" invites a host to try a different
		// class.
		return program.Cell{}, quote(img.Path(n)) +
			" is a signature field, and only sign writes one"
	}
	if !carries(info.Base, v.class) {
		return program.Cell{}, quote(img.Path(n)) + " holds " +
			program.ClassName(info.Base) + ", and this is " + valueName(v.class)
	}
	out := program.Cell{Set: true}
	switch info.Base {
	case program.BaseString, program.BaseOptions:
		out.Text = v.s
	case program.BaseInteger:
		out.Int = v.i
	case program.BaseDecimal:
		// T2 made precision and scale legal at declaration and §3.3 turned
		// them into a closure, so the bounds are `fits`'s business and the
		// numbers themselves are not in the image to quote. What a host
		// needs is which field and which value, which is both.
		if info.Fits != nil && !info.Fits(v.d) {
			return program.Cell{}, quote(img.Path(n)) +
				"'s decimal type does not hold " + decText(v.d)
		}
		out.Decimal = v.d
	case program.BaseBoolean:
		out.Int = v.i
	case program.BaseDate, program.BaseDatetime:
		out.Time = v.t
	case program.BaseBinary:
		out.Binary = v.bin
	case program.BaseEnum:
		tag, ok := img.TagByName(v.s)
		if !ok {
			return program.Cell{}, "no tag " + quote(v.s) + " is declared"
		}
		if !img.TagMatches(uint64(tag), sl.Type) {
			return program.Cell{}, "tag " + quote(v.s) + " is not a member of " +
				quote(img.Path(n)) + "'s enum"
		}
		out.Int = int64(tag)
	}
	return out, ""
}

// decText renders a decimal the way its caller wrote it: `0.00` stays two
// places, `7` stays a whole number. shopspring's own `String` normalizes
// trailing zeros away, which is right for arithmetic and wrong for a refusal
// — the number a host has to find in its log line is the one it sent, and the
// row keeps the value with its scale, so the message can too.
func decText(d decimal.Decimal) string {
	if e := d.Exponent(); e < 0 {
		return d.StringFixed(int32(-e))
	}
	return d.String()
}

// storeRole handles the one field shape with no type. The identity is not
// resolved against anything: there is no directory here, and the field's own
// declaration says which role a value confers (T4), so the only thing that
// can be wrong about the text is that it names nobody.
//
// An empty identity is refused rather than stored. A role field cleared with
// `Empty` is *unset*, which is the state §5.1's verb produces and the state
// `rolesHeld` cannot match, so a stored "" would be a third answer — a field
// that looks assigned, confers nothing, and matches an actor with no
// identity if the guard in authority.go were ever relaxed. `UserID`'s doc
// gives the same reason from the other side: nobody is not somebody.
//
// The role the field confers is deliberately not in the payload either. It is
// the *definition*'s answer about that declaration — §7's metadata — and the
// row holds only who was named, which is what keeps a recompiled definition's
// role rename from being a data change (§3).
func storeRole(img *program.Program, n program.NodeID, v Value) (program.Cell, string) {
	if v.class != classUser {
		return program.Cell{}, quote(img.Path(n)) + " holds " + valueName(classUser) +
			", and this is " + valueName(v.class)
	}
	if v.s == "" {
		return program.Cell{}, quote(img.Path(n)) +
			" holds an identity, and this names nobody"
	}
	return program.Cell{Set: true, Text: v.s}, ""
}

// load is store's other direction: the payload a row holds, back in the
// boundary's class. It exists because a `Validate` hook speaks `Value` and
// Submit asks the current hook about a value that is already stored (DESIGN
// §6.3, RUNTIME §6), so the row's bytes have to become the host's type again
// — and because a view shows what a case holds rather than what somebody
// last sent.
//
// An unset payload loads as the zero Value, which no constructor makes:
// every one of them sets a class, so "this field has no value" stays
// tellable apart from a value that happens to be empty (SYNTAX E6) — the
// same distinction `Set` and `Empty` draw at the other end of the seam.
//
// A signature slot has nothing to load. Its content is a `sigEntry`'s answer
// about who signed what, `Sign`'s to render (§3.6), and inventing a
// class for it here would be the engine guessing at a type the language does
// not have.
func load(img *program.Program, n program.NodeID, v program.Cell) Value {
	if !v.Set {
		return Value{}
	}
	if sl := img.Slot(n); sl.Role != 0 {
		return User(UserID(v.Text))
	}
	switch img.BaseOf(n) {
	case program.BaseString:
		return Text(v.Text)
	case program.BaseOptions:
		// An options value is the chosen option's text (§3.3), and `Text` is
		// the only spelling text has at the boundary — the same soft spot
		// `carries` allows in the other direction.
		return Text(v.Text)
	case program.BaseInteger:
		return Int(v.Int)
	case program.BaseDecimal:
		return Decimal(v.Decimal)
	case program.BaseBoolean:
		return Bool(v.Int != 0)
	case program.BaseDate:
		return Date(v.Time)
	case program.BaseDatetime:
		return DateTime(v.Time)
	case program.BaseBinary:
		return Binary(v.Binary)
	case program.BaseEnum:
		return Tag(img.TagName(program.TagID(v.Int)))
	}
	return Value{}
}
