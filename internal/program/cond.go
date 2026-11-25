package program

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Row holds one cell per slot. Interpretation hands a
// condition either the committed row or the candidate overlay, and nothing
// else, which is what makes the two evaluations share one code path
// (RUNTIME §5.1).
type Row []Cell

// Predicate evaluates one condition against one state view.
type Predicate func(Row) bool

// always and never are the folded forms of a compile-time constant
// condition (ST6): identical semantics, zero evaluation. A never guard
// makes its edge deterministically unsatisfiable.
func always() Predicate { return func(Row) bool { return true } }

func never() Predicate { return func(Row) bool { return false } }

// andAll and orAll are n-ary and flattened at lowering, so no source
// nesting depth survives in the closure tree. The single-child shortcut
// keeps the common "one operand" case free of a loop.
func andAll(cs ...Predicate) Predicate {
	if len(cs) == 1 {
		return cs[0]
	}
	return func(v Row) bool {
		for _, c := range cs {
			if !c(v) {
				return false
			}
		}
		return true
	}
}

func orAll(cs ...Predicate) Predicate {
	if len(cs) == 1 {
		return cs[0]
	}
	return func(v Row) bool {
		for _, c := range cs {
			if c(v) {
				return true
			}
		}
		return false
	}
}

func neg(c Predicate) Predicate { return func(v Row) bool { return !c(v) } }

// allSet is the guard negation needs: it is satisfied when every slot it
// was built over carries a value. Lowering wraps `!(condition over data)`
// in it so that an absent operand cannot be read as satisfaction: the
// inner condition is unsatisfied without data, and negating an unsatisfied
// condition would otherwise make structure appear precisely because
// nothing was entered (RUNTIME §5.2's absence rule).
func allSet(slots []NodeID) Predicate {
	if len(slots) == 1 {
		n := slots[0]
		return func(v Row) bool { return v[n].Set }
	}
	return func(v Row) bool {
		for _, n := range slots {
			if !v[n].Set {
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
func order(op cmpOp, c func(Row) (int, bool)) Predicate {
	switch op {
	case cmpEq:
		return func(v Row) bool { r, ok := c(v); return ok && r == 0 }
	case cmpNe:
		return func(v Row) bool { r, ok := c(v); return ok && r != 0 }
	case cmpLt:
		return func(v Row) bool { r, ok := c(v); return ok && r < 0 }
	case cmpLe:
		return func(v Row) bool { r, ok := c(v); return ok && r <= 0 }
	case cmpGt:
		return func(v Row) bool { r, ok := c(v); return ok && r > 0 }
	default: // cmpGe
		return func(v Row) bool { r, ok := c(v); return ok && r >= 0 }
	}
}

// isTrue is the standalone boolean operand E4 allows: present[.urgent].
// notTrue is its negation, built directly so ! never allocates a wrapper.
func isTrue(n NodeID) Predicate {
	return func(v Row) bool {
		s := v[n]
		return s.Set && s.Int != 0
	}
}

func notTrue(n NodeID) Predicate {
	return func(v Row) bool {
		s := v[n]
		return s.Set && s.Int == 0
	}
}

func strCmp(n NodeID, op cmpOp, want string) Predicate {
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		return strings.Compare(s.Text, want), true
	})
}

func intCmp(n NodeID, op cmpOp, want int64) Predicate {
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		return cmpInt64(s.Int, want), true
	})
}

func decCmp(n NodeID, op cmpOp, want decimal.Decimal) Predicate {
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		return s.Decimal.Cmp(want), true
	})
}

func timeCmp(n NodeID, op cmpOp, want time.Time) Predicate {
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		// Time.Compare orders instants, so two spellings of one moment
		// with different zones compare equal (SYNTAX Appendix A).
		return s.Time.Compare(want), true
	})
}

// tagCmp and boolCmp cover the classes E3 gives ordering to never: only
// == and != can reach them, which the lowering checks (§4's contract).
func tagCmp(n NodeID, op cmpOp, want TagID) Predicate {
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		return cmpInt64(s.Int, int64(want)), true
	})
}

func boolCmp(n NodeID, op cmpOp, want bool) Predicate {
	var w int64
	if want {
		w = 1
	}
	return order(op, func(v Row) (int, bool) {
		s := v[n]
		if !s.Set {
			return 0, false
		}
		return cmpInt64(s.Int, w), true
	})
}

func strSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return strings.Compare(x.Text, y.Text), true
	})
}

func intSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return cmpInt64(x.Int, y.Int), true
	})
}

func decSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return x.Decimal.Cmp(y.Decimal), true
	})
}

func timeSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return x.Time.Compare(y.Time), true
	})
}

func tagSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return cmpInt64(x.Int, y.Int), true
	})
}

func boolSlotCmp(a, b NodeID, op cmpOp) Predicate {
	return order(op, func(v Row) (int, bool) {
		x, y := v[a], v[b]
		if !x.Set || !y.Set {
			return 0, false
		}
		return cmpInt64(x.Int, y.Int), true
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
