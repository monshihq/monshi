package monshi

// The runtime image (RUNTIME §3): a definition lowered into index-addressable
// arrays plus one compiled condition per guard. Nothing here is exported;
// the shapes are the contract docs/RUNTIME.md describes.
//
// Ordinals follow one rule: 0 is always the absence of a thing, so real
// roles, stages, forms, types and tags are numbered from 1. Slots are the
// exception — slot 0 is the case root, which is a real node: it is where
// the "all" grant target lives in the Case → Form → Group → Field chain
// (DESIGN §4.3).

import (
	"time"

	"github.com/shopspring/decimal"
)

type (
	nodeID   uint32 // dense slot index; 0 is the case root
	stageID  uint8  // declared stage ordinal (ST1 order)
	formID   uint8  // top-level form ordinal, for require bitsets
	roleID   uint8  // declared role ordinal
	typeID   uint16 // built-in or custom type
	tagID    uint16 // enum tag ordinal, contiguous per type
	valueID  uint32 // index into image.consts; 0 means no constant
	condID   uint32 // index into image.conds; 0 means unconditional
	hookID   uint16 // hook binding key; 0 means not bound
	stringID uint32 // index into the interned name table
	roleMask uint64 // set of roleID bits, so at most 64 declared roles
)

// A stageID of 0 means no stage at all: a case that has not been placed
// in the workflow yet, so no stage-scoped overlay applies to it.
const noStage stageID = 0

// Node kinds, in the order SYNTAX §5 documents them.
const (
	kindRoot uint8 = iota
	kindForm
	kindGroup
	kindField
)

// Type bases, mirroring SYNTAX Appendix A. A custom type carries the base
// of the primitive it derives from (T1), so these are the only classes the
// runtime can dispatch on — and it dispatches on them at lowering only.
const (
	baseString uint8 = iota + 1
	baseInteger
	baseDecimal
	baseBoolean
	baseDate
	baseDatetime
	baseBinary
	baseEnum
	baseOptions
	baseSignature
)

// Targeted permission bits (RUNTIME §3.5). accept and reject have no bit
// here: P1 confines them to stage blocks, where they are role sets.
const (
	permView uint8 = 1 << iota
	permEdit
	permSubmit
	permReview
	permSign
)

// Declaration flags. Required and condReq are mutually exclusive, as
// SYNTAX §4 makes "required" and "required[expr]" two variants of one
// attribute.
const (
	flagRequired uint16 = 1 << iota
	flagMultiple
	flagVerifiable
)

// cmpOp is the comparison operator of a leaf test. One integer switch per
// evaluation replaces the front end's string Op tags (RUNTIME §2.2).
type cmpOp uint8

const (
	cmpEq cmpOp = iota
	cmpNe
	cmpLt
	cmpLe
	cmpGt
	cmpGe
)

// holds reports the comparison result for a three-way ordering. Absent
// operands never reach it: cmpBy short-circuits first (RUNTIME §5.2).
func (o cmpOp) holds(r int) bool {
	switch o {
	case cmpEq:
		return r == 0
	case cmpNe:
		return r != 0
	case cmpLt:
		return r < 0
	case cmpLe:
		return r <= 0
	case cmpGt:
		return r > 0
	default: // cmpGe
		return r >= 0
	}
}

// Hook binding kinds (DESIGN §7: Validate is looked up by custom type,
// Options by field path).
const (
	hookByType uint8 = iota + 1
	hookByPath
)

// BinaryRef names retained binary content held outside the case record
// (DESIGN §5.2). The store that resolves it arrives with the storage
// milestone; the value row reserves the field now so rows never change
// shape later.
type BinaryRef []byte

// slotValue is one value in one frame row. A row carries every payload
// field but reads exactly one of them: the class is fixed per slot by
// typeInfo, so a getter selects the field at lowering time — no tag test,
// no interface boxing on the hot path (RUNTIME §5.1). Booleans live in i
// as 0 or 1; an enum value is its tagID.
type slotValue struct {
	set bool
	i   int64
	d   decimal.Decimal
	s   string
	t   time.Time
	bin BinaryRef
}

