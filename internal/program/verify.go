package program

import (
	"math/bits"
	"time"

	"github.com/shopspring/decimal"
)

func (l *lowerer) verify() {
	img := l.img
	n := len(img.slots)

	if img.slots[0].Kind != kindRoot {
		l.reportf("", "slot 0 is not the case root")
	}
	if len(l.nodeAt) != n {
		l.reportf("", "%d declarations for %d slots", len(l.nodeAt), n)
	}
	l.verifySlots(n)
	l.verifyTypes()
	l.verifyConds(n)
	l.verifyPerms(n)
	l.verifySigs()
	l.verifyStages()
	l.verifyDeps(n)
}

func (l *lowerer) verifySlots(n int) {
	img := l.img
	for i, s := range img.slots {
		at := l.where(NodeID(i))
		if i == 0 && s.Kind != kindRoot {
			l.reportf(at, "the case root has kind %d", s.Kind)
		}
		if i != 0 && s.Kind == kindRoot {
			l.reportf(at, "a second case root")
		}
		if s.Parent != 0 && int(s.Parent) >= n {
			l.reportf(at, "parent slot %d is out of range", s.Parent)
		}
		if uint64(s.first)+uint64(s.n) > uint64(len(img.children)) {
			l.reportf(at, "child range [%d,%d) exceeds %d children",
				s.first, s.first+s.n, len(img.children))
		}
		if i != 0 && s.Span == 0 {
			l.reportf(at, "a subtree is empty")
		}
		if uint64(i)+uint64(s.Span) > uint64(n) {
			l.reportf(at, "subtree range exceeds %d slots", n)
		}
		if s.Kind == KindField && s.Type == 0 && s.Role == 0 {
			l.reportf(at, "a field has neither a value type nor a role")
		}
		if s.Kind == KindField && s.Type != 0 && s.Role != 0 {
			l.reportf(at, "a field has both a value type and a role")
		}
		if s.Kind != KindField && (s.Type != 0 || s.Role != 0) {
			l.reportf(at, "a %s carries a value type or a role", kindName(s.Kind))
		}
		if int(s.Role) >= len(img.roleIdx) {
			l.reportf(at, "role ordinal %d is not declared", s.Role)
		}
		if int(s.Type) >= len(img.types) {
			l.reportf(at, "type ordinal %d is not declared", s.Type)
		}
		if s.Flags&FlagRequired != 0 && s.CondReq != 0 {
			l.reportf(at, "required and required[expr] are one attribute")
		}
		if int(s.Def) >= len(img.consts) {
			l.reportf(at, "default constant %d is out of range", s.Def)
		}
		if int(s.Hook) >= len(img.hooks) {
			l.reportf(at, "hook binding %d is out of range", s.Hook)
		}
		if s.Sig != 0 && uint32(len(img.sigs)) <= s.Sig {
			l.reportf(at, "signature entry %d is out of range", s.Sig)
		}
		if s.Verif != 0 {
			if int(s.Verif) >= n {
				l.reportf(at, "verifiable unit %d is out of range", s.Verif)
			} else if img.slots[s.Verif].Flags&FlagVerifiable == 0 {
				l.reportf(at, "governing unit %s is not verifiable",
					img.Path(s.Verif))
			}
		}
		for _, c := range []ConditionID{s.CondPres, s.CondReq} {
			if int(c) >= len(img.conds) {
				l.reportf(at, "condition %d is out of range", c)
				continue
			}
			if c != 0 && img.conds[c] == nil {
				l.reportf(at, "condition %d was not built", c)
			}
		}
		// The collection ordinal is how a state finds a collection's rows by
		// index, so it has to point back and be dense.
		switch {
		case s.Coll != 0 && s.Flags&flagMultiple == 0:
			l.reportf(at, "a non-collection carries a collection ordinal")
		case s.Coll == 0 && s.Flags&flagMultiple != 0:
			l.reportf(at, "a multiple declaration has no collection ordinal")
		case s.Coll != 0 && int(s.Coll) > len(img.colls):
			l.reportf(at, "collection ordinal %d exceeds %d collections",
				s.Coll, len(img.colls))
		case s.Coll != 0 && img.colls[s.Coll-1] != NodeID(i):
			l.reportf(at, "collection ordinal %d does not point back here", s.Coll)
		}
	}
	for k, c := range img.colls {
		// collOf stops at the first ordinal past its slot and keeps the last
		// one that reaches, so preorder is what makes it find the *innermost*
		// enclosing collection.
		if k > 0 && c <= img.colls[k-1] {
			l.reportf(l.where(c), "collection ordinals are not in preorder")
		}
	}
}

