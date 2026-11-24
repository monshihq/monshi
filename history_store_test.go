package monshi

// In-package tests for the trail a case leaves and the state the fold derives
// from it (RUNTIME §5.11, §4.2's replay; DESIGN §5.1's instance addresses). What
// a host observes — the page, the cursor, the mask, the refusals — is asserted
// externally in history_test.go. This file asserts the five claims no host call
// reaches: that folding the entries forward lands on the snapshot the store kept
// at every revision of a scripted case, that an erasure derives without being
// recorded, that a removal neither resurrects a row nor erases a surviving one,
// that the window a reader asks for is a slice of the trail and not a copy of it,
// and that the two things a replay cannot recover are tests rather than promises.
//
// The fixture is the interesting part. A trail that never held a retraction, a
// removed row, a row inside a row and a stage move would let a fold that ignores
// all four pass, so the script below commits each of them — and the equivalence
// test replays *every* revision of it, because one comparison at the head proves
// only that the errors cancel.

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// histSrc states every shape the derivation needs in one definition: a
// `verifiable` form whose default nobody overwrites, a decimal, a `multiple`
// group with a `multiple` inside it, a `required` signature whose role the signer
// actually holds, a second form a second hand commits, and a stage a third hand
// reaches by an edge. The `Auditor` exists for the exported read's mask, which
// history_test.go asserts; the fold never consults it.
const histSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
    amount     : decimal(14,2);
    dateday    : date;
}
roles {
    Applicant {
        allow view, edit, submit application;
        allow sign application.submitted;
    }
    Registrar {
        allow view all;
        allow review application;
    }
    Clerk {
        allow view, edit, submit archive;
    }
    Auditor {
        allow view application.pages;
    }
}
forms {
    application verifiable {
        kind      : legal_form required;
        full_name : string required;
        fee       : amount default 1.00;
        stamp     : string default "created";

        pages multiple {
            note : string;

            marks multiple {
                day : dateday;
            }
        }

        submitted : signature(Applicant) required;
    }

    archive {
        filed_at : string;
    }
}
stages {
    draft {
        allow Registrar accept;
        accept filed;
    }

    filed {
        require application;
    }
}
`

var (
	histOwner = Actor{User: "dana", PrimaryRole: "Applicant"}
	histRuler = Actor{User: "elliot", PrimaryRole: "Registrar"}
	histClerk = Actor{User: "bo", PrimaryRole: "Clerk"}
)

// hist is one runtime, its image, and the four hands on one case: the applicant
// who types and attests, the registrar who decides and moves the case along an
// edge, the clerk who commits the other form, and the auditor whose whole grant
// is one closed subtree.
type hist struct {
	rt     *Runtime
	img    *image
	dana   Case
	elliot Case
	clerk  Case

	pages []string // the outer rows histTrail created, in creation order
	marks []string // the inner rows, one per page, in the same order
}

// histSetup lowers the fixture and creates the case the applicant owns.
func histSetup(t testing.TB) *hist {
	t.Helper()
	h := &hist{rt: NewRuntime()}
	d := &Definition{img: mustLowerSource(t, histSrc)}
	if err := h.rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.img = d.img
	c, err := h.rt.Create(ctx(), d.ID(), histOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h.dana = c
	for _, a := range []Actor{histRuler, histClerk} {
		who, err := h.rt.Open(ctx(), c.ID(), a)
		if err != nil {
			t.Fatalf("Open(%s): %v", a.User, err)
		}
		if a.User == "elliot" {
			h.elliot = who
		} else {
			h.clerk = who
		}
	}
	return h
}

// save is a batch in the actor's own copy: the rebase first, because a script
// that commits through three hands is a script whose drafts fall behind, and then
// the `Update` whose token §6.2's guard presents to a commit. It answers with the
// whole result, because a fixture that guessed an instance ID would be asserting
// about a row the case may not hold.
func (h *hist) save(t testing.TB, who Case, edits ...Edit) ChangeResult {
	t.Helper()
	if _, err := who.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	w, err := who.w.cases.working(newRefuser(open_, who.id), who.actor.User,
		h.img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	res, err := who.Update(ctx(), w.rev, edits...)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return res
}

// commit is a batch saved and then committed as one form, in the two steps §6.3's
// diff requires. It checks that the revision the case reached is the one the
// script expected, so that a claim about "revision 7" means the seventh commit.
func (h *hist) commit(t testing.TB, who Case, form string,
	edits ...Edit) ChangeResult {
	t.Helper()
	before := h.head(t)
	res := h.save(t, who, edits...)
	if _, err := who.Submit(ctx(), form, res.Working); err != nil {
		t.Fatalf("Submit(%s): %v", form, err)
	}
	if at, want := h.head(t), before+1; at != want {
		t.Fatalf("the commit reached revision %d, want %d", at, want)
	}
	return res
}

// head is what the case holds now, read from the store rather than remembered.
func (h *hist) head(t testing.TB) uint64 {
	t.Helper()
	return stored(t, h.rt, h.dana).revision
}

// snap is the snapshot the store kept, which is the half of every equivalence
// claim below that the fold is not allowed to have written.
func (h *hist) snap(t testing.TB) committedState {
	t.Helper()
	return stored(t, h.rt, h.dana).commit
}

// trail is the store's own list of what happened, and the input the fold consumes.
func (h *hist) trail(t testing.TB) []commitRecord {
	t.Helper()
	all, err := h.dana.w.cases.history(newRefuser(open_, h.dana.id))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	return all
}

// histTrail is the scripted case the derivation tests below replay: eleven commits
// covering content and a default left alone, a row inside a row, that inner row's
// own value, a second form by a second hand, a decision, an attestation, a stage
// move that wrote no data, a retraction, a second decision, and a content commit
// that erased both verdicts and a whole subtree of rows. `want` is the snapshot the
// store held at each revision, captured as the script ran, which is what makes the
// comparison a check and not a restatement of the fold.
func histTrail(t testing.TB) (*hist, []commitRecord, []committedState) {
	t.Helper()
	h := histSetup(t)
	want := []committedState{h.snap(t)}

	// 1: the form's content and two pages. `fee` and `stamp` are never written by
	// any batch below, so every replay from here on answers them from the base and
	// not from an entry — which is frame.go's rule that a default is data, priced.
	first := h.commit(t, h.dana, "application",
		Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Ada")),
		Add("application.pages"), Add("application.pages"))
	if len(first.Created) != 2 {
		t.Fatalf("two Adds created %v", first.Created)
	}
	h.pages = first.Created
	want = append(want, h.snap(t))

	// 2: the pages' own values, and a mark inside *each* of them — the nesting a
	// replay has to build from an entry that names both IDs, since an inner set
	// lives inside the outer row and nothing else records where it hangs. Both
	// pages get one because revision 10 removes one of them: a fold that lost the
	// surviving page's inner rows, or kept the removed page's, would have nothing
	// to be wrong about otherwise.
	second := h.commit(t, h.dana, "application",
		Set(h.pages[0]+".note", Text("first")),
		Set(h.pages[1]+".note", Text("second")),
		Add(h.pages[0]+".marks"), Add(h.pages[1]+".marks"))
	if len(second.Created) != 2 {
		t.Fatalf("the inner Adds created %v", second.Created)
	}
	h.marks = second.Created
	want = append(want, h.snap(t))

	// 3: the inner rows' values, committed alone, so the fold must carry an outer
	// row's content across a record that speaks only about its children.
	h.commit(t, h.dana, "application",
		Set(h.marks[0]+".day", Date(time.Date(2026, 12, 31, 0, 0, 0, 0,
			time.UTC))),
		Set(h.marks[1]+".day", Date(time.Date(2027, 1, 1, 0, 0, 0, 0,
			time.UTC))))
	want = append(want, h.snap(t))

	// 4: another form, by another hand, so the trail is not one subtree's story.
	h.commit(t, h.clerk, "archive", Set("archive.filed_at", Text("12/1")))
	want = append(want, h.snap(t))

	// 5: a decision, with a comment the state keeps and history must too.
	if _, err := h.elliot.Review(ctx(), "application", ReviewApprove,
		h.head(t), "clean"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	want = append(want, h.snap(t))

	// 6: the attestation the decision made possible, and a reference no state
	// survives to explain.
	if _, err := h.dana.Sign(ctx(), "application.submitted", h.head(t),
		"envelope-7"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	want = append(want, h.snap(t))

	// 7: a stage move, whose commit carries no entry at all, into the stage that
	// asks for §5.5's three conjuncts: the content, the decision, the attestation.
	if _, err := h.elliot.Transition(ctx(), "filed", h.head(t)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	want = append(want, h.snap(t))

	// 8: a retraction. The state keeps nobody; the entry keeps the act.
	if _, err := h.elliot.Review(ctx(), "application", ReviewRevoke,
		h.head(t), ""); err != nil {
		t.Fatalf("Review revoke: %v", err)
	}
	want = append(want, h.snap(t))

	// 9: the same hand deciding again, so the last content commit has a live
	// verdict to erase rather than one already gone.
	if _, err := h.elliot.Review(ctx(), "application", ReviewApprove,
		h.head(t), "again"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	want = append(want, h.snap(t))

	// 10: content, which erases the standing decision *and* the attestation with
	// no entry saying either happened, and drops a page whose values and inner
	// rows the same commit has to call absent.
	h.commit(t, h.dana, "application",
		Set("application.full_name", Text("Ada L")),
		Remove(h.pages[1]))
	want = append(want, h.snap(t))

	// 11: the surviving page's own value, after its sibling was dropped — a fold
	// that lost that row's content would show it in this record's entry.
	h.commit(t, h.dana, "application", Set(h.pages[0]+".note", Text("first!")))
	want = append(want, h.snap(t))

	hist := h.trail(t)
	if got := h.head(t); got != uint64(len(want)-1) ||
		len(hist) != int(got) {
		t.Fatalf("the script committed %d times, snapshots hold %d and the "+
			"trail %d entries", got, len(want)-1, len(hist))
	}
	return h, hist, want
}

// TestReplayLandsOnTheStoredSnapshot is the claim §4.2, §5.6, §5.9 and §5.10 have
// all rested on and none of them priced: that a case's state at revision *i* is
// derivable from the entries between there and here. It is checked at every
// revision of a case that holds content, defaults, nested rows, a decision, a
// retraction, an attestation, a stage move, an erasure and a removal — because a
// fold that dropped any one of those kinds would pass a trail that only had
// values, and because one comparison at the head would pass on errors that happen
// to cancel.
func TestReplayLandsOnTheStoredSnapshot(t *testing.T) {
	_, hist, want := histTrail(t)
	img := mustLowerSource(t, histSrc)
	for rev := uint64(0); rev < uint64(len(want)); rev++ {
		got := answers(t, img, replayTo(img, hist, rev))
		diff := diffAnswers(got, answers(t, img, want[rev]))
		if len(diff) > 0 {
			t.Errorf("replay to revision %d differs:\n\t%s", rev,
				strings.Join(diff, "\n\t"))
		}
	}
}

// answers is every question a committed state answers about itself, one line
// each: the stage, the instance counter, a value per addressed cell, a row list
// per collection at each place it has rows, and one verdict per slot the
// definition says carries one.
//
// The list is deliberately not the struct. Two states can differ in capacity, in
// whether an untouched collection's set is nil or empty, and in a map nobody has
// looked through, and none of that is a fact the language reports. The claims §4.2
// and §5.11 make are about answers, so this is the comparison a representation
// accident cannot satisfy — and cannot hide a real difference either, because
// every value a view renders is one of these lines.
func answers(t *testing.T, img *image, cs committedState) map[string]string {
	t.Helper()
	out := map[string]string{
		"<stage>": img.stageName(cs.stage),
		"<seq>":   dec(uint64(cs.s.seq)),
	}
	for i, sl := range img.slots {
		n := nodeID(i)
		switch {
		case sl.coll != 0:
			// A collection's answer is the list its instances left behind, in
			// creation order, and a set nobody has added to is the empty list.
			// §5.1's repeated fields are not in this fixture and are asserted
			// about by §5.1's own tests; `pathsOf` is the walker that handles
			// them, and a fold that needed one more case would fail there first.
			for _, host := range cs.s.hostsOf(n) {
				var ids []string
				for _, r := range cs.s.rowsFor(n, host).list() {
					ids = append(ids, dec(uint64(r.id)))
				}
				out["rows "+cs.s.addr(n, host)] = strings.Join(ids, " ")
			}
		case sl.kind == kindField:
			for _, p := range cs.s.pathsOf(n) {
				out["value "+cs.s.addr(n, p)] = show(img.load(n,
					cs.s.cellValue(n, p)))
			}
		}
		if sl.verif == n {
			r := reviewAt(cs.reviews, n)
			out["review "+img.path(n)] = verdictLine(string(r.state()),
				r.by, r.at, r.note)
		}
		if sl.sig != 0 {
			g := sigAt(cs.sigs, n)
			// A standing signature has no state word: §5.10's reason the state
			// stores no flag is that standing is `at != 0` and nothing else.
			out["sign "+img.path(n)] = verdictLine(sigWord(g), g.by, g.at,
				g.ref)
		}
	}
	return out
}

// sigWord is a signature's one bit of state, spelled so that a diff line says
// which of the two answers changed.
func sigWord(g sigState) string {
	if g.at == 0 {
		return "unsigned"
	}
	return "stands"
}

// verdictLine is a verdict's account in one line, and the quoting keeps an empty
// comment apart from no comment at all: §5.9's rule that a retraction carries no
// note is worth asserting, and a renderer that printed the two alike would make
// the equivalence test blind to the one difference a payload has.
func verdictLine(state string, by UserID, at uint64, note string) string {
	return state + " " + string(by) + " " + dec(at) + " " +
		strconv.Quote(note)
}

// diffAnswers lists the lines two states disagree about, in a stable order so a
// failure reads the same twice.
func diffAnswers(got, want map[string]string) []string {
	var keys []string
	for k, v := range got {
		if want[k] != v {
			keys = append(keys, k)
		}
	}
	for k := range want {
		if _, have := got[k]; !have {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+": derived "+strconv.Quote(got[k])+
			", committed "+strconv.Quote(want[k]))
	}
	return out
}

// TestReplayDerivesWhatNoEntryRecords is §4.2's whole argument for why a reset
// costs nothing: the erasure of a decision and of an attestation follows from the
// content commit that caused it, while a retraction — which somebody chose — is an
// entry. Four verdicts come and go across the script and only three records speak,
// which is the difference §5.9 draws between a reset and a revoke.
func TestReplayDerivesWhatNoEntryRecords(t *testing.T) {
	_, hist, want := histTrail(t)
	img := mustLowerSource(t, histSrc)
	unit := slotAt(t, img, "application")
	field := slotAt(t, img, "application.submitted")

	// 5 decided, 6 attested, 8 retracted by an act that *is* recorded, 9 decided
	// again, and 10 erased both by content. The attestation outlives the
	// retraction, which is §5.10's independence claim stated as a table.
	for _, tc := range []struct {
		rev              uint64
		approved, signed bool
	}{
		{rev: 4}, {rev: 5, approved: true},
		{rev: 6, approved: true, signed: true},
		{rev: 7, approved: true, signed: true},
		{rev: 8, signed: true},
		{rev: 9, approved: true, signed: true},
		{rev: 10}, {rev: 11},
	} {
		cs := replayTo(img, hist, tc.rev)
		if got := reviewAt(cs.reviews, unit).state(); (got ==
			ReviewApproved) != tc.approved {
			t.Errorf("revision %d: derived %s for the unit, approved wanted %v",
				tc.rev, got, tc.approved)
		}
		if got := sigAt(cs.sigs, field).at != 0; got != tc.signed {
			t.Errorf("revision %d: signature standing = %v, want %v", tc.rev,
				got, tc.signed)
		}
	}
	if n := countKind(hist, chgReview); n != 3 {
		t.Errorf("the trail holds %d decision entries, want 3: an approve, a "+
			"retraction, an approve", n)
	}
	if n := countKind(hist, chgSign); n != 1 {
		t.Errorf("the trail holds %d attestation entries, want 1: the erasure is "+
			"derived, and a retraction of a signature is not a thing §4.2 has", n)
	}
	// The head's blank answers are the derivation's and not a table the store
	// rebuilt from the entries: the two agree because one driver ran on both.
	erased, kept := answers(t, img, replayTo(img, hist, 10)),
		answers(t, img, want[10])
	for _, k := range []string{"review application",
		"sign application.submitted"} {
		if erased[k] != kept[k] {
			t.Errorf("%s after the erasure: derived %s, committed %s", k,
				strconv.Quote(erased[k]), strconv.Quote(kept[k]))
		}
	}
}

// TestReplayKeepsWhatARemovalDoesNotErase is the fold's other silent risk: a row
// set rebuilt from an entry could drop the content of the rows that survived, or
// resurrect the ones the entry stopped naming. Both directions are checked at the
// revision after the removal *and* at the one after that, where a fold that lost a
// surviving row's value would show up in the next commit's own entry.
func TestReplayKeepsWhatARemovalDoesNotErase(t *testing.T) {
	h, hist, want := histTrail(t)
	img := mustLowerSource(t, histSrc)
	kept := "value " + h.pages[0] + ".note"
	gone := "value " + h.pages[1] + ".note"
	for _, rev := range []uint64{10, 11} {
		got := answers(t, img, replayTo(img, hist, rev))
		held := answers(t, img, want[rev])
		if held[kept] == "" {
			t.Fatalf("committed revision %d holds no answer for %q", rev, kept)
		}
		if got[kept] != held[kept] {
			t.Errorf("revision %d: derived %s for the surviving page, want %s",
				rev, strconv.Quote(got[kept]), strconv.Quote(held[kept]))
		}
		if _, have := got[gone]; have {
			t.Errorf("revision %d still answers for the removed page: %s", rev,
				strconv.Quote(got[gone]))
		}
		inner := "rows " + h.pages[0] + ".marks"
		if got[inner] == "" || got[inner] != held[inner] {
			t.Errorf("revision %d: the surviving page's inner rows are %q, "+
				"committed %q", rev, got[inner], held[inner])
		}
	}
	// The removed page's inner set and rows are gone with it, and no entry has to
	// say so: §5.1's cascade is a property of where a nested set lives, and the
	// fold that rebuilds the outer list drops the subtree that hung off it. The
	// committed state at revision 9 held those rows, which is what makes the two
	// absences below a claim rather than a vacuity.
	held9 := answers(t, img, want[9])
	deadRows, deadCell := "rows "+h.pages[1]+".marks", "value "+
		h.marks[1]+".day"
	if held9[deadRows] == "" || held9[deadCell] == "" {
		t.Fatalf("revision 9 holds no rows (%q) or no value (%q) inside the "+
			"page revision 10 removed: %q / %q", deadRows, deadCell,
			held9[deadRows], held9[deadCell])
	}
	after := answers(t, img, replayTo(img, hist, 10))
	for _, k := range []string{deadRows, deadCell} {
		if _, have := after[k]; have {
			t.Errorf("the removed page still answers %s: %s", k,
				strconv.Quote(after[k]))
		}
	}
	// The trail reached that answer by *naming* the emptied set, which is the
	// entry that makes `restore`'s early return load-bearing rather than
	// defensive: by the time the fold gets to it, the outer list no longer holds
	// the page it hung off.
	var cascade bool
	for _, ch := range hist[9].changes {
		if ch.kind == chgRows &&
			img.addr(ch.slot, ch.path) == h.pages[1]+".marks" &&
			len(ch.ids) == 0 {
			cascade = true
		}
	}
	if !cascade {
		t.Errorf("revision 10 recorded no emptied inner set for %q, so the "+
			"fold above never had to ignore an address that is not data",
			deadRows)
	}
	// And the surviving page keeps its own inner row's value across the commit that
	// dropped its sibling's, which is the direction a fold that rebuilt every set
	// from scratch would get wrong.
	keptCell := "value " + h.marks[0] + ".day"
	if after[keptCell] != answers(t, img, want[10])[keptCell] {
		t.Errorf("the surviving page's inner value derived as %q, committed %q",
			strconv.Quote(after[keptCell]),
			strconv.Quote(answers(t, img, want[10])[keptCell]))
	}
}

// TestRestoreIgnoresAnAddressThatIsNotData prices the two cases in `restore`'s
// guard that no scripted trail reaches, and the counter rule that runs before
// them. A commit cannot list rows under a host that exists but has never had a set
// of that kind — nothing moved, so §5.1's diff says nothing — so `set == nil` is
// reachable only by asking the function directly. Both guards must write nothing
// and stop nothing: a fold that died here would lose every revision after it, and
// one that wrote here would invent a row set the case never had.
//
// The counter is the third claim and the one a mutant finds easiest: an ID an entry
// *named* was issued even when its row lived inside a page the same commit removed,
// and §5.1 forbids reusing one. So `restore` moves `seq` before it decides whether
// it has anywhere to write.
func TestRestoreIgnoresAnAddressThatIsNotData(t *testing.T) {
	img := mustLowerSource(t, histSrc)
	home, away := slotAt(t, img, "application.pages"),
		slotAt(t, img, "application.pages.marks")

	s := newState(img)
	s.applyDefaults()
	// Two pages, one mark inside the first: so `away` has a set somewhere and an
	// address with none, and the difference between the two guards is real.
	for i := 0; i < 2; i++ {
		if _, _, ok := s.addAt(home, nil); !ok {
			t.Fatal("addAt(pages)")
		}
	}
	host := instPath{{home, 1}}
	id, _, ok := s.addAt(away, host)
	if !ok || id != 3 {
		t.Fatalf("addAt(marks) = %d, %v, want instance 3", id, ok)
	}
	before := s.seq

	// (a) A host the state does not hold: the address is not data, so `ok` is
	// false. The listed ID is still an ID that was issued.
	s.restore(away, instPath{{home, 9}}, []instanceID{7})
	if s.seq != 7 {
		t.Errorf("an entry under a dead host left the counter at %d, want 7: "+
			"a replay that reissues 7 hands back an address a host may hold",
			s.seq)
	}
	if got := len(s.rowsFor(away, instPath{{home, 9}}).list()); got != 0 {
		t.Errorf("the dead host now holds %d rows, want nothing written", got)
	}
	if set, had := s.rowsBelow(away, host, false); !had || set.count() != 1 {
		t.Errorf("the live host's set became %v, %v: an unrelated restore "+
			"must not disturb it", set, had)
	}

	// (b) A live host whose set for this collection was never made: `ok` is true
	// and the set is nil, which is the case `restore` must not create out of an
	// empty list.
	empty := instPath{{home, 2}}
	if set, had := s.rowsBelow(away, empty, false); had != true ||
		set != nil {
		t.Fatalf("the fixture's second page already holds a marks set: %v, %v",
			set, had)
	}
	s.restore(away, empty, nil)
	if set, had := s.rowsBelow(away, empty, false); set != nil || !had {
		t.Errorf("an empty list made a set at the second page: %v, %v",
			set, had)
	}
	if s.seq != before+4 {
		t.Errorf("the counter moved to %d on an entry that listed nothing, "+
			"want %d", s.seq, before+4)
	}
	// (c) And with a non-empty list the same address does make the set: the guard
	// is about a list that asks for nothing and an address that is not data, not
	// about nested collections in general.
	s.restore(away, empty, []instanceID{8})
	if set, had := s.rowsBelow(away, empty, true); !had ||
		set.count() != 1 {
		t.Errorf("the second page holds %v after a restore that listed one "+
			"row, want a set with one instance", set)
	}
	if s.seq != 8 {
		t.Errorf("counter %d, want the largest ID ever named (8)", s.seq)
	}
}

// TestACommittedRowSetListsItsInstances is §5.1's address rule read out of the
// store's own entries rather than out of a rendered page: an inner collection's
// entry names both IDs, a decision's entry names no instance at all, and the two
// verdict kinds carry their payload in the cells the record does not duplicate.
func TestACommittedRowSetListsItsInstances(t *testing.T) {
	_, hist, _ := histTrail(t)
	img := mustLowerSource(t, histSrc)
	var inner, decisions, signs int
	for _, rec := range hist {
		for _, ch := range rec.changes {
			switch ch.kind {
			case chgReview:
				decisions++
				if len(ch.path) != 0 {
					t.Errorf("revision %d: a decision hangs at %v, and §5.1's "+
						"F4 case decides a unit as a whole", rec.revision,
						ch.path)
				}
				if got := img.addr(ch.slot, ch.path); got != "application" {
					t.Errorf("the decision entry addresses %q, want the unit",
						got)
				}
				if uint8(ch.v.i) == verifPending && ch.v.s != "" {
					t.Errorf("a retraction carries the note %q, and §5.9 says "+
						"it carries no comment", ch.v.s)
				}
			case chgSign:
				signs++
				if ch.v.s != "envelope-7" {
					t.Errorf("the attestation carries %q, want the caller's "+
						"own reference", ch.v.s)
				}
			case chgRows:
				if addr := img.addr(ch.slot, ch.path); strings.HasPrefix(addr,
					"application.pages.") {
					inner++
					// §5.1 spells a row inside a row by both IDs, so a nested
					// set's entry always names its host. The list may be empty:
					// that is how the trail says a collection went to nothing,
					// including when the whole host row went with it — which the
					// removal at revision 10 does, and `restore` reads as "no
					// host, nothing to write".
					if len(ch.path) == 0 {
						t.Errorf("the inner set %q has host %v and %d ids: an "+
							"entry for a row inside a row names both", addr,
							ch.path, len(ch.ids))
					}
					for i, id := range ch.ids {
						if id == 0 {
							t.Errorf("the set %q lists instance 0 at %d, and "+
								"§5.1's allocator starts at 1", addr, i)
						}
					}
				}
			}
		}
	}
	if decisions != 3 || signs != 1 {
		t.Errorf("the trail holds %d decisions and %d attestations, want 3 and 1",
			decisions, signs)
	}
	if inner == 0 {
		t.Fatal("the trail never held an inner row set, so the nesting above " +
			"is a claim about nothing")
	}
}

// TestReplayToZeroIsTheCreatedCase is the fold's base claim: revision 0 has no
// entry, so the base is the whole answer — the defaults `create` materialized, the
// initial stage, no rows, nothing decided, nothing signed.
func TestReplayToZeroIsTheCreatedCase(t *testing.T) {
	_, _, want := histTrail(t)
	img := mustLowerSource(t, histSrc)
	got := answers(t, img, replayTo(img, nil, 0))
	for _, line := range diffAnswers(got, answers(t, img, want[0])) {
		t.Errorf("replay of an untouched case differs: %s", line)
	}
	if stamp := got["value application.stamp"]; stamp != `text "created"` {
		t.Errorf("the derived case holds stamp %s, want the declared default",
			stamp)
	}
	// The scale a decimal default carries is the type's, so a fold that answered
	// "nothing" here would be silently dropping every defaulted field in a case.
	if fee := got["value application.fee"]; !strings.HasPrefix(fee, "dec 1") {
		t.Errorf("the derived case holds fee %q, want the declared default", fee)
	}
	if rows := got["rows application.pages"]; rows != "" {
		t.Errorf("an untouched case has rows %q", rows)
	}
	for _, k := range []string{"review application",
		"sign application.submitted"} {
		if got[k] != answers(t, img, want[0])[k] {
			t.Errorf("%s of an untouched case is %s, committed %s", k,
				strconv.Quote(got[k]),
				strconv.Quote(answers(t, img, want[0])[k]))
		}
	}
}

// TestReplayCannotRecoverAnUnrecordedAddress is the derivation's first known
// limit, written down as a test rather than as a promise: a draft that added a row
// and removed it before submitting advanced the case's instance counter without
// advancing any row set, so §5.1's forward diff has no way to say it happened —
// there is no old value to record and no row that exists now to name. Replay
// therefore tracks the largest ID an entry *listed*, which is one less than the
// counter a case with such a draft carries. Nothing here commits a derived state,
// so the gap is inert, and it is the second reason §5.11 exports a trail and not a
// state.
func TestReplayCannotRecoverAnUnrecordedAddress(t *testing.T) {
	h := histSetup(t)
	h.commit(t, h.dana, "application",
		Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Ada")),
		Add("application.pages"))
	listed := uint64(1)
	if got := uint64(h.snap(t).s.seq); got != listed {
		t.Fatalf("the committed counter is %d after one Add, want %d", got,
			listed)
	}
	// One batch adds a second row to the draft; the next removes it and commits.
	// The set the entry lists is the set that survives, and ID 2 appears in no
	// entry in the trail.
	h.save(t, h.dana, Add("application.pages"))
	h.commit(t, h.dana, "application",
		Remove("application.pages.2"),
		Set("application.full_name", Text("Ada L")))
	if got := uint64(h.snap(t).s.seq); got == listed {
		t.Fatalf("the case's counter never moved, so this script shows no gap")
	}

	cs := replayTo(h.img, h.trail(t), 99)
	if got := uint64(cs.s.seq); got != listed {
		t.Errorf("the derived counter is %d, want the largest ID ever listed "+
			"(%d): a replay that invented a higher one is guessing", got, listed)
	}
	// Everything a host can be shown still lands, which is what makes the gap one
	// integer and not a state.
	img := h.img
	want := answers(t, img, h.snap(t))
	have := answers(t, img, cs)
	for _, k := range []string{"<stage>", "value application.full_name",
		"value application.kind", "value application.stamp",
		"rows application.pages"} {
		if have[k] != want[k] {
			t.Errorf("%s: derived %s, committed %s", k, strconv.Quote(have[k]),
				strconv.Quote(want[k]))
		}
	}
	if have["<seq>"] == want["<seq>"] {
		t.Errorf("the counters matched, so this script no longer shows the gap")
	}
}

// TestADefaultMovedByAReloadIsNotRecoverable states the derivation's second
// limit, and the sharpest reason nothing here commits a `StateAt`: a default
// nobody wrote is data the base materialized, so a *semantic* reload that moved
// the default makes a replayed state disagree with the snapshot the case actually
// held — in a case whose ordinal space never moved, which is precisely the change
// §4's drift check is written to allow.
//
// The comparison is made in-package on purpose: `replayTo` takes the image to fold
// under, so the limit is priced without the persistence bridge milestone 4 will
// build.
func TestADefaultMovedByAReloadIsNotRecoverable(t *testing.T) {
	_, hist, want := histTrail(t)
	img := mustLowerSource(t, histSrc)
	reloaded := mustLowerSource(t, strings.Replace(histSrc,
		`stamp     : string default "created";`,
		`stamp     : string default "reissued";`, 1))
	if len(reloaded.slots) != len(img.slots) {
		t.Fatal("the fixture's reload changed the slot count, so it is the " +
			"drift §4 refuses rather than the drift it permits")
	}
	// Revision 9 is the head's last verdict and the first row removal: a state
	// where every entry has landed and only the base can disagree.
	derived := answers(t, reloaded, replayTo(reloaded, hist, 9))
	kept := answers(t, img, want[9])
	if derived["value application.stamp"] == kept["value application.stamp"] {
		t.Fatal("a moved default left the derived state untouched, so this " +
			"script no longer shows the limit")
	}
	// And the gap is exactly that one cell: every entry the trail holds was
	// written under the moved declaration too, so a fold over a semantic reload
	// differs from the stored snapshot only where the base answers. A change that
	// moved more than that would be §4's refusal, not this limit.
	for _, line := range diffAnswers(derived, kept) {
		if !strings.HasPrefix(line, "value application.stamp:") {
			t.Errorf("the reload moved more than the default: %s", line)
		}
	}
}

// TestTheWindowIsTheSliceItWasAskedFor is §5.11's cost claim about the read,
// stated where the store can be inspected: a page is a slice of the stored trail
// and not a copy of it, so a reader who asked for three commits of a long trail
// pays for three — and the whole-trail form the derivation uses is the opposite
// function. `BenchmarkHistory` is the number.
func TestTheWindowIsTheSliceItWasAskedFor(t *testing.T) {
	h, hist, _ := histTrail(t)
	rec := stored(t, h.rt, h.dana)
	all := rec.revision
	for _, tc := range []struct {
		from        uint64
		limit       int
		first, last uint64
		n, withheld int
	}{
		// A cursor of 0 is the beginning, because revision 0 has no entry to find.
		{from: 0, limit: 0, first: 1, last: all, n: int(all)},
		{from: 1, limit: 3, first: 1, last: 3, n: 3},
		{from: 4, limit: 0, first: 4, last: all, n: int(all) - 3},
		{from: 5, limit: 99, first: 5, last: all, n: int(all) - 4},
		{from: all, limit: 1, first: all, last: all, n: 1},
		// Past the head there is nothing, which is an empty page and not a
		// refusal: the trail is dense, so both readings are the same fact.
		{from: all + 1, limit: 5},
		{from: all + 1, limit: 0},
	} {
		page := rec.window(tc.from, tc.limit)
		if len(page) != tc.n {
			t.Errorf("window(%d, %d) holds %d entries, want %d", tc.from,
				tc.limit, len(page), tc.n)
			continue
		}
		if tc.n == 0 {
			continue
		}
		if page[0].revision != tc.first || page[len(page)-1].revision != tc.last {
			t.Errorf("window(%d, %d) runs %d..%d, want %d..%d", tc.from,
				tc.limit, page[0].revision, page[len(page)-1].revision,
				tc.first, tc.last)
		}
	}
	// The page shares its backing array with the trail it was cut from, which is
	// what the cost claim rests on: `window` is arithmetic on a dense index, not a
	// search and not a copy. `history` is the function that copies, and a caller
	// that keeps the list it was given is a caller that asked for it.
	short := rec.window(2, 3)
	if len(short) != 3 || &short[0] != &rec.hist[1] {
		t.Errorf("the window did not slice the trail: %d entries, first %p "+
			"want %p", len(short), &short[0], &rec.hist[1])
	}
	if len(hist) != int(all) {
		t.Fatalf("the whole trail holds %d entries and the head is %d",
			len(hist), all)
	}
	// And the copy is a copy: writing through what `history` handed out cannot
	// reach the store, which is the replace-never-mutate rule read from the side
	// of a reader rather than of a commit.
	hist[0].changes = append(hist[0].changes, commitChange{})
	if n := len(rec.window(1, 1)[0].changes); n != len(hist[0].changes)-1 {
		t.Errorf("the store's own first record holds %d entries after a "+
			"caller appended to the copy", n)
	}
}

// TestEveryCommitKindIsNamed is the vocabulary the exported read renders: the four
// ordinals store.go keeps are the four words a host branches on, and an operation
// that joined them without a line in `name` answers with the empty string, which
// this refuses rather than leaves to chance.
func TestEveryCommitKindIsNamed(t *testing.T) {
	for _, tc := range []struct {
		op   commitOp
		want CommitOp
	}{
		{opSubmit, CommitSubmit},
		{opTransition, CommitTransition},
		{opReview, CommitReview},
		{opSign, CommitSign},
		{commitOp(99), ""},
	} {
		if got := tc.op.name(); got != tc.want {
			t.Errorf("commitOp(%d) names %q, want %q", tc.op, got, tc.want)
		}
	}
}

// countKind counts entries of one kind across a trail: the cheapest way to ask
// "did anything else write this", and enough for the claims above.
func countKind(hist []commitRecord, kind uint8) int {
	var n int
	for _, rec := range hist {
		for _, ch := range rec.changes {
			if ch.kind == kind {
				n++
			}
		}
	}
	return n
}

// show is a value as one line. It exists to keep the equivalence test about
// meaning: §5.1 counts a committed `Empty` as a change, and a renderer that
// printed an empty text and no value alike would be blind to the one difference
// that matters in a removal entry.
func show(v Value) string {
	switch v.Kind() {
	case KindNone:
		return "nothing"
	case KindText:
		return "text " + strconv.Quote(v.Text())
	case KindInteger:
		return "int " + strconv.FormatInt(v.Int(), 10)
	case KindDecimal:
		return "dec " + v.Decimal().String()
	case KindBoolean:
		return "bool " + strconv.FormatBool(v.Bool())
	case KindDate:
		return "date " + v.Date().Format("2006-01-02")
	case KindDatetime:
		return "dt " + v.DateTime().Format(time.RFC3339Nano)
	case KindBinary:
		return "bin " + strconv.Quote(string(v.Binary()))
	case KindTag:
		return "tag " + v.Tag()
	case KindUser:
		return "user " + strconv.Quote(string(v.User()))
	}
	return "?"
}
