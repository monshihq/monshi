package monshi

// The value half of an edit — DESIGN §5.1's `Set(path, value)`.
//
// A `Value` is one exported type with unexported payload fields and a
// constructor per class, rather than `any`. The difference is not
// cosmetic. With `any`, every host call becomes a runtime type assertion
// whose failure mode is a value the engine guessed at, and a `float64`
// arrives from a JSON decoder whether the host meant it or not — while T2
// makes `decimal(p, s)` the language's money type and §3.3 gives it a
// prebuilt bounds test. One constructor per class means the only spelling
// of a decimal is `decimal.Decimal`, the only spelling of an integer is
// `int64`, and a wrong class is a call the host did not compile rather than
// a refusal it has to handle. The engine still checks the class it was
// given, because which class a field takes is the definition's answer, not
// the caller's.
//
// The classes are the image's own (`baseString` and friends, T1: a value
// has exactly one), with one addition: an identity. A role-typed field has
// no type and no value class at all (§3.3), so its value needs a class the
// type table never numbers, and `User` is the only constructor that carries
// it.

import (
	"time"

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
	bin   BinaryRef
}

// The class a Value carries: the image's base classes, under the boundary's
// names, plus the identity class. A host never sees these words — the
// constructors below are the vocabulary — but the codes that say what they
// expect are these.
const (
	classText = baseString
	classInt  = baseInteger
	classDec  = baseDecimal
	classBool = baseBoolean
	classDate = baseDate
	classDT   = baseDatetime
	classBin  = baseBinary
	classTag  = baseEnum
	classOpt  = baseOptions

	// classUser is one past every class a *type* can have, which is the
	// honest place for it: `baseOf` answers 0 for a role-typed field
	// precisely because there is no type behind it. Stated relative to
	// `baseSignature` so it cannot collide with a class added later.
	classUser = baseSignature + 1
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
func Binary(ref BinaryRef) Value { return Value{class: classBin, bin: ref} }

// User carries the value of a role-typed field, which *is* an actor's
// identity (SYNTAX §5.2-T4) rather than a role name. Assigning one is how a
// definition hands out a role; whether the assignee holds it is a committed
// question, and authority reads the committed snapshot (DESIGN §4.3).
func User(id UserID) Value { return Value{class: classUser, s: string(id)} }

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
	return className(class)
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
func (img *image) store(n nodeID, v Value) (slotValue, string) {
	sl := &img.slots[n]
	if sl.kind != kindField {
		// `apply` refuses this shape before it gets here, in the longer
		// sentence `whyNoValue` gives. Stating it again is the function's own
		// contract: a group has no type and no role, so falling through would
		// ask the type table about structure.
		return slotValue{}, quote(img.path(n)) + " is structure and has no value"
	}
	if sl.role != 0 {
		return img.storeRole(n, v)
	}
	info := &img.types[sl.typ]
	if info.base == baseSignature {
		// A signature is not a value a caller assigns. It is the record of
		// a `Sign` that checked authority, content and the observed revision
		// (DESIGN §6.4), so accepting one here would be the engine letting a
		// host forge it — which is why this answers before the class test:
		// "you cannot write a signature" is the truth about *any* value here,
		// and "that is not a signature" invites a host to try a different
		// class.
		return slotValue{}, quote(img.path(n)) +
			" is a signature field, and only sign writes one"
	}
	if !carries(info.base, v.class) {
		return slotValue{}, quote(img.path(n)) + " holds " +
			className(info.base) + ", and this is " + valueName(v.class)
	}
	out := slotValue{set: true}
	switch info.base {
	case baseString, baseOptions:
		out.s = v.s
	case baseInteger:
		out.i = v.i
	case baseDecimal:
		// T2 made precision and scale legal at declaration and §3.3 turned
		// them into a closure, so the bounds are `fits`'s business and the
		// numbers themselves are not in the image to quote. What a host
		// needs is which field and which value, which is both.
		if info.fits != nil && !info.fits(v.d) {
			return slotValue{}, quote(img.path(n)) +
				"'s decimal type does not hold " + decText(v.d)
		}
		out.d = v.d
	case baseBoolean:
		out.i = v.i
	case baseDate, baseDatetime:
		out.t = v.t
	case baseBinary:
		out.bin = v.bin
	case baseEnum:
		tag, ok := img.tagByName(v.s)
		if !ok {
			return slotValue{}, "no tag " + quote(v.s) + " is declared"
		}
		if img.tagType[tag] != sl.typ {
			return slotValue{}, "tag " + quote(v.s) + " is not a member of " +
				quote(img.path(n)) + "'s enum"
		}
		out.i = int64(tag)
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
func (img *image) storeRole(n nodeID, v Value) (slotValue, string) {
	if v.class != classUser {
		return slotValue{}, quote(img.path(n)) + " holds " + valueName(classUser) +
			", and this is " + valueName(v.class)
	}
	if v.s == "" {
		return slotValue{}, quote(img.path(n)) +
			" holds an identity, and this names nobody"
	}
	return slotValue{set: true, s: v.s}, ""
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
// about who signed what, milestone 5's to render (§3.6), and inventing a
// class for it here would be the engine guessing at a type the language does
// not have.
func (img *image) load(n nodeID, v slotValue) Value {
	if !v.set {
		return Value{}
	}
	if sl := &img.slots[n]; sl.role != 0 {
		return User(UserID(v.s))
	}
	switch img.baseOf(n) {
	case baseString:
		return Text(v.s)
	case baseOptions:
		// An options value is the chosen option's text (§3.3), and `Text` is
		// the only spelling text has at the boundary — the same soft spot
		// `carries` allows in the other direction.
		return Text(v.s)
	case baseInteger:
		return Int(v.i)
	case baseDecimal:
		return Decimal(v.d)
	case baseBoolean:
		return Bool(v.i != 0)
	case baseDate:
		return Date(v.t)
	case baseDatetime:
		return DateTime(v.t)
	case baseBinary:
		return Binary(v.bin)
	case baseEnum:
		return Tag(img.tagName(tagID(v.i)))
	}
	return Value{}
}

// same is the equality a commit diff and a no-op submit are decided on: does
// this slot hold the same value in these two payloads.
//
// Each class is compared by what it *means* rather than by its bytes, and
// two of those choices are worth stating. A decimal compares by value, so
// `1.0` over a stored `1.00` is not a content change — shopspring keeps the
// written scale in the representation, and §5.1's diff records what the case
// now says, not how a client typed it. A time compares by instant, so a
// value that arrived through a different `*Location` is the same datetime;
// `Date` and `DateTime` both drop to the wall clock here rather than to the
// struct's pointer fields, which is the difference between a diff that means
// something and one that reports a reformat.
//
// An unset payload equals another unset payload: `Empty` over a field that
// was already empty is no change, which is §6.3's "no meaningless commit"
// read at the level of one cell.
func (img *image) same(n nodeID, a, b slotValue) bool {
	if a.set != b.set {
		return false
	}
	if !a.set {
		return true
	}
	if img.slots[n].role != 0 {
		return a.s == b.s
	}
	switch img.baseOf(n) {
	case baseString, baseOptions:
		return a.s == b.s
	case baseInteger, baseBoolean, baseEnum:
		return a.i == b.i
	case baseDecimal:
		return a.d.Equal(b.d)
	case baseDate, baseDatetime:
		return a.t.Equal(b.t)
	case baseBinary:
		return string(a.bin) == string(b.bin)
	}
	return false
}
