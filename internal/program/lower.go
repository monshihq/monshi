package program

import (
	"fmt"
	"strconv"

	"github.com/monshihq/monshi/schema"
	"github.com/shopspring/decimal"
)

// Ceilings recorded in RUNTIME §3.1. They bound definitions, never cases,
// and exceeding one is reported at Load as an internal defect rather than
// silently widening a mask.
const (
	maxRoles  = 64  // roleMask is one word
	maxForms  = 64  // stageInfo.requires is one word
	maxStages = 255 // stageID is one byte, ordinals from 1
	maxTypes  = 65535
	maxTags   = 65535

	// How deep `multiple` may nest. Instance paths are addressed by a fixed
	// array at request time, so resolution allocates nothing (§3.9), and a
	// path deeper than this cannot be formed by a definition that loaded.
	MaxCollDepth = 4
)

type lowerer struct {
	s   *schema.Schema
	img *Program

	slotOf  map[*schema.Node]NodeID
	typeOf  map[*schema.Type]TypeID
	tagOf   map[string]TagID
	roleOf  map[string]RoleID
	stageOf map[*schema.Stage]StageID
	formOf  map[*schema.Node]FormID
	nameOf  map[string]StringID
	constOf map[string]ValueID
	hookOf  map[hookKey]HookID

	custom map[*schema.Type]bool // declared in types, so Validate may bind

	nextID NodeID // next preorder slot index
	nodeAt []*schema.Node
	// collection marks slots inside a multiple subtree: E2 keeps them out
	// of every condition, which §5's "one evaluation per collection"
	// argument depends on.
	collection map[NodeID]bool
	// sigNodes keeps the front end's field reference per signature entry
	// until every slot exists, because S1/S2 allow a signature to point at
	// a role-typed field declared later in the same form.
	sigNodes []sigTarget
	// condOwner and reads belong to the conditions pass: fanout is built
	// from what each compiled guard reads (§5.3).
	condOwner map[ConditionID]NodeID
	reads     map[ConditionID][]NodeID

	problems []string
}

// sigTarget pairs a sigEntry index with the schema node it must resolve to.
type sigTarget struct {
	entry uint32
	node  *schema.Node
}

// Compile lowers a checked schema into an immutable execution image.
func Compile(id string, s *schema.Schema) (*Program, error) {
	l := &lowerer{
		s:          s,
		img:        newImage(id),
		slotOf:     map[*schema.Node]NodeID{},
		typeOf:     map[*schema.Type]TypeID{},
		tagOf:      map[string]TagID{},
		roleOf:     map[string]RoleID{},
		stageOf:    map[*schema.Stage]StageID{},
		formOf:     map[*schema.Node]FormID{},
		nameOf:     map[string]StringID{},
		constOf:    map[string]ValueID{},
		hookOf:     map[hookKey]HookID{},
		custom:     map[*schema.Type]bool{},
		collection: map[NodeID]bool{},
		sigNodes:   []sigTarget{},
		condOwner:  map[ConditionID]NodeID{},
		reads:      map[ConditionID][]NodeID{},
	}
	for _, t := range s.Types {
		l.custom[t] = true
	}

	l.roles()
	l.types()
	l.forms()
	l.stages()
	l.signatures()
	l.conditions()
	l.permissions()
	l.dependencies()
	// The §4 checks read the image as if it were finished. A build that had
	// to refuse something — a ceiling, a missing ordinal — leaves arrays
	// short, and walking them would report a second defect about the wrong
	// node, or crash before saying anything. The first problem is the one
	// that matters, so the drift checks wait for a complete build.
	if len(l.problems) == 0 {
		l.verify()
	}

	if len(l.problems) > 0 {
		// Refused, not repaired: the definition compiled, so the fault is
		// the runtime's (RUNTIME §10).
		return nil, &Error{ID: id, Defects: l.problems}
	}
	l.img.layout = layoutID(l.img)
	return l.img, nil
}

func newImage(id string) *Program {
	return &Program{
		id:       id,
		names:    []string{""}, // stringID 0 is the empty name
		types:    []Type{{}},
		consts:   []Cell{{}},
		conds:    []Predicate{never()}, // conds[0] is never called (§3.4)
		forms:    []NodeID{0},
		sigs:     []Signature{{}},
		stages:   []Stage{{}},
		hooks:    []hookKey{{}},
		tagNames: []StringID{0},
		tagType:  []TypeID{0},
		roleIdx:  []StringID{0},
		initial:  noStage,
		// perms, depFrom and depOwn are filled by their own passes; the
		// zero values are the empty tables §10's verify pass checks.
	}
}

