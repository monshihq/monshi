package program

import (
	"time"

	"github.com/shopspring/decimal"
)

type (
	NodeID      uint32 // dense slot index; 0 is the case root
	StageID     uint8  // declared stage ordinal
	FormID      uint8  // top-level form ordinal, for require bitsets
	RoleID      uint8  // declared role ordinal
	TypeID      uint16 // built-in or custom type
	TagID       uint16 // enum tag ordinal, contiguous per type
	ValueID     uint32 // index into image.consts; 0 means no constant
	ConditionID uint32 // index into image.conds; 0 means unconditional
	HookID      uint16 // hook binding key; 0 means not bound
	StringID    uint32 // index into the interned name table
	roleMask    uint64 // set of roleID bits, so at most 64 declared roles
)

// A StageID of 0 means no stage at all: a case that has not been placed
// in the workflow yet, so no stage-scoped overlay applies to it.
const noStage StageID = 0

// Node kinds stored in the compiled slot table.
const (
	kindRoot uint8 = iota
	KindForm
	KindGroup
	KindField
)

// Type bases, mirroring the built-in type set. A custom type carries the base
// of the primitive it derives from, so these are the only classes the
// runtime can dispatch on — and it dispatches on them at lowering only.
const (
	BaseString uint8 = iota + 1
	BaseInteger
	BaseDecimal
	BaseBoolean
	BaseDate
	BaseDatetime
	BaseBinary
	BaseEnum
	BaseOptions
	BaseSignature
)

// Targeted permission bits. accept and reject have no bit here.
const (
	PermView uint8 = 1 << iota
	PermEdit
	permSubmit
	permReview
	permSign
)

// Declaration flags. Required and condReq are mutually exclusive.
const (
	FlagRequired uint16 = 1 << iota
	flagMultiple
	FlagVerifiable
)

// cmpOp is the comparison operator of a leaf test. One integer switch per
// evaluation replaces the front end's string Op tags.
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
// operands never reach it: cmpBy short-circuits first.
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

// Hook binding kinds (Validate is looked up by custom type,
// Options by field path).
const (
	HookByType uint8 = iota + 1
	HookByPath
)

// BinaryRef names retained binary content held outside the case record. The
// store is the exported BinaryStore in storage.go — offered for hosts to
// implement and compose, with the engine keeping every reference opaque: it
// stores the bytes and resolves nothing through them. The value row reserved
// the field so rows never change shape.
type BinaryRef []byte

// Cell is one value in one frame row. A row carries every payload
// field but reads exactly one of them: the class is fixed per slot by
// Type, so a getter selects the field at lowering time — no tag test,
// no interface boxing on the hot path. Booleans live in i
// as 0 or 1; an enum value is its TagID; an options value is the chosen
// text, since its choice set is hook data the row cannot hold.
type Cell struct {
	Set     bool
	Int     int64
	Decimal decimal.Decimal
	Text    string
	Time    time.Time
	Binary  BinaryRef
}

// Slot is one declaration: a form, group or field.
// Children are a dense range in the image's child array; the subtree is
// the dense preorder range [id, id+span).
type Slot struct {
	Kind     uint8
	Name     StringID // for rendering, never for logic
	Path     StringID // absolute path, for View and affected reporting
	Parent   NodeID
	first    uint32 // [first, first+n) into image.children
	n        uint32 // direct children, declaration order
	Span     uint32 // preorder subtree size, at least 1
	Coll     uint32 // 1-based ordinal into image.colls; 0 = not a collection
	Type     TypeID // 0 on role-typed fields, which carry role instead
	Role     RoleID // role-typed field: the role it confers
	Form     FormID // owning top-level form, 1-based; 0 = the case root
	Flags    uint16
	CondPres ConditionID // present[cond]; 0 = unconditional
	CondReq  ConditionID // required[cond]; 0 = plain or not required
	Def      ValueID     // default in image.consts; 0 = none
	Hook     HookID      // Options binding for an options field; 0 = none
	Verif    NodeID      // governing verifiable unit; 0 = none
	Sig      uint32      // index into image.sigs; 0 = not a signature field
}

