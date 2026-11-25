package engine

// The storage contract seen from inside (DESIGN §8, RUNTIME §10): the two
// implementations of `caseBackend` asked the same questions, with the same
// refuser, over the same definition — and answered with the same bytes of
// refusal. What the adapter exists to guarantee is that a refusal does
// not announce which backend served the call; that is a claim about
// characters, and it can only be graded by comparing characters. The
// transcript test in storage_test.go reads the same fact from the
// exported side; what lives here is the struct-level check, the store
// shape the adapter hands back, and the clock rule.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/monshihq/monshi/internal/program"
)

// pairSrc is the smallest definition whose data crosses every wire class
// the codec carries on a plain value path: text and integer.
const pairSrc = `
roles {
    Applicant { allow view, edit, submit application; }
}
forms {
    application {
        name  : string;
        score : integer;
    }
}
stages {
    start { accept done; }
    done  { }
}
`

// pairBackends is the seam served two ways over one shape: the in-memory
// store the default runtime has always run on, and the adapter over
// fresh `MemStorage` with the same definition registered — and one fixed
// clock, so a commit record from each can be compared down to its
// stamp. The clock is the point of the field: `at` is data (§5.11), and
// two backends that disagree about the moment would be disagreeing about
// the trail.
func pairBackends(t *testing.T) (*Definition, *caseStore, *hostBackend) {
	t.Helper()
	d := NewDefinition(mustLower(t, "pair", pairSrc), nil)
	defs := newRegistry()
	if err := defs.add(newRefuser("register", 0), d, false); err != nil {
		t.Fatalf("register: %v", err)
	}
	fixed := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	mem := newCaseStore()
	mem.now = func() time.Time { return fixed }
	ms := NewMemStorage()
	host := &hostBackend{st: ms, defs: defs,
		now: func() time.Time { return fixed }}
	return d, mem, host
}

// sameRefusals runs one call against both backends and compares the
// refusals as structs — Op, Reason, Case, Detail and Cause together —
// which is a stricter reading of "character identical" than the sentence
// alone: a field that differs below Error() is still a field a host can
// branch on.
func sameRefusals(t *testing.T, name string, memErr, hostErr error) {
	t.Helper()
	if (memErr == nil) != (hostErr == nil) {
		t.Fatalf("%s: in-memory %v, contract %v — one refused and one did"+
			" not", name, memErr, hostErr)
	}
	if memErr == nil {
		return
	}
	var a, b *CaseError
	if !errors.As(memErr, &a) || !errors.As(hostErr, &b) {
		t.Fatalf("%s: refusal is not a CaseError on both paths: %v / %v",
			name, memErr, hostErr)
	}
	if *a != *b {
		t.Errorf("%s: the two backends refused differently:\n in-memory"+
			" %+v\n  contract %+v", name, *a, *b)
	}
}

