package engine

// The store's wire format (DESIGN §5, §8; RUNTIME §10's milestone 4). A
// storage implementation persists *bytes*: the committed snapshot, each
// actor's draft and each history entry are opaque to the host, and
// DESIGN §5 says so out loud — "the exact Go representation remains an
// implementation detail". This file is that detail made a fact: the
// shapes in store.go become sequences of bytes and come back unchanged,
// so durability stops being an accident of Go struct layout and becomes
// a format with a version number.
//
// Four rules hold the format to the shape of the semantics rather than
// the shape of the structs.
//
//   - Cells travel sparsely. A row is emitted as the (slot, value)
//     pairs that are *set*, and the verdict tables as the (slot,
//     record) pairs that are not the zero value — absence is what the
//     engine models, so a cleared field and an untouched one encode the
//     same nothing, and the bytes never grow with the width of a
//     definition.
//   - A cell's payload is chosen by the image, not by the Go shape of
//     the struct. Each cell names its slot and its class, and the class
//     selects which field of `slotValue` the payload fills: §5.1's
//     "the class is fixed per slot" read the other way, so a row's
//     unused Go fields never reach the wire and a `Text("")` cannot be
//     mistaken for an `Int(0)`. The decoder *checks* the written class
//     against the live image's type for that slot and refuses bytes
//     that contradict it — this is the only place outside §3.9 where a
//     foreign definition can be caught, and the reason bytes must be
//     decoded under the definition that wrote them.
//   - Instances are addressed by what §5.1 makes permanent: stable
//     instance IDs and the per-collection nesting they live in, never
//     by row numbers, so a store that reorders nothing must be able to
//     say so from the bytes alone. The ID counter travels with the
//     state because renumbering rows would silently invalidate every
//     `path` in the history the same store holds.
//   - Decode rebuilds through a live image and refuses before it
//     allocates: a foreign version byte, a length larger than the
//     residue can hold, or a trailing byte nobody claimed are the same
//     refusal — corrupt bytes must fail closed rather than half-parse
//     into a state whose conditions then run against invented slots.
//
// Integers are varints, zigzagged where a value can be negative;
// strings and byte runs are length-prefixed. A `decimal` travels as its
// shortest exact text and returns by parse — shopspring's representation
// keeps written scale, and `same` compares decimals by value, so text is
// the faithful lossless form. A `time.Time` travels as seconds,
// nanoseconds and the zone it displayed in: the instant and its printed
// offset survive; the *location* does not, because a monotonic reading
// belongs to the process that took it and the engine reads stored times
// only as instants (§5.1's equality). A nil table and an empty one, a
// nil binary handle and an empty slice encode alike and decode to nil:
// every reader of these fields asks length, never identity.