func (l *lowerer) internName(s string) StringID {
	if id, ok := l.nameOf[s]; ok {
		return id
	}
	id := StringID(len(l.img.names))
	l.img.names = append(l.img.names, s)
	l.nameOf[s] = id
	return id
}

// reportf records an invariant violation. Lowering never panics and never
// guesses: the definition is refused instead (RUNTIME §10).
func (l *lowerer) reportf(at, format string, args ...any) {
	if at == "" {
		at = "case root"
	}
	l.problems = append(l.problems, at+": "+fmt.Sprintf(format, args...))
}

// roles assigns role ordinals in declaration order (P4 already proved every
// referenced role is declared).
func (l *lowerer) roles() {
	if len(l.s.Roles) > maxRoles {
		l.reportf("", "definition declares %d roles, the limit is %d",
			len(l.s.Roles), maxRoles)
		return
	}
	for _, r := range l.s.Roles {
		l.internName(r.Name)
	}
	l.img.roleIdx = make([]StringID, 0, len(l.s.Roles)+1)
	l.img.roleIdx = append(l.img.roleIdx, 0) // index 0 is "no role"
	for i, r := range l.s.Roles {
		l.roleOf[r.Name] = RoleID(i + 1)
		l.img.roleIdx = append(l.img.roleIdx, l.internName(r.Name))
	}
}

// types numbers every declared custom type before the slot pass runs, so a
// tag or default that belongs to a type no field uses is still resolvable:
// P5 makes an unused declaration harmless, not invisible (§4).
func (l *lowerer) types() {
	for _, t := range l.s.Types {
		l.internType(t)
	}
}

func (l *lowerer) roleIDOf(r *schema.Role) RoleID {
	if r == nil {
		return 0
	}
	id, ok := l.roleOf[r.Name]
	if !ok {
		l.reportf(r.Name, "role was not assigned an ordinal")
	}
	return id
}

// baseCode maps the compiled model's type base name to its ordinal class.
// Every name comes from SYNTAX Appendix A; anything else means the front end
// produced a type the language does not define.
func baseCode(base string) uint8 {
	switch base {
	case "string":
		return BaseString
	case "integer":
		return BaseInteger
	case "decimal":
		return BaseDecimal
	case "boolean":
		return BaseBoolean
	case "date":
		return BaseDate
	case "datetime":
		return BaseDatetime
	case "binary":
		return BaseBinary
	case "options":
		return BaseOptions
	case "enum":
		return BaseEnum
	case "signature":
		return BaseSignature
	}
	return 0
}

// className names a class for problem text only; runtime dispatch is on the
// ordinal.
func ClassName(base uint8) string {
	switch base {
	case BaseString:
		return "string"
	case BaseInteger:
		return "integer"
	case BaseDecimal:
		return "decimal"
	case BaseBoolean:
		return "boolean"
	case BaseDate:
		return "date"
	case BaseDatetime:
		return "datetime"
	case BaseBinary:
		return "binary"
	case BaseOptions:
		return "options"
	case BaseEnum:
		return "enum"
	case BaseSignature:
		return "signature"
	}
	return "unknown"
}

// internType numbers one compiled type. Built-ins are shared pointers in the
// front end, so each arrives once; inline enums and custom types arrive once
// per declaration, which is exactly how E3 pairs them.
func (l *lowerer) internType(t *schema.Type) TypeID {
	if t == nil {
		return 0
	}
	if id, ok := l.typeOf[t]; ok {
		return id
	}
	if len(l.img.types) > maxTypes {
		l.reportf(t.Name, "definition declares more than %d types", maxTypes)
		return 0
	}
	base := baseCode(t.Base)
	if base == 0 {
		l.reportf(t.Name, "type base %q is not a language type", t.Base)
		return 0
	}
	id := TypeID(len(l.img.types))
	info := Type{Base: base, name: l.internName(t.Name)}

	switch base {
	case BaseEnum:
		info.Tags = make([]TagID, 0, len(t.Tags))
		for _, tag := range t.Tags {
			if _, dup := l.tagOf[tag]; dup {
				l.reportf(t.Name, "enum tag %s is declared twice", tag) // N3
				continue
			}
			if len(l.img.tagNames) > maxTags {
				l.reportf(t.Name, "definition declares more than %d tags", maxTags)
				break
			}
			tid := TagID(len(l.img.tagNames))
			l.tagOf[tag] = tid
			l.img.tagNames = append(l.img.tagNames, l.internName(tag))
			l.img.tagType = append(l.img.tagType, id)
			info.Tags = append(info.Tags, tid)
		}
	case BaseDecimal:
		info.Fits = decimalFits(t.Precision, t.Scale)
	}
	if l.custom[t] {
		// DESIGN §7 binds Validate by custom type, so a declared type
		// always carries its lookup key; built-ins never do.
		info.Hook = l.internHook(HookByType, t.Name)
	}
	l.img.types = append(l.img.types, info)
	l.typeOf[t] = id
	return id
}

