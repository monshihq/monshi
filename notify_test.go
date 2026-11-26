package monshi_test

// The post-commit observer, seen from outside the
// module: what a host is told after a commit, what it is not allowed to
// change about it, and where the call sits relative to the store.
//
// Observers see the installed state, including verdicts erased by the commit,
// filtered by the committing actor's visibility. An observer panic must not
// undo that commit.
//
// What is asserted in-package instead is plumbing: that the store hands the
// installed snapshot back, and that an unwired host's dispatch is one slice
// read (the benchmark prices it).

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/monshihq/monshi"
)

const notifySrc = `
types {
    legal : enum(PERSON, COMPANY);
}
roles {
    Editor {
        allow view, edit, submit application;
        allow edit, submit ledger;
        allow review application;
        allow sign application.sealed;
    }
    Auditor {
        allow view all;
    }
}
forms {
    application verifiable {
        kind   : legal;
        title  : string;
        sealed : signature(Editor);
        members multiple {
            who : string;
        }
    }
    ledger {
        amount : string;
        keeper : Auditor;
    }
}
stages {
    draft {
        allow Editor accept;
        accept filed;
    }
    filed {
        deny Editor view application;
    }
}
`

var (
	notifyEditor = monshi.Actor{User: "dana", PrimaryRole: "Editor"}
	notifyWide   = monshi.Actor{User: "elliot", PrimaryRole: "Auditor"}
)

// note is one observer call: the snapshot and the record it came with.
type note struct {
	at  monshi.CaseContext
	com monshi.Commit
}