// nodeSlot is one declaration: a form, group or field (RUNTIME §3.2).
// Children are a dense range in the image's child array; the subtree is
// the dense preorder range [id, id+span).
type nodeSlot struct {
	kind     uint8
	name     stringID // for rendering, never for logic
	path     stringID // absolute path, for View and affected reporting
	parent   nodeID
	first    uint32 // [first, first+n) into image.children
	n        uint32 // direct children, declaration order
	span     uint32 // preorder subtree size, at least 1
	typ      typeID // 0 on role-typed fields, which carry role instead
	role     roleID // role-typed field: the role it confers (T4)
	flags    uint16
	condPres condID  // present[cond]; 0 = unconditional
	condReq  condID  // required[cond]; 0 = plain or not required
	def      valueID // default in image.consts (F5); 0 = none
	hook     hookID  // Options binding for an options field (§6); 0 = none
	verif    nodeID  // governing verifiable unit (F3); 0 = none
	sig      uint32  // index into image.sigs; 0 = not a signature field
}

type typeInfo struct {
	base uint8   // one of the base* constants (T1: exactly one)
	tags []tagID // enum only: contiguous ordinals, declaration order
	// fits reports whether a decimal value is legal for decimal(p, s),
	// prebuilt once per type from the bounds T2 already validated.
	fits func(decimal.Decimal) bool
	hook hookID // custom types only: the Validate lookup key (§6)
	name stringID
}

type sigEntry struct {
	role     roleID // signature(Role): authorized = that role's holders
	assigned nodeID // signature(field): authorized = committed value
}

// permTable is one role's permission algebra: bits folded down the scope
// chain, plus the sparse stage overlays that alter them (RUNTIME §3.5).
type permTable struct {
	allow []uint8        // per slot: bits held on that slot or an ancestor
	deny  []uint8        // per slot: bits denied on that slot or an ancestor
	over  []stageOverlay // stage-scoped targeted grants, sorted by stage
}

// permTables holds every declared role's table, indexed by roleID. Index 0
// belongs to no role and grants nothing, so ordinals and indexes agree.
type permTables struct {
	byRole []permTable
}

// stageOverlay carries the targeted grants declared in one stage block for
// one role: they hold only while the case sits in that stage, so they are
// kept out of the unconditional arrays (RUNTIME §3.5).
type stageOverlay struct {
	at    stageID
	slots []nodeID // every slot the grants reach, sorted
	add   []uint8  // parallel to slots
	del   []uint8  // parallel to slots
}

type edge struct {
	dest   stageID
	guard  condID // 0 = unconditional edge
	reject bool   // reject edges skip destination require (DESIGN §4.4)
}

type stageInfo struct {
	name     stringID
	requires uint64   // bitset over formID ordinals (ST4)
	accept   roleMask // roles granted accept out of this stage
	reject   roleMask // roles granted reject out of it
	acceptNo roleMask // roles denied accept (P6 fold at request time)
	rejectNo roleMask // roles denied reject
	edges    []edge   // dest unique (ST3)
}

// mayAccept and mayReject apply P6 to a transition mask pair: an actor
// holding any role in the allow mask may take the transition, unless any
// role they hold is denied it — a deny from one held role strips what
// another grants, the same precedence §3.5 gives targeted bits
// (DESIGN §4.3).
func (s *stageInfo) mayAccept(roles []roleID) bool { return masksAllow(s.accept, s.acceptNo, roles) }

func (s *stageInfo) mayReject(roles []roleID) bool { return masksAllow(s.reject, s.rejectNo, roles) }

func masksAllow(allow, deny roleMask, roles []roleID) bool {
	var held roleMask
	for _, r := range roles {
		if r == 0 {
			continue // ordinal 0 is "no role": it confers nothing
		}
		held |= 1 << (r - 1)
	}
	return held&allow != 0 && held&deny == 0
}

// edgeTo finds the declared transition to one destination. ST3 makes the
// answer unique, so this is a direct scan of a handful of edges, and the
// caller never has to choose between parallel edges (§5.5).
func (s *stageInfo) edgeTo(dest stageID) (edge, bool) {
	for _, e := range s.edges {
		if e.dest == dest {
			return e, true
		}
	}
	return edge{}, false
}

type depRange struct{ first, n uint32 }

// hookKey is the lookup key the engine uses to find a host hook for a
// binding (DESIGN §7). The image records that a binding exists and under
// what key; resolving keys to host code is not a definition concern.
type hookKey struct {
	kind uint8 // hookByType or hookByPath
	name stringID
}