func TestTheTwoBackendsRefuseWithOneVoice(t *testing.T) {
	d, mem, host := pairBackends(t)
	bg := context.Background()
	mID, err := mem.create(bg, newRefuser("create", 0), d)
	if err != nil {
		t.Fatalf("in-memory create: %v", err)
	}
	hID, err := host.create(bg, newRefuser("create", 0), d)
	if err != nil {
		t.Fatalf("contract create: %v", err)
	}
	if mID != hID {
		t.Fatalf("the stores issued %d and %d for the first case; the"+
			" comparison below assumes the same ID", mID, hID)
	}
	img := d.img
	none := newRefuser("open", 404)
	steps := []struct {
		name string
		mem  func() error
		host func() error
	}{
		{"load an unissued ID",
			func() error { _, e := mem.load(bg, none); return e },
			func() error { _, e := host.load(bg, none); return e }},
		{"open a case that is gone",
			func() error {
				_, e := mem.working(bg, newRefuser("open", 404),
					"dana", img)
				return e
			},
			func() error {
				_, e := host.working(bg, newRefuser("open", 404),
					"dana", img)
				return e
			}},
		{"rebase over a case that is gone",
			func() error {
				_, _, e := mem.draft(bg, newRefuser("rebase", 404),
					"dana", img)
				return e
			},
			func() error {
				_, _, e := host.draft(bg, newRefuser("rebase", 404),
					"dana", img)
				return e
			}},
		{"read a page of a case that is gone",
			func() error {
				_, e := mem.history(bg, newRefuser("history", 404),
					1, 0)
				return e
			},
			func() error {
				_, e := host.history(bg, newRefuser("history", 404),
					1, 0)
				return e
			}},
		{"write over a copy that was never opened",
			func() error {
				_, e := mem.saveWorking(bg, newRefuser("update", mID),
					"dana", 0, workingState{}, 0)
				return e
			},
			func() error {
				_, e := host.saveWorking(bg, newRefuser("update", hID),
					"dana", 0, workingState{}, 0)
				return e
			}},
		{"discard a copy that is not there",
			func() error {
				return mem.discard(bg, newRefuser("discard", mID), "dana", 0)
			},
			func() error {
				return host.discard(bg, newRefuser("discard", hID), "dana", 0)
			}},
		{"discard under a number nobody held",
			func() error {
				return mem.discard(bg, newRefuser("discard", mID), "dana", 5)
			},
			func() error {
				return host.discard(bg, newRefuser("discard", hID), "dana", 5)
			}},
		{"commit against a revision the case never reached",
			func() error {
				_, e := mem.commit(bg, newRefuser("submit", mID), img, 7,
					initialState(img), committedBy{by: "dana", op: opSubmit})
				return e
			},
			func() error {
				_, e := host.commit(bg, newRefuser("submit", hID), img, 7,
					initialState(img), committedBy{by: "dana", op: opSubmit})
				return e
			}},
	}
	for _, tc := range steps {
		sameRefusals(t, tc.name, tc.mem(), tc.host())
	}

	// The same two guards after the copy exists, so the refusals that
	// depend on a real number are compared on one: both backends now
	// hold dana's draft at revision 0.
	mw, err := mem.working(bg, newRefuser("update", mID), "dana", img)
	if err != nil {
		t.Fatalf("in-memory working: %v", err)
	}
	hw, err := host.working(bg, newRefuser("update", hID), "dana", img)
	if err != nil {
		t.Fatalf("contract working: %v", err)
	}
	if mw.rev != hw.rev || mw.base != hw.base {
		t.Fatalf("the fresh copies disagree: {base %d rev %d} versus {%d"+
			" %d}", mw.base, mw.rev, hw.base, hw.rev)
	}
	sameRefusals(t, "save under a stale expected",
		func() error {
			_, e := mem.saveWorking(bg, newRefuser("update", mID), "dana", 4,
				mw, 0)
			return e
		}(),
		func() error {
			_, e := host.saveWorking(bg, newRefuser("update", hID), "dana", 4,
				hw, 0)
			return e
		}())
	// And the defect guard §5.8 pays for a rebase lineage no caller of
	// this package can reach: same numbers, same SCHEMA_MISMATCH, on a
	// path only in-package tests (and the `Rebase` arithmetic) can walk.
	sameRefusals(t, "move a lineage outside the trail",
		func() error {
			_, e := mem.saveWorking(bg, newRefuser("rebase", mID), "dana", 0,
				mw, 5)
			return e
		}(),
		func() error {
			_, e := host.saveWorking(bg, newRefuser("rebase", hID), "dana", 0,
				hw, 5)
			return e
		}())
}

