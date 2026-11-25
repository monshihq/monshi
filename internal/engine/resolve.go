package engine

import (
	"strconv"
	"strings"

	"github.com/monshihq/monshi/internal/program"
)

// resolved is what a path names. slot is always the declaration — and on a
// failed walk, the last declaration the path did reach, which is the node a
// refusal about it is asked of; cell is the value it holds in the state the
// path was read from, and hasValue is false when the declaration has no single
// value there — a form or group (structure, not data), a collection (which has
// rows), or a member of a collection named without saying which instance
// (§5.1).
//
// The instance path lives in an array of the lowering's own depth bound, which
// is what keeps resolution free of allocation: an answer is one value, copied
// or discarded, never a graph to collect.
type resolved struct {
	slot     program.NodeID
	cell     cell
	depth    uint8
	steps    [program.MaxCollDepth]instStep
	hasValue bool
}

// path is the list of instances the caller's text traversed, empty for a path
// that never entered a collection. It borrows the answer's own array, so keep
// the resolved value as long as the path is in use.
func (r *resolved) path() instPath { return r.steps[:r.depth] }

// resolve walks one caller path through the image's own tree, which *is* the
// index (§3.9): names are compared against the current container's children in
// declaration order, and a numeric segment inside a collection is a stable
// instance ID that must address a row that exists. Nothing is copied —
// path[i:j] on a Go string is a header — and the instance path is backed by a
// local array, so an ordinary path allocates nothing.
//
// An unmatched or mistyped path answers false. There is no near miss and no
// partial answer to interpret: that is DESIGN §9's unknown-path error, decided
// here rather than deep inside an operation.
//
// A false answer still reports how far the walk got in `slot`, because the
// view (§6.1) has to decide which of two refusals a failed path deserves and
// the two differ by whether case data was consulted. A segment that is not a
// name fails against the definition alone, and a numeric segment that is not a
// row fails against somebody's draft — so the refusal is only safe to spell
// when the caller may see the node the walk stopped at. Every other caller
// checks the flag first and ignores the value, as it always has.
func (s *state) resolve(path string) (resolved, bool) {
	img := s.img
	cur := program.NodeID(0) // the case root: every path is absolute (§3.6)
	var out resolved

	for i := 0; ; {
		cut := strings.IndexByte(path[i:], '.')
		seg := path[i:]
		more := cut >= 0
		if more {
			seg = path[i : i+cut]
		}
		if seg == "" {
			return out.stoppedAt(cur), false // an empty segment is nobody's name
		}
		if child, ok := img.Child(cur, seg); ok {
			cur = child
		} else if out.depth < program.MaxCollDepth {
			// The step says which row, not which declaration: the members of
			// every instance are the same slots, so cur stays where it is.
			id, ok := s.instanceAt(cur, seg, out.path())
			if !ok {
				return out.stoppedAt(cur), false
			}
			out.steps[out.depth] = instStep{cur, id}
			out.depth++
		} else {
			return out.stoppedAt(cur), false
		}
		if !more {
			break
		}
		i += cut + 1
	}

	out.slot = cur
	if out.depth == 0 {
		out.cell, out.hasValue = s.cellOf(cur)
	} else {
		out.cell, out.hasValue = s.cellAt(cur, out.path())
	}
	return out, true
}

// stoppedAt is the failure answer: the walk went no further than n, which is
// the node a permission test about this path has to be asked of.
func (r resolved) stoppedAt(n program.NodeID) resolved {
	r.slot = n
	return r
}

// instanceAt accepts a segment as an instance ID only when cur is a collection
// that has not already been addressed — descending into a nested collection
// takes its name first — and only when the ID reaches a row this state has.
// The depth bound is the lowering ceiling, so it never fires for a definition
// that loaded; it is here to keep the array claim true rather than hopeful.
func (s *state) instanceAt(cur program.NodeID, seg string, ip instPath) (instanceID, bool) {
	if s.img.Slot(cur).Coll == 0 {
		return 0, false
	}
	if n := len(ip); n > 0 && ip[n-1].coll == cur {
		return 0, false // "tags.1.2" addresses one collection twice
	}
	id, ok := parseInstanceID(seg)
	if !ok {
		return 0, false
	}
	if _, ok := s.rowsFor(cur, ip).find(id); !ok {
		return 0, false // a stable ID that is not data
	}
	return id, true
}

// rowsFor returns the row set a collection's instances live in, given the
// instances already addressed: nil means there are none, which is a valid
// answer rather than a bad address.
func (s *state) rowsFor(coll program.NodeID, ip instPath) *instances {
	if len(ip) == 0 {
		return s.rows(coll)
	}
	row, ok := s.rowAtPath(ip)
	if !ok {
		return nil
	}
	set, _ := row.sub(coll)
	return set
}

// parseInstanceID reads a stable instance ID: decimal, and canonical. A
// leading zero is refused rather than silently re-addressed, and 0 was never
// issued, because the allocator starts at 1 (§5.1).
func parseInstanceID(seg string) (instanceID, bool) {
	if seg == "" || (len(seg) > 1 && seg[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(seg, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return instanceID(v), true
}