// decimalFits builds the precision/scale predicate once per decimal type,
// mirroring the bound T2 validated at compile time: the value must need at
// most s fractional digits and at most p significant ones.
func decimalFits(precision, scale int) func(decimal.Decimal) bool {
	return func(d decimal.Decimal) bool {
		return schema.FitsDecimal(d, precision, scale)
	}
}

func (l *lowerer) typeBase(id TypeID) uint8 {
	if id == 0 || int(id) >= len(l.img.types) {
		return 0
	}
	return l.img.types[id].Base
}

// internConst numbers one compile-time constant: a default or a guard
// literal. The class is the operand type's, which SYNTAX §5.8 guarantees the
// literal was already normalized to — lowering asserts that, it does not
// parse or widen anything.
func (l *lowerer) internConst(class uint8, at string, v *schema.Value) ValueID {
	if v == nil {
		return 0
	}
	sv, key, ok := l.constValue(class, at, v)
	if !ok {
		return 0
	}
	if id, seen := l.constOf[key]; seen {
		return id
	}
	id := ValueID(len(l.img.consts))
	l.img.consts = append(l.img.consts, sv)
	l.constOf[key] = id
	return id
}

func (l *lowerer) constValue(class uint8, at string, v *schema.Value) (Cell, string, bool) {
	var sv Cell
	sv.Set = true
	switch class {
	case BaseString:
		if v.Kind != "string" {
			l.reportf(at, "%s constant compiled as %s", ClassName(class), v.Kind)
			return sv, "", false
		}
		sv.Text = v.Str
	case BaseInteger:
		if v.Kind != "integer" {
			l.reportf(at, "%s constant compiled as %s", ClassName(class), v.Kind)
			return sv, "", false
		}
		sv.Int = v.Int
	case BaseDecimal:
		if v.Kind != "decimal" {
			l.reportf(at, "decimal constant compiled as %s, not normalized", v.Kind)
			return sv, "", false
		}
		sv.Decimal = v.Dec
	case BaseBoolean:
		if v.Kind != "boolean" {
			l.reportf(at, "%s constant compiled as %s", ClassName(class), v.Kind)
			return sv, "", false
		}
		if v.Bool {
			sv.Int = 1
		}
	case BaseDate, BaseDatetime:
		if v.Kind != "date" && v.Kind != "datetime" {
			l.reportf(at, "%s constant compiled as %s, not normalized",
				ClassName(class), v.Kind)
			return sv, "", false
		}
		sv.Time = v.Time
	case BaseEnum:
		tag, ok := l.tagOf[v.Str]
		if v.Kind != "enum" || !ok {
			l.reportf(at, "enum constant %q is not a declared tag", v.Str)
			return sv, "", false
		}
		sv.Int = int64(tag)
	default:
		l.reportf(at, "%s takes no constant", ClassName(class))
		return sv, "", false
	}
	return sv, constKey(class, v), true
}

// constKey identifies a constant for de-duplication: two constants of the
// same class with the same content are one entry in image.consts.
func constKey(class uint8, v *schema.Value) string {
	switch class {
	case BaseString:
		return "s" + v.Str
	case BaseInteger:
		return "i" + strconv.FormatInt(v.Int, 10)
	case BaseDecimal:
		return "d" + v.Dec.String()
	case BaseBoolean:
		if v.Bool {
			return "b1"
		}
		return "b0"
	case BaseDate, BaseDatetime:
		return "t" + v.Time.String()
	default: // baseEnum
		return "e" + v.Str
	}
}

func (l *lowerer) internHook(kind uint8, name string) HookID {
	key := hookKey{kind: kind, name: l.internName(name)}
	if id, ok := l.hookOf[key]; ok {
		return id
	}
	id := HookID(len(l.img.hooks))
	l.img.hooks = append(l.img.hooks, key)
	l.hookOf[key] = id
	return id
}

