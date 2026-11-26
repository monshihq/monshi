package monshi_test

// History seen from outside the module: what a host reads back of what the case
// committed — the records and their order, which page a cursor selects, what an
// actor's mask does to a trail, and what a row set, a decision and an
// attestation look like once the row they named is gone.
//
// What a host cannot observe is in the in-package file: that the entries replay to
// the snapshot the store kept at every revision, that a reset derives without
// recording one, and that the page is cut out of the trail rather than built from
// the case behind it.
//
// The applicant edits and signs, the registrar reviews and transitions, and the
// auditor can read just one subtree. All three read the same commits with
// different visibility.

import (
	"strings"
	"testing"
	"time"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

const trailSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
    amount     : decimal(14,2);
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
    Auditor {
        allow view application.notes;
    }
}
forms {
    application verifiable {
        kind      : legal_form required;
        full_name : string required;
        fee       : amount;

        notes multiple {
            who   : string;
            since : date;
        }

        submitted : signature(Applicant);
    }
}
stages {
    draft {
        allow Registrar accept;
        accept filed;
    }

    filed {}
}
`

var (
	trailOwner = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}
	trailRuler = monshi.Actor{User: "elliot", PrimaryRole: "Registrar"}
	trailAudit = monshi.Actor{User: "bo", PrimaryRole: "Auditor"}
)

// trailPast is a runtime with one case that has done six things — typed a form,
// added two rows and filled them, been decided, been attested, moved along an
// edge, and lost one of those rows — beside the three handles. It answers with the
// paths the rows were created at, because the claims about a removed instance are
// the interesting ones and cannot be made about a number a test guessed.
func trailPast(t *testing.T) (
	*monshi.Runtime, monshi.Case, monshi.Case, monshi.Case, []string) {
	t.Helper()
	rt := monshi.NewRuntime()
	d := define(t, rt, "trail.app", trailSrc)
	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 1: the form's own content.
	res := mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// 2: two rows and their values.
	res = mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Add("application.notes"), monshi.Add("application.notes"))
	rows := res.Created
	if len(rows) != 2 {
		t.Fatalf("two Adds created %v", rows)
	}
	res = mustTrailUpdate(t, dana, res.Working,
		monshi.Set(rows[0]+".who", monshi.Text("Ada")),
		monshi.Set(rows[1]+".who", monshi.Text("Zoe")),
		monshi.Set(rows[1]+".since", monshi.Date(
			time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC))))
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// 3: a decision about the unit, by the hand that may make one.
	registrar := mustTrailOpen(t, rt, dana.ID(), trailRuler)
	if _, err := registrar.Review(ctx(), "application",
		monshi.ReviewApprove, trailHead(t, dana), "clean"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// 4: the attestation, and the reference the case keeps without interpreting.
	if _, err := dana.Sign(ctx(), "application.submitted",
		trailHead(t, dana), "envelope-7"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// 5: the edge, which commits and changes no data.
	if _, err := registrar.Transition(ctx(), "filed",
		trailHead(t, dana)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	// 6: a removal beside a new value, so one record names an instance that stops
	// existing and one that goes on existing.
	auditor := mustTrailOpen(t, rt, dana.ID(), trailAudit)
	res = mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Set("application.fee",
			monshi.Decimal(decimal.RequireFromString("1500.00"))),
		monshi.Remove(rows[1]))
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return rt, dana, registrar, auditor, rows
}

// trailHead is the case's committed revision, read rather than remembered: a
// decision and an attestation both advance it, and a script that tracked the
// number by hand would be asserting its own arithmetic.
func trailHead(t *testing.T, c monshi.Case) uint64 {
	t.Helper()
	rev, err := c.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	return rev
}

// trailWorking is the copy's own number, read the way a host reads it.
func trailWorking(t *testing.T, c monshi.Case) uint64 {
	t.Helper()
	if _, err := c.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	return v.Working
}

// mustTrailOpen is a handle or a failed test: a trail is one case's, so the second
// and third hands have to point at the same ID.
func mustTrailOpen(t *testing.T, rt *monshi.Runtime, id monshi.CaseID,
	a monshi.Actor) monshi.Case {
	t.Helper()
	c, err := rt.Open(ctx(), id, a)
	if err != nil {
		t.Fatalf("Open(%s): %v", a.User, err)
	}
	return c
}

// mustTrailUpdate is a batch that must be accepted, and the result a host would
// send the next batch from.
func mustTrailUpdate(t *testing.T, c monshi.Case, base uint64,
	edits ...monshi.Edit) monshi.ChangeResult {
	t.Helper()
	res, err := c.Update(ctx(), base, edits...)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return res
}

// TestTheTrailIsTheCaseInOrder is the claim about the store read from the other
// side.
func TestTheTrailIsTheCaseInOrder(t *testing.T) {
	_, dana, registrar, _, _ := trailPast(t)
	res, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if res.Revision != 6 {
		t.Errorf("the page reports head %d, want 6", res.Revision)
	}
	if len(res.Commits) != 6 {
		t.Fatalf("the trail holds %d commits, want 6: %+v", len(res.Commits),
			res.Commits)
	}
	if res.Next != 0 {
		t.Errorf("a whole trail continues at %d, want 0", res.Next)
	}
	// One row set entry plus the three cells that batch filled, and the last
	// commit's set entry plus its new value and the two cells the removal erased.
	want := []struct {
		rev     uint64
		by      monshi.UserID
		op      monshi.CommitOp
		stage   string
		entries int
	}{
		{1, "dana", monshi.CommitSubmit, "draft", 2},
		{2, "dana", monshi.CommitSubmit, "draft", 4},
		{3, "elliot", monshi.CommitReview, "draft", 1},
		{4, "dana", monshi.CommitSign, "draft", 1},
		{5, "elliot", monshi.CommitTransition, "filed", 0},
		{6, "dana", monshi.CommitSubmit, "filed", 4},
	}
	for i, w := range want {
		c := res.Commits[i]
		if c.Revision != w.rev || c.By != w.by || c.Op != w.op ||
			c.Stage != w.stage || len(c.Changes) != w.entries {
			t.Errorf("commit %d is r%d by %s %s at %s with %d entries: %+v",
				i+1, c.Revision, c.By, c.Op, c.Stage, len(c.Changes), c)
			continue
		}
		if c.At.IsZero() {
			t.Errorf("commit %d carries no timestamp", w.rev)
		}
	}
	// The four words are the four operations that move a revision, and a trail
	// that held all of them is what makes this an audit rather than a diff log: a
	// reader of this case can say who decided, who attested and who moved the case
	// along an edge, and not only that the data moved.
	seen := map[monshi.CommitOp]bool{}
	for _, c := range res.Commits {
		seen[c.Op] = true
	}
	for _, op := range []monshi.CommitOp{monshi.CommitSubmit,
		monshi.CommitReview, monshi.CommitSign, monshi.CommitTransition} {
		if !seen[op] {
			t.Errorf("the trail never held a %s commit, so the four words are "+
				"not all read back", op)
		}
	}
	// The transition is a commit that changed no data. A reader who could not
	// see it would reconstruct a case that was never at `draft` at revision 5.
	if got := res.Commits[4].Changes; len(got) != 0 {
		t.Errorf("the transition commit holds %d entries, want none: %+v",
			len(got), got)
	}
	// The records are ordered by something a host cannot arrange: the store's own
	// clock, monotonically, whichever hand committed.
	for i := 1; i < len(res.Commits); i++ {
		if res.Commits[i-1].At.After(res.Commits[i].At) {
			t.Errorf("commit %d is stamped after commit %d: %s then %s", i,
				i+1, res.Commits[i-1].At, res.Commits[i].At)
		}
	}
	// And a page from the other handle is the same trail: the acts are the case's
	// and not the reader's.
	page, err := registrar.History(ctx(), 5, 1)
	if err != nil {
		t.Fatalf("History as the registrar: %v", err)
	}
	if len(page.Commits) != 1 || page.Commits[0].Revision != 5 ||
		page.Commits[0].Op != monshi.CommitTransition || page.Next != 6 ||
		page.Revision != 6 {
		t.Errorf("the page at 5 is %+v, want the edge and the next cursor",
			page)
	}
}

// TestAChangeNamesAnAddressItCanWriteBack is the rule that an instance is
// addressed by its stable ID, checked where it is hardest: in a trail, whose rows
// may be gone. The entries are keyed by slot ordinal internally, so this is also
// the read that turns the numbering back into the text a host holds.
func TestAChangeNamesAnAddressItCanWriteBack(t *testing.T) {
	_, dana, _, _, rows := trailPast(t)
	res, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	set := changes(t, res, 2, monshi.ChangeRows)
	if len(set) != 1 {
		t.Fatalf("revision 2 holds %d row entries, want 1", len(set))
	}
	if got := set[0].Path; got != "application.notes" {
		t.Errorf("the collection is addressed %q, want the declaration", got)
	}
	if got := set[0].Rows; len(got) != 2 || got[0] != rows[0] ||
		got[1] != rows[1] {
		t.Errorf("the set entry lists %v, want the paths the batch reported %v",
			got, rows)
	}
	for _, r := range set[0].Rows {
		if strings.Count(r, ".") != 2 ||
			!strings.HasPrefix(r, "application.notes.") {
			t.Errorf("the row %q is not the collection's path plus one ID", r)
		}
	}
	// The values the same commit wrote are addressed *through* their instances: one
	// path per cell, at the depth the walk parses back.
	var dated string
	for _, ch := range changes(t, res, 2, monshi.ChangeValue) {
		if ch.Value.Kind() == monshi.KindNone {
			t.Errorf("revision 2 wrote %q as a removal", ch.Path)
		}
		if !strings.HasPrefix(ch.Path, "application.notes.") {
			t.Errorf("the value entry is addressed %q, want the instance it "+
				"lives in", ch.Path)
		}
		if strings.HasSuffix(ch.Path, ".since") {
			dated = ch.Path
		}
	}
	if dated != rows[1]+".since" {
		t.Errorf("the dated cell is at %q, want %q", dated, rows[1]+".since")
	}
	// The set entry comes first, because the diff writes a collection's rows before
	// the cells inside them: a reader who walks the entries in order learns which
	// instances exist before learning what any of them holds.
	if first := res.Commits[1].Changes[0]; first.Kind != monshi.ChangeRows {
		t.Errorf("revision 2's first entry is %s at %q, want the row set",
			first.Kind, first.Path)
	}
	// A path the trail gives is a path the write side takes: the row this case
	// still holds is editable through its own history's text, which is what makes a
	// trail a starting point for a fix rather than a screenshot.
	mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Set(set[0].Rows[0]+".who", monshi.Text("Ada L")))
	if _, err := dana.View(ctx(), set[0].Rows[0]); err != nil {
		t.Errorf("an address from history is not readable either: %v", err)
	}
}

// TestTheTrailSpellsWhatMovedAndNotWhatDidNot is the rule that history records
// new values and never old ones, read from the host's side: a removal arrives as a
// commit holding no value, and a row that left the set is named by the list that no
// longer holds it.
func TestTheTrailSpellsWhatMovedAndNotWhatDidNot(t *testing.T) {
	_, dana, _, _, rows := trailPast(t)
	res, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	// The last commit removed the second row: the set entry lists the one that
	// survived, and the removed row's address is in the trail and not in the list
	// the same revision reports.
	head := res.Commits[len(res.Commits)-1]
	set := entry(t, head, monshi.ChangeRows)
	if len(set.Rows) != 1 || set.Rows[0] != rows[0] {
		t.Errorf("the set entry holds %v, want the one row left %v", set.Rows,
			rows[:1])
	}
	// The erased cells are reported as absent and not as the text they used to
	// be.
	var erased []string
	for _, ch := range head.Changes {
		if ch.Kind == monshi.ChangeValue && ch.Value.Kind() == monshi.KindNone {
			erased = append(erased, ch.Path)
		}
	}
	if len(erased) != 2 {
		t.Fatalf("the commit that emptied a row left %d absent cells: %+v",
			len(erased), head.Changes)
	}
	for _, p := range erased {
		if p != rows[1]+".who" && p != rows[1]+".since" {
			t.Errorf("the absent cell %q is not one the removed row held", p)
		}
	}
	// The removed row's address survives in the trail — that is the point of the
	// list being IDs and not a count — and stops resolving: the rule about a
	// decision whose unit is gone, reached for a row. The trail records the act and
	// does not promise the object.
	older := changes(t, res, 2, monshi.ChangeRows)[0]
	if _, err := dana.View(ctx(), older.Rows[1]); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("%q resolves after its row was removed: %v", older.Rows[1], err)
	}
	// The surviving row's cells are not in this commit at all: a diff that wrote
	// every reachable cell would make a removal look like a rewrite of the case.
	for _, ch := range head.Changes {
		if ch.Path == rows[0]+".who" || ch.Path == rows[0]+".since" {
			t.Errorf("the commit that removed a row wrote %q, and the "+
				"surviving row's cells never moved", ch.Path)
		}
	}
	// And what the trail does hold is the value the field carried, rendered through
	// the definition the case is read under now: a tag, a decimal and a date answer
	// here the way the form answers them today.
	first := res.Commits[0].Changes[0]
	if first.Kind != monshi.ChangeValue || first.Path != "application.kind" {
		t.Errorf("the first commit's first entry is %s at %q, want the kind",
			first.Kind, first.Path)
	}
	if got := first.Value.Tag(); got != "PERSON" {
		t.Errorf("the trail says kind %q, want the committed tag", got)
	}
	var fee bool
	for _, ch := range head.Changes {
		if ch.Path != "application.fee" {
			continue
		}
		fee = true
		if k := ch.Value.Kind(); k != monshi.KindDecimal {
			t.Errorf("the trail's fee is %v, want a decimal", k)
		}
		if got := ch.Value.Decimal().String(); got != "1500" {
			t.Errorf("the trail's fee is %q, want the amount at the scale the "+
				"type declares", got)
		}
	}
	if !fee {
		t.Error("the last commit has no fee entry, so the render above said " +
			"nothing about the decimal")
	}
}

// History cursors are revision numbers. Next continues the page; an empty page
// ends the traversal.
func TestHistoryPagesAndACursorThatRanOut(t *testing.T) {
	_, dana, _, _, _ := trailPast(t)
	whole, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var paged []monshi.Commit
	for cur := uint64(0); ; {
		page, err := dana.History(ctx(), cur, 2)
		if err != nil {
			t.Fatalf("History(%d, 2): %v", cur, err)
		}
		if len(page.Commits) == 0 {
			if page.Next != 0 {
				t.Errorf("an empty page continues at %d, want 0", page.Next)
			}
			break
		}
		if page.Revision != whole.Revision {
			t.Errorf("a page reports head %d, want %d", page.Revision,
				whole.Revision)
		}
		// `Next` continues exactly when the page stopped short of the head, and
		// a caller that ignored it would ask for the beginning again: 0 is both
		// "nothing follows" and the cursor for "start over", so the loop has to
		// read the flag rather than the cursor. That is why the page reports the
		// head beside it — the one number a caller cannot infer from a short
		// page.
		last := page.Commits[len(page.Commits)-1].Revision
		want := uint64(0)
		if last < whole.Revision {
			want = last + 1
		}
		if page.Next != want {
			t.Errorf("a page ending at %d of %d continues at %d, want %d",
				last, whole.Revision, page.Next, want)
		}
		if len(page.Commits) > 2 {
			t.Errorf("a page asked for 2 holds %d", len(page.Commits))
		}
		paged = append(paged, page.Commits...)
		if page.Next == 0 {
			break
		}
		cur = page.Next
		if len(paged) > len(whole.Commits) {
			t.Fatalf("the walk never stopped, after %d commits", len(paged))
		}
	}
	if len(paged) != len(whole.Commits) {
		t.Fatalf("paging read %d commits and one call read %d", len(paged),
			len(whole.Commits))
	}
	for i := range paged {
		if paged[i].Revision != whole.Commits[i].Revision ||
			len(paged[i].Changes) != len(whole.Commits[i].Changes) {
			t.Errorf("page %d is %+v, want %+v", i, paged[i],
				whole.Commits[i])
		}
	}
	// A cursor past the head is nothing to read, and nothing to refuse: the trail
	// is dense, so "there is no revision 900" and "you have read everything" are
	// the same fact, and a loop that stops on an empty page is what a host writes.
	page, err := dana.History(ctx(), 900, 5)
	if err != nil {
		t.Fatalf("History past the head: %v", err)
	}
	if len(page.Commits) != 0 || page.Next != 0 || page.Revision != 6 {
		t.Errorf("a cursor past the head is %+v", page)
	}
	// A `limit` of 0 means everything.
	if all, err := dana.History(ctx(), 1, 0); err != nil ||
		len(all.Commits) != len(whole.Commits) {
		t.Errorf("History(1, 0) is %d commits, err %v", len(all.Commits), err)
	}
	if _, err := dana.History(ctx(), 0, -1); !monshi.Failed(err,
		monshi.InvalidOp) {
		t.Errorf("a negative page fails %v, want INVALID_OPERATION", err)
	}
	// A cursor of 0 and a cursor of 1 ask the same question, because revision 0 has
	// no entry to start at: creating a case commits nothing.
	if also, err := dana.History(ctx(), 1, 6); err != nil ||
		len(also.Commits) != len(whole.Commits) {
		t.Errorf("History(1, 6) is %d commits, err %v", len(also.Commits), err)
	}
	// And the one trap the paging contract has: `Next` answers 0 both when the
	// trail is done and as the cursor for "from the beginning", so a caller that
	// copies it without looking reads the whole history a second time. A page that
	// ends at the head is exactly the page that says so, and `Revision` is on the
	// result for the caller that wants to check rather than trust.
	done, err := dana.History(ctx(), 5, 2)
	if err != nil {
		t.Fatalf("History at the head: %v", err)
	}
	if len(done.Commits) != 2 || done.Next != 0 {
		t.Fatalf("the last page is %+v, want two commits and no continuation",
			done)
	}
	again, err := dana.History(ctx(), done.Next, 2)
	if err != nil {
		t.Fatalf("History from the continuation token: %v", err)
	}
	if len(again.Commits) != 2 || again.Commits[0].Revision != 1 {
		t.Errorf("copying a finished page's `Next` back in is %+v: it restarts "+
			"the trail, which is why 0 means done and callers must read it",
			again.Commits)
	}
	rt := monshi.NewRuntime()
	fresh, err := rt.Create(ctx(),
		define(t, rt, "trail.new", trailSrc).ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := fresh.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History of a new case: %v", err)
	}
	if len(res.Commits) != 0 || res.Next != 0 || res.Withheld != 0 ||
		res.Revision != 0 {
		t.Errorf("a new case's trail is %+v, want nothing at revision 0", res)
	}
	if also, err := fresh.History(ctx(), 3, 0); err != nil ||
		len(also.Commits) != 0 {
		t.Errorf("a cursor past an empty head is %+v, err %v", also, err)
	}
	// A handle a host never got back from a runtime is refused the way every
	// operation says an unknown case is: the zero Case is not a case.
	if _, err := (monshi.Case{}).History(ctx(), 0, 0); !monshi.Failed(err,
		monshi.NotFound) {
		t.Errorf("History through a zero handle fails %v, want NOT_FOUND", err)
	}
}

// underGrant is the auditor's whole view of the case: one subtree, addressed at
// the collection and through it. The test spells the grant as text rather than
// borrowing the package's mask, because the claim is that the trail's visibility
// and the form's agree — and two copies of the walker would always agree.
func underGrant(path string) bool {
	return path == "application.notes" ||
		strings.HasPrefix(path, "application.notes.")
}

// History exposes commit envelopes to this actor, but omits changes to fields
// outside the actor's view.
func TestHistoryKeepsTheRecordAndDropsTheContent(t *testing.T) {
	_, dana, _, auditor, rows := trailPast(t)
	whole, err := dana.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	res, err := auditor.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the auditor: %v", err)
	}
	if len(res.Commits) != len(whole.Commits) {
		t.Fatalf("the auditor read %d records, want the same %d",
			len(res.Commits), len(whole.Commits))
	}
	if res.Withheld == 0 {
		t.Error("the auditor's trail hides nothing, so the mask is not applied")
	}
	var shown, hidden int
	for i, c := range res.Commits {
		for _, ch := range c.Changes {
			if !underGrant(ch.Path) {
				t.Errorf("the auditor reads %q in commit %d, and the role's "+
					"grant reaches one subtree only", ch.Path, c.Revision)
			}
			shown++
		}
		for _, ch := range whole.Commits[i].Changes {
			if !underGrant(ch.Path) {
				hidden++
			}
		}
	}
	if shown == 0 {
		t.Error("the auditor reads no content at all, so the grant above is " +
			"wider than the fixture")
	}
	// The records survive with no content at all: the commits the auditor may not
	// see a byte of are each still a revision with a hand, an operation and a time
	// beside it. That is what an audit of a case one cannot read consists of, and
	// the argument about a wall that is not a refusal applies here — so does the
	// fact that a `Transition` record always has no visible changes, which a read
	// that dropped empty records would delete along with the case's stage history.
	var silent int
	for _, c := range res.Commits {
		if len(c.Changes) != 0 {
			continue
		}
		silent++
		if c.By == "" || c.Op == "" || c.At.IsZero() || c.Stage == "" {
			t.Errorf("commit %d lost its envelope with its content",
				c.Revision)
		}
	}
	if silent < 3 {
		t.Errorf("only %d of the auditor's records are silent, so the fixture "+
			"no longer hides content behind a grant", silent)
	}
	// What the applicant sees and the auditor does not is exactly the withheld
	// count, and the count is over entries and not records.
	if res.Withheld != hidden {
		t.Errorf("the page counts %d withheld entries and the grant hides %d",
			res.Withheld, hidden)
	}
	// The stage and the head are the same answers the view gives, which is
	// why filtering envelopes would be a disclosure that hides nothing: a
	// caller who can open the case already holds both numbers, and the trail's
	// length *is* the head revision.
	view, err := auditor.View(ctx())
	if err != nil {
		t.Fatalf("View as the auditor: %v", err)
	}
	last := res.Commits[len(res.Commits)-1]
	if view.Revision != res.Revision || view.Stage != last.Stage {
		t.Errorf("the trail ends at %d/%q and the view says %d/%q: two reads "+
			"of one case disagree", res.Revision, last.Stage, view.Revision,
			view.Stage)
	}
	// And the rows the auditor may see are the same rows the applicant may see, at
	// the same addresses: the mask prunes declarations, but the addresses do not
	// move with it.
	got := changes(t, res, 2, monshi.ChangeRows)
	if len(got) != 1 || len(got[0].Rows) != len(rows) {
		t.Errorf("the auditor's set entry is %+v, want both rows", got)
	}
	// The verdicts are behind the same wall: a decision about the unit and the
	// attestation on it are not in the subtree this actor may see, and the page
	// says two things are missing without saying which.
	if len(changes(t, res, 3, monshi.ChangeReview)) != 0 ||
		len(changes(t, res, 4, monshi.ChangeSign)) != 0 {
		t.Error("the auditor reads a verdict the grant does not reach")
	}
	if len(changes(t, whole, 3, monshi.ChangeReview)) != 1 ||
		len(changes(t, whole, 4, monshi.ChangeSign)) != 1 {
		t.Fatal("the applicant does not read both verdicts, so the mask above " +
			"pruned nothing worth pruning")
	}
}

// The envelope identifies the actor and revision; the change identifies the
// review unit or signature field and its payload.
func TestAVerdictEntryNamesTheUnitAndCarriesTheAccount(t *testing.T) {
	_, dana, registrar, _, _ := trailPast(t)
	res, err := registrar.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	decisions := changes(t, res, 3, monshi.ChangeReview)
	if len(decisions) != 1 {
		t.Fatalf("the decision commit holds %d entries: %+v", len(decisions),
			res.Commits[2])
	}
	// The entry addresses the unit and not a row: a `verifiable` form is decided
	// as a whole, and a host that read it as one instance's verdict would go and
	// fix the wrong row.
	if got := decisions[0].Path; got != "application" {
		t.Errorf("the decision entry addresses %q, want the unit itself", got)
	}
	if got := decisions[0].Review; got.Unit != "application" ||
		got.State != monshi.ReviewApproved || got.Comment != "clean" {
		t.Errorf("the decision entry reports %+v, want the approve and the "+
			"comment beside it", got)
	}
	// The hand and the number are the record's: an entry keeps no second copy of an
	// author, which is what makes the two agree by construction rather than by a
	// commit writing the same fact twice.
	head := res.Commits[2]
	if got := decisions[0].Review; got.By != "elliot" ||
		got.At != head.Revision {
		t.Errorf("the decision entry says %s at %d, want the record's %s at %d",
			got.By, got.At, head.By, head.Revision)
	}
	// The attestation is the other verdict kind, on the field and not the unit.
	sigs := changes(t, res, 4, monshi.ChangeSign)
	if len(sigs) != 1 {
		t.Fatalf("the signature commit holds %d entries: %+v", len(sigs),
			res.Commits[3])
	}
	if got := sigs[0].Path; got != "application.submitted" {
		t.Errorf("the attestation is at %q, want the signature field", got)
	}
	if got := sigs[0].Signature; got.Evidence != "envelope-7" ||
		got.By != "dana" || got.At != res.Commits[3].Revision {
		t.Errorf("the attestation reports %+v, want the hand, the record's "+
			"revision and the reference", got)
	}
	// Both verdicts remain in history after the content commit clears them from
	// the current state.
	if erased := changes(t, res, 6, monshi.ChangeValue); len(erased) == 0 {
		t.Fatal("the last commit wrote no values, so nothing erased either")
	}
	state, err := dana.View(ctx(), "application")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, state, "application").Review.State; got !=
		monshi.ReviewPending {
		t.Errorf("after the content commit the unit reads %s, want PENDING "+
			"while revision 3 still says APPROVED", got)
	}
	if got := node(t, state,
		"application.submitted").Signature.By; got != "" {
		t.Errorf("after the content commit the field carries %q, want no "+
			"signature while revision 4 still names dana", got)
	}
}

// TestHistoryRefusesWhatViewRefuses is the boundary with the drift rule, and
// it is the read where that rule does its stated work. compat.go compares each
// slot's *path* and says the reason plainly: dropping the name from the path space
// "would leave committed history pointing at a path that no longer resolves". A
// view addresses what the caller typed just now, so a moved ordinal could only
// mis-render that one value; a trail addresses what a past call recorded, so the
// same move makes every entry an address in a space that no longer exists — a
// relabel laundered as history. The refusal is the store's, identical to the one
// `View` pays, and this test says so about both halves: a rename is structural
// here, and a semantic edit is read through, changing what the page discloses
// without changing one entry.
func TestHistoryRefusesWhatViewRefuses(t *testing.T) {
	rt, dana, _, auditor, _ := trailPast(t)
	before, err := auditor.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the auditor: %v", err)
	}
	if before.Withheld == 0 {
		t.Fatal("the auditor's page hides nothing, so the reads below " +
			"cannot show a semantic edit landing")
	}

	// A rename changes the path space.
	renamed, err := monshi.Load("trail.app", strings.NewReader(strings.Replace(
		trailSrc, "full_name : string required;",
		"codename : string required;", 1)))
	if err != nil {
		t.Fatalf("Load of the relabelled case: %v", err)
	}
	if err := rt.Replace(renamed); err != nil {
		t.Fatalf("Replace with a relabel: %v", err)
	}
	if _, err := dana.History(ctx(), 0, 0); !monshi.Failed(err,
		monshi.SchemaMismatch) {
		t.Errorf("History under a relabel fails %v, want SCHEMA_MISMATCH: an "+
			"entry's address is a path in the ordinal space", err)
	}
	// The refusal is not this read's invention: every operation on the case
	// answers the same way until the host fixes the registry, and the data is
	// untouched rather than half-read.
	if _, err := dana.View(ctx()); !monshi.Failed(err, monshi.SchemaMismatch) {
		t.Errorf("View under the relabel fails %v, so the trail was not "+
			"reading the same refusal", err)
	}
	if _, err := dana.Rebase(ctx()); !monshi.Failed(err,
		monshi.SchemaMismatch) {
		t.Errorf("Rebase under the relabel fails %v, so the refusal is not "+
			"one read's caution", err)
	}
	// Inserting a field moves the ordinals after it, and is refused for the same
	// reason a view is: an entry keyed by ordinal 6 would name whatever is at 6
	// now.
	grown, err := monshi.Load("trail.app", strings.NewReader(strings.Replace(
		trailSrc, "fee       : amount;",
		"fee       : amount;\n        extra : string;", 1)))
	if err != nil {
		t.Fatalf("Load of the widened case: %v", err)
	}
	if err := rt.Replace(grown); err != nil {
		t.Fatalf("Replace with a wider definition: %v", err)
	}
	if _, err := dana.History(ctx(), 0, 0); !monshi.Failed(err,
		monshi.SchemaMismatch) {
		t.Errorf("History under a moved definition fails %v, want "+
			"SCHEMA_MISMATCH", err)
	}
	// A case created *under* the widened definition reads its own trail normally:
	// the refusal is about the stored data and not about the definition.
	younger, err := rt.Create(ctx(), grown.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create under the widened definition: %v", err)
	}
	if got, err := younger.History(ctx(), 0, 0); err != nil ||
		len(got.Commits) != 0 {
		t.Errorf("a case written into the new ordinal space is %+v, err %v",
			got, err)
	}

	// And a purely semantic edit — one permission word, which compat.go accepts
	// because nothing about an ordinal moved — is *not* a refusal. It lands on the
	// mask, because the mask is evaluated at the read and not recorded at the
	// commit: the same stored entries, the same order and the same envelopes, now
	// with nothing withheld. That is the honest half of the rule, and the
	// reason a page stores no promises about who could see what.
	broadened, err := monshi.Load("trail.app", strings.NewReader(
		strings.Replace(trailSrc, "allow view application.notes;",
			"allow view all;", 1)))
	if err != nil {
		t.Fatalf("Load of the widened grant: %v", err)
	}
	if err := rt.Replace(broadened); err != nil {
		t.Fatalf("Replace with a wider grant: %v", err)
	}
	res, err := auditor.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History under the wider grant: %v", err)
	}
	if res.Withheld != 0 {
		t.Errorf("the wider grant still hides %d entries", res.Withheld)
	}
	if len(res.Commits) != len(before.Commits) ||
		res.Revision != before.Revision {
		t.Fatalf("the grant change moved the trail: %d records at head %d, was "+
			"%d at %d", len(res.Commits), res.Revision, len(before.Commits),
			before.Revision)
	}
	for i, c := range res.Commits {
		if c.Revision != before.Commits[i].Revision || c.By !=
			before.Commits[i].By || c.Op != before.Commits[i].Op ||
			c.Stage != before.Commits[i].Stage {
			t.Errorf("record %d's envelope moved: %v/%v/%v at %d, was %v/%v/"+
				"%v at %d", i, c.By, c.Op, c.Stage, c.Revision,
				before.Commits[i].By, before.Commits[i].Op,
				before.Commits[i].Stage, before.Commits[i].Revision)
		}
		if len(c.Changes) < len(before.Commits[i].Changes) {
			t.Errorf("record %d discloses %d entries where the narrow grant "+
				"showed %d: a wider grant cannot show less", i, len(c.Changes),
				len(before.Commits[i].Changes))
		}
	}
	// The verdicts the narrow grant hid are now on the page, and they are the
	// same acts: the trail did not re-derive anything to disclose them.
	if len(changes(t, res, 3, monshi.ChangeReview)) != 1 ||
		len(changes(t, res, 4, monshi.ChangeSign)) != 1 {
		t.Errorf("the wider grant does not read both verdicts back: %+v",
			res.Commits[2:4])
	}
}

// maskSrc is the fixture the last two claims of the masking rule need, and
// `trailSrc` cannot state them: one grant that names a *member* of a row rather
// than the row's collection, and one that exists only while the case sits at a
// particular stage.
const maskSrc = `
roles {
    Applicant {
        allow view, edit, submit application;
        allow view, edit, submit archive;
    }
    Registrar {
        allow view all;
    }
    Member {
        // One field of one row's members: everything this actor may read hangs
        // off a container it has no grant on at all.
        allow view application.notes.who;
        allow view archive.filed_at;
    }
    Visitor {
        // A grant with almost no reach of its own: the
        // subtree the stage below hands it is readable
        // only while the case sits there, because the
        // mask is a function of roles and stage
        // together.
        allow view archive;
    }
}
forms {
    application {
        full_name : string;

        notes multiple {
            who   : string;
            since : date;
        }
    }

    archive {
        filed_at : string;
    }
}
stages {
    draft {
        allow Visitor view application.notes;
        allow Registrar accept;
        accept filed;
    }

    filed {}
}
`

// TestTheTrailIsMaskedAtTheNodeAndStageTheCaseSitsAt prices the two halves of the
// rule `disclosed` states: the mask is the view's own function, evaluated now, and
// so it says something a per-node lookup would get wrong in both directions.
//
// A grant on one member of a row discloses that member's values and the row
// addresses they hang off — because the view shows a container to anyone it
// shows a child to, and an entry that listed instances the reader could not
// address would be a page that cannot be read on. It does not disclose the sibling
// member the grant does not name.
//
// The other half moves with the case. A stage-scoped subtree grant made the notes
// readable at `draft`; after the edge, the same actor reads the same stored
// entries and is told none of them. Nothing about the trail changed, which is the
// point: this read has no second rule about what somebody could see then, because
// such a rule would be a disclosure the definition no longer grants.
func TestTheTrailIsMaskedAtTheNodeAndStageTheCaseSitsAt(t *testing.T) {
	rt := monshi.NewRuntime()
	d := define(t, rt, "mask.trail", maskSrc)
	dana, err := rt.Create(ctx(), d.ID(), monshi.Actor{User: "dana",
		PrimaryRole: "Applicant"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res := mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	res = mustTrailUpdate(t, dana, trailWorking(t, dana),
		monshi.Add("application.notes"), monshi.Add("application.notes"))
	rows := res.Created
	res = mustTrailUpdate(t, dana, res.Working,
		monshi.Set(rows[0]+".who", monshi.Text("Ada")),
		monshi.Set(rows[1]+".who", monshi.Text("Zoe")),
		monshi.Set(rows[0]+".since", monshi.Date(
			time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))),
		monshi.Set(rows[1]+".since", monshi.Date(
			time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC))))
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	member := mustTrailOpen(t, rt, dana.ID(), monshi.Actor{User: "bo",
		PrimaryRole: "Member"})
	visitor := mustTrailOpen(t, rt, dana.ID(), monshi.Actor{User: "alf",
		PrimaryRole: "Visitor"})

	// Revision 2 is the batch that both grants are about: five entries, one row
	// set and four cells.
	page, err := member.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the member reader: %v", err)
	}
	set := entry(t, page.Commits[1], monshi.ChangeRows)
	if len(set.Rows) != 2 || set.Rows[0] != rows[0] ||
		set.Rows[1] != rows[1] {
		t.Errorf("the member reader's set entry is %v, want both row addresses"+
			": a value the actor may read is unaddressable without them",
			set.Rows)
	}
	var shown []string
	for _, ch := range page.Commits[1].Changes {
		if ch.Kind == monshi.ChangeValue {
			shown = append(shown, ch.Path)
		}
	}
	if len(shown) != 2 || shown[0] != rows[0]+".who" ||
		shown[1] != rows[1]+".who" {
		t.Errorf("the member reader shows %v of revision 2, want the two `who`"+
			" cells only", shown)
	}
	// The whole page: two commits, six entries, of which three are beyond this
	// grant — the form's own field and the two dated cells.
	if page.Withheld != 3 {
		t.Errorf("the member reader's page withholds %d entries, want 3",
			page.Withheld)
	}
	if got := len(page.Commits[0].Changes); got != 0 {
		t.Errorf("the first commit shows %d entries to a reader whose grant is "+
			"one member of a group, want none", got)
	}

	// The same page under a grant that names the collection itself: all five of
	// revision 2's entries, and still not the form's field.
	at, err := visitor.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the visitor: %v", err)
	}
	if got := len(at.Commits[1].Changes); got != 5 {
		t.Errorf("the visitor sees %d of revision 2, want the whole subtree",
			got)
	}
	if at.Withheld != page.Withheld-2 {
		t.Errorf("the visitor withholds %d entries, want 1: the subtree grant "+
			"reaches the two dated cells the member grant does not",
			at.Withheld)
	}

	// Now move the case out of the stage that carried the visitor's grant, and
	// read the same trail again.
	registrar := mustTrailOpen(t, rt, dana.ID(), monshi.Actor{User: "elliot",
		PrimaryRole: "Registrar"})
	if _, err := registrar.Transition(ctx(), "filed",
		trailHead(t, dana)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	moved, err := visitor.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History after the edge: %v", err)
	}
	if len(moved.Commits) != len(at.Commits)+1 {
		t.Fatalf("the trail holds %d records after the edge, want %d: the move "+
			"is a commit and the read says so", len(moved.Commits),
			len(at.Commits)+1)
	}
	var silent int
	for i, c := range at.Commits {
		got := moved.Commits[i]
		if len(got.Changes) != 0 {
			t.Errorf("commit %d still shows %d entries after the stage that "+
				"granted them went away", got.Revision, len(got.Changes))
		}
		if got.Revision != c.Revision || got.By != c.By || got.Op != c.Op ||
			!got.At.Equal(c.At) {
			t.Errorf("commit %d's envelope changed with the stage: %v",
				got.Revision, got)
		}
		silent++
	}
	// The edge's own record was never about content at all, so it is silent for
	// this actor in both stages — the case where dropping empty records would
	// erase the only place a stage move survives.
	last := moved.Commits[len(moved.Commits)-1]
	if len(last.Changes) != 0 || last.Op != monshi.CommitTransition ||
		last.Stage != "filed" {
		t.Errorf("the edge's record is %+v, want a silent transition at filed",
			last)
	}
	silent++
	total := at.Withheld
	for _, c := range at.Commits {
		total += len(c.Changes)
	}
	if moved.Withheld != total {
		t.Errorf("the page after the edge withholds %d entries, want the "+
			"trail's %d: the grant that reached them was stage-scoped",
			moved.Withheld, total)
	}
	if silent != len(moved.Commits) {
		t.Errorf("%d of %d records are silent, and every entry was outside "+
			"the grant", silent, len(moved.Commits))
	}
	// The member reader is untouched by the edge, because the mask moved only
	// for the grant that was stage-scoped: two reads of one trail that differ
	// because of who is asking, which is the property the whole rule exists for.
	after, err := member.History(ctx(), 0, 0)
	if err != nil {
		t.Fatalf("History as the member reader after the edge: %v", err)
	}
	if after.Withheld != page.Withheld ||
		len(after.Commits[1].Changes) != len(page.Commits[1].Changes) {
		t.Errorf("the member reader's page moved with the stage: withheld %d "+
			"(was %d), entries %d (was %d)", after.Withheld, page.Withheld,
			len(after.Commits[1].Changes), len(page.Commits[1].Changes))
	}
}

// changes is one revision's entries of one kind. The helper exists because most
// claims above are about "what revision 2 wrote", and a test that indexed the trail
// by hand would be asserting its own arithmetic.
func changes(t *testing.T, res monshi.HistoryResult, rev uint64,
	kind monshi.ChangeKind) []monshi.CommitChange {
	t.Helper()
	for _, c := range res.Commits {
		if c.Revision != rev {
			continue
		}
		var out []monshi.CommitChange
		for _, ch := range c.Changes {
			if ch.Kind == kind {
				out = append(out, ch)
			}
		}
		return out
	}
	t.Fatalf("the trail holds no revision %d: %+v", rev, res.Commits)
	return nil
}

// entry is one commit's single entry of a kind, for the claims about exactly one
// thing having moved.
func entry(t *testing.T, c monshi.Commit, kind monshi.ChangeKind,
) monshi.CommitChange {
	t.Helper()
	var out *monshi.CommitChange
	for i, ch := range c.Changes {
		if ch.Kind != kind {
			continue
		}
		if out != nil {
			t.Fatalf("revision %d holds two %s entries", c.Revision, kind)
		}
		out = &c.Changes[i]
	}
	if out == nil {
		t.Fatalf("revision %d holds no %s entry: %+v", c.Revision, kind,
			c.Changes)
	}
	return *out
}