// runTheCommit drives the one accepted lifecycle both backends can be
// asked to share — open the copy, edit it, save it, commit it — and
// returns what each store handed back, so the *accepted* answers are
// compared with the same rigour as the refused ones: the record the
// trail holds, the snapshot that landed, the committer's copy number and
// the erased set the effects found.
func TestTheTwoBackendsCommitOneRecord(t *testing.T) {
	d, mem, host := pairBackends(t)
	bg := context.Background()
	img := d.img
	mID, _ := mem.create(bg, newRefuser("create", 0), d)
	hID, _ := host.create(bg, newRefuser("create", 0), d)

	type outcome struct {
		got   committed
		rev   uint64
		draft workingState
		trail []commitRecord
	}
	run := func(b caseBackend, id CaseID) outcome {
		w, err := b.working(bg, newRefuser("update", id), "dana", img)
		if err != nil {
			t.Fatalf("working(%d): %v", id, err)
		}
		val := program.Cell{Set: true, Text: "Ada", Int: 0}
		cand := w.clone()
		put(t, img, &cand.s, "application.name", val)
		rev, err := b.saveWorking(bg, newRefuser("update", id), "dana",
			w.rev, cand, 0)
		if err != nil {
			t.Fatalf("saveWorking(%d): %v", id, err)
		}
		next := committedState{s: cand.s.clone(), stage: img.InitialStage()}
		got, err := b.commit(bg, newRefuser("submit", id), img, 0, next,
			committedBy{by: "dana", op: opSubmit,
				changes: []commitChange{{kind: chgValue,
					slot: held(t, img, "application.name"),
					v:    val}}})
		if err != nil {
			t.Fatalf("commit(%d): %v", id, err)
		}
		trail, err := b.history(bg, newRefuser("history", id), 1, 0)
		if err != nil {
			t.Fatalf("history(%d): %v", id, err)
		}
		draft, _, err := b.draft(bg, newRefuser("rebase", id), "dana", img)
		if err != nil {
			t.Fatalf("draft(%d): %v", id, err)
		}
		return outcome{got: got, rev: rev, draft: draft, trail: trail}
	}
	a, b := run(mem, mID), run(host, hID)

	if a.rev != b.rev {
		t.Fatalf("the two stores numbered the draft %d and %d", a.rev, b.rev)
	}
	if !sameRecord(img, a.got.rec, b.got.rec) {
		t.Errorf("the trail disagrees: in-memory %+v, contract %+v",
			a.got.rec, b.got.rec)
	}
	if !sameCommitted(img, a.got.post, b.got.post) {
		t.Errorf("the installed snapshot disagrees")
	}
	if a.got.copy != b.got.copy {
		t.Errorf("the committer's copy number: %d versus %d", a.got.copy,
			b.got.copy)
	}
	if !reflect.DeepEqual(a.got.erased, b.got.erased) {
		t.Errorf("the effects erased %v on one path and %v on the other",
			a.got.erased, b.got.erased)
	}
	if len(a.trail) != 1 || len(b.trail) != 1 ||
		!sameRecord(img, a.trail[0], b.trail[0]) {
		t.Errorf("the trail read back disagrees: %v versus %v", a.trail,
			b.trail)
	}
	if a.draft.base != b.draft.base || a.draft.rev != b.draft.rev ||
		!sameState(img, a.draft.s, b.draft.s) {
		t.Errorf("the committer's rebased draft disagrees: {base %d rev %d}"+
			" versus {%d %d}", a.draft.base, b.draft.rev, b.draft.base,
			b.draft.rev)
	}
}

// Both backends return the same case head without reading history or drafts.
func TestBackendsReturnSameCaseHead(t *testing.T) {
	d, mem, host := pairBackends(t)
	bg := context.Background()
	mID, _ := mem.create(bg, newRefuser("create", 0), d)
	hID, _ := host.create(bg, newRefuser("create", 0), d)
	mrec, err := mem.load(bg, newRefuser("open", mID))
	if err != nil {
		t.Fatalf("mem load: %v", err)
	}

	hrec, err := host.load(bg, newRefuser("open", hID))
	if err != nil {
		t.Fatalf("host load: %v", err)
	}

	if hrec.def != mrec.def || hrec.revision != mrec.revision || !sameCommitted(d.img, hrec.commit, mrec.commit) {
		t.Errorf("the records disagree on what a case *is* at create:"+
			" %+v versus %+v", mrec, hrec)
	}
}

// TestCodecRefusalIsAStoreFailure reaches the decoder's own sentinel: a
// store whose bytes are not this package's format fails as STORAGE_
// FAILURE with errCodec under the Cause — the same translation from the
// inside, where the sentinel itself is nameable.
func TestCodecRefusalIsAStoreFailure(t *testing.T) {
	d, _, host := pairBackends(t)
	bg := context.Background()
	id, err := host.create(bg, newRefuser("create", 0), d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ms := host.st.(*MemStorage)
	ms.cases[id].rec.State = []byte("someone else's bytes")
	_, err = host.load(bg, newRefuser("open", id))
	var ce *CaseError
	if !errors.As(err, &ce) || ce.Reason != StorageFailure {
		t.Fatalf("load over foreign bytes = %v, want STORAGE_FAILURE", err)
	}
	if !errors.Is(err, errCodec) {
		t.Errorf("the refusal did not carry the codec's complaint as"+
			" Cause: %v", err)
	}
}
