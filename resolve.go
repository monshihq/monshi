package monshi

// The resolution seam (RUNTIME §3.9): the one place a case reads caller text,
// and the last place a string means anything. Resolution hands back ordinals
// and a row address; every operation after it walks slots, bits and closures,
// so no code path depends on which letters a host typed. That asymmetry is the
// containment argument — a name can select data, never behavior.

import (
	"strconv"
	"strings"
)

// resolved is what a path names. slot is always the declaration; cell is the
// value it holds in the state the path was read from, and hasValue is false
// when the declaration has no single value there — a form or group (structure,
// not data), a collection (which has rows), or a member of a collection named
// without saying which instance (§5.1).
//
// The instance path lives in an array of the lowering's own depth bound, which
// is what keeps resolution free of allocation: an answer is one value, copied
// or discarded, never a graph to collect.
type resolved struct {
	slot     nodeID
	cell     cell
	depth    uint8
	steps    [maxCollDepth]instStep
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
func (s *state) resolve(path string) (resolved, bool) {
	img := s.img
	cur := nodeID(0) // the case root: every path is absolute (§3.6)
	var out resolved

	for i := 0; ; {
		cut := strings.IndexByte(path[i:], '.')
		seg := path[i:]
		more := cut >= 0
		if more {
			seg = path[i : i+cut]
		}
		if seg == "" {
			return resolved{}, false // an empty segment is nobody's name
		}
		if child, ok := img.child(cur, seg); ok {
			cur = child
		} else if out.depth < maxCollDepth {
			// The step says which row, not which declaration: the members of
			// every instance are the same slots, so cur stays where it is.
			id, ok := s.instanceAt(cur, seg, out.path())
			if !ok {
				return resolved{}, false
			}
			out.steps[out.depth] = instStep{cur, id}
			out.depth++
		} else {
			return resolved{}, false
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

// instanceAt accepts a segment as an instance ID only when cur is a collection
// that has not already been addressed — descending into a nested collection
// takes its name first — and only when the ID reaches a row this state has.
// The depth bound is the lowering ceiling, so it never fires for a definition
// that loaded; it is here to keep the array claim true rather than hopeful.
func (s *state) instanceAt(cur nodeID, seg string, ip instPath) (instanceID, bool) {
	if s.img.slots[cur].coll == 0 {
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
func (s *state) rowsFor(coll nodeID, ip instPath) *instances {
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

// child finds a direct child by name, in declaration order. The scan is bounded
// by declaration breadth, which is a fact about the definition known before any
// request arrives, and member names are unique within a container (N3), so the
// first match is the only one. Names are lowercase-initial and IDs are digits
// (SYNTAX §3.3-N2), so the two can never be confused for each other.
func (img *image) child(cur nodeID, name string) (nodeID, bool) {
	for _, c := range img.childrenOf(cur) {
		if img.name(img.slots[c].name) == name {
			return c, true
		}
	}
	return 0, false
}

// --- the rest of the language's vocabulary, same rule on closed arrays ---
//
// A host also names a form (Submit), a stage (Transition), a role (an actor's
// primary role) and a tag (a value a default or an option set chose). Each scan
// is bounded by what the definition declares, and every one of these ordinals
// exists only for something that was declared (§3.9).

func (img *image) formByName(name string) (formID, bool) {
	for i, f := range img.forms {
		if i == 0 {
			continue // ordinal 0 is "no form"
		}
		if img.name(img.slots[f].name) == name {
			return formID(i), true
		}
	}
	return 0, false
}

func (img *image) stageByName(name string) (stageID, bool) {
	for i := range img.stages {
		if i == 0 {
			continue // ordinal 0 is "no stage"
		}
		if img.name(img.stages[i].name) == name {
			return stageID(i), true
		}
	}
	return 0, false
}

func (img *image) roleByName(name string) (roleID, bool) {
	for i, n := range img.roleIdx {
		if i == 0 {
			continue // ordinal 0 confers nothing
		}
		if img.name(n) == name {
			return roleID(i), true
		}
	}
	return 0, false
}

func (img *image) tagByName(name string) (tagID, bool) {
	for i, n := range img.tagNames {
		if i == 0 {
			continue
		}
		if img.name(n) == name {
			return tagID(i), true
		}
	}
	return 0, false
}