// forms builds the case root and every declaration beneath it, numbered in
// preorder: the subtree of a slot is then the dense range [id, id+span).
func (l *lowerer) forms() {
	l.appendSlot(Slot{Kind: kindRoot}, nil) // slot 0 is the case root
	kids := make([]NodeID, 0, len(l.s.Forms))
	for i, f := range l.s.Forms {
		if i+1 > maxForms {
			l.reportf(f.Path, "definition declares more than %d top-level forms",
				maxForms)
			return
		}
		l.formOf[f] = FormID(i + 1)
		slot := l.buildNode(f, 0, 0, 0)
		l.img.forms = append(l.img.forms, slot)
		kids = append(kids, slot)
	}
	// The root reaches every declaration: its subtree is the whole slot
	// array, which is what makes "allow view all" one grant on slot 0.
	first := uint32(len(l.img.children))
	l.img.children = append(l.img.children, kids...)
	l.img.slots[0].first = first
	l.img.slots[0].n = uint32(len(kids))
	l.img.slots[0].Span = uint32(l.nextID)
	l.formAnchors()
}

// formAnchors answers "which form owns this slot" once, for the whole image,
// so that no request-time path walks one. Preorder makes the answer a range
// copy: a form's subtree is contiguous, so stamping the ordinal over it is
// the same loop the span already describes (§3.2).
//
// Two things are read from it at request time and neither is a walk. §5.6's
// commit effects map a changed slot to the signature fields its form's
// content binds — `slot.form` is the whole question, one array read per diff
// entry — and the merge's veiled-clash address asks for the form whose name
// it may print when the slot's own address is case data (§5.8).
//
// The same pass groups the signature fields by form, declaration order,
// because a destination that requires a form asks about its signatures and
// nothing else (S1 keeps them direct members). The group is a dense range,
// so that question is bounded by the signatures a form declares rather than
// by its fields.
func (l *lowerer) formAnchors() {
	img := l.img
	img.sigSpan = make([]depRange, len(img.forms)+1)
	img.sigSlots = nil
	for f := range img.forms {
		if f == 0 {
			continue // index 0 is "no form"
		}
		root := img.forms[f]
		lo, hi := img.Subtree(root)
		for n := lo; n < hi; n++ {
			img.slots[n].Form = FormID(f)
		}
		first := uint32(len(img.sigSlots))
		for _, c := range img.ChildrenOf(root) {
			if img.slots[c].Sig != 0 {
				img.sigSlots = append(img.sigSlots, c)
			}
		}
		last := uint32(len(img.sigSlots))
		img.sigSpan[f] = depRange{First: first, N: last - first}
	}
}

// buildNode numbers one subtree and returns its slot. parent is the slot of
// the enclosing declaration (0 for a form), unit the outermost verifiable
// ancestor's slot, and depth the number of `multiple` collections enclosing
// this one: their members are stored one row per instance (§5.1) and kept out
// of every condition (E2).
func (l *lowerer) buildNode(n *schema.Node, parent, unit NodeID, depth int) NodeID {
	id := l.nextID
	slot := Slot{
		Parent: parent,
		Name:   l.internName(n.Name),
		Path:   l.internName(n.Path),
	}
	switch n.Kind {
	case "form":
		slot.Kind = KindForm
	case "group":
		slot.Kind = KindGroup
	case "field":
		slot.Kind = KindField
	default:
		l.reportf(n.Path, "declaration kind %q is not a language kind", n.Kind)
	}

	if n.Role != nil {
		slot.Role = l.roleIDOf(n.Role)
		if n.Type != nil {
			l.reportf(n.Path, "role-typed field also carries a value type")
		}
	} else {
		slot.Type = l.internType(n.Type)
	}
	if n.Required {
		slot.Flags |= FlagRequired
	}
	if n.Multiple {
		slot.Flags |= flagMultiple
	}
	if n.Verifiable {
		slot.Flags |= FlagVerifiable
		if unit == 0 {
			unit = id // F3: the outermost unit governs
		}
	}
	slot.Verif = unit
	if n.Default != nil {
		slot.Def = l.internConst(l.typeBase(slot.Type), n.Path, n.Default)
	}

	switch l.typeBase(slot.Type) {
	case BaseSignature:
		slot.Sig = l.addSigEntry(n)
	case BaseOptions:
		// DESIGN §7 binds Options by field path; the view layer decides
		// whether to call it, permission-gated like any other value.
		slot.Hook = l.internHook(HookByPath, n.Path)
	}

	// A collection gets a dense ordinal, so a state addresses its rows by
	// index rather than by lookup (§5.1). Preorder numbering makes img.colls
	// the declaration-order list as well, which is what collOf relies on.
	if n.Multiple {
		if depth+1 > MaxCollDepth {
			l.reportf(n.Path, "collections nest %d deep, the limit is %d",
				depth+1, MaxCollDepth)
		} else {
			slot.Coll = uint32(len(l.img.colls)) + 1
			l.img.colls = append(l.img.colls, id)
		}
		depth++
	}
	l.appendSlot(slot, n)
	if depth > 0 {
		l.collection[id] = true // E2 keeps this subtree out of conditions
	}

	kids := make([]NodeID, 0, len(n.Nodes))
	for _, c := range n.Nodes {
		kids = append(kids, l.buildNode(c, id, unit, depth))
	}
	// The child run is written after the whole subtree was built: the
	// descendants' own lists were appended to the same array while this
	// node's list was still in the local slice.
	first := uint32(len(l.img.children))
	l.img.children = append(l.img.children, kids...)
	l.img.slots[id].first = first
	l.img.slots[id].n = uint32(len(kids))
	l.img.slots[id].Span = uint32(l.nextID - id)
	return id
}

