package monshi

// Lowering, pass: conditions (RUNTIME §3.4, §4 rows E1–E5). Each guard of
// the compiled definition becomes one closure from the fixed constructor
// set, and the set of slots it reads is recorded for §5.3's fanout.
//
// The shape of every tree here is what SYNTAX §5.7 allows and nothing
// else: boolean trees of comparisons over scalar leaves, literals and enum
// tags. An expression the emitter cannot represent is a violated front-end
// guarantee, reported and refused.

import (
	"strings"

	"github.com/monshihq/monshi/schema"
	"github.com/shopspring/decimal"
)

// condition being lowered right now, so leaves can record what they read.
type condScope struct {
	at     string   // problem text location
	owner  nodeID   // slot that owns this condition; 0 for an edge guard
	refs   []nodeID // read slots, deduplicated
	seen   map[nodeID]bool
	inColl bool // owner sits inside a multiple collection
}

// take folds a sub-scope's reads into its parent. Negation compiles its
// operand on a scope of its own, so the guard can name exactly the slots
// whose absence would make it vacuous — and the parent still has to know
// about every one of them, because §5.3's fanout is built from this list.
func (sc *condScope) take(sub *condScope) {
	for _, s := range sub.refs {
		if sc.seen[s] {
			continue
		}
		sc.seen[s] = true
		sc.refs = append(sc.refs, s)
	}
}

func (l *lowerer) conditions() {
	for id := nodeID(1); int(id) < len(l.img.slots); id++ {
		n := l.nodeAt[id]
		if n == nil {
			continue
		}
		s := &l.img.slots[id]
		if n.PresentCond != nil {
			s.condPres = l.addCond(n.PresentCond, id, n.Path)
		}
		if n.RequiredCond != nil {
			s.condReq = l.addCond(n.RequiredCond, id, n.Path)
		}
	}
	for _, st := range l.s.Stages {
		sid := l.stageOf[st]
		info := &l.img.stages[sid]
		if len(st.Edges) != len(info.edges) {
			// Only a stage error (ST2/ST3) drops an edge, and Load refused
			// the definition in that case; a mismatch here is drift.
			l.reportf(st.Name, "%d edges compiled, %d lowered",
				len(st.Edges), len(info.edges))
			continue
		}
		for i, e := range st.Edges {
			if e.Cond == nil {
				continue
			}
			info.edges[i].guard = l.addCond(e.Cond, 0, st.Name+" → "+e.Dest.Name)
		}
	}
}

// addCond lowers one guard into image.conds and remembers its reads. A
// condition that fails to lower yields condID 0 plus a reported problem:
// the definition is refused, so no half-built guard is ever reachable.
func (l *lowerer) addCond(x *schema.Expr, owner nodeID, at string) condID {
	sc := &condScope{
		at:     at,
		owner:  owner,
		seen:   map[nodeID]bool{},
		inColl: owner != 0 && l.collection[owner],
	}
	c, ok := l.cond(x, sc)
	if !ok {
		return 0
	}
	id := condID(len(l.img.conds))
	l.img.conds = append(l.img.conds, c)
	l.reads[id] = sc.refs
	l.condOwner[id] = sc.owner
	return id
}

// cond compiles one expression node.
func (l *lowerer) cond(x *schema.Expr, sc *condScope) (condFn, bool) {
	if x == nil {
		l.reportf(sc.at, "condition is missing")
		return nil, false
	}
	if !readsData(x) {
		// No operand of this subtree is a case value: ST6's constant
		// condition, folded once here instead of evaluated per request.
		v, ok := l.evalConst(x, sc)
		if !ok {
			return nil, false
		}
		if v {
			return always(), true
		}
		return never(), true
	}
	switch x.Op {
	case "":
		// A standalone primary must be boolean (E4): a path to a boolean
		// field, never a bare tag or literal.
		if x.Node == nil {
			l.reportf(sc.at, "a standalone constant is not a condition")
			return nil, false
		}
		n := l.leafSlot(x.Node, sc)
		if n == badSlot {
			return nil, false
		}
		if l.typeBase(l.img.slots[n].typ) != baseBoolean {
			l.reportf(sc.at, "%s is not a boolean", l.img.path(n)) // E4
			return nil, false
		}
		return isTrue(n), true
	case "!":
		if x.Left != nil && x.Left.Op == "" && x.Left.Node != nil {
			n := l.leafSlot(x.Left.Node, sc)
			if n == badSlot {
				return nil, false
			}
			return notTrue(n), true // the common !.flag, with no wrapper
		}
		// Negation needs the data it negates. The subtree's reads are
		// collected on their own scope so the guard can name exactly the
		// slots whose absence would make `!` vacuous.
		sub := &condScope{at: sc.at, owner: sc.owner, inColl: sc.inColl,
			seen: map[nodeID]bool{}}
		c, ok := l.cond(x.Left, sub)
		if !ok {
			return nil, false
		}
		sc.take(sub)
		if len(sub.refs) == 0 {
			return neg(c), true // constant inside, folded already
		}
		return andAll(allSet(sub.refs), neg(c)), true
	case "&&", "||":
		var terms []condFn
		l.collect(x, x.Op, sc, &terms)
		if len(terms) == 0 {
			return nil, false // reported inside collect
		}
		if x.Op == "&&" {
			return andAll(terms...), true
		}
		return orAll(terms...), true
	default:
		op, ok := l.cmpOf(x.Op)
		if !ok {
			return nil, false
		}
		return l.comparison(x, op, sc)
	}
}