// image is one lowered definition: immutable after construction, shared by
// every goroutine and actor without locks (RUNTIME §7).
type image struct {
	id       string
	slots    []nodeSlot
	children []nodeID
	names    []string // interned definition names, never case data
	types    []typeInfo
	consts   []slotValue // defaults and guard literals (§3.3)
	conds    []condFn    // conds[0] is never called: condID 0 = unconditional
	perms    permTables  // one folded table per declared roleID (§3.5)
	sigs     []sigEntry  // indexed by nodeSlot.sig; index 0 unused
	stages   []stageInfo // indexed by stageID; index 0 unused
	forms    []nodeID    // indexed by formID; index 0 unused
	hooks    []hookKey   // indexed by hookID; index 0 unused
	tagNames []stringID  // indexed by tagID; index 0 unused
	tagType  []typeID    // indexed by tagID; index 0 unused
	roleIdx  []stringID  // indexed by roleID; index 0 unused
	depFrom  []depRange  // per slot: adjacency bounds into depOwn
	depOwn   []nodeID    // condition-bearing owners that read that slot
	depBound uint32      // closure queue bound: no slot is visited twice (§5.3)
	initial  stageID     // first declared stage (ST1)
}

// --- accessors used by interpretation ---

func (img *image) slot(n nodeID) nodeSlot { return img.slots[n] }

func (img *image) name(s stringID) string { return img.names[s] }

func (img *image) tagName(t tagID) string { return img.name(img.tagNames[t]) }

func (img *image) roleName(r roleID) string { return img.name(img.roleIdx[r]) }

func (img *image) path(n nodeID) string { return img.name(img.slots[n].path) }

func (img *image) constOf(v valueID) slotValue { return img.consts[v] }

func (img *image) cond(c condID) condFn { return img.conds[c] }

// childrenOf returns the declaration-order direct children of a slot. The
// slice aliases the image, so callers must not retain or write it.
func (img *image) childrenOf(n nodeID) []nodeID {
	s := img.slots[n]
	return img.children[s.first : s.first+s.n]
}

// subtree is the dense preorder range of slots under n, n included: slot
// numbering is preorder, so a subtree is contiguous in image.slots and a
// View or Submit walk is an index loop with no pointer chasing (RUNTIME
// §5.4). Callers iterate lo inclusive, hi exclusive.
func (img *image) subtree(n nodeID) (lo, hi nodeID) {
	return n, n + nodeID(img.slots[n].span)
}

// permAt resolves the effective allow/deny bits one role holds for one slot
// in one stage: unconditional bits, altered by that stage's overlay if there
// is one (RUNTIME §3.5). Pure bitwise algebra, no tree walking.
func (p *permTables) permAt(r roleID, stage stageID, s nodeID) (allow, deny uint8) {
	t := &p.byRole[r]
	allow, deny = t.allow[s], t.deny[s]
	i := overlayIndex(t.over, stage)
	if i < 0 {
		return allow, deny
	}
	o := t.over[i]
	j := upperBound(o.slots, s)
	if j < len(o.slots) && o.slots[j] == s {
		allow |= o.add[j]
		deny |= o.del[j]
	}
	return allow, deny
}

// overlayIndex binary-searches the sorted per-role stage list; in practice
// it holds one or two entries.
func overlayIndex(over []stageOverlay, stage stageID) int {
	lo, hi := 0, len(over)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case over[mid].at < stage:
			lo = mid + 1
		case over[mid].at > stage:
			hi = mid - 1
		default:
			return mid
		}
	}
	return -1
}

// upperBound returns the first index at which slots[i] >= want.
func upperBound(slots []nodeID, want nodeID) int {
	lo, hi := 0, len(slots)
	for lo < hi {
		mid := (lo + hi) / 2
		if slots[mid] < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// effective combines the bits of every role an actor holds: the union of
// allows, stripped by the union of denies, whoever granted what (P6,
// DESIGN §4.3). roles is the actor's committed role set.
func (img *image) effective(roles []roleID, stage stageID, s nodeID) uint8 {
	var allow, deny uint8
	for _, r := range roles {
		a, d := img.perms.permAt(r, stage, s)
		allow |= a
		deny |= d
	}
	return allow &^ deny
}
