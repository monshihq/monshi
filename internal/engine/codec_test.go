package engine

// In-package tests for the wire format. Three claims, one per test. The
// scripted case: every snapshot, draft and history entry a real eleven-commit
// run produced must survive bytes — the fold's fixture is the broadest state
// this package can build without inventing one. The hand-built cell: every
// class and every shape a getter reads must survive as its own value, because
// the scripted fixture leaves the rarer payloads (negative numbers, empty text,
// a nil binary) to chance. And the hostile buffer: truncated, versioned-wrong
// or trailed bytes must refuse as one error, never panic and never half-decode
// — a store that returns corrupt bytes is a fact of life (the failure modes),
// and this is the code standing between it and a state whose conditions would
// then run against invented slots.

import (
	"bytes"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// codecSrc states every cell class the row can hold, including the two
// no operation ever writes by accident: a role-typed field (its value
// is an identity, not a type's instance) and a `binary` (whose handle
// the engine carries but never reads). `off` distinguishes a stored
// false from a cleared field, `when` a date from a datetime.
const codecSrc = `
roles {
    Keeper {
        allow view, edit, submit box;
    }
}
forms {
    box {
        note  : string;
        off   : boolean;
        num   : integer;
        price : decimal(9,2);
        day   : date;
        when  : datetime;
        choice : enum(ONE, TWO, THREE);
        pick   : options;
        file   : binary;
        holder : Keeper;

        items multiple {
            label : string;
            parts multiple {
                qty : integer;
            }
        }
    }
}
stages {
    open {
    }
}
`

func TestCodecScriptedCase(t *testing.T) {
	h, trail, snaps := histTrail(t)
	for i, rec := range trail {
		buf := encodeRecord(h.img, rec)
		got, err := decodeRecord(h.img, buf)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if again := encodeRecord(h.img, got); !bytes.Equal(buf, again) {
			t.Errorf("record %d does not re-encode byte-identically", i)
		}
		if !sameRecord(h.img, rec, got) {
			t.Errorf("record %d decoded wrong: %+v", i, got)
		}
	}
	for i, cs := range snaps {
		buf := encodeCommitted(cs)
		got, err := decodeCommitted(h.img, buf)
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		if again := encodeCommitted(got); !bytes.Equal(buf, again) {
			t.Errorf("snapshot %d does not re-encode byte-identically", i)
		}
		if !sameCommitted(h.img, cs, got) {
			t.Errorf("snapshot %d decoded wrong", i)
		}
	}
	// The drafts: the applicant's copy mid-batch, and the others'.
	rec := stored(t, h.rt, h.dana)
	if len(rec.work) == 0 {
		t.Fatal("scripted case has no drafts to test")
	}
	for who, w := range rec.work {
		buf := encodeWorking(*w)
		got, err := decodeWorking(h.img, buf)
		if err != nil {
			t.Fatalf("draft %s: %v", who, err)
		}
		if again := encodeWorking(got); !bytes.Equal(buf, again) {
			t.Errorf("draft %s does not re-encode byte-identically", who)
		}
		if w.base != got.base || w.rev != got.rev ||
			!sameState(h.img, w.s, got.s) {
			t.Errorf("draft %s decoded wrong", who)
		}
	}
}

// TestCodecEveryClass writes one of every payload — the edges the
// scripted run never produces — into a hand-built state, and asks of
// each that the decoded cell loads back as the same value.
func TestCodecEveryClass(t *testing.T) {
	img := mustLowerSource(t, codecSrc)
	st := newState(img)
	st.applyDefaults()
	when := time.Date(2026, 11, 24, 9, 30, 45, 123456789,
		time.FixedZone("TST", 5*3600+1800))
	day := time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC)
	want := map[string]Value{
		"box.note":   Text(""),
		"box.off":    Bool(false),
		"box.num":    Int(math.MinInt64),
		"box.price":  Decimal(mustDec(t, "0.00")),
		"box.day":    Date(day),
		"box.when":   DateTime(when),
		"box.choice": Tag("THREE"),
		"box.pick":   Text("custom"),
		"box.file":   Binary([]byte("blob-7")),
		"box.holder": User("carla"),
	}
	for p, v := range want {
		n := slotByPath(t, img, p)
		c, ok := st.cellOf(n)
		if !ok {
			t.Fatalf("cellOf(%s): no cell", p)
		}
		var sv program.Cell
		var msg string
		if v.class == classUser {
			sv, msg = storeRole(img, n, v)
		} else {
			sv, msg = store(img, n, v)
		}
		if msg != "" {
			t.Fatalf("store(%s): %s", p, msg)
		}
		c.set(sv)
	}
	// An untouched sibling is the control on the claim that an empty
	// text is a value and not absence: `box.note` holds "" and decodes
	// as "", while no other base cell exists at all.

	// Two rows, the inner one addressed through the outer — the nesting
	// the encode recursion must not flatten.
	outer, _, ok := st.addAt(slotByPath(t, img, "box.items"), nil)
	if !ok {
		t.Fatal("add outer")
	}
	ipath := instPath{{coll: slotByPath(t, img, "box.items"), id: outer}}
	inner, _, ok := st.addAt(slotByPath(t, img, "box.items.parts"), ipath)
	if !ok {
		t.Fatal("add inner")
	}
	oc := mustCellAt(t, &st, slotByPath(t, img, "box.items.label"), ipath)
	oc.set(mustStore(t, img, slotByPath(t, img, "box.items.label"),
		Text("first")))
	ic := mustCellAt(t, &st,
		slotByPath(t, img, "box.items.parts.qty"),
		append(ipath, instStep{coll: slotByPath(t, img,
			"box.items.parts"), id: inner}))
	ic.set(mustStore(t, img, slotByPath(t, img, "box.items.parts.qty"),
		Int(-7)))

	cs := committedState{s: st, stage: img.InitialStage()}
	buf := encodeCommitted(cs)
	got, err := decodeCommitted(img, buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if again := encodeCommitted(got); !bytes.Equal(buf, again) {
		t.Fatal("hand-built state does not re-encode byte-identically")
	}
	if !sameState(img, st, got.s) {
		t.Fatal("hand-built state decoded wrong")
	}
	// The claim a reader actually makes: the decoded cell loads back as
	// the value that went in, edges included.
	for p, v := range want {
		n := slotByPath(t, img, p)
		c, _ := got.s.cellOf(n)
		back := load(img, n, c.get())
		if !sameValue(n, v, back) {
			t.Errorf("%s: got %#v back", p, back)
		}
	}

	// One shape the fixture never holds: a *set* cell whose binary
	// handle is nil — present, and naming no content at all. It must
	// survive as present, because the emptiness test asks length
	// and the length must not have been confused with absence.
	fn := slotByPath(t, img, "box.file")
	cf, _ := st.cellOf(fn)
	cf.set(mustStore(t, img, fn, Binary(nil)))
	buf2 := encodeCommitted(committedState{s: st, stage: img.InitialStage()})
	got2, err := decodeCommitted(img, buf2)
	if err != nil {
		t.Fatalf("empty handle: %v", err)
	}
	if !bytes.Equal(buf2, encodeCommitted(got2)) ||
		!sameState(img, st, got2.s) {
		t.Error("the nil handle's presence did not survive")
	}
	c2, _ := got2.s.cellOf(fn)
	if !c2.get().Set || len(c2.get().Binary) != 0 {
		t.Error("nil handle decoded as absence or as content")
	}
}

// TestCodecRefusals: corrupt bytes fail closed. Every prefix of a real
// buffer shorter than its length, a foreign version, trailing junk and
// plain nonsense all answer errCodec, and nothing panics.
func TestCodecRefusals(t *testing.T) {
	img := mustLowerSource(t, codecSrc)
	st := newState(img)
	st.applyDefaults()
	cs := committedState{s: st, stage: img.InitialStage()}
	buf := encodeCommitted(cs)
	for _, cut := range prefixes(len(buf)) {
		if _, err := decodeCommitted(img, buf[:cut]); err == nil {
			t.Errorf("truncation at %d/%d decoded", cut, len(buf))
		}
	}
	if cut := buf[:len(buf)-1]; len(cut) > 0 {
		if _, err := decodeCommitted(img, append(cut[:len(cut):len(cut)],
			0xFF, 0x01)); err == nil {
			t.Error("trailing bytes decoded")
		}
	}
	bad := append([]byte{codecVersion + 1}, buf[1:]...)
	if _, err := decodeCommitted(img, bad); err == nil {
		t.Error("foreign version decoded")
	}
	junk := [][]byte{nil, {}, {0x01}, {0x01, 0x80},
		bytes.Repeat([]byte{0xFF}, 64)}
	for _, b := range junk {
		if _, err := decodeCommitted(img, b); err == nil {
			t.Errorf("junk %v decoded", b)
		}
	}
	rec := commitRecord{
		revision: 3, at: time.Now(), by: "dana", op: opSubmit,
		stage: img.InitialStage(),
		changes: []commitChange{
			{kind: chgValue, slot: slotByPath(t, img, "box.note"),
				v: program.Cell{Set: true, Text: "x"}},
			{kind: chgRows, slot: slotByPath(t, img, "box.items"),
				ids: []instanceID{5, 6}},
			{kind: chgReview, slot: slotByPath(t, img, "box")},
			{kind: chgSign, slot: slotByPath(t, img, "box")},
		},
	}
	rbuf := encodeRecord(img, rec)
	if _, err := decodeRecord(img, rbuf); err != nil {
		t.Fatal(err)
	}
	for _, cut := range prefixes(len(rbuf)) {
		if _, err := decodeRecord(img, rbuf[:cut]); err == nil {
			t.Errorf("record truncation at %d/%d decoded", cut, len(rbuf))
		}
	}
	if _, err := decodeRecord(img, append(rbuf, 0x00)); err == nil {
		t.Error("trailed record decoded")
	}
	wbuf := encodeWorking(workingState{base: 2, rev: 5, s: st})
	for _, cut := range prefixes(len(wbuf)) {
		if _, err := decodeWorking(img, wbuf[:cut]); err == nil {
			t.Errorf("working truncation at %d decoded", cut)
		}
	}
	if _, err := decodeWorking(img, append(wbuf, 0x01)); err == nil {
		t.Error("trailed working decoded")
	}

	// Crafted section headers: a slot number the image does not have,
	// at each of the three places a count can promise one. These must
	// refuse, and refusing must not mean a panic on the write.
	nSlots := byte(img.SlotCount())
	revs := []byte{legacyVersion,
		0,               // seq
		0,               // base cells
		2, 13, 0, 15, 0, // two empty sets, the image's own ordinals
		1,                          // stage
		5,                          // reviews table length
		1, nSlots, 1, 1, 'x', 0, 0, // one pair, at a slot past the image
		0, 0, // sigs: no table, no pairs
	}
	if _, err := decodeCommitted(img, revs); err == nil {
		t.Error("out-of-image review slot decoded")
	}
	sigs := []byte{legacyVersion,
		0, 0, 2, 13, 0, 15, 0, 1,
		0, 0, // reviews: absent
		5, 1, nSlots, 1, 'x', 0, 0, // a signature at no slot
	}
	if _, err := decodeCommitted(img, sigs); err == nil {
		t.Error("out-of-image signature slot decoded")
	}
	cellsBuf := []byte{legacyVersion,
		0,     // seq
		1, 63, // one base cell, at a slot past the image
		2, 13, 0, 15, 0,
		1, 0, 0, 0, 0,
	}
	if _, err := decodeCommitted(img, cellsBuf); err == nil {
		t.Error("out-of-image base cell decoded")
	}
	// A history entry at a slot the image does not have. Entries are
	// decoded without a row to bound them, so the cell's own check is
	// the one that must refuse.
	recSlot := []byte{legacyVersion, 1,
		0, 0, 0, 3, 'U', 'T', 'C', // the instant, 1970 at UTC
		4, 'd', 'a', 'n', 'a', // the actor
		1, 1, // op and stage
		1, chgValue, byte(img.SlotCount()), 0, // one entry, past the image
		1, program.BaseString, 0, 0, // a cell, an empty text, no IDs
	}
	if _, err := decodeRecord(img, recSlot); err == nil {
		t.Error("out-of-image entry decoded")
	}
}

// prefixes are the truncation points past the version byte, where a
// refusal is the only acceptable answer; cut 0 is refused by the
// version check itself and included for the panic guard.
func prefixes(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

func mustDec(tb testing.TB, text string) decimal.Decimal {
	tb.Helper()
	d, err := decimal.NewFromString(text)
	if err != nil {
		tb.Fatalf("decimal %q: %v", text, err)
	}
	return d
}

func mustCellAt(tb testing.TB, st *state, n program.NodeID, path instPath) cell {
	tb.Helper()
	c, ok := st.cellAt(n, path)
	if !ok {
		tb.Fatalf("cellAt(%d, %v): not addressable", n, path)
	}
	return c
}

func mustStore(tb testing.TB, img *program.Program, n program.NodeID, v Value) program.Cell {
	tb.Helper()
	sv, msg := store(img, n, v)
	if msg != "" {
		tb.Fatalf("store: %s", msg)
	}
	return sv
}

// sameTime is what the format promises for an instant: equality of the
// moment and of the zone it displays in, and nothing else. The
// monotonic reading deliberately does not qualify — it belongs to the
// process that took it, and a durable timestamp is compared by its
// wall half, which is what RFC3339Nano renders.
func sameTime(a, b time.Time) bool {
	an, ao := a.Zone()
	bn, bo := b.Zone()
	return a.Equal(b) && a.IsZero() == b.IsZero() &&
		an == bn && ao == bo &&
		a.UTC().Format(time.RFC3339Nano) == b.UTC().Format(time.RFC3339Nano)
}

func sameValue(n program.NodeID, a, b Value) bool {
	if a.class != b.class {
		return false
	}
	if a.class == 0 {
		// The untyped rows-count cell: load answers the empty Value for
		// both sides, so the only observable difference is the ordinal.
		return a.i == b.i
	}
	switch a.class {
	case classText, classTag, classOpt:
		return a.s == b.s
	case classInt:
		return a.i == b.i
	case classBool:
		return a.i == b.i
	case classDec:
		// Text equality, not Equal: the format promises the written
		// scale survives, and a lossy render is exactly the bug.
		return a.d.String() == b.d.String()
	case classDate, classDT:
		return sameTime(a.t, b.t)
	case classBin:
		return string(a.bin) == string(b.bin)
	case classUser:
		return a.s == b.s
	}
	return false
}

func sameCell(img *program.Program, n program.NodeID, a, b program.Cell) bool {
	if a.Set != b.Set {
		return false
	}
	if !a.Set {
		return true
	}
	if img.Slot(n).Role == 0 {
		switch img.BaseOf(n) {
		case 0, program.BaseSignature:
			// Pseudo-cells — the rows count, the decision code, the
			// signing reference. `load` answers the empty Value for
			// them, so the payload is compared where it lives.
			return a.Int == b.Int && a.Text == b.Text
		}
	}
	return sameValue(n, load(img, n, a), load(img, n, b))
}

func sameRow(img *program.Program, base program.NodeID, a, b program.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameCell(img, base+program.NodeID(i), a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameState(img *program.Program, a, b state) bool {
	if a.seq != b.seq || !sameRow(img, 0, a.row, b.row) {
		return false
	}
	if len(a.insts) != len(b.insts) {
		return false
	}
	for i := range a.insts {
		if !sameInstances(img, a.insts[i], b.insts[i]) {
			return false
		}
	}
	return true
}

// sameInstances compares by the readers' questions: count, collation,
// rows. A nil set and an empty one are equal here because they are
// equal to the engine — `count`, `find` and `add` treat them alike —
// and a format that distinguished them would be encoding a Go choice.
func sameInstances(img *program.Program, a, b *instances) bool {
	if a.count() != b.count() {
		return false
	}
	if a.count() == 0 {
		return true
	}
	if a.coll != b.coll {
		return false
	}
	for i := range a.rows {
		ar, br := &a.rows[i], &b.rows[i]
		if ar.id != br.id ||
			!sameRow(img, img.RowLo(a.coll), ar.row, br.row) ||
			len(ar.nest) != len(br.nest) {
			return false
		}
		for j := range ar.nest {
			if !sameInstances(img, ar.nest[j], br.nest[j]) {
				return false
			}
		}
	}
	return true
}

func sameCommitted(img *program.Program, a, b committedState) bool {
	return a.stage == b.stage && sameState(img, a.s, b.s) &&
		reflect.DeepEqual(a.reviews, b.reviews) &&
		reflect.DeepEqual(a.sigs, b.sigs)
}

func sameRecord(img *program.Program, a, b commitRecord) bool {
	if a.revision != b.revision || a.by != b.by || a.op != b.op ||
		a.stage != b.stage || !sameTime(a.at, b.at) ||
		len(a.changes) != len(b.changes) {
		return false
	}
	for i := range a.changes {
		ac, bc := &a.changes[i], &b.changes[i]
		if ac.kind != bc.kind || ac.slot != bc.slot ||
			!reflect.DeepEqual(ac.path, bc.path) ||
			ac.decision != bc.decision || ac.note != bc.note || !sameCell(img, ac.slot, ac.v, bc.v) ||
			!reflect.DeepEqual(ac.ids, bc.ids) {
			return false
		}
	}
	return true
}