type Type struct {
	Base uint8   // one of the base* constants (exactly one)
	Tags []TagID // enum only: contiguous ordinals, declaration order
	// fits checks a decimal against this type's compiled precision and scale.
	Fits func(decimal.Decimal) bool
	Hook HookID // custom types only: the Validate lookup key
	name StringID
}

type Signature struct {
	Role     RoleID // signature(Role): authorized = that role's holders
	Assigned NodeID // signature(field): authorized = committed value
}

// permTable is one role's permission algebra: bits folded down the scope
// chain, plus the sparse stage overlays that alter them.
type permTable struct {
	allow []uint8        // per slot: bits held on that slot or an ancestor
	deny  []uint8        // per slot: bits denied on that slot or an ancestor
	over  []stageOverlay // stage-scoped targeted grants, sorted by stage
}

// permTables holds every declared role's table, indexed by RoleID. Index 0
// belongs to no role and grants nothing, so ordinals and indexes agree.
type permTables struct {
	byRole []permTable
}

// stageOverlay carries the targeted grants declared in one stage block for
// one role: they hold only while the case sits in that stage, so they are
// kept out of the unconditional arrays.
type stageOverlay struct {
	at    StageID
	slots []NodeID // every slot the grants reach, sorted
	add   []uint8  // parallel to slots
	del   []uint8  // parallel to slots
}

type Edge struct {
	dest   StageID
	Guard  ConditionID // 0 = unconditional edge
	Reject bool        // reject edges skip destination require
}

type Stage struct {
	Name     StringID
	Requires uint64   // bitset over formID ordinals
	accept   roleMask // roles granted accept out of this stage
	reject   roleMask // roles granted reject out of it
	acceptNo roleMask // roles denied accept
	rejectNo roleMask // roles denied reject
	edges    []Edge   // dest unique
}

// A transition needs an allow from at least one held role. A deny from any held
// role overrides it.
func (s *Stage) MayAccept(roles []RoleID) bool { return masksAllow(s.accept, s.acceptNo, roles) }

func (s *Stage) MayReject(roles []RoleID) bool { return masksAllow(s.reject, s.rejectNo, roles) }

func masksAllow(allow, deny roleMask, roles []RoleID) bool {
	var held roleMask
	for _, r := range roles {
		if r == 0 {
			continue // ordinal 0 is "no role": it confers nothing
		}
		held |= 1 << (r - 1)
	}
	return held&allow != 0 && held&deny == 0
}

// edgeTo finds the declared transition to one destination.
func (s *Stage) EdgeTo(dest StageID) (Edge, bool) {
	for _, e := range s.edges {
		if e.dest == dest {
			return e, true
		}
	}
	return Edge{}, false
}

// requires is the list of top-level forms one stage's `require` block names,
// as slots, in declaration order.
//
// Bits index top-level form ordinals. Walk them in declaration order so the
// caller's list of missing fields is stable across forms.
func (img *Program) Requires(st StageID) []NodeID {
	mask := img.stages[st].Requires
	var out []NodeID
	for bit := 0; mask != 0; bit, mask = bit+1, mask>>1 {
		if mask&1 != 0 {
			out = append(out, img.forms[bit+1]) // ordinal 0 is "no form"
		}
	}
	return out
}

type depRange struct{ First, N uint32 }

// hookKey is the lookup key the engine uses to find a host hook for a
// binding. The image records that a binding exists and under
// what key; resolving keys to host code is not a definition concern.
type hookKey struct {
	kind uint8 // hookByType or hookByPath
	name StringID
}