// kindName names a node kind for problem text.
func kindName(k uint8) string {
	switch k {
	case kindRoot:
		return "case root"
	case KindForm:
		return "form"
	case KindGroup:
		return "group"
	case KindField:
		return "field"
	}
	return "unknown kind"
}

// where renders a slot for problem text; slot 0 has no path.
func (l *lowerer) where(id NodeID) string {
	if id == 0 {
		return ""
	}
	return l.img.Path(id)
}

func (l *lowerer) verifyTypes() {
	img := l.img
	if len(img.tagNames) != len(img.tagType) {
		l.reportf("", "%d tag names for %d tag types",
			len(img.tagNames), len(img.tagType))
	}
	for id, t := range img.types {
		if id == 0 {
			continue // the no-type placeholder
		}
		at := img.Name(t.name)
		if t.Base == 0 {
			l.reportf(at, "type has no class")
		}
		if t.Base == BaseDecimal && t.Fits == nil {
			l.reportf(at, "decimal type has no bounds test")
		}
		if t.Base == BaseEnum && len(t.Tags) == 0 {
			l.reportf(at, "enum type has no tags")
		}
		for _, tag := range t.Tags {
			if int(tag) >= len(img.tagNames) {
				l.reportf(at, "tag %d is out of range", tag)
			} else if img.tagType[tag] != TypeID(id) {
				l.reportf(at, "tag %s belongs to another type", img.TagName(tag))
			}
		}
	}
	for tag := 1; tag < len(img.tagNames); tag++ {
		if img.tagType[tag] == 0 {
			l.reportf(img.Name(img.tagNames[tag]), "tag belongs to no type")
		}
	}
}

func (l *lowerer) verifyConds(n int) {
	img := l.img
	if len(img.conds) == 0 || img.conds[0] == nil {
		l.reportf("", "the unconditional placeholder is missing")
	}
	for id, reads := range l.reads {
		if int(id) >= len(img.conds) {
			l.reportf("", "condition %d has reads but was not built", id)
			continue
		}
		for _, s := range reads {
			if int(s) >= len(img.slots) {
				l.reportf("", "condition %d reads slot %d, not a declaration", id, s)
			} else if l.collection[s] {
				l.reportf(img.Path(s), "a condition reads inside a collection")
			}
		}
		if len(reads) != 0 {
			continue
		}
		// The containment claim, asserted: a guard that registered no reads
		// must answer without data. This catches a mis-wired constructor that
		// captured a slot nobody declared; it cannot prove the converse for a
		// closure that happens to answer alike either way, and the construction
		// argument is what does that.
		c := img.conds[id]
		empty := c(make(Row, n))
		if c(probeView(n)) != empty {
			l.reportf("", "condition %d reads data it did not declare", id)
		}
	}
}

// probeView is verify's "every value present" frame: one row per slot with
// a plausible payload in each field, so any class's comparator reads
// something. Booleans and tags live in i, so a single row shape covers
// them.
func probeView(n int) Row {
	v := make(Row, n)
	full := Cell{Set: true, Int: 1, Text: "probe", Decimal: decimal.NewFromInt(1),
		Time: time.Unix(1_700_000_000, 0).UTC(), Binary: BinaryRef{0xff}}
	for i := range v {
		v[i] = full
	}
	return v
}

func (l *lowerer) verifyPerms(n int) {
	img := l.img
	if len(img.perms.byRole) != len(l.s.Roles)+1 {
		l.reportf("", "%d permission tables for %d roles",
			len(img.perms.byRole), len(l.s.Roles))
	}
	for r, t := range img.perms.byRole {
		if r == 0 {
			continue // the no-role table is empty by construction
		}
		at := img.RoleName(RoleID(r))
		if len(t.allow) != n || len(t.deny) != n {
			l.reportf(at, "permission arrays are %d and %d long, want %d",
				len(t.allow), len(t.deny), n)
		}
		for i := 1; i < len(t.over); i++ {
			if t.over[i-1].at >= t.over[i].at {
				l.reportf(at, "stage overlays are not sorted")
			}
		}
		for _, o := range t.over {
			if int(o.at) >= len(img.stages) {
				l.reportf(at, "overlay stage %d is not declared", o.at)
			}
			if len(o.slots) != len(o.add) || len(o.slots) != len(o.del) {
				l.reportf(at, "overlay arrays disagree on length")
				continue
			}
			for i, s := range o.slots {
				if int(s) >= n {
					l.reportf(at, "overlay slot %d is out of range", s)
				}
				if i > 0 && o.slots[i-1] >= s {
					l.reportf(at, "overlay slots are not sorted and unique")
				}
				// add and del overlapping is not drift: a definition may grant
				// and deny the same permission to the same role in the same
				// stage, and the front end only warns about it.
			}
		}
	}
}

