package monshi

// The commit-side half of the walk (RUNTIME §5.3, §5.4, §9), asserted where the
// marks, the entry kinds and the store's erasures are visible. What a host
// sees — the `Affected` fields of `Submit`, `Review` and `Sign` — is
// `affected_test.go`'s claim; this file holds the three things no host call
// reaches: what `commit` answers with when §5.6's effects erase a fact, how
// `seedCommit` decides the walk's shape per entry kind, and what the mask walk
// of a role-conferring commit reports, skips and refuses to reorder.

import (
	"reflect"
	"testing"
)

// seedSrc is the fixture for both walks. One declaration per fact a commit seed
// has to tell apart: a plain field, two sibling `verifiable` groups — the
// commit-erase claim needs a content change that moves one unit and leaves the
// other standing, and F3 takes the nested shape away from a unit, so neither
// sits in the form's own governed shadow — a field whose guard reads a *member*
// of the first group, which is what distinguishes re-asking a unit from marking
// one slot, a `multiple`, a signature field the fixture's one actor may both be
// granted and be the hand for, a whole second form only the second role
// reaches — visibility a commit can *add* has to sit outside what the holder
// already sees, since P6's fold unions allows and strips by any held deny —
// and a third role that grants a judgement and no view.
//
// One actor holds type, decide and attest deliberately: every claim here is
// about which slots a commit's facts mark, and an actor who does all three is
// two handoffs fewer between the fixture and the assertion.
const seedSrc = `
roles {
    Applicant {
        allow view, edit, submit application;
        allow review application.unit;
        allow review application.keep;
        allow sign application.attest;
    }
    Auditor {
        allow view audit;
    }
    Witness {
        allow review application.unit;
    }
}
forms {
    application {
        a : string;

        unit verifiable {
            b : string;
        }
        extra : string present[.unit.b == "x"];

        keep verifiable {
            e : string;
        }

        coll multiple {
            c : string;
        }

        attest : signature(Applicant);
    }

    audit {
        total : string;
    }
}
stages {
    draft { }
}
`

var sdActor = Actor{User: "dana", PrimaryRole: "Applicant"}

// seedCase is one handle on one fresh case of the seed fixture: the runtime to
// reach the store through, the image to name slots by, the case to commit
// through.
func seedCase(t *testing.T) (*Runtime, *image, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, seedSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), sdActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

func TestSeedCommitMarksByEntryKind(t *testing.T) {
	// The seed rule is a switch over the kind, and each case of it is a
	// different walk shape: a value and a signature mark one slot, a row set
	// marks the collection alone, and a verdict re-asks the unit's whole
	// subtree — which is where the adjacency starts again, since `extra` reads
	// a member of the re-asked `unit` and nothing else reaches it.
	img := mustLowerSource(t, seedSrc)
	a := slotAt(t, img, "application.a")
	unit := slotAt(t, img, "application.unit")
	b := slotAt(t, img, "application.unit.b")
	extra := slotAt(t, img, "application.extra")
	keep := slotAt(t, img, "application.keep")
	e := slotAt(t, img, "application.keep.e")
	coll := slotAt(t, img, "application.coll")
	c := slotAt(t, img, "application.coll.c")
	attest := slotAt(t, img, "application.attest")

	fan := newFanout(img)
	seedCommit(fan, []commitChange{
		{kind: chgValue, slot: a, v: txt("1")},
		{kind: chgRows, slot: coll, ids: []instanceID{1}},
		{kind: chgReview, slot: keep, v: slotValue{set: true, i: 2}},
		{kind: chgSign, slot: attest, v: slotValue{set: true, s: "dana"}},
	}, []nodeID{unit})
	got := fan.affected()
	want := []string{"application.a", "application.unit",
		"application.extra", "application.keep", "application.coll",
		"application.attest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the seeds render %q, want %q", got, want)
	}
	shape(t, img, fan, got)

	// The claims the *rendering* cannot show, because a group reports for its
	// subtree either way: the verdict entry and the erased seed each marked
	// their unit's members, and the row set marked the collection and nothing
	// that lives inside a row. A seed that read `chgReview` or `erased` as a
	// bare mark, or `chgRows` as a re-ask, passes the list above and fails
	// here.
	if !fan.mark[b] {
		t.Error("the erased unit's member went unmarked, so a client " +
			"refreshing the group re-reads nothing under it")
	}
	if !fan.mark[e] {
		t.Error("the verdict entry marked the unit alone, which is the " +
			"group answer without the group's walk")
	}
	if fan.mark[c] {
		t.Error("a row set re-asked the collection's members, which E2 " +
			"guarantee no guard reads")
	}
	// The walk kept going *past* the re-asked subtree: `extra` is marked only
	// if the member `b` was queued and its dependents reached, which is the
	// line between an erased seed and a bare mark of the unit's own slot.
	if !fan.mark[extra] {
		t.Error("the guard on the erased unit's member was never re-asked")
	}
	if fan.mark[slotAt(t, img, "audit")] {
		t.Error("the seed walk reached a form no entry and no guard " +
			"names, which is the role walk's business alone")
	}
}

