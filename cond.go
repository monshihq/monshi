package monshi

// Compiled conditions (RUNTIME §3.4). A condition is a tree built from the
// constructors in this file and nothing else: the lowering emits them from
// the closed operand and operator set the grammar admits (E5), which is the
// containment argument of §7 — there is no interpreter, no loop and no call
// reachable from definition text.
//
// Every constructor resolves its class and operator at lowering time, so a
// running condition performs one typed read of the slots it captured and
// one typed comparison. There is no type dispatch, and no error or
// validity channel: E4 and ST6 guarantee the tree is boolean.

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// stateView is one dense value row per slot. Interpretation hands a
// condition either the committed row or the candidate overlay, and nothing
// else, which is what makes the two evaluations share one code path
// (RUNTIME §5.1).
type stateView []slotValue

// condFn evaluates one condition against one state view.
type condFn func(stateView) bool

// always and never are the folded forms of a compile-time constant
// condition (ST6): identical semantics, zero evaluation. A never guard
// makes its edge deterministically unsatisfiable.
func always() condFn { return func(stateView) bool { return true } }

func never() condFn { return func(stateView) bool { return false } }

// andAll and orAll are n-ary and flattened at lowering, so no source
// nesting depth survives in the closure tree. The single-child shortcut
// keeps the common "one operand" case free of a loop.
func andAll(cs ...condFn) condFn {
	if len(cs) == 1 {
		return cs[0]
	}
	return func(v stateView) bool {
		for _, c := range cs {
			if !c(v) {
				return false
			}
		}
		return true
	}
}

func orAll(cs ...condFn) condFn {
	if len(cs) == 1 {
		return cs[0]
	}
	return func(v stateView) bool {
		for _, c := range cs {
			if c(v) {
				return true
			}
		}
		return false
	}
}

func neg(c condFn) condFn { return func(v stateView) bool { return !c(v) } }

// allSet is the guard negation needs: it is satisfied when every slot it
// was built over carries a value. Lowering wraps `!(condition over data)`
// in it so that an absent operand cannot be read as satisfaction: the
// inner condition is unsatisfied without data, and negating an unsatisfied
// condition would otherwise make structure appear precisely because
// nothing was entered (RUNTIME §5.2's absence rule).
func allSet(slots []nodeID) condFn {
	if len(slots) == 1 {
		n := slots[0]
		return func(v stateView) bool { return v[n].set }
	}
	return func(v stateView) bool {
		for _, n := range slots {
			if !v[n].set {
				return false
			}
		}
		return true
	}
}

// order binds an operator into a closure at lowering, so no operator
// dispatch remains at evaluation time beyond the one three-way comparison
// the class needs anyway. An unavailable operand reads (0, false), which is
// how "absent satisfies nothing" enters every class uniformly (see below).
func order(op cmpOp, c func(stateView) (int, bool)) condFn {
	switch op {
	case cmpEq:
		return func(v stateView) bool { r, ok := c(v); return ok && r == 0 }
	case cmpNe:
		return func(v stateView) bool { r, ok := c(v); return ok && r != 0 }
	case cmpLt:
		return func(v stateView) bool { r, ok := c(v); return ok && r < 0 }
	case cmpLe:
		return func(v stateView) bool { r, ok := c(v); return ok && r <= 0 }
	case cmpGt:
		return func(v stateView) bool { r, ok := c(v); return ok && r > 0 }
	default: // cmpGe
		return func(v stateView) bool { r, ok := c(v); return ok && r >= 0 }
	}
}

// An absent operand makes every comparison false, including !=: the
// language has no existence operator (SYNTAX Appendix C), so a value that
// is not there satisfies nothing. Frames materialize defaults before
// evaluation (RUNTIME §5.1), so "absent" means genuinely unset.

// isTrue is the standalone boolean operand E4 allows: present[.urgent].
// notTrue is its negation, built directly so ! never allocates a wrapper.
func isTrue(n nodeID) condFn {
	return func(v stateView) bool {
		s := v[n]
		return s.set && s.i != 0
	}
}

func notTrue(n nodeID) condFn {
	return func(v stateView) bool {
		s := v[n]
		return s.set && s.i == 0
	}
}

// --- literal-operand comparisons ---

func strCmp(n nodeID, op cmpOp, want string) condFn {
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		return strings.Compare(s.s, want), true
	})
}

func intCmp(n nodeID, op cmpOp, want int64) condFn {
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		return cmpInt64(s.i, want), true
	})
}

func decCmp(n nodeID, op cmpOp, want decimal.Decimal) condFn {
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		return s.d.Cmp(want), true
	})
}

func timeCmp(n nodeID, op cmpOp, want time.Time) condFn {
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		// Time.Compare orders instants, so two spellings of one moment
		// with different zones compare equal (SYNTAX Appendix A).
		return s.t.Compare(want), true
	})
}

// tagCmp and boolCmp cover the classes E3 gives ordering to never: only
// == and != can reach them, which the lowering checks (§4's contract).
func tagCmp(n nodeID, op cmpOp, want tagID) condFn {
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		return cmpInt64(s.i, int64(want)), true
	})
}

func boolCmp(n nodeID, op cmpOp, want bool) condFn {
	var w int64
	if want {
		w = 1
	}
	return order(op, func(v stateView) (int, bool) {
		s := v[n]
		if !s.set {
			return 0, false
		}
		return cmpInt64(s.i, w), true
	})
}

// --- slot-versus-slot comparisons (E3: identical types) ---

func strSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return strings.Compare(x.s, y.s), true
	})
}

func intSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return cmpInt64(x.i, y.i), true
	})
}

func decSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return x.d.Cmp(y.d), true
	})
}

func timeSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return x.t.Compare(y.t), true
	})
}

func tagSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return cmpInt64(x.i, y.i), true
	})
}

func boolSlotCmp(a, b nodeID, op cmpOp) condFn {
	return order(op, func(v stateView) (int, bool) {
		x, y := v[a], v[b]
		if !x.set || !y.set {
			return 0, false
		}
		return cmpInt64(x.i, y.i), true
	})
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