// Program is one lowered definition: immutable after construction, shared by
// every goroutine and actor without locks.
type Program struct {
	layout   [32]byte
	id       string
	slots    []Slot
	children []NodeID
	names    []string // interned definition names, never case data
	types    []Type
	consts   []Cell      // defaults and guard literals
	conds    []Predicate // conds[0] is never called: condID 0 = unconditional
	perms    permTables  // one folded table per declared roleID
	sigs     []Signature // indexed by nodeSlot.sig; index 0 unused
	stages   []Stage     // indexed by stageID; index 0 unused
	forms    []NodeID    // indexed by formID; index 0 unused
	colls    []NodeID    // multiple collections, in preorder; slot.coll is 1-based here
	hooks    []hookKey   // indexed by hookID; index 0 unused
	tagNames []StringID  // indexed by tagID; index 0 unused
	tagType  []TypeID    // indexed by tagID; index 0 unused
	roleIdx  []StringID  // indexed by roleID; index 0 unused
	depFrom  []depRange  // per slot: adjacency bounds into depOwn
	depOwn   []NodeID    // condition-bearing owners that read that slot
	sigSpan  []depRange  // per formID: bounds into sigSlots
	sigSlots []NodeID    // signature fields, grouped by owning form
	depBound uint32      // closure queue bound: no slot is visited twice
	initial  StageID     // first declared stage
}

func (img *Program) Slot(n NodeID) Slot { return img.slots[n] }

func (img *Program) Name(s StringID) string { return img.names[s] }

func (img *Program) TagName(t TagID) string { return img.Name(img.tagNames[t]) }

func (img *Program) RoleName(r RoleID) string { return img.Name(img.roleIdx[r]) }

// stageName renders a stage's declared name. Index 0 is the "no stage"
// answer and renders empty, the same way ordinal 0 means "none" elsewhere.
func (img *Program) StageName(s StageID) string { return img.Name(img.stages[s].Name) }

func (img *Program) Path(n NodeID) string { return img.Name(img.slots[n].Path) }

func (img *Program) ConstOf(v ValueID) Cell { return img.consts[v] }

func (img *Program) Cond(c ConditionID) Predicate { return img.conds[c] }

// childrenOf returns the declaration-order direct children of a slot. The
// slice aliases the image, so callers must not retain or write it.
func (img *Program) ChildrenOf(n NodeID) []NodeID {
	s := img.slots[n]
	return img.children[s.first : s.first+s.n]
}

// subtree is the dense preorder range of slots under n, n included: slot
// numbering is preorder, so a subtree is contiguous in image.slots and a View
// or Submit walk is an index loop with no pointer chasing. Callers iterate lo
// inclusive, hi exclusive.
func (img *Program) Subtree(n NodeID) (lo, hi NodeID) {
	return n, n + NodeID(img.slots[n].Span)
}

// owner is the top-level declaration a slot descends from — the form whose
// name a report uses when it may not name the slot itself (the veiled
// clash), and the scope a signature binds to. It is one array
// read: the lowering pass stamps the ordinal, so no request-time answer
// walks a parent chain.
func (img *Program) Owner(n NodeID) NodeID {
	if f := img.slots[n].Form; f != 0 {
		return img.forms[f]
	}
	return 0 // the case root owns itself, which is what the old walk returned
}

// baseOf is a slot's value class, or 0 for a role-typed field, which has no
// value class at all.
func (img *Program) BaseOf(n NodeID) uint8 {
	t := img.slots[n].Type
	if t == 0 {
		return 0
	}
	return img.types[t].Base
}

// collOf is the innermost multiple collection whose instance rows hold slot n,
// and 0 when n lives in the base row. img.colls is in preorder, so the
// sigSlotsOf is one form's signature fields, in declaration order: the dense
// range the lowering pass grouped so that the question "what does this form owe
// in signatures" is bounded by the signatures a form declares rather than by
// its fields. The case root has an empty range and asks nothing, which is what
// lets a caller hand this the ordinal read from `slots[n].form` without a
// second question about whether the slot was in a form at all.
func (img *Program) SigSlotsOf(f FormID) []NodeID {
	r := img.sigSpan[f]
	return img.sigSlots[r.First : r.First+r.N]
}

