package monshi

// Lowering turns the compiled definition of the schema package into the
// runtime image (RUNTIME §3, §4). This file runs the identity passes:
// ordinals, interned names, types, constants and the dense preorder slot
// array. Later passes in this package attach conditions, permission
// tables, dependency adjacency, and finally the invariant check that
// lets nothing half-lowered escape Load.
//
// Lowering reads only exported schema fields and treats every §4
// invariant as an assumption. A violated assumption is collected as a
// problem and refused (RUNTIME §10): the code never repairs a definition,
// never panics, and never re-runs the front end.

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
	maxCollDepth = 4
)

type lowerer struct {
	s   *schema.Schema
	img *image

	slotOf  map[*schema.Node]nodeID
	typeOf  map[*schema.Type]typeID
	tagOf   map[string]tagID
	roleOf  map[string]roleID
	stageOf map[*schema.Stage]stageID
	formOf  map[*schema.Node]formID
	nameOf  map[string]stringID
	constOf map[string]valueID
	hookOf  map[hookKey]hookID

	custom map[*schema.Type]bool // declared in types, so Validate may bind

	nextID nodeID // next preorder slot index
	nodeAt []*schema.Node
	// collection marks slots inside a multiple subtree: E2 keeps them out
	// of every condition, which §5's "one evaluation per collection"
	// argument depends on.
	collection map[nodeID]bool
	// sigNodes keeps the front end's field reference per signature entry
	// until every slot exists, because S1/S2 allow a signature to point at
	// a role-typed field declared later in the same form.
	sigNodes []sigTarget
	// condOwner and reads belong to the conditions pass: fanout is built
	// from what each compiled guard reads (§5.3).
	condOwner map[condID]nodeID
	reads     map[condID][]nodeID

	problems []string
}

// sigTarget pairs a sigEntry index with the schema node it must resolve to.
type sigTarget struct {
	entry uint32
	node  *schema.Node
}