func (l *lowerer) appendSlot(s Slot, n *schema.Node) {
	if NodeID(len(l.img.slots)) != l.nextID {
		l.reportf(l.img.Name(s.Path), "slot numbering drifted")
	}
	l.img.slots = append(l.img.slots, s)
	l.nodeAt = append(l.nodeAt, n)
	l.nextID++
	if n != nil {
		l.slotOf[n] = NodeID(len(l.img.slots) - 1)
	}
}

// addSigEntry records the S2 resolution. Only the role half is known here:
// the field target is patched in the signatures pass, because S2 permits a
// signature to name a role-typed field declared later in the same form.
func (l *lowerer) addSigEntry(n *schema.Node) uint32 {
	switch {
	case n.Signer != nil && n.SignField != nil:
		l.reportf(n.Path, "signature reference resolved to a role and a field")
		return 0
	case n.Signer == nil && n.SignField == nil:
		l.reportf(n.Path, "signature reference is unresolved") // S2
		return 0
	}
	l.img.sigs = append(l.img.sigs, Signature{Role: l.roleIDOf(n.Signer)})
	idx := uint32(len(l.img.sigs) - 1)
	if n.SignField != nil {
		l.sigNodes = append(l.sigNodes, sigTarget{entry: idx, node: n.SignField})
	}
	return idx
}

// signatures patches signature-field targets now that every slot exists.
// S1 puts signature fields at form level and S2 resolved the reference to a
// role-typed field of the same form, so an unresolvable target here means a
// front-end guarantee was broken.
func (l *lowerer) signatures() {
	for _, t := range l.sigNodes {
		slot, ok := l.slotOf[t.node]
		if !ok {
			l.reportf(t.node.Path, "signature field reference is not a declaration")
			continue
		}
		if t.node.Role == nil {
			l.reportf(l.img.Path(l.sigSlot(t.entry)), "%s is not a role-typed field",
				t.node.Path)
			continue
		}
		l.img.sigs[t.entry].Assigned = slot
	}
}

// sigSlot is the signature field a sig entry belongs to, for problem text.
func (l *lowerer) sigSlot(idx uint32) NodeID {
	for i := range l.img.slots {
		if l.img.slots[i].Sig == idx {
			return NodeID(i)
		}
	}
	return 0
}

// stages numbers the stage graph. Ordinals follow declaration order, which
// makes ST1's first declared stage the initial stage as a constant.
func (l *lowerer) stages() {
	if len(l.s.Stages) > maxStages {
		l.reportf("", "definition declares more than %d stages", maxStages)
		return
	}
	for i, st := range l.s.Stages {
		l.stageOf[st] = StageID(i + 1)
	}
	for _, st := range l.s.Stages {
		id := l.stageOf[st]
		info := Stage{Name: l.internName(st.Name)}
		for _, f := range st.Requires {
			fid, ok := l.formOf[f]
			if !ok {
				l.reportf(st.Name, "require names %s, not a declared form", f.Path)
				continue
			}
			info.Requires |= 1 << (fid - 1) // ST4: top-level forms only
		}
		for _, e := range st.Edges {
			dest, ok := l.stageOf[e.Dest]
			if !ok {
				l.reportf(st.Name, "edge destination %s was not numbered", e.Dest.Name)
				continue
			}
			if dest == id {
				l.reportf(st.Name, "stage %s transitions to itself", st.Name) // ST3
				continue
			}
			if _, dup := info.EdgeTo(dest); dup {
				l.reportf(st.Name, "two edges lead to stage %s", e.Dest.Name) // ST3
				continue
			}
			info.edges = append(info.edges, Edge{dest: dest, Reject: e.Reject})
		}
		l.img.stages = append(l.img.stages, info)
	}
	if l.s.Initial == nil {
		l.reportf("", "definition declares no initial stage")
		return
	}
	l.img.initial = l.stageOf[l.s.Initial]
	if l.img.initial == 0 {
		l.reportf(l.s.Initial.Name, "initial stage was not numbered")
	}
}