// innermost enclosing range is the last one that reaches n.
func (img *Program) CollOf(n NodeID) NodeID {
	var inner NodeID
	for _, c := range img.colls {
		if c > n {
			break // preorder: no later collection can enclose n either
		}
		if n >= img.RowLo(c) && n < c+NodeID(img.slots[c].Span) {
			inner = c
		}
	}
	return inner
}

// rowsIn is the collection inside whose instance rows a collection's rows live,
// and 0 when the state holds the set itself. It is `collOf` with the
// collection's own range excluded. Asked about a repeated field nested inside a
// collection, this answers the outer one, which is where its rows actually are.
//
// The pair matters to any walk that asks "how many instances does this
// collection have, and where do I look": answering it with `collOf` would
// send a nested declaration to the state's top-level set, which is allocated
// for every collection and used by none of the nested ones.
func (img *Program) RowsIn(coll NodeID) NodeID {
	var inner NodeID
	for _, c := range img.colls {
		if c == coll {
			continue
		}
		if coll >= img.RowLo(c) && coll < c+NodeID(img.slots[c].Span) {
			inner = c
		}
	}
	return inner
}

// rowLo is the first slot an instance row covers. A repeated group's own slot
// stays in the base row — what it "has" is instances — so its rows begin at its
// first descendant. A repeated *field* has no descendants, and its instances
// are values of that field, so its rows cover the slot itself.
func (img *Program) RowLo(coll NodeID) NodeID {
	if img.slots[coll].Span > 1 {
		return coll + 1
	}
	return coll
}

// rowLen is how many slots one instance of a collection holds.
func (img *Program) RowLen(coll NodeID) int {
	return int(coll) + int(img.slots[coll].Span) - int(img.RowLo(coll))
}

// nonEmpty is the emptiness test, and the only class-sensitive question
// interpretation asks about a stored value. Absence is empty, and so are ""
// and an empty binary handle. A stored zero, a stored false and a first-tag
// enum are values: `required` asks whether something was entered, not
// whether it is interesting.
func (img *Program) NonEmpty(n NodeID, v Cell) bool {
	if !v.Set {
		return false
	}
	switch img.BaseOf(n) {
	case BaseString, BaseOptions:
		return v.Text != ""
	case BaseBinary:
		return len(v.Binary) > 0
	default:
		return true
	}
}

// permAt resolves the effective allow/deny bits one role holds for one slot
// in one stage: unconditional bits, altered by that stage's overlay if there
// is one. Pure bitwise algebra, no tree walking.
func (p *permTables) permAt(r RoleID, stage StageID, s NodeID) (allow, deny uint8) {
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
func overlayIndex(over []stageOverlay, stage StageID) int {
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
func upperBound(slots []NodeID, want NodeID) int {
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
// allows, stripped by the union of denies, whoever granted what. roles is the
// actor's committed role set.
func (img *Program) Effective(roles []RoleID, stage StageID, s NodeID) uint8 {
	var allow, deny uint8
	for _, r := range roles {
		a, d := img.perms.permAt(r, stage, s)
		allow |= a
		deny |= d
	}
	return allow &^ deny
}

// Program tables are populated during compilation and read-only thereafter.
func (img *Program) ID() string                    { return img.id }
func (img *Program) SlotCount() int                { return len(img.slots) }
func (img *Program) InitialStage() StageID         { return img.initial }
func (img *Program) Stage(id StageID) Stage        { return img.stages[id] }
func (img *Program) Type(id TypeID) Type           { return img.types[id] }
func (img *Program) Signature(id uint32) Signature { return img.sigs[id] }
func (img *Program) TagMatches(tag uint64, typ TypeID) bool {
	return tag < uint64(len(img.tagType)) && img.tagType[tag] == typ
}
func (img *Program) Collections() []NodeID { return img.colls }
func (img *Program) FormSlots() []NodeID   { return img.forms }
func (img *Program) ConditionCount() int   { return len(img.conds) }
func (img *Program) FanoutBound() uint32   { return img.depBound }
func (img *Program) Dependents(n NodeID) []NodeID {
	r := img.depFrom[n]
	return img.depOwn[r.First : r.First+r.N]
}