import (
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// codecVersion is the format's one number, written as the first byte of
// every buffer this file produces. It is bumped, not extended: silent
// reinterpretation of durable bytes is the one failure a storage format
// cannot be allowed, and DESIGN §1's no-migration rule means no older
// version will ever ask for a reader.
const codecVersion = 1

// errCodec is the decoder's whole vocabulary: these bytes are not this
// format. Every refusal is this one error and not a sentence, because
// the bytes are the engine's own and a host that meets one has a corrupt
// store, not a mistyped argument — the caller wraps it as a storage
// failure, and naming a byte offset would only pretend that an
// inspection of someone else's file is useful in a refusal message.
var errCodec = errors.New("case data is not in a known format")

// classPseudo is the format's one non-type class byte: the integer-and-
// text pair a pseudo-cell holds — a collection's committed row count, a
// unit's decision code and comment, a signature's reference. These are
// values the engine stores at slots no getter reads as data (classless
// or signature-typed), so no `baseOf` answer describes them and the
// pair spells all three without a per-kind schema.
const classPseudo = classUser + 1

// maxListLen bounds every counted list against a corrupt header: a
// count this large cannot be real data, and refusing it is what keeps a
// truncated buffer from working through an allocation promised by its
// own length field. Row counts are not the schema's bounded numbers, so
// the bound is deliberately far above anything a case holds.
const maxListLen = 1 << 24

// encodeCommitted renders one materialized committed snapshot: the
// state, the stage, and the two verdict tables that sit beside the data
// (store.go says why they are not *in* it). The tables re-dense at the
// decoding image's slot count, which is the only index their readers
// use.
// Each table is written twice over, by count: the table's length (0 is
// the nil table, and a made-but-empty one re-denses at the decoding
// image's slot count, which is all any reader can observe) and then the
// non-zero entries as sparse pairs.
func encodeCommitted(cs committedState) []byte {
	e := encoder{img: cs.s.img}
	e.v(codecVersion)
	e.state(cs.s)
	e.u(uint64(cs.stage))
	e.verdictsR(cs.reviews)
	e.verdictsS(cs.sigs)
	return e.b
}

func (e *encoder) verdictsR(t []reviewState) {
	e.u(uint64(len(t)))
	var n int
	for _, r := range t {
		if r != (reviewState{}) {
			n++
		}
	}
	e.u(uint64(n))
	for i, r := range t {
		if r != (reviewState{}) {
			e.u(uint64(i))
			e.v(r.code)
			e.user(r.by)
			e.u(r.at)
			e.str(r.note)
		}
	}
}

func (e *encoder) verdictsS(t []sigState) {
	e.u(uint64(len(t)))
	var n int
	for _, sg := range t {
		if sg != (sigState{}) {
			n++
		}
	}
	e.u(uint64(n))
	for i, sg := range t {
		if sg != (sigState{}) {
			e.u(uint64(i))
			e.user(sg.by)
			e.u(sg.at)
			e.str(sg.ref)
		}
	}
}

func decodeCommitted(img *program.Program, buf []byte) (committedState, error) {
	d := decoder{buf: buf}
	var ok bool
	var n uint64
	if !d.header() {
		return committedState{}, errCodec
	}
	s, ok := d.state(img)
	if !ok {
		return committedState{}, errCodec
	}
	cs := committedState{s: s}
	var stage uint64
	if stage, ok = d.uvar(); !ok || stage > math.MaxUint32 {
		return committedState{}, errCodec
	}
	cs.stage = program.StageID(stage)
	if n, ok = d.uvar(); !ok {
		return committedState{}, errCodec
	}
	if n > 0 {
		cs.reviews = make([]reviewState, img.SlotCount())
	}
	if n, ok = d.uvar(); !ok || n > maxListLen {
		return committedState{}, errCodec
	}
	for i := uint64(0); i < n; i++ {
		var slot, code uint64
		if slot, ok = d.uvar(); !ok || slot >= uint64(img.SlotCount()) {
			return committedState{}, errCodec
		}
		if code, ok = d.byte(); !ok {
			return committedState{}, errCodec
		}
		var by, note string
		if by, ok = d.str(); !ok {
			return committedState{}, errCodec
		}
		var at uint64
		if at, ok = d.uvar(); !ok {
			return committedState{}, errCodec
		}
		if note, ok = d.str(); !ok {
			return committedState{}, errCodec
		}
		if cs.reviews == nil {
			cs.reviews = make([]reviewState, img.SlotCount())
		}
		cs.reviews[slot] = reviewState{
			code: uint8(code), by: UserID(by), at: at, note: note}
	}
	if n, ok = d.uvar(); !ok {
		return committedState{}, errCodec
	}
	if n > 0 {
		cs.sigs = make([]sigState, img.SlotCount())
	}
	if n, ok = d.uvar(); !ok || n > maxListLen {
		return committedState{}, errCodec
	}
	for i := uint64(0); i < n; i++ {
		var slot uint64
		if slot, ok = d.uvar(); !ok || slot >= uint64(img.SlotCount()) {
			return committedState{}, errCodec
		}
		var by, ref string
		if by, ok = d.str(); !ok {
			return committedState{}, errCodec
		}
		var at uint64
		if at, ok = d.uvar(); !ok {
			return committedState{}, errCodec
		}
		if ref, ok = d.str(); !ok {
			return committedState{}, errCodec
		}
		if cs.sigs == nil {
			cs.sigs = make([]sigState, img.SlotCount())
		}
		cs.sigs[slot] = sigState{by: UserID(by), at: at, ref: ref}
	}
	if !d.done() {
		return committedState{}, errCodec
	}
	return cs, nil
}

// encodeWorking renders one actor's uncommitted copy. The two numbers
// are the guards themselves — §5.8's edit check reads `rev`, §5.8's
// rebase compares `base` — so they travel before the data they protect.
func encodeWorking(w workingState) []byte {
	e := encoder{img: w.s.img}
	e.v(codecVersion)
	e.u(w.base)
	e.u(w.rev)
	e.state(w.s)
	return e.b
}

func decodeWorking(img *program.Program, buf []byte) (workingState, error) {
	d := decoder{buf: buf}
	if !d.header() {
		return workingState{}, errCodec
	}
	base, ok := d.uvar()
	if !ok {
		return workingState{}, errCodec
	}
	rev, ok := d.uvar()
	if !ok {
		return workingState{}, errCodec
	}
	s, ok := d.state(img)
	if !ok || !d.done() {
		return workingState{}, errCodec
	}
	return workingState{base: base, rev: rev, s: s}, nil
}

// encodeRecord renders one history entry, and decodeRecord rebuilds
// it. Entries are what a store must serve back with nothing lost:
// §5.11's read hands the same facts to a caller, and its fold replays
// them, so every field of commitRecord travels — including the
// per-entry stage that makes "revision 7 was at review" knowable from
// the entry alone, and the `ids` that make a rows entry a fact about
// the set and not just its size.
func encodeRecord(img *program.Program, rec commitRecord) []byte {
	e := encoder{img: img}
	e.v(codecVersion)
	e.u(rec.revision)
	e.time(rec.at)
	e.user(rec.by)
	e.v(uint8(rec.op))
	e.u(uint64(rec.stage))
	e.u(uint64(len(rec.changes)))
	for _, ch := range rec.changes {
		e.v(ch.kind)
		e.u(uint64(ch.slot))
		e.u(uint64(len(ch.path)))
		for _, st := range ch.path {
			e.u(uint64(st.coll))
			e.u(uint64(st.id))
		}
		// One payload rule for every kind: a content entry's cell is
		// typed and the pseudo-cells carry the pair, all written by
		// `cell` from the image's table — §5.1's "the reader asks the
		// kind, not the shape" holds in the engine, and the format
		// stays out of that decision entirely.
		e.presence(ch.v.Set)
		if ch.v.Set {
			e.cell(ch.slot, ch.v)
		}
		e.u(uint64(len(ch.ids)))
		for _, id := range ch.ids {
			e.u(uint64(id))
		}
	}
	return e.b
}

func decodeRecord(img *program.Program, buf []byte) (commitRecord, error) {
	d := decoder{buf: buf}
	var ok bool
	if !d.header() {
		return commitRecord{}, errCodec
	}
	var rec commitRecord
	if rec.revision, ok = d.uvar(); !ok {
		return commitRecord{}, errCodec
	}
	if rec.at, ok = d.time(); !ok {
		return commitRecord{}, errCodec
	}
	var by string
	if by, ok = d.str(); !ok {
		return commitRecord{}, errCodec
	}
	rec.by = UserID(by)
	var op uint64
	if op, ok = d.byte(); !ok || op < uint64(opSubmit) || op > uint64(opSign) {
		return commitRecord{}, errCodec
	}
	rec.op = commitOp(op)
	var stage uint64
	if stage, ok = d.uvar(); !ok || stage > math.MaxUint32 {
		return commitRecord{}, errCodec
	}
	rec.stage = program.StageID(stage)
	var n uint64
	if n, ok = d.uvar(); !ok || n > maxListLen {
		return commitRecord{}, errCodec
	}
	for i := uint64(0); i < n; i++ {
		var kind, slot uint64
		if kind, ok = d.byte(); !ok || kind < uint64(chgValue) ||
			kind > uint64(chgSign) {
			return commitRecord{}, errCodec
		}
		if slot, ok = d.uvar(); !ok || slot > math.MaxUint32 {
			return commitRecord{}, errCodec
		}
		ch := commitChange{kind: uint8(kind), slot: program.NodeID(slot)}
		var steps uint64
		if steps, ok = d.uvar(); !ok || steps > maxListLen {
			return commitRecord{}, errCodec
		}
		for j := uint64(0); j < steps; j++ {
			var coll, id uint64
			if coll, ok = d.uvar(); !ok || coll > math.MaxUint32 {
				return commitRecord{}, errCodec
			}
			if id, ok = d.uvar(); !ok {
				return commitRecord{}, errCodec
			}
			ch.path = append(ch.path,
				instStep{coll: program.NodeID(coll), id: instanceID(id)})
		}
		present, ok := d.presence()
		if !ok {
			return commitRecord{}, errCodec
		}
		if present {
			if ch.v, ok = d.cell(img, ch.slot); !ok {
				return commitRecord{}, errCodec
			}
		}
		var ids uint64
		if ids, ok = d.uvar(); !ok || ids > maxListLen {
			return commitRecord{}, errCodec
		}
		for j := uint64(0); j < ids; j++ {
			var id uint64
			if id, ok = d.uvar(); !ok {
				return commitRecord{}, errCodec
			}
			ch.ids = append(ch.ids, instanceID(id))
		}
		rec.changes = append(rec.changes, ch)
	}
	if !d.done() {
		return commitRecord{}, errCodec
	}
	return rec, nil
}

// state renders §5.1's three pieces of data — the base row, the
// instance sets, and the ID counter that keeps them addressable into
// the future.
func (e *encoder) state(s state) {
	e.u(uint64(s.seq))
	e.cells(0, s.row)
	e.u(uint64(len(s.insts)))
	for _, in := range s.insts {
		e.instances(in)
	}
}

func (d *decoder) state(img *program.Program) (state, bool) {
	s := newState(img)
	seq, ok := d.uvar()
	if !ok {
		return state{}, false
	}
	s.seq = instanceID(seq)
	if !d.cells(img, 0, s.row) {
		return state{}, false
	}
	n, ok := d.uvar()
	if !ok || int(n) != len(img.Collections()) {
		return state{}, false
	}
	for i := 0; i < int(n); i++ {
		in, ok := d.instances(img)
		if !ok {
			return state{}, false
		}
		if in != nil {
			// The set must be the one the image has at this position;
			// bytes written under another definition would otherwise
			// decode into a collection whose rows hold the wrong slots.
			if in.coll != img.Collections()[i] {
				return state{}, false
			}
			s.insts[i] = in
		}
	}
	return s, true
}

// instances renders one collection's rows in the order they were
// created, which is the order §5.7 renders them and the order a
// rebase's row-list merge preserves. A set nobody opened encodes as the
// empty marker and decodes to nil: `count` and `find` read nil and an
// empty set alike, and `add` builds a fresh one, so the two states are
// not distinguishable anywhere the format could be observed.
//
// The collection's own slot travels beside it as slot+1 — a nodeID is
// never 0, so the same byte says "no set" and never lies about which
// collection a set is.
func (e *encoder) instances(in *instances) {
	if in == nil {
		e.u(0)
		return
	}
	e.u(uint64(in.coll) + 1)
	e.u(uint64(len(in.rows)))
	for k := range in.rows {
		r := &in.rows[k]
		e.u(uint64(r.id))
		e.cells(e.img.RowLo(in.coll), r.row)
		e.u(uint64(len(r.nest)))
		for _, sub := range r.nest {
			e.instances(sub)
		}
	}
}

func (d *decoder) instances(img *program.Program) (*instances, bool) {
	raw, ok := d.uvar()
	if !ok || raw > math.MaxUint32 {
		return nil, false
	}
	if raw == 0 {
		return nil, true
	}
	coll := program.NodeID(raw - 1)
	if coll >= program.NodeID(img.SlotCount()) {
		return nil, false
	}
	n, ok := d.uvar()
	if !ok || n > maxListLen {
		return nil, false
	}
	in := &instances{coll: coll, index: map[instanceID]int{}}
	for i := uint64(0); i < n; i++ {
		var id uint64
		if id, ok = d.uvar(); !ok {
			return nil, false
		}
		row := instRow{id: instanceID(id), row: make(program.Row, img.RowLen(coll))}
		if !d.cells(img, img.RowLo(coll), row.row) {
			return nil, false
		}
		var depth uint64
		if depth, ok = d.uvar(); !ok || depth > maxListLen {
			return nil, false
		}
		for j := uint64(0); j < depth; j++ {
			sub, ok := d.instances(img)
			if !ok {
				return nil, false
			}
			if sub != nil {
				row.nest = append(row.nest, sub)
			}
		}
		in.index[row.id] = len(in.rows)
		in.rows = append(in.rows, row)
	}
	return in, true
}

// cells renders the set values of one row at the slots the row's own
// address base names: the base row sits at the image's ordinals, a
// collection row at rowLo plus offset — §5.1's addressing restated for
// the wire so slot numbers mean the same thing in bytes and in frames.
func (e *encoder) cells(base program.NodeID, row program.Row) {
	var n int
	for _, v := range row {
		if v.Set {
			n++
		}
	}
	e.u(uint64(n))
	for i, v := range row {
		if v.Set {
			at := base + program.NodeID(i)
			e.u(uint64(at))
			e.cell(at, v)
		}
	}
}

func (d *decoder) cells(img *program.Program, base program.NodeID, row program.Row) bool {
	n, ok := d.uvar()
	if !ok || n > maxListLen {
		return false
	}
	for i := uint64(0); i < n; i++ {
		var slot uint64
		if slot, ok = d.uvar(); !ok {
			return false
		}
		off := int(slot - uint64(base))
		if slot < uint64(base) || off >= len(row) {
			return false
		}
		v, ok := d.cell(img, program.NodeID(slot))
		if !ok {
			return false
		}
		row[off] = v
	}
	return true
}

// cell writes one value's class and payload. The class is the image's:
// `baseOf` for a typed field, `classUser` for a role field — the only
// stored value that is not some type's instance — and `classPseudo`
// for the pair a classless or signature slot holds, which is what
// §5.1's rule that "every reader of a diff asks the kind rather than
// the shape" costs on the wire: one tag, two payloads, no schema per
// kind. The class written for `options` is its storage class: the
// payload is text either way (§3.3), and the decoder accepts both
// spellings so a future split of the two cannot silently change what
// old bytes mean.
func (e *encoder) cell(n program.NodeID, v program.Cell) {
	sl := e.img.Slot(n)
	if sl.Role != 0 {
		e.v(classUser)
		e.str(v.Text)
		return
	}
	switch e.img.BaseOf(n) {
	case 0, program.BaseSignature:
		e.v(classPseudo)
		e.i(v.Int)
		e.str(v.Text)
	case program.BaseString, program.BaseOptions:
		e.v(program.BaseString)
		e.str(v.Text)
	case program.BaseInteger:
		e.v(program.BaseInteger)
		e.i(v.Int)
	case program.BaseDecimal:
		e.v(program.BaseDecimal)
		e.str(v.Decimal.String())
	case program.BaseBoolean:
		e.v(program.BaseBoolean)
		e.v(uint8(v.Int))
	case program.BaseDate:
		e.v(program.BaseDate)
		e.time(v.Time)
	case program.BaseDatetime:
		e.v(program.BaseDatetime)
		e.time(v.Time)
	case program.BaseBinary:
		e.v(program.BaseBinary)
		e.bytes(v.Binary)
	case program.BaseEnum:
		e.v(program.BaseEnum)
		e.u(uint64(v.Int))
	default:
		// A field slot with no value class is not storable; reaching
		// one means a caller offered a cell the image says holds
		// nothing. Write the zero class and let the decoder refuse it.
		e.v(0)
	}
}

func (d *decoder) cell(img *program.Program, n program.NodeID) (program.Cell, bool) {
	cls, ok := d.byte()
	if !ok || n >= program.NodeID(img.SlotCount()) {
		return program.Cell{}, false
	}
	sl := img.Slot(n)
	want := uint8(classUser)
	if sl.Role == 0 {
		want = img.BaseOf(n)
	}
	// Classless and signature slots hold only the pseudo pair; typed
	// slots hold only a type's payload. A byte that crosses the two is
	// bytes from another definition, and this is where it dies.
	if want == 0 || want == program.BaseSignature {
		if uint8(cls) != classPseudo {
			return program.Cell{}, false
		}
		x, ok := d.varint()
		if !ok {
			return program.Cell{}, false
		}
		s, ok := d.str()
		if !ok {
			return program.Cell{}, false
		}
		return program.Cell{Set: true, Int: x, Text: s}, true
	}
	// Options are stored and read as text (§3.3), so the wire does not
	// distinguish them and the check allows either spelling.
	if uint8(cls) == 0 || uint8(cls) != want &&
		!(want == program.BaseOptions && uint8(cls) == program.BaseString) {
		return program.Cell{}, false
	}
	v := program.Cell{Set: true}
	switch want {
	case program.BaseString, program.BaseOptions:
		if v.Text, ok = d.str(); !ok {
			return program.Cell{}, false
		}
	case program.BaseInteger:
		if v.Int, ok = d.varint(); !ok {
			return program.Cell{}, false
		}
	case program.BaseDecimal:
		s, ok := d.str()
		if !ok {
			return program.Cell{}, false
		}
		dd, err := decimal.NewFromString(s)
		if err != nil {
			return program.Cell{}, false
		}
		v.Decimal = dd
	case program.BaseBoolean:
		b, ok := d.byte()
		if !ok || b > 1 {
			return program.Cell{}, false
		}
		v.Int = int64(b)
	case program.BaseDate, program.BaseDatetime:
		if v.Time, ok = d.time(); !ok {
			return program.Cell{}, false
		}
	case program.BaseBinary:
		b, ok := d.bytes()
		if !ok {
			return program.Cell{}, false
		}
		v.Binary = program.BinaryRef(b)
	case program.BaseEnum:
		// The stored payload is §3.3's tagID — a global ordinal — so
		// the check is `store`'s own: the ID must name a tag of this
		// slot's type, which bounds it without guessing the range.
		tag, ok := d.uvar()
		if !ok || !img.TagMatches(tag, sl.Type) {
			return program.Cell{}, false
		}
		v.Int = int64(tag)
	case classUser:
		if v.Text, ok = d.str(); !ok {
			return program.Cell{}, false
		}
	}
	return v, true
}

type encoder struct {
	b   []byte
	img *program.Program
}

func (e *encoder) v(x uint8) { e.b = append(e.b, x) }

func (e *encoder) u(x uint64) { e.b = binary.AppendUvarint(e.b, x) }

func (e *encoder) i(x int64) { e.b = binary.AppendVarint(e.b, x) }

func (e *encoder) str(s string) {
	e.b = binary.AppendUvarint(e.b, uint64(len(s)))
	e.b = append(e.b, s...)
}

func (e *encoder) bytes(b []byte) {
	e.u(uint64(len(b)))
	e.b = append(e.b, b...)
}

func (e *encoder) user(id UserID) { e.str(string(id)) }

// presence marks an optional payload — the `v` of an entry whose kind
// may leave it empty, and nothing else needs it.
func (e *encoder) presence(set bool) {
	if set {
		e.v(1)
	} else {
		e.v(0)
	}
}

func (d *decoder) presence() (bool, bool) {
	x, ok := d.byte()
	if !ok || x > 1 {
		return false, false
	}
	return x == 1, true
}

// time writes an instant and the zone it prints in. A location object
// is process data — a monotonic reading most of all — but the offset
// and its name are part of what a host put there, so both survive and
// the value is rebuilt as a fixed zone of the same name and offset.
func (e *encoder) time(t time.Time) {
	name, off := t.Zone()
	e.i(t.Unix())
	e.u(uint64(t.Nanosecond()))
	e.i(int64(off))
	e.str(name)
}

type decoder struct{ buf []byte }

func (d *decoder) header() bool {
	b, ok := d.byte()
	return ok && b == codecVersion
}

// done is the last guard: a buffer with bytes nobody claimed is either
// corruption or a newer writer, and both refuse.
func (d *decoder) done() bool { return len(d.buf) == 0 }

func (d *decoder) byte() (uint64, bool) {
	if len(d.buf) == 0 {
		return 0, false
	}
	x := d.buf[0]
	d.buf = d.buf[1:]
	return uint64(x), true
}

func (d *decoder) uvar() (uint64, bool) {
	x, n := binary.Uvarint(d.buf)
	if n <= 0 {
		return 0, false
	}
	d.buf = d.buf[n:]
	return x, true
}

func (d *decoder) varint() (int64, bool) {
	x, n := binary.Varint(d.buf)
	if n <= 0 {
		return 0, false
	}
	d.buf = d.buf[n:]
	return x, true
}

func (d *decoder) str() (string, bool) {
	b, ok := d.bytes()
	return string(b), ok
}

func (d *decoder) bytes() ([]byte, bool) {
	n, ok := d.uvar()
	if !ok || n > uint64(len(d.buf)) {
		return nil, false
	}
	// The copy detaches the decoded state from the caller's buffer: a
	// host that reuses its read buffer must not be able to write over a
	// case that is currently open.
	out := make([]byte, n)
	copy(out, d.buf[:n])
	d.buf = d.buf[n:]
	return out, true
}

func (d *decoder) time() (time.Time, bool) {
	secs, ok := d.varint()
	if !ok {
		return time.Time{}, false
	}
	nanos, ok := d.uvar()
	if !ok {
		return time.Time{}, false
	}
	off, ok := d.varint()
	if !ok {
		return time.Time{}, false
	}
	name, ok := d.str()
	if !ok {
		return time.Time{}, false
	}
	if secs == -62135596800 && nanos == 0 && off == 0 && name == "UTC" {
		// The year-one zero time, rebuilt exactly — down to its nil
		// location. `same` asks Equal, but a host's `IsZero` on a
		// read-back value must agree with what was written, and the
		// zero value's internals are not reachable any other way.
		return time.Time{}, true
	}
	t := time.Unix(secs, int64(nanos))
	if off == 0 && name == "UTC" {
		return t.UTC(), true
	}
	return t.In(time.FixedZone(name, int(off))), true
}