// collect flattens one operator chain, so no source nesting depth survives
// in the closure tree (§3.4).
func (l *lowerer) collect(x *schema.Expr, op string, sc *condScope, out *[]condFn) {
	if x.Op == op {
		if x.Left != nil {
			l.collect(x.Left, op, sc, out)
		}
		if x.Right != nil {
			l.collect(x.Right, op, sc, out)
		}
		return
	}
	c, ok := l.cond(x, sc)
	if ok {
		*out = append(*out, c)
	}
}

func (l *lowerer) cmpOf(op string) (cmpOp, bool) {
	switch op {
	case "==":
		return cmpEq, true
	case "!=":
		return cmpNe, true
	case "<":
		return cmpLt, true
	case "<=":
		return cmpLe, true
	case ">":
		return cmpGt, true
	case ">=":
		return cmpGe, true
	}
	l.reportf("", "operator %q is not a comparison", op)
	return cmpEq, false
}

// --- operands ---

const badSlot nodeID = 0xffffffff

// operand is one comparison side, resolved to a class at lowering.
type operand struct {
	kind  uint8 // opSlot, opTag or opConst
	slot  nodeID
	class uint8
	tag   tagID
	sv    slotValue
	src   *schema.Value // literal text, kept to re-derive it per operand
}

const (
	opSlot uint8 = iota + 1
	opTag
	opConst
)

// leafSlot resolves a value-path operand to its slot, applying the §4
// anti-drift checks that E1/E2/F7 already settled at compile time.
func (l *lowerer) leafSlot(n *schema.Node, sc *condScope) nodeID {
	if n == nil {
		l.reportf(sc.at, "condition operand is unresolved")
		return badSlot
	}
	s, ok := l.slotOf[n]
	if !ok {
		l.reportf(sc.at, "operand %s is not a declaration", n.Path)
		return badSlot
	}
	if l.img.slots[s].role != 0 {
		l.reportf(sc.at, "role-typed field %s is not a value", n.Path) // E2
		return badSlot
	}
	if l.collection[s] {
		l.reportf(sc.at, "condition reads %s inside a multiple collection", n.Path) // E2
		return badSlot
	}
	if sc.owner != 0 && l.inSubtree(sc.owner, s) {
		l.reportf(sc.at, "condition of %s reads its own subtree",
			l.img.path(sc.owner)) // F7
		return badSlot
	}
	if !sc.seen[s] {
		sc.seen[s] = true
		sc.refs = append(sc.refs, s)
	}
	return s
}

// inSubtree covers F7's self-reference test: a declaration whose condition
// reads itself or a descendant would make the dependency graph cyclic. The
// front end rejects that; reaching here is drift.
func (l *lowerer) inSubtree(root, n nodeID) bool {
	return n >= root && n < root+nodeID(l.img.slots[root].span)
}

// operandOf classifies one expression leaf.
func (l *lowerer) operandOf(x *schema.Expr, sc *condScope) (operand, bool) {
	var o operand
	switch {
	case x.Node != nil:
		s := l.leafSlot(x.Node, sc)
		if s == badSlot {
			return o, false
		}
		o.kind, o.slot = opSlot, s
		o.class = l.typeBase(l.img.slots[s].typ)
		if o.class == 0 {
			l.reportf(sc.at, "%s has no value class", x.Node.Path)
			return o, false
		}
		return o, true
	case x.Tag != "":
		tag, ok := l.tagOf[x.Tag]
		if !ok {
			l.reportf(sc.at, "tag %s was not declared", x.Tag) // E1
			return o, false
		}
		o.kind, o.tag, o.class = opTag, tag, baseEnum
		o.sv = slotValue{set: true, i: int64(tag)}
		return o, true
	case x.Lit != nil:
		class := litClass(x.Lit)
		if class == 0 {
			l.reportf(sc.at, "literal kind %q is not a value", x.Lit.Kind)
			return o, false
		}
		sv, _, ok := l.constValue(class, sc.at, x.Lit)
		if !ok {
			return o, false
		}
		o.kind, o.class, o.sv, o.src = opConst, class, sv, x.Lit
		return o, true
	}
	l.reportf(sc.at, "operand is not a value, tag or literal")
	return o, false
}