func TestCommitAnswersWithWhatItErased(t *testing.T) {
	// §5.6's effects run inside the store, and the 4D rule that an operation
	// reads the *installed* state is what made the store the only place that
	// knows what a commit erased. This is that knowledge arriving: a case at
	// a content commit, two unit approvals, a standing signature, and then a
	// commit of a new value for one member of one group.
	rt, img, c := seedCase(t)
	base, err := c.Update(ctx(), 0, Set("application.a", Text("first")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Submit(ctx(), "application", base.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := c.Review(ctx(), "application.unit", ReviewApprove, 1,
		""); err != nil {
		t.Fatalf("Review(unit): %v", err)
	}
	if _, err := c.Review(ctx(), "application.keep", ReviewApprove, 2,
		""); err != nil {
		t.Fatalf("Review(keep): %v", err)
	}
	if _, err := c.Sign(ctx(), "application.attest", 3, "ceremony"); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	unit := slotAt(t, img, "application.unit")
	keep := slotAt(t, img, "application.keep")
	b := slotAt(t, img, "application.unit.b")
	attest := slotAt(t, img, "application.attest")
	rec := stored(t, rt, c)

	// The commit is the store's, spelled the way the store's callers spell
	// it: a state built from copies (replace-never-mutate) and the diff it
	// records. The field that moved is governed by `unit`, so exactly one
	// verdict and exactly one attestation fall, and `keep` — the group the
	// commit wrote nothing under — stands.
	out, err := rt.w.cases.commit(newRefuser(submit, c.ID()), img, rec.revision,
		committedState{s: rec.commit.s, stage: rec.commit.stage,
			reviews: reviewTable(img, rec.commit.reviews),
			sigs:    signTable(img, rec.commit.sigs)},
		committedBy{by: "dana", op: opSubmit, changes: []commitChange{
			{kind: chgValue, slot: b, v: txt("2")}}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// What the commit erased, in the order the effects run: the verdict
	// first, the attestation second.
	if len(out.erased) != 2 || out.erased[0] != unit ||
		out.erased[1] != attest {
		t.Errorf("erased = %v, want [%d %d]: the moved unit and the "+
			"signed field", out.erased, unit, attest)
	}
	// The entry account says none of it: a reset writes no entry (DESIGN
	// §4.2), which is the fact §5.3's commit seed exists to repair.
	if len(out.rec.changes) != 1 || out.rec.changes[0].slot != b {
		t.Errorf("the record wrote %d entries, want the one value change",
			len(out.rec.changes))
	}
	if reviewAt(out.post.reviews, unit).code != verifPending ||
		reviewAt(out.post.reviews, keep).code != verifApproved {
		t.Error("the installed state erased the wrong verdict: the moved " +
			"unit is pending and its untouched sibling stands")
	}
	if sigAt(out.post.sigs, attest).at != 0 {
		t.Error("the installed state kept an attestation over moved content")
	}
	// And the caller's own tables survived: `erased` is news about the
	// commit, not a mutation of the snapshot it read.
	if reviewAt(rec.commit.reviews, unit).code != verifApproved ||
		sigAt(rec.commit.sigs, attest).at == 0 {
		t.Error("the commit rewrote the state it was handed, not its copy")
	}
}

func TestRoleChangesReportsTheTopmostMover(t *testing.T) {
	img := mustLowerSource(t, seedSrc)
	applicant, ok := img.roleByName("Applicant")
	if !ok {
		t.Fatal("the fixture lost its first role")
	}
	auditor, ok := img.roleByName("Auditor")
	if !ok {
		t.Fatal("the fixture lost its second role")
	}
	draft, ok := img.stageByName("draft")
	if !ok {
		t.Fatal("the fixture lost its stage")
	}
	audit := slotAt(t, img, "audit")
	deep := slotAt(t, img, "audit.total")

	// `audit` and its field both change answer; the mover alone returns,
	// because the collapse in `affected` is one rule and the walk feeds it
	// groups. A test at one depth would pass a mutation that stopped the
	// cover jump; two nodes deep is the shape that cannot.
	one := []roleID{applicant}
	two := []roleID{applicant, auditor}
	got := img.roleChanges(one, two, draft)
	if len(got) != 1 || got[0] != audit {
		t.Errorf("movers = %v, want the second form alone", got)
	}
	for _, n := range got {
		if n == deep {
			t.Error("the field was reported beside the form it lives in")
		}
	}

	// Folds that agree move nothing, at either depth of the same question:
	// the walk itself finds no differing bit, and the caller's compare says
	// the lists are the same set whatever order `rolesHeld` produced them
	// in — which is data order, not ordinal order, and a commit touching no
	// role field must still answer "unchanged".
	if img.roleChanges(one, one, draft) != nil {
		t.Error("equal folds moved a mask")
	}
	if !roleSetsMatch(two, []roleID{auditor, applicant}) {
		t.Error("roleSetsMatch read `rolesHeld`'s order as a difference")
	}
	if roleSetsMatch(one, two) || roleSetsMatch(nil, one) {
		t.Error("roleSetsMatch called different sets the same")
	}
	// The compare the caller skips on is a *cheap* no, not the whole answer:
	// a fold can differ and move nothing, because a role that grants a
	// judgement and no view changes §3.5's output nowhere. That walk runs and
	// returns nothing, which is the same cost shape §5.5 states for
	// `overlaysDiffer`'s skip: the guard may save the walk, never a verdict.
	witness, ok := img.roleByName("Witness")
	if !ok {
		t.Fatal("the fixture lost its witness")
	}
	if img.roleChanges(one, []roleID{applicant, witness}, draft) != nil {
		t.Error("a judgement-only role moved a view bit")
	}
}