// lower compiles a checked definition into an image.
func lower(id string, s *schema.Schema) (*image, error) {
	l := &lowerer{
		s:          s,
		img:        newImage(id),
		slotOf:     map[*schema.Node]nodeID{},
		typeOf:     map[*schema.Type]typeID{},
		tagOf:      map[string]tagID{},
		roleOf:     map[string]roleID{},
		stageOf:    map[*schema.Stage]stageID{},
		formOf:     map[*schema.Node]formID{},
		nameOf:     map[string]stringID{},
		constOf:    map[string]valueID{},
		hookOf:     map[hookKey]hookID{},
		custom:     map[*schema.Type]bool{},
		collection: map[nodeID]bool{},
		sigNodes:   []sigTarget{},
		condOwner:  map[condID]nodeID{},
		reads:      map[condID][]nodeID{},
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
	return l.img, nil
}

func newImage(id string) *image {
	return &image{
		id:       id,
		names:    []string{""}, // stringID 0 is the empty name
		types:    []typeInfo{{}},
		consts:   []slotValue{{}},
		conds:    []condFn{never()}, // conds[0] is never called (§3.4)
		forms:    []nodeID{0},
		sigs:     []sigEntry{{}},
		stages:   []stageInfo{{}},
		hooks:    []hookKey{{}},
		tagNames: []stringID{0},
		tagType:  []typeID{0},
		roleIdx:  []stringID{0},
		initial:  noStage,
		// perms, depFrom and depOwn are filled by their own passes; the
		// zero values are the empty tables §10's verify pass checks.
	}
}

// --- identity ---

func (l *lowerer) internName(s string) stringID {
	if id, ok := l.nameOf[s]; ok {
		return id
	}
	id := stringID(len(l.img.names))
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
	l.img.roleIdx = make([]stringID, 0, len(l.s.Roles)+1)
	l.img.roleIdx = append(l.img.roleIdx, 0) // index 0 is "no role"
	for i, r := range l.s.Roles {
		l.roleOf[r.Name] = roleID(i + 1)
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

func (l *lowerer) roleIDOf(r *schema.Role) roleID {
	if r == nil {
		return 0
	}
	id, ok := l.roleOf[r.Name]
	if !ok {
		l.reportf(r.Name, "role was not assigned an ordinal")
	}
	return id
}

// --- types and constants ---

// baseCode maps the compiled model's type base name to its ordinal class.
// Every name comes from SYNTAX Appendix A; anything else means the front end
// produced a type the language does not define.
func baseCode(base string) uint8 {
	switch base {
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
	case "binary":
		return baseBinary
	case "options":
		return baseOptions
	case "enum":
		return baseEnum
	case "signature":
		return baseSignature
	}
	return 0
}

// className names a class for problem text only; runtime dispatch is on the
// ordinal.
func className(base uint8) string {
	switch base {
	case baseString:
		return "string"
	case baseInteger:
		return "integer"
	case baseDecimal:
		return "decimal"
	case baseBoolean:
		return "boolean"
	case baseDate:
		return "date"
	case baseDatetime:
		return "datetime"
	case baseBinary:
		return "binary"
	case baseOptions:
		return "options"
	case baseEnum:
		return "enum"
	case baseSignature:
		return "signature"
	}
	return "unknown"
}

// internType numbers one compiled type. Built-ins are shared pointers in the
// front end, so each arrives once; inline enums and custom types arrive once
// per declaration, which is exactly how E3 pairs them.
func (l *lowerer) internType(t *schema.Type) typeID {
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
	id := typeID(len(l.img.types))
	info := typeInfo{base: base, name: l.internName(t.Name)}

	switch base {
	case baseEnum:
		info.tags = make([]tagID, 0, len(t.Tags))
		for _, tag := range t.Tags {
			if _, dup := l.tagOf[tag]; dup {
				l.reportf(t.Name, "enum tag %s is declared twice", tag) // N3
				continue
			}
			if len(l.img.tagNames) > maxTags {
				l.reportf(t.Name, "definition declares more than %d tags", maxTags)
				break
			}
			tid := tagID(len(l.img.tagNames))
			l.tagOf[tag] = tid
			l.img.tagNames = append(l.img.tagNames, l.internName(tag))
			l.img.tagType = append(l.img.tagType, id)
			info.tags = append(info.tags, tid)
		}
	case baseDecimal:
		info.fits = decimalFits(t.Precision, t.Scale)
	}
	if l.custom[t] {
		// DESIGN §7 binds Validate by custom type, so a declared type
		// always carries its lookup key; built-ins never do.
		info.hook = l.internHook(hookByType, t.Name)
	}
	l.img.types = append(l.img.types, info)
	l.typeOf[t] = id
	return id
}

// decimalFits builds the precision/scale predicate once per decimal type,
// mirroring the bound T2 validated at compile time: the value must need at
// most s fractional digits and at most p significant ones.
func decimalFits(precision, scale int) func(decimal.Decimal) bool {
	s := int32(scale)
	return func(d decimal.Decimal) bool {
		r := d.Round(s)
		return r.Equal(d) && r.NumDigits() <= precision
	}
}

func (l *lowerer) typeBase(id typeID) uint8 {
	if id == 0 || int(id) >= len(l.img.types) {
		return 0
	}
	return l.img.types[id].base
}

// internConst numbers one compile-time constant: a default or a guard
// literal. The class is the operand type's, which SYNTAX §5.8 guarantees the
// literal was already normalized to — lowering asserts that, it does not
// parse or widen anything.
func (l *lowerer) internConst(class uint8, at string, v *schema.Value) valueID {
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
	id := valueID(len(l.img.consts))
	l.img.consts = append(l.img.consts, sv)
	l.constOf[key] = id
	return id
}

func (l *lowerer) constValue(class uint8, at string, v *schema.Value) (slotValue, string, bool) {
	var sv slotValue
	sv.set = true
	switch class {
	case baseString:
		if v.Kind != "string" {
			l.reportf(at, "%s constant compiled as %s", className(class), v.Kind)
			return sv, "", false
		}
		sv.s = v.Str
	case baseInteger:
		if v.Kind != "integer" {
			l.reportf(at, "%s constant compiled as %s", className(class), v.Kind)
			return sv, "", false
		}
		sv.i = v.Int
	case baseDecimal:
		if v.Kind != "decimal" {
			l.reportf(at, "decimal constant compiled as %s, not normalized", v.Kind)
			return sv, "", false
		}
		sv.d = v.Dec
	case baseBoolean:
		if v.Kind != "boolean" {
			l.reportf(at, "%s constant compiled as %s", className(class), v.Kind)
			return sv, "", false
		}
		if v.Bool {
			sv.i = 1
		}
	case baseDate, baseDatetime:
		if v.Kind != "date" && v.Kind != "datetime" {
			l.reportf(at, "%s constant compiled as %s, not normalized",
				className(class), v.Kind)
			return sv, "", false
		}
		sv.t = v.Time
	case baseEnum:
		tag, ok := l.tagOf[v.Str]
		if v.Kind != "enum" || !ok {
			l.reportf(at, "enum constant %q is not a declared tag", v.Str)
			return sv, "", false
		}
		sv.i = int64(tag)
	default:
		l.reportf(at, "%s takes no constant", className(class))
		return sv, "", false
	}
	return sv, constKey(class, v), true
}

// constKey identifies a constant for de-duplication: two constants of the
// same class with the same content are one entry in image.consts.
func constKey(class uint8, v *schema.Value) string {
	switch class {
	case baseString:
		return "s" + v.Str
	case baseInteger:
		return "i" + strconv.FormatInt(v.Int, 10)
	case baseDecimal:
		return "d" + v.Dec.String()
	case baseBoolean:
		if v.Bool {
			return "b1"
		}
		return "b0"
	case baseDate, baseDatetime:
		return "t" + v.Time.String()
	default: // baseEnum
		return "e" + v.Str
	}
}

func (l *lowerer) internHook(kind uint8, name string) hookID {
	key := hookKey{kind: kind, name: l.internName(name)}
	if id, ok := l.hookOf[key]; ok {
		return id
	}
	id := hookID(len(l.img.hooks))
	l.img.hooks = append(l.img.hooks, key)
	l.hookOf[key] = id
	return id
}

// --- slots ---

// forms builds the case root and every declaration beneath it, numbered in
// preorder: the subtree of a slot is then the dense range [id, id+span).
func (l *lowerer) forms() {
	l.appendSlot(nodeSlot{kind: kindRoot}, nil) // slot 0 is the case root
	kids := make([]nodeID, 0, len(l.s.Forms))
	for i, f := range l.s.Forms {
		if i+1 > maxForms {
			l.reportf(f.Path, "definition declares more than %d top-level forms",
				maxForms)
			return
		}
		l.formOf[f] = formID(i + 1)
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
	l.img.slots[0].span = uint32(l.nextID)
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
		lo, hi := img.subtree(root)
		for n := lo; n < hi; n++ {
			img.slots[n].form = formID(f)
		}
		first := uint32(len(img.sigSlots))
		for _, c := range img.childrenOf(root) {
			if img.slots[c].sig != 0 {
				img.sigSlots = append(img.sigSlots, c)
			}
		}
		last := uint32(len(img.sigSlots))
		img.sigSpan[f] = depRange{first: first, n: last - first}
	}
}

// buildNode numbers one subtree and returns its slot. parent is the slot of
// the enclosing declaration (0 for a form), unit the outermost verifiable
// ancestor's slot, and depth the number of `multiple` collections enclosing
// this one: their members are stored one row per instance (§5.1) and kept out
// of every condition (E2).
func (l *lowerer) buildNode(n *schema.Node, parent, unit nodeID, depth int) nodeID {
	id := l.nextID
	slot := nodeSlot{
		parent: parent,
		name:   l.internName(n.Name),
		path:   l.internName(n.Path),
	}
	switch n.Kind {
	case "form":
		slot.kind = kindForm
	case "group":
		slot.kind = kindGroup
	case "field":
		slot.kind = kindField
	default:
		l.reportf(n.Path, "declaration kind %q is not a language kind", n.Kind)
	}

	if n.Role != nil {
		slot.role = l.roleIDOf(n.Role)
		if n.Type != nil {
			l.reportf(n.Path, "role-typed field also carries a value type")
		}
	} else {
		slot.typ = l.internType(n.Type)
	}
	if n.Required {
		slot.flags |= flagRequired
	}
	if n.Multiple {
		slot.flags |= flagMultiple
	}
	if n.Verifiable {
		slot.flags |= flagVerifiable
		if unit == 0 {
			unit = id // F3: the outermost unit governs
		}
	}
	slot.verif = unit
	if n.Default != nil {
		slot.def = l.internConst(l.typeBase(slot.typ), n.Path, n.Default)
	}

	switch l.typeBase(slot.typ) {
	case baseSignature:
		slot.sig = l.addSigEntry(n)
	case baseOptions:
		// DESIGN §7 binds Options by field path; the view layer decides
		// whether to call it, permission-gated like any other value.
		slot.hook = l.internHook(hookByPath, n.Path)
	}

	// A collection gets a dense ordinal, so a state addresses its rows by
	// index rather than by lookup (§5.1). Preorder numbering makes img.colls
	// the declaration-order list as well, which is what collOf relies on.
	if n.Multiple {
		if depth+1 > maxCollDepth {
			l.reportf(n.Path, "collections nest %d deep, the limit is %d",
				depth+1, maxCollDepth)
		} else {
			slot.coll = uint32(len(l.img.colls)) + 1
			l.img.colls = append(l.img.colls, id)
		}
		depth++
	}
	l.appendSlot(slot, n)
	if depth > 0 {
		l.collection[id] = true // E2 keeps this subtree out of conditions
	}

	kids := make([]nodeID, 0, len(n.Nodes))
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
	l.img.slots[id].span = uint32(l.nextID - id)
	return id
}

func (l *lowerer) appendSlot(s nodeSlot, n *schema.Node) {
	if nodeID(len(l.img.slots)) != l.nextID {
		l.reportf(l.img.name(s.path), "slot numbering drifted")
	}
	l.img.slots = append(l.img.slots, s)
	l.nodeAt = append(l.nodeAt, n)
	l.nextID++
	if n != nil {
		l.slotOf[n] = nodeID(len(l.img.slots) - 1)
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
	l.img.sigs = append(l.img.sigs, sigEntry{role: l.roleIDOf(n.Signer)})
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
			l.reportf(l.img.path(l.sigSlot(t.entry)), "%s is not a role-typed field",
				t.node.Path)
			continue
		}
		l.img.sigs[t.entry].assigned = slot
	}
}

// sigSlot is the signature field a sig entry belongs to, for problem text.
func (l *lowerer) sigSlot(idx uint32) nodeID {
	for i := range l.img.slots {
		if l.img.slots[i].sig == idx {
			return nodeID(i)
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
		l.stageOf[st] = stageID(i + 1)
	}
	for _, st := range l.s.Stages {
		id := l.stageOf[st]
		info := stageInfo{name: l.internName(st.Name)}
		for _, f := range st.Requires {
			fid, ok := l.formOf[f]
			if !ok {
				l.reportf(st.Name, "require names %s, not a declared form", f.Path)
				continue
			}
			info.requires |= 1 << (fid - 1) // ST4: top-level forms only
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
			if _, dup := info.edgeTo(dest); dup {
				l.reportf(st.Name, "two edges lead to stage %s", e.Dest.Name) // ST3
				continue
			}
			info.edges = append(info.edges, edge{dest: dest, reject: e.Reject})
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
