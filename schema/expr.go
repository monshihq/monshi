package schema

import (
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// vt is the checker's internal classification of an expression result:
// either a literal, a typed operand (node or enum tag) or a boolean
// sub-expression.
type vt struct {
	lit        string // "" | "string" | "number" | "bool"
	text       string // literal text, for diagnostics and pairing
	val        *Value // the literal's compiled value, for normalization
	typ        *Type  // node or tag type; nil for literals
	isBoolExpr bool   // usable where a boolean expression is required
	bad        bool   // already reported; suppresses cascades
}

func operandText(o *astOperand) string {
	if o.kind == "path" {
		s := strings.Join(o.segs, ".")
		if o.relative {
			s = "." + s
		}
		return s
	}
	if o.kind == "string" {
		return `"` + o.text + `"`
	}
	return o.text
}

func (a *analyzer) checkExpr(x *astExpr, st *site) (*Expr, vt) {
	switch x.op {
	case "":
		return a.checkOperandExpr(x.operand, st)
	case "!":
		e, t := a.checkExpr(x.left, st)
		if !t.bad && !t.isBoolExpr {
			a.errf(pos{x.left.line, x.left.col}, CodeNotBoolean, "operand of !")
		}
		return &Expr{Op: "!", Left: e}, vt{isBoolExpr: true, bad: t.bad}
	case "&&", "||":
		le, lt := a.checkExpr(x.left, st)
		re, rt := a.checkExpr(x.right, st)
		if !lt.bad && !lt.isBoolExpr {
			a.errf(pos{x.left.line, x.left.col}, CodeNotBoolean, "operand of "+x.op)
		}
		if !rt.bad && !rt.isBoolExpr {
			a.errf(pos{x.right.line, x.right.col}, CodeNotBoolean, "operand of "+x.op)
		}
		return &Expr{Op: x.op, Left: le, Right: re}, vt{isBoolExpr: true, bad: lt.bad || rt.bad}
	default: // comparison
		le, lt := a.checkExpr(x.left, st)
		re, rt := a.checkExpr(x.right, st)
		bad := lt.bad || rt.bad
		if !bad && !a.checkPair(x.op, lt, rt, pos{x.right.line, x.right.col}) {
			bad = true
		}
		return &Expr{Op: x.op, Left: le, Right: re}, vt{isBoolExpr: true, bad: bad}
	}
}

// checkOperandExpr resolves a named operand and checks that it denotes a single
// value.
func (a *analyzer) checkOperandExpr(o *astOperand, st *site) (*Expr, vt) {
	p := pos{o.line, o.col}
	switch o.kind {
	case "string":
		v := &Value{Kind: "string", Str: o.text}
		return &Expr{Lit: v}, vt{lit: "string", text: o.text, val: v}
	case "number":
		v := numberValue(o.text)
		if v == nil {
			a.errf(p, CodeInvalidOperand, o.text)
			return nil, vt{bad: true}
		}
		return &Expr{Lit: v}, vt{lit: "number", text: o.text, val: v}
	case "bool":
		v := &Value{Kind: "boolean", Bool: o.text == "true"}
		return &Expr{Lit: v}, vt{lit: "bool", text: o.text, val: v}
	}
	joined := strings.Join(o.segs, ".")
	switch {
	case o.relative:
		if st.form == nil {
			a.errf(p, CodeInvalidOperand, "relative path ."+joined+" has no anchor here")
			return nil, vt{bad: true}
		}
		n := walkPath(st.form, o.segs)
		if n == nil {
			a.errf(p, CodeUnknownTarget, joined)
			return nil, vt{bad: true}
		}
		return a.finishOperand(n, p, st)
	case len(o.segs) == 1 && isUpperStart(o.segs[0]):
		if t, ok := a.tags[o.segs[0]]; ok {
			return &Expr{Tag: o.segs[0]}, vt{typ: t}
		}
		if _, ok := a.roles[o.segs[0]]; ok {
			a.errf(p, CodeInvalidOperand, "role "+o.segs[0]+" is not a value")
		} else {
			a.errf(p, CodeInvalidOperand, "unknown enum tag "+o.segs[0])
		}
		return nil, vt{bad: true}
	case len(o.segs) == 1:
		a.errf(p, CodeInvalidOperand, "bare reference "+joined+" names a container, not a value")
		return nil, vt{bad: true}
	case isUpperStart(o.segs[0]):
		a.errf(p, CodeInvalidOperand, "path root must be a form name: "+joined)
		return nil, vt{bad: true}
	default:
		n, ok := a.nodes[joined]
		if !ok {
			a.errf(p, CodeUnknownTarget, joined)
			return nil, vt{bad: true}
		}
		return a.finishOperand(n, p, st)
	}
}

// finishOperand rejects non-value paths.
func (a *analyzer) finishOperand(n *Node, p pos, st *site) (*Expr, vt) {
	if n.Kind != "field" {
		a.errf(p, CodeInvalidOperand, n.Path+" is a "+n.Kind+", not a value")
		return nil, vt{bad: true}
	}
	if !n.resolved {
		return nil, vt{bad: true} // type error already reported
	}
	if n.Role != nil {
		a.errf(p, CodeInvalidOperand, "role-typed field "+n.Path+" is not a comparable value")
		return nil, vt{bad: true}
	}
	if n.Multiple {
		a.errf(p, CodeInvalidOperand, "multiple node "+n.Path+" is not a single value")
		return nil, vt{bad: true}
	}
	if n.Type == nil || a.bad[n.Type] {
		return nil, vt{bad: true}
	}
	switch n.Type.Base {
	case "binary", "options", "signature":
		a.errf(p, CodeInvalidOperand, n.Type.Base+" field "+n.Path+" is never a DSL value")
		return nil, vt{bad: true}
	}
	for q := n.Parent; q != nil && q.Kind != "form"; q = q.Parent {
		if q.Multiple { // no single value per case through a collection
			a.errf(p, CodeInvalidOperand, "path to "+n.Path+" traverses multiple node "+q.Path)
			return nil, vt{bad: true}
		}
	}
	st.refs = append(st.refs, n)
	return &Expr{Node: n}, vt{typ: n.Type, isBoolExpr: n.Type.Base == "boolean"}
}

// checkPair validates comparison operands and reports the first incompatible
// pairing.
func (a *analyzer) checkPair(op string, l, r vt, p pos) bool {
	order := op == "<" || op == "<=" || op == ">" || op == ">="
	switch {
	case l.lit != "" && r.lit != "":
		if l.lit != r.lit {
			return a.mismatch(p, l.text, r.text)
		}
		if order && l.lit == "bool" {
			return a.mismatch(p, op, "boolean literals")
		}
		return true
	case l.lit != "" || r.lit != "":
		lit, text, typ, val := l.lit, l.text, r.typ, l.val
		if l.typ != nil {
			lit, text, typ, val = r.lit, r.text, l.typ, r.val
		}
		if typ == nil {
			return false
		}
		switch lit {
		case "string":
			switch typ.Base {
			case "string":
			case "date":
				if _, ok := parseDateOnly(text); !ok {
					return a.mismatch(p, text, "a valid date")
				}
			case "datetime":
				if _, ok := parseDateTimeOnly(text); !ok {
					return a.mismatch(p, text, "a valid datetime")
				}
			default:
				return a.mismatch(p, text, typeName(typ))
			}
		case "number":
			switch typ.Base {
			case "integer":
				if strings.Contains(text, ".") {
					return a.mismatch(p, text, "integer")
				}
				if _, err := strconv.ParseInt(text, 10, 64); err != nil {
					return a.mismatch(p, text, "the signed 64-bit integer range")
				}
			case "decimal":
				if !decimalFits(text, typ) {
					return a.mismatch(p, text, typeName(typ))
				}
			default:
				return a.mismatch(p, text, typeName(typ))
			}
		case "bool":
			if typ.Base != "boolean" {
				return a.mismatch(p, text, typeName(typ))
			}
		}
		if order && !orderBases[typ.Base] {
			return a.mismatch(p, op, typeName(typ))
		}
		normalizeLiteral(val, typ) // compiled literals are typed
		return true
	default:
		if l.typ == nil || r.typ == nil {
			return false
		}
		if l.typ != r.typ { // identical types, incl. same-enum tags
			return a.mismatch(p, typeName(l.typ), typeName(r.typ))
		}
		if order && !orderBases[l.typ.Base] {
			return a.mismatch(p, op, typeName(l.typ))
		}
		return true
	}
}

func (a *analyzer) mismatch(p pos, l, r string) bool {
	a.errf(p, CodeTypeMismatch, l+" vs "+r)
	return false
}

// normalizeLiteral converts a comparison literal to its checked operand type,
// so evaluation never reparses it.
func normalizeLiteral(v *Value, t *Type) {
	if v == nil || t == nil {
		return
	}
	switch t.Base {
	case "date", "datetime":
		if v.Kind != "string" {
			return
		}
		var tm time.Time
		var ok bool
		if t.Base == "date" {
			tm, ok = parseDateOnly(v.Str)
		} else {
			tm, ok = parseDateTimeOnly(v.Str)
		}
		if !ok {
			return
		}
		v.Kind, v.Time = t.Base, tm
	case "decimal":
		if v.Kind == "integer" {
			v.Dec, v.Kind = decimal.NewFromInt(v.Int), "decimal"
		}
	}
}

// decimalFits checks a literal against decimal(p, s): the value
// must need at most s fractional digits and at most p significant ones,
// so trailing zeros beyond the scale still fit (1.200 in decimal(4,2)).
func decimalFits(text string, t *Type) bool {
	d, err := decimal.NewFromString(text)
	if err != nil {
		return false
	}
	return FitsDecimal(d, t.Precision, t.Scale)
}

// FitsDecimal reports whether d fits decimal(precision, scale) without rounding.
func FitsDecimal(d decimal.Decimal, precision, scale int) bool {
	r := d.Round(int32(scale))
	return r.Equal(d) && r.NumDigits() <= precision
}

// checkDefault requires a compile-time constant compatible with the field's
// resolved type.
func (a *analyzer) checkDefault(n *Node, at *astAttr) {
	p := pos{at.line, at.col}
	o := at.value
	if o == nil || !n.resolved {
		return
	}
	if n.Role != nil {
		a.errf(p, CodeInvalidDefault, "role-typed field "+n.Path+" takes no default")
		return
	}
	t := n.Type
	if t == nil {
		return
	}
	switch t.Base {
	case "binary", "options", "signature":
		a.errf(p, CodeInvalidDefault, t.Base+" field "+n.Path+" takes no default")
		return
	}
	if a.bad[t] {
		return
	}
	switch t.Base {
	case "enum":
		if o.kind == "path" && !o.relative && len(o.segs) == 1 && isUpperStart(o.segs[0]) && a.tags[o.segs[0]] == t {
			n.Default = &Value{Kind: "enum", Str: o.segs[0]}
			return
		}
	case "string":
		if o.kind == "string" {
			n.Default = &Value{Kind: "string", Str: o.text}
			return
		}
	case "integer":
		if o.kind == "number" && !strings.Contains(o.text, ".") {
			if v, err := strconv.ParseInt(o.text, 10, 64); err == nil {
				n.Default = &Value{Kind: "integer", Int: v}
				return
			}
		}
	case "decimal":
		if o.kind == "number" && decimalFits(o.text, t) {
			d, _ := decimal.NewFromString(o.text)
			n.Default = &Value{Kind: "decimal", Dec: d}
			return
		}
	case "boolean":
		if o.kind == "bool" {
			n.Default = &Value{Kind: "boolean", Bool: o.text == "true"}
			return
		}
	case "date":
		if o.kind == "string" {
			if tm, ok := parseDateOnly(o.text); ok {
				n.Default = &Value{Kind: "date", Str: o.text, Time: tm}
				return
			}
		}
	case "datetime":
		if o.kind == "string" {
			if tm, ok := parseDateTimeOnly(o.text); ok {
				n.Default = &Value{Kind: "datetime", Str: o.text, Time: tm}
				return
			}
		}
	}
	a.errf(p, CodeInvalidDefault, n.Path+" : "+typeName(t)+" default "+operandText(o))
}

// numberValue converts a NUMBER literal to an integer or decimal Value.
func numberValue(text string) *Value {
	if !strings.Contains(text, ".") {
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return &Value{Kind: "integer", Int: i}
		}
	}
	if d, err := decimal.NewFromString(text); err == nil {
		return &Value{Kind: "decimal", Dec: d}
	}
	return nil
}

func parseDateOnly(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

func parseDateTimeOnly(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// constantExpr reports whether an expression has no data-path operands,
// i.e. its value is fixed at compile time.
func constantExpr(e *Expr) bool {
	if e == nil {
		return false
	}
	if e.Node != nil {
		return false
	}
	if e.Tag != "" || e.Lit != nil {
		return true
	}
	return constantExpr(e.Left) && constantExpr(e.Right)
}