func litClass(v *schema.Value) uint8 {
	switch v.Kind {
	case "string":
		return baseString
	case "integer":
		return baseInteger
	case "decimal":
		return baseDecimal
	case "boolean":
		return baseBoolean
	case "date":
		return baseDate
	case "datetime":
		return baseDatetime
	case "enum":
		return baseEnum
	}
	return 0
}

// comparison builds the closure for one comparison, after checking the
// pairing E3 requires: at least one typed operand, identical types for two
// slots, an enum field paired only with a tag of that same enum, and a
// literal already normalized to the operand's class.
func (l *lowerer) comparison(x *schema.Expr, op cmpOp, sc *condScope) (condFn, bool) {
	a, ok := l.operandOf(x.Left, sc)
	if !ok {
		return nil, false
	}
	b, ok := l.operandOf(x.Right, sc)
	if !ok {
		return nil, false
	}
	// Keep the value operand on the left; a literal or tag on the left
	// means the operator has to be read from the other side.
	if a.kind != opSlot && b.kind == opSlot {
		a, b = b, a
		op = reverse(op)
	}
	if ordered(op) && (!orderable(a.class) || !orderable(b.class)) {
		// E3: < <= > >= need integer, decimal, date, datetime or string.
		// A boolean or enum pair can only be constant, and the fold above
		// owns that case, so reaching here is drift.
		l.reportf(sc.at, "%s cannot order a %s against %s pair",
			x.Op, className(a.class), className(b.class))
		return nil, false
	}
	switch {
	case a.kind == opSlot && b.kind == opSlot:
		return l.twoSlots(a, b, op, sc)
	case a.kind == opSlot && b.kind == opTag:
		if l.img.slots[a.slot].typ != l.img.tagType[b.tag] {
			l.reportf(sc.at, "tag %s belongs to another enum", l.img.tagName(b.tag))
			return nil, false
		}
		return tagCmp(a.slot, op, b.tag), true
	case a.kind == opSlot:
		// The constant is compiled against the *operand's* class, so
		// SYNTAX §5.8's promise — a literal already normalized to the type
		// it pairs with — is what gets asserted here (RUNTIME §3.3).
		sv, _, ok := l.constValue(a.class, sc.at, b.src)
		if !ok {
			return nil, false
		}
		return l.slotConst(a.slot, op, sv, sc)
	case a.kind == opTag || b.kind == opTag:
		// Two tags, or a tag against a literal: constant, so the fold above
		// should have caught it. Reaching here means drift.
		l.reportf(sc.at, "constant operand pair was not folded")
		return nil, false
	default:
		l.reportf(sc.at, "comparison has no value operand")
		return nil, false
	}
}

// twoSlots compares two case values. E3 admits only operands of an
// identical type, so the classes agree by construction and the check below
// is drift, not validation.
func (l *lowerer) twoSlots(a, b operand, op cmpOp, sc *condScope) (condFn, bool) {
	if a.class != b.class {
		l.reportf(sc.at, "%s and %s are not the same type",
			l.img.path(a.slot), l.img.path(b.slot))
		return nil, false
	}
	if a.class == baseEnum && l.img.slots[a.slot].typ != l.img.slots[b.slot].typ {
		l.reportf(sc.at, "%s and %s are different enums",
			l.img.path(a.slot), l.img.path(b.slot)) // E3
		return nil, false
	}
	switch a.class {
	case baseString:
		return strSlotCmp(a.slot, b.slot, op), true
	case baseInteger:
		return intSlotCmp(a.slot, b.slot, op), true
	case baseDecimal:
		return decSlotCmp(a.slot, b.slot, op), true
	case baseDate, baseDatetime:
		return timeSlotCmp(a.slot, b.slot, op), true
	case baseEnum:
		return tagSlotCmp(a.slot, b.slot, op), true
	case baseBoolean:
		return boolSlotCmp(a.slot, b.slot, op), true
	}
	l.reportf(sc.at, "%s is never comparable", className(a.class))
	return nil, false
}