// notifyCase is a runtime with the fixture loaded and one case at revision 0.
// Every test here registers after the case exists, which is the rule for all
// three registrations and one of the things being asserted.
func notifyCase(t *testing.T,
	actor monshi.Actor) (*monshi.Runtime, monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	define(t, rt, "notify.app", notifySrc)
	c, err := rt.Create(ctx(), "notify.app", actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, c
}

// notifyFill writes the visible form over two batches and submits it,
// returning both numbers the host needs next: the copy revision the batches
// left, and the revision the commit produced. The second batch exists to
// keep those two apart — the observer's `Working` is the copy's own number,
// and a fixture where one batch equals the other would not say so.
func notifyFill(t *testing.T, c monshi.Case) (uint64, uint64) {
	t.Helper()
	res, err := c.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.title", monshi.Text("Monshi Ltd")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err = c.Update(ctx(), res.Working,
		monshi.Add("application.members"),
		monshi.Set("application.members.1.who", monshi.Text("Dana")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	sub, err := c.Submit(ctx(), "application", res.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return res.Working, sub.Revision
}

func TestObserverSeesTheCommittedCase(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var notes []note
	rt.RegisterOnCommit(func(_ context.Context, at monshi.CaseContext,
		com monshi.Commit) {
		notes = append(notes, note{at, com})
	})

	work, rev := notifyFill(t, c)
	if len(notes) != 1 {
		t.Fatalf("the observer was called %d times, want 1", len(notes))
	}
	at, com := notes[0].at, notes[0].com

	// The numbers are the commit's, not the operation's starting point:
	// `Revision` is the number this commit produced, and the record's own
	// envelope says the same thing.
	if at.Revision() != rev || com.Revision != rev || rev != 1 {
		t.Errorf("Revision = %d, record %d, result %d, want all 1",
			at.Revision(), com.Revision, rev)
	}
	// The copy keeps its own number across a commit — the store rebases
	// `base`, never `rev` — and the observer's snapshot says so too.
	if at.Working() != work {
		t.Errorf("Working = %d, want the same %d the submit presented",
			at.Working(), work)
	}
	if at.Stage() != "draft" || com.Stage != "draft" {
		t.Errorf("stage = %q, record %q, want draft",
			at.Stage(), com.Stage)
	}
	if at.Actor() != notifyEditor {
		t.Errorf("Actor = %+v, want the committing actor", at.Actor())
	}
	if at.Case() != c.ID() || at.Definition() != "notify.app" {
		t.Errorf("identity = %v %q, want the case and definition",
			at.Case(), at.Definition())
	}
	if com.Op != monshi.CommitSubmit || com.By != "dana" {
		t.Errorf("record = %s by %q, want SUBMIT by dana", com.Op, com.By)
	}

	// The context reads the *committed* state: the values this commit
	// installed, three entries in the definition's preorder.
	if got, ok := at.Value("application.title"); !ok ||
		got.Text() != "Monshi Ltd" {
		t.Errorf("Value(title) = %v, %v, want the committed text", got, ok)
	}
	if got, ok := at.Value("application.kind"); !ok || got.Tag() != "COMPANY" {
		t.Errorf("Value(kind) = %v, %v, want the committed tag", got, ok)
	}
	// Four entries reach this observer — `application` is its form, and the
	// second batch added a row to it, which is two entries in the accounting:
	// the container's instance list, and the field inside the row. The withheld
	// case is a commit of the other form, down below.
	if len(com.Changes) != 4 {
		t.Fatalf("Changes = %+v, want four disclosed entries", com.Changes)
	}
	if com.Changes[0].Path != "application.kind" ||
		com.Changes[1].Path != "application.title" ||
		com.Changes[2].Path != "application.members" ||
		com.Changes[3].Path != "application.members.1.who" {
		t.Errorf("paths = %q, %q, %q, %q, want the definition's preorder",
			com.Changes[0].Path, com.Changes[1].Path, com.Changes[2].Path,
			com.Changes[3].Path)
	}
	for _, ch := range []int{0, 1, 3} {
		if com.Changes[ch].Kind != monshi.ChangeValue {
			t.Errorf("entry %q has kind %s, want VALUE",
				com.Changes[ch].Path, com.Changes[ch].Kind)
		}
	}
	// The row entry lists the instances the commit produced, as paths an
	// edit can name against the same address `Update` takes.
	if com.Changes[2].Kind != monshi.ChangeRows ||
		len(com.Changes[2].Rows) != 1 ||
		com.Changes[2].Rows[0] != "application.members.1" {
		t.Errorf("the row entry = %+v, want the one instance added",
			com.Changes[2])
	}
}

func TestObserverRecordMatchesTheTrail(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var seen []monshi.Commit
	var read []monshi.Commit
	rt.RegisterOnCommit(func(_ context.Context, at monshi.CaseContext,
		com monshi.Commit) {
		// Re-entering the case from inside the observer is the point: the
		// store's lock is released, so the reads below cannot deadlock, and
		// the trail already holds the commit being announced. One update
		// proves writes re-enter too — the copy's number is the batch's
		// guard, and the snapshot is where the observer learns it.
		if _, err := c.Update(ctx(), at.Working(),
			monshi.Set("application.title", monshi.Text("Second"))); err != nil {
			t.Errorf("an observer's Update was refused: %v", err)
		}
		h, err := c.History(ctx(), com.Revision, 1)
		if err != nil {
			t.Fatalf("an observer's History: %v", err)
		}
		seen = append(seen, com)
		read = append(read, h.Commits...)
	})

	notifyFill(t, c)
	if len(seen) != 1 || len(read) != 1 {
		t.Fatalf("seen %d, read %d, want one commit each", len(seen), len(read))
	}
	// The notification and the audit trail are one rendering: same actor,
	// same roles, same fold — a record that drifted from the trail would be
	// a host told one thing and a user shown another about one act.
	if !reflect.DeepEqual(seen[0], read[0]) {
		t.Errorf("the record and the trail disagree:\nnote  %+v\ntrail %+v",
			seen[0], read[0])
	}
}

func TestObserverReadsAtItsCallersMask(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var at monshi.CaseContext
	var com monshi.Commit
	rt.RegisterOnCommit(func(_ context.Context, x monshi.CaseContext,
		y monshi.Commit) {
		at, com = x, y
	})

	work, _ := notifyFill(t, c)
	res, err := c.Update(ctx(), work,
		monshi.Set("ledger.amount", monshi.Text("secret")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Submit(ctx(), "ledger", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The second commit is the masked one: `ledger.amount` sits under a
	// form this Editor may write and not read, and a commit entry is
	// disclosed at its node (the rule about a verdict on a container
	// applies to a value the same way). The record holds no entry and the
	// context answers absence.
	for _, ch := range com.Changes {
		if ch.Path == "ledger.amount" {
			t.Fatalf("the record disclosed a path its caller was denied")
		}
	}
	if _, ok := at.Value("ledger.amount"); ok {
		t.Error("the context answered a path the caller may not render")
	}

	// The same commit, read by an actor who may see everything: the entry
	// was withheld and not lost. This is the pair that turns the mask test
	// into a claim about disclosure rather than about missing data.
	wide, err := rt.Open(ctx(), c.ID(), notifyWide)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h, err := wide.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Commits) != 2 || len(h.Commits[1].Changes) != 1 {
		t.Fatalf("the wide trail = %+v, want two commits and a full ledger",
			h.Commits)
	}
	found := h.Commits[1].Changes[0]
	if found.Path != "ledger.amount" || found.Value.Text() != "secret" {
		t.Errorf("the committed amount entry = %+v, want secret", found)
	}

	// And the committer's own trail says the same thing the notification
	// said: `Withheld` counts what the mask dropped from that same commit,
	// so the two exports agree about one act without either seeing the
	// other's answer.
	own, err := c.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if own.Withheld != 1 || len(own.Commits[1].Changes) != 0 ||
		len(own.Commits[0].Changes) != 4 {
		t.Errorf("the committer's page = %d and %d changes, %d withheld, "+
			"want 4, 0 and 1", len(own.Commits[0].Changes),
			len(own.Commits[1].Changes), own.Withheld)
	}
}

func TestObserverCannotVetoOrRollBack(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var calls int
	rt.RegisterOnCommit(func(_ context.Context, _ monshi.CaseContext,
		_ monshi.Commit) {
		calls++
		panic("the observer fell over")
	})

	// The signature has no error to return, so the only hostile observer
	// left to write is a panicking one — and the commit it follows is
	// durable by the time the panic starts unwinding.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the runtime swallowed the observer's panic")
			}
		}()
		_, err := c.Update(ctx(), 0,
			monshi.Set("application.kind", monshi.Tag("PERSON")),
			monshi.Set("application.title", monshi.Text("Monshi Ltd")))
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, err := c.Submit(ctx(), "application", 1); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}()

	if calls != 1 {
		t.Errorf("the observer ran %d times, want 1 before the panic", calls)
	}
	if rev, err := c.Revision(ctx()); err != nil || rev != 1 {
		t.Errorf("Revision = %d (%v), want the commit to have survived",
			rev, err)
	}
	h, err := c.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Commits) != 1 {
		t.Errorf("the trail holds %d commits, want the durable one",
			len(h.Commits))
	}
	// And the case is still live afterwards: a rollback would show up here
	// as a stale-number refusal, not as a missing history entry.
	if _, err := c.Update(ctx(), 1,
		monshi.Set("application.title", monshi.Text("Later"))); err != nil {
		t.Errorf("editing after the observer's panic: %v", err)
	}
}

func TestOnlyCommitsNotifyAndEveryCommitDoes(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var ops []monshi.CommitOp
	rt.RegisterOnCommit(func(_ context.Context, _ monshi.CaseContext,
		com monshi.Commit) {
		ops = append(ops, com.Op)
	})

	work, _ := notifyFill(t, c)

	// The reads commit nothing, so they announce nothing: a view and a
	// trail are not acts at all.
	if _, err := c.View(ctx()); err != nil {
		t.Fatalf("View: %v", err)
	}
	if _, err := c.History(ctx(), 0, 0); err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(ops) != 1 || ops[0] != monshi.CommitSubmit {
		t.Fatalf("ops after reads = %v, want just the submit", ops)
	}

	// A submit of content the case already holds is the sharp case.
	sub, err := c.Submit(ctx(), "application", work)
	if err != nil {
		t.Fatalf("empty submit: %v", err)
	}
	if sub.Revision != 1 || len(ops) != 1 {
		t.Fatalf("empty submit notified %v at revision %d, want silence at 1",
			ops, sub.Revision)
	}

	// An edit, and a sync of the draft, are not commits either.
	res, err := c.Update(ctx(), work,
		monshi.Set("application.title", monshi.Text("Uncommitted")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops after a draft act = %v, want no news", ops)
	}

	// The other three committing verbs: a decision, an attestation, a stage
	// move. Each produces one commit and one announcement.
	rv, err := c.Review(ctx(), "application", monshi.ReviewApprove, 1, "ok")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	sg, err := c.Sign(ctx(), "application.sealed", rv.Revision, "ev-1")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := c.Transition(ctx(), "filed", sg.Revision); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	want := []monshi.CommitOp{monshi.CommitSubmit, monshi.CommitReview,
		monshi.CommitSign, monshi.CommitTransition}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %v, want %v", ops, want)
	}

	// A discarded draft is not a commit either, and the trail stays the
	// four acts long.
	if err := c.Discard(ctx(), res.Working); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if len(ops) != 4 {
		t.Errorf("ops after Discard = %v, want the four commits", ops)
	}
}

func TestVerdictAndTransitionRecords(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var notes []note
	rt.RegisterOnCommit(func(_ context.Context, at monshi.CaseContext,
		com monshi.Commit) {
		notes = append(notes, note{at, com})
	})

	notifyFill(t, c)
	if _, err := c.Review(ctx(), "application", monshi.ReviewApprove,
		1, "looks right"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if _, err := c.Sign(ctx(), "application.sealed", 2, "ev-1"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := c.Transition(ctx(), "filed", 3); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if len(notes) != 4 {
		t.Fatalf("notes = %d, want four commits", len(notes))
	}

	// The decision entry: the unit's path with no instance, the shape the
	// view reports, stamped at the revision this commit produced.
	rv := notes[1].com.Changes
	if len(rv) != 1 || rv[0].Kind != monshi.ChangeReview ||
		rv[0].Path != "application" {
		t.Fatalf("review record = %+v, want one decision at the unit", rv)
	}
	if rv[0].Review.State != monshi.ReviewApproved ||
		rv[0].Review.By != "dana" || rv[0].Review.At != 2 ||
		rv[0].Review.Comment != "looks right" {
		t.Errorf("decision = %+v, want dana's approval at 2 with a comment",
			rv[0].Review)
	}
	// The context answers the same verdict the record carries, because the
	// reviewer just made it: one state, two shapes.
	if got, ok := notes[1].at.Review("application"); !ok ||
		!reflect.DeepEqual(got, rv[0].Review) {
		t.Errorf("context decision = %+v (%v), want the record's %+v",
			got, ok, rv[0].Review)
	}

	// The attestation entry: the field, the hand, the reference, and the
	// revision the signature itself produced (the binding as a number).
	sg := notes[2].com.Changes
	if len(sg) != 1 || sg[0].Kind != monshi.ChangeSign ||
		sg[0].Path != "application.sealed" ||
		sg[0].Signature.By != "dana" || sg[0].Signature.At != 3 ||
		string(sg[0].Signature.Evidence) != "ev-1" {
		t.Errorf("sign record = %+v, want dana's attestation at 3", sg)
	}

	// The stage move writes no content entries at all, and says where the
	// case landed: the record's stage and the context's agree on the
	// destination, which is the whole news of a transition commit.
	if tr := notes[3].com; len(tr.Changes) != 0 || tr.Stage != "filed" ||
		notes[3].at.Stage() != "filed" {
		t.Errorf("transition record = %+v at stage %q, want filed with no"+
			" entries", tr, notes[3].at.Stage())
	}

	// And the fold at dispatch is the *installed* stage's. `filed` denies
	// this Editor the form's view, so the transition's snapshot answers
	// the way the same actor's next `View` would — not the way it did
	// while the case sat in draft, which is what the review's own pin
	// still says. Two snapshots of one case, two answers, each frozen at
	// its own commit's state: the now-mask and the snapshot rule in
	// one pair.
	if _, ok := notes[3].at.Value("application.title"); ok {
		t.Error("the snapshot answered a path the committed stage withholds")
	}
	if _, ok := notes[1].at.Value("application.title"); !ok {
		t.Error("the draft-stage snapshot withheld a reachable path")
	}
}

func TestRoleGrantedByTheCommitCountsAtDispatch(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var at monshi.CaseContext
	var com monshi.Commit
	rt.RegisterOnCommit(func(_ context.Context, x monshi.CaseContext,
		y monshi.Commit) {
		at, com = x, y
	})

	// One ledger batch writes both the role field and the field the primary
	// role cannot see. By the time the observer runs, the commit has
	// conferred Auditor — and the snapshot is cut from the state *after*
	// that act, so what the host is told matches what the same actor may
	// now read. The mask widens with the commit and not with the draft: an
	// uncommitted assignment confers nothing, and here it is the
	// committed one that moves the fold.
	res, err := c.Update(ctx(), 0,
		monshi.Set("ledger.keeper", monshi.User("dana")),
		monshi.Set("ledger.amount", monshi.Text("secret")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Submit(ctx(), "ledger", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	roles := append([]string(nil), at.Roles()...)
	sort.Strings(roles)
	if !reflect.DeepEqual(roles, []string{"Auditor", "Editor"}) {
		t.Errorf("Roles = %v, want both roles the committed state confers",
			roles)
	}
	var disclosed bool
	for _, ch := range com.Changes {
		if ch.Path == "ledger.amount" {
			disclosed = ch.Value.Text() == "secret"
		}
	}
	if !disclosed {
		t.Error("the record withheld a value the committed roles now reach")
	}
	if _, ok := at.Value("ledger.amount"); !ok {
		t.Error("the context withheld a path the committed roles reach")
	}
}

func TestLateRegistrationSpeaksAndClearStops(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)

	// Wiring after a case exists reaches it: observers are looked up on the
	// runtime, and a handle is a handle (the registry rule, proven for all
	// three seams the same way).
	work, _ := notifyFill(t, c)
	var calls int
	rt.RegisterOnCommit(func(_ context.Context, _ monshi.CaseContext,
		_ monshi.Commit) {
		calls++
	})
	rt.RegisterOnCommit(nil) // an empty seat is not added, and not an error

	res, err := c.Update(ctx(), work,
		monshi.Set("application.title", monshi.Text("Round two")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("late registration was heard %d times, want 1", calls)
	}

	// Clearing the list stops the announcements the same runtime started,
	// with no new case and no new handle.
	rt.ClearOnCommit()
	res, err = c.Update(ctx(), res.Working,
		monshi.Set("application.title", monshi.Text("Round three")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := c.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if calls != 1 {
		t.Errorf("after ClearOnCommit the observer ran again: %d calls", calls)
	}
}

func TestObserversRunInRegistrationOrder(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	var order []int
	for i := 1; i <= 3; i++ {
		n := i
		rt.RegisterOnCommit(func(_ context.Context, _ monshi.CaseContext,
			_ monshi.Commit) {
			order = append(order, n)
		})
	}
	notifyFill(t, c)
	if !reflect.DeepEqual(order, []int{1, 2, 3}) {
		t.Errorf("order = %v, want registration order", order)
	}
}

func TestReadingTheTrailNotifiesNobody(t *testing.T) {
	rt, c := notifyCase(t, notifyEditor)
	notifyFill(t, c)

	// Hooks never rerun during replay of old commits, and the shipped form of
	// that rule is that no read dispatches at all: the observer list is fired
	// by the operations that commit, and the fold — or any other read — walks
	// the trail without announcing it.
	var calls int
	rt.RegisterOnCommit(func(_ context.Context, _ monshi.CaseContext,
		_ monshi.Commit) {
		calls++
	})
	for i := 0; i < 20; i++ {
		if _, err := c.History(ctx(), 0, 0); err != nil {
			t.Fatalf("History: %v", err)
		}
		if _, err := c.View(ctx()); err != nil {
			t.Fatalf("View: %v", err)
		}
		if _, err := c.Rebase(ctx()); err != nil {
			t.Fatalf("Rebase: %v", err)
		}
	}
	if calls != 0 {
		t.Errorf("reads notified %d times, want nobody", calls)
	}
}