func (l *lowerer) verifySigs() {
	img := l.img
	for i, e := range img.sigs {
		if i == 0 {
			continue // the not-a-signature placeholder
		}
		if (e.Role == 0) == (e.Assigned == 0) {
			l.reportf("", "signature entry %d resolves to %s", i, sigKind(e))
		}
		if int(e.Role) >= len(img.roleIdx) {
			l.reportf("", "signature entry %d names an undeclared role", i)
		}
		if e.Assigned != 0 {
			if int(e.Assigned) >= len(img.slots) {
				l.reportf("", "signature entry %d targets slot %d", i, e.Assigned)
			} else if img.slots[e.Assigned].Role == 0 {
				l.reportf(img.Path(e.Assigned),
					"signature target is not a role-typed field")
			}
		}
	}
}

// sigKind renders the half of the contract that an entry got wrong. Its
// caller is an assertion: every way to build such an entry reports a problem
// during the build, so verify does not run on that image at all and this
// branch is unreachable from a definition — it is here because the table
// says so, and it costs nothing but a sentence.
// sigKind names the shape of a signature entry for a defect message. The
// caller reaches it only when the two targets agree on being empty, so the
// last line is unreachable by construction: it is here because the function
// has to answer for every entry, not because a third shape is expected.
func sigKind(e Signature) string {
	switch {
	case e.Role == 0 && e.Assigned == 0:
		return "neither a role nor a field"
	case e.Role != 0 && e.Assigned != 0:
		return "both a role and a field"
	}
	return "exactly one target"
}

func (l *lowerer) verifyStages() {
	img := l.img
	if len(img.stages) != len(l.s.Stages)+1 {
		l.reportf("", "%d stage rows for %d stages",
			len(img.stages)-1, len(l.s.Stages))
	}
	if int(img.initial) >= len(img.stages) {
		l.reportf("", "initial stage %d is not declared", img.initial)
	}
	for i, st := range img.stages {
		if i == 0 {
			continue // the no-stage placeholder
		}
		at := img.Name(st.Name)
		seen := map[StageID]bool{}
		for _, e := range st.edges {
			if int(e.dest) >= len(img.stages) {
				l.reportf(at, "edge destination %d is out of range", e.dest)
			}
			if e.dest == StageID(i) {
				l.reportf(at, "stage transitions to itself")
			}
			if seen[e.dest] {
				l.reportf(at, "two edges lead to stage %d", e.dest)
			}
			seen[e.dest] = true
			if int(e.Guard) >= len(img.conds) {
				l.reportf(at, "edge guard %d is out of range", e.Guard)
			}
		}
		// Only top-level forms have requirement bits. Reject any bit beyond the
		// form table.
		if n := bits.Len64(st.Requires); n > len(img.forms)-1 {
			l.reportf(at, "require bitset names form %d, not declared", n)
		}
	}
}

func (l *lowerer) verifyDeps(n int) {
	img := l.img
	if len(img.depFrom) != n {
		l.reportf("", "%d dependency rows for %d slots", len(img.depFrom), n)
	}
	for s, r := range img.depFrom {
		if uint64(r.First)+uint64(r.N) > uint64(len(img.depOwn)) {
			l.reportf(l.where(NodeID(s)), "dependency range [%d,%d) exceeds %d owners",
				r.First, r.First+r.N, len(img.depOwn))
			continue
		}
		for i := r.First; i < r.First+r.N; i++ {
			owner := img.depOwn[i]
			if int(owner) >= n {
				l.reportf(l.where(NodeID(s)), "dependency owner %d is out of range",
					owner)
			}
			if i > r.First && owner <= img.depOwn[i-1] {
				l.reportf(l.where(NodeID(s)), "dependency owners are not sorted")
			}
		}
	}
}