// slotConst compares a case value against a constant. SYNTAX §5.8 says the
// literal already carries the operand's normalized type, so an integer
// against a decimal or a text date against a date operand is drift here and
// never a repair opportunity.
func (l *lowerer) slotConst(n nodeID, op cmpOp, c slotValue, sc *condScope) (condFn, bool) {
	switch l.typeBase(l.img.slots[n].typ) {
	case baseString:
		return strCmp(n, op, c.s), true
	case baseInteger:
		return intCmp(n, op, c.i), true
	case baseDecimal:
		return decCmp(n, op, c.d), true
	case baseDate, baseDatetime:
		return timeCmp(n, op, c.t), true
	case baseBoolean:
		return boolCmp(n, op, c.i != 0), true
	case baseEnum:
		return tagCmp(n, op, tagID(c.i)), true
	}
	l.reportf(sc.at, "%s is never comparable with a constant", l.img.path(n))
	return nil, false
}

// --- constant conditions ---

// readsData reports whether a subtree touches case data at all. A tree that
// does not is ST6's constant condition.
func readsData(x *schema.Expr) bool {
	if x == nil {
		return false
	}
	if x.Node != nil {
		return true
	}
	return readsData(x.Left) || readsData(x.Right)
}

// evalConst computes a data-free condition at lowering. Only comparisons
// between constants and the boolean connectives can reach it, so the whole
// evaluator is this function.
func (l *lowerer) evalConst(x *schema.Expr, sc *condScope) (bool, bool) {
	switch x.Op {
	case "":
		o, ok := l.operandOf(x, sc)
		if !ok {
			return false, false
		}
		if o.class != baseBoolean {
			l.reportf(sc.at, "a standalone %s constant is not a condition",
				className(o.class)) // E4
			return false, false
		}
		return o.sv.i != 0, true
	case "!":
		v, ok := l.evalConst(x.Left, sc)
		return !v, ok
	case "&&":
		left, ok := l.evalConst(x.Left, sc)
		if !ok {
			return false, false
		}
		if !left {
			return false, true
		}
		return l.evalConst(x.Right, sc)
	case "||":
		left, ok := l.evalConst(x.Left, sc)
		if !ok {
			return false, false
		}
		if left {
			return true, true
		}
		return l.evalConst(x.Right, sc)
	}
	op, ok := l.cmpOf(x.Op)
	if !ok {
		return false, false
	}
	a, ok := l.operandOf(x.Left, sc)
	if !ok {
		return false, false
	}
	b, ok := l.operandOf(x.Right, sc)
	if !ok {
		return false, false
	}
	r, ok := constOrder(a, b)
	if !ok {
		l.reportf(sc.at, "%s and %s cannot be compared",
			className(a.class), className(b.class))
		return false, false
	}
	if ordered(op) && (!orderable(a.class) || !orderable(b.class)) {
		l.reportf(sc.at, "%s cannot order a %s against %s pair",
			x.Op, className(a.class), className(b.class))
		return false, false
	}
	return op.holds(r), true
}

// constOrder orders two constants of a reconciled class. Two number
// literals of different widths are one class here, which is what E3's
// NUMBER pairing means for a constant pair.
func constOrder(a, b operand) (int, bool) {
	if a.class == b.class {
		return constCompare(a.class, a.sv, b.sv), true
	}
	if numericLit(a.class) && numericLit(b.class) {
		x, y := toDecimal(a.class, a.sv), toDecimal(b.class, b.sv)
		return x.Cmp(y), true
	}
	return 0, false
}

func numericLit(class uint8) bool { return class == baseInteger || class == baseDecimal }

func toDecimal(class uint8, sv slotValue) decimal.Decimal {
	if class == baseInteger {
		return decimal.NewFromInt(sv.i)
	}
	return sv.d
}

func constCompare(class uint8, x, y slotValue) int {
	switch class {
	case baseString:
		return strings.Compare(x.s, y.s)
	case baseInteger, baseEnum, baseBoolean:
		return cmpInt64(x.i, y.i)
	case baseDecimal:
		return x.d.Cmp(y.d)
	default: // baseDate, baseDatetime
		return x.t.Compare(y.t)
	}
}

// --- small helpers ---

func reverse(op cmpOp) cmpOp {
	switch op {
	case cmpLt:
		return cmpGt
	case cmpLe:
		return cmpGe
	case cmpGt:
		return cmpLt
	case cmpGe:
		return cmpLe
	}
	return op // == and != are symmetric
}

func ordered(op cmpOp) bool {
	return op == cmpLt || op == cmpLe || op == cmpGt || op == cmpGe
}

// orderable is E3's set for the four ordering operators: the classes the
// language can put an inequality on.
func orderable(class uint8) bool {
	switch class {
	case baseString, baseInteger, baseDecimal, baseDate, baseDatetime:
		return true
	}
	return false
}
