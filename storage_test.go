package monshi_test

// External tests for the exported storage contract (DESIGN §8, RUNTIME
// §10): that a runtime built on `Storage` behaves exactly like the default
// one, that durable state crosses runtimes while definitions stay local,
// and that a host's refusals arrive in the engine's own words. What the
// adapter does *inside* — which sentinel becomes which sentence, what the
// commit encodes in which order — is in storage_store_test.go; what lives
// here is only what a host can see: results, renders, errors and receipts.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
	"github.com/shopspring/decimal"
)

// contractScript runs the lifecycle through one runtime and writes down
// everything a host could observe: every result number, every rendered
// node, every refusal in the caller's own words. The transcript carries no
// wall-clock stamps — commit `At` is the store's moment and the two runs
// never share one — and no case ID beyond the first line, which both
// stores issue as 1 from their own counters. A transcript that matches
// line for line is DESIGN §8's promise ("the operations' shapes do not
// change") stated as data.
func contractScript(t *testing.T, rt *monshi.Runtime) string {
	t.Helper()
	var sb strings.Builder
	line := func(format string, a ...any) {
		fmt.Fprintf(&sb, format+"\n", a...)
	}
	d := define(t, rt, "contract", trailSrc)

	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	line("create id=%d", dana.ID())

	view := func(c monshi.Case, who string, paths ...string) {
		v, err := c.View(ctx(), paths...)
		if err != nil {
			line("view %s err=%v", who, err)
			return
		}
		line("view %s head=%d work=%d stage=%s", who, v.Revision,
			v.Working, v.Stage)
		for _, n := range v.Roots {
			renderNode(&sb, n)
		}
	}

	// 1: content, then the submit that publishes it.
	v, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	line("create work=%d", v.Working)
	res, err := dana.Update(ctx(), v.Working,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")),
		monshi.Set("application.fee", monshi.Decimal(
			decimal.NewFromInt(15).Div(decimal.NewFromInt(2)))),
	)
	line("update work=%d affected=%v err=%v", res.Working, res.Affected, err)
	cr, err := dana.Submit(ctx(), "application", res.Working)
	line("submit rev=%d stage=%s missing=%v aff=%v err=%v", cr.Revision,
		cr.Stage, cr.Missing, cr.Affected, err)

	// 2: the attestation and the decision, one revision apart.
	head, err := dana.Revision(ctx())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	sr, err := dana.Sign(ctx(), "application.submitted", head, "web-audit-9")
	line("sign rev=%d missing=%v aff=%v err=%v", sr.Revision, sr.Missing,
		sr.Affected, err)

	registrar := mustTrailOpen(t, rt, dana.ID(), trailRuler)
	head, _ = registrar.Revision(ctx())
	rr, err := registrar.Review(ctx(), "application", monshi.ReviewApprove,
		head, "complete")
	line("review rev=%d stage=%s aff=%v err=%v", rr.Revision, rr.Stage,
		rr.Affected, err)

	// 3: a draft that survives being behind the case. dana edits, elliot
	// moves the stage past the draft, and the rebase brings it forward
	// cleanly — every step taken through the exported verbs only.
	v, err = dana.View(ctx())
	if err == nil {
		_, err = dana.Update(ctx(), v.Working,
			monshi.Add("application.notes"))
	}
	line("draft err=%v", err)
	head, _ = registrar.Revision(ctx())
	tr, err := registrar.Transition(ctx(), "filed", head)
	line("transition rev=%d stage=%s aff=%v missing=%v err=%v", tr.Revision,
		tr.Stage, tr.Affected, tr.Missing, err)
	rb, err := dana.Rebase(ctx())
	line("rebase work=%d base=%d conflicts=%v err=%v", rb.Working, rb.Base,
		rb.Conflicts, err)
	cr2, err := dana.Submit(ctx(), "application", rb.Working)
	line("submit2 rev=%d missing=%v err=%v", cr2.Revision, cr2.Missing, err)

	// 4: the refusals a host actually receives, in the store's own voice.
	_, err = dana.Update(ctx(), 0, monshi.Set("application.full_name",
		monshi.Text("Grace")))
	line("stale-update err=%v", err)
	_, err = registrar.Transition(ctx(), "draft", cr2.Revision)
	line("no-edge err=%v", err)
	_, err = registrar.Submit(ctx(), "application", 1)
	line("no-submit-grant err=%v", err)

	// 5: the same case rendered through the auditor's narrower mask, then
	// the trail in pages.
	auditor := mustTrailOpen(t, rt, dana.ID(), trailAudit)
	view(auditor, "bo")
	view(dana, "dana", "application")
	page, err := dana.History(ctx(), 0, 0)
	line("history head=%d next=%d withheld=%d err=%v", page.Revision,
		page.Next, page.Withheld, err)
	for _, c := range page.Commits {
		if c.At.IsZero() {
			t.Errorf("%s committed at the zero time", c.Op)
		}
		renderCommit(&sb, c)
	}
	page2, err := dana.History(ctx(), 2, 3)
	line("page2 next=%d withheld=%d err=%v", page2.Next, page2.Withheld, err)
	for _, c := range page2.Commits {
		renderCommit(&sb, c)
	}

	// 6: `Discard`'s three answers: a guard that holds, a no-op that is a
	// success, and a number for a copy that is not there.
	v, err = dana.View(ctx())
	if err == nil {
		line("discard err=%v", dana.Discard(ctx(), v.Working))
		line("discard0 err=%v", dana.Discard(ctx(), 0))
	}
	line("discard7 err=%v", registrar.Discard(ctx(), 7))

	// 6.5: the two answers a *late* copy gives. An edit after a discard
	// initializes at the head it met, so the rebase of a draft already
	// at the head reports that base — revision zero would merge a trail
	// the copy never was behind (§5.7's rule, kept by the store's init).
	// And a rebase asked with no copy at all is §6.5's refusal, not a
	// no-op: `Draft` finds, and finding nothing is the answer.
	if _, err := dana.Update(ctx(), 0, monshi.Set("application.full_name",
		monshi.Text("Ada"))); err == nil {
		rb2, err := dana.Rebase(ctx())
		line("late-rebase work=%d base=%d conflicts=%v err=%v", rb2.Working,
			rb2.Base, rb2.Conflicts, err)
		line("late-discard err=%v", dana.Discard(ctx(), rb2.Working))
	}
	_, err = registrar.Rebase(ctx())
	line("no-copy-rebase err=%v", err)

	view(dana, "dana-final", "application")
	return sb.String()
}

// renderNode writes one view node — its address, class, value and the mask
// answers — and walks its children, so a transcript renders a subtree the
// way a client would: the whole visible shape, not just the root.
func renderNode(sb *strings.Builder, n *monshi.ViewNode) {
	fmt.Fprintf(sb, "node %s kind=%s value=%s req=%v edit=%v sub=%v "+
		"rev=%v sig=%v\n", n.Path, n.Kind, valueText(n.Value), n.Required,
		n.CanEdit, n.CanSubmit, n.Review, n.Signature)
	for _, c := range n.Children {
		renderNode(sb, c)
	}
}

// valueText renders a value by the class `Kind` reports, through the
// getter that class designates — the getter-switch discipline §5.1's read
// follows, so a transcript that matches is matching real data and not one
// getter's accident.
func valueText(v monshi.Value) string {
	switch v.Kind() {
	case monshi.KindNone:
		return ""
	case monshi.KindText:
		return strconv.Quote(v.Text())
	case monshi.KindInteger:
		return strconv.FormatInt(v.Int(), 10)
	case monshi.KindDecimal:
		return v.Decimal().String()
	case monshi.KindBoolean:
		return strconv.FormatBool(v.Bool())
	case monshi.KindDate:
		return v.Date().Format("2006-01-02")
	case monshi.KindDatetime:
		return v.DateTime().Format("2006-01-02T15:04:05")
	case monshi.KindBinary:
		return fmt.Sprintf("%x", v.Binary())
	case monshi.KindTag:
		return v.Tag()
	case monshi.KindUser:
		return string(v.User())
	}
	return "?"
}

// renderCommit writes one history entry without its timestamp: the
// revision, the act, the hand, the stage it stood at, and every change —
// values, row lists, verdicts — as recorded.
func renderCommit(sb *strings.Builder, c monshi.Commit) {
	fmt.Fprintf(sb, "commit %d op=%s by=%s stage=%s changes=%d\n",
		c.Revision, c.Op, c.By, c.Stage, len(c.Changes))
	for _, ch := range c.Changes {
		fmt.Fprintf(sb, "  change %s %s value=%s rows=%v rev=%v sig=%v\n",
			ch.Kind, ch.Path, valueText(ch.Value), ch.Rows, ch.Review,
			ch.Signature)
	}
}

// TestTheContractPathAnswersLikeTheDefaultOne is DESIGN §8's claim that
// handing case state to a host's `Storage` changes nothing a caller can
// observe: the same lifecycle script run on `NewRuntime` and on
// `NewRuntimeWithStorage(NewMemStorage())` must write identical
// transcripts. `nil` is the same promise in one call: a runtime built on
// no storage *is* the default one.
func TestTheContractPathAnswersLikeTheDefaultOne(t *testing.T) {
	rtA := monshi.NewRuntime()
	a := contractScript(t, rtA)
	rtB := monshi.NewRuntimeWithStorage(monshi.NewMemStorage())
	b := contractScript(t, rtB)
	if b != a {
		for i := range min(len(a), len(b)) {
			if a[i] != b[i] {
				t.Fatalf("the transcript through the contract differs at"+
					" byte %d (lengths %d versus %d):\n--- default\n%s\n"+
					"--- contract\n%s", i, len(a), len(b), a, b)
			}
		}
		t.Fatalf("the transcript through the contract has length %d, the"+
			" default %d\n--- default\n%s\n--- contract\n%s", len(b),
			len(a), a, b)
	}
	rtC := monshi.NewRuntimeWithStorage(nil)
	if n := contractScript(t, rtC); n != a {
		t.Error("NewRuntimeWithStorage(nil) answered a different transcript" +
			" than NewRuntime")
	}
}

// TestStoredStateCrossesRuntimesDefinitionsDoNot is the point of the
// contract read from the second runtime's side: a case a host committed
// through one runtime opens, renders and continues through another built
// on the same `Storage`, because case state crossed and only the registry
// stayed local (DESIGN §8's shape, §3's reload rule). The second runtime
// that has *not* registered the case's definition gets the same
// SCHEMA_MISMATCH sentence the default path gives — the pairing is the
// handle's problem in both worlds.
func TestStoredStateCrossesRuntimesDefinitionsDoNot(t *testing.T) {
	store := monshi.NewMemStorage()
	rtA := monshi.NewRuntimeWithStorage(store)
	d := define(t, rtA, "relay", trailSrc)
	dana, err := rtA.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := dana.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("COMPANY")),
		monshi.Set("application.full_name", monshi.Text("Monshi Ltd")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	first, err := dana.Submit(ctx(), "application", res.Working)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rtB := monshi.NewRuntimeWithStorage(store)
	_, err = rtB.Open(ctx(), dana.ID(), trailOwner)
	var ce *monshi.CaseError
	if !errors.As(err, &ce) || ce.Reason != monshi.SchemaMismatch ||
		ce.Case != dana.ID() || !strings.Contains(ce.Detail, "relay") {
		t.Fatalf("Open without the definition = %v, want the SCHEMA_MISMATCH"+
			" naming the governing definition", err)
	}
	define(t, rtB, "relay", trailSrc)
	danaB, err := rtB.Open(ctx(), dana.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Open through the second runtime: %v", err)
	}
	rev, err := danaB.Revision(ctx())
	if err != nil || rev != first.Revision {
		t.Fatalf("the second runtime reads head %d (%v), want %d", rev, err,
			first.Revision)
	}
	page, err := danaB.History(ctx(), 0, 0)
	if err != nil || len(page.Commits) != 1 ||
		page.Commits[0].Revision != 1 {
		t.Fatalf("the second runtime reads the trail as %+v (%v), want one"+
			" entry at revision 1", page.Commits, err)
	}

	// And the case is live on the second runtime: content through to a
	// second commit that the *first* runtime then sees, because both read
	// one store and neither owns a private copy of the head.
	v, err := danaB.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	res, err = danaB.Update(ctx(), v.Working,
		monshi.Set("application.fee", monshi.Decimal(decimal.NewFromInt(50))))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := danaB.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit through the second runtime: %v", err)
	}
	rev, err = dana.Revision(ctx())
	if err != nil || rev != 2 {
		t.Fatalf("the first runtime reads head %d (%v), want the commit it"+
			" never made: 2", rev, err)
	}
}

// goneStore answers only for cases it never held: every Load says the ID
// is unknown, which is the sentinel contract, not a host's prose.
type goneStore struct{ monshi.Storage }

func (s goneStore) Load(context.Context, monshi.CaseID) (monshi.CaseRecord,
	error) {
	return monshi.CaseRecord{}, monshi.ErrNoSuchCase
}

// TestMissingCaseRefusesLikeTheStoreItself reads the same absence from
// both worlds: an ID the default store never issued and an ID a host
// store says it does not have must refuse with the same category, the
// same operation word and the same sentence — only the ID in the envelope
// can differ, and the fields say so while the string carries it.
func TestMissingCaseRefusesLikeTheStoreItself(t *testing.T) {
	rtA := monshi.NewRuntime()
	_, errA := rtA.Open(ctx(), 777, trailOwner)
	rtB := monshi.NewRuntimeWithStorage(goneStore{monshi.NewMemStorage()})
	d := define(t, rtB, "gone", trailSrc)
	_, errB := rtB.Create(ctx(), d.ID(), trailOwner)
	if !monshi.Failed(errB, monshi.NotFound) ||
		!strings.HasSuffix(errB.Error(), "no such case") {
		t.Fatalf("Create = %v, want NOT_FOUND ending in the store's own"+
			" absence sentence: the working-copy init reads the head", errB)
	}
	_, errO := rtB.Open(ctx(), 1, trailOwner)
	var ca, cb *monshi.CaseError
	if !errors.As(errA, &ca) || !errors.As(errO, &cb) {
		t.Fatalf("absences refused as %v and %v, want CaseErrors", errA,
			errO)
	}
	ca.Case, cb.Case = 0, 0 // the IDs are the stores' numbers, not the
	// sentence
	if *ca != *cb {
		t.Errorf("an unissued ID refused as %+v through the contract and"+
			" %+v through the default store: the fields must match", cb, ca)
	}
}

// staleCommitStore refuses every transaction with the guard's answer for a
// case it says is at revision 42. The engine must turn that into the same
// STALE_REVISION sentence its own guard writes, naming 42, and leave the
// case exactly where it was.
type staleCommitStore struct{ monshi.Storage }

func (s staleCommitStore) Commit(context.Context, monshi.CaseID,
	monshi.Transaction) (monshi.Receipt, error) {
	return monshi.Receipt{}, &monshi.Stale{Have: 42}
}

// TestGuardSentinelsBecomeTheEnginesOwnSentence pins the translation of a
// *Stale down to its words: a host cannot write a refusal sentence, and
// gets none — the engine's sentence with the host's number.
func TestGuardSentinelsBecomeTheEnginesOwnSentence(t *testing.T) {
	rt := monshi.NewRuntimeWithStorage(staleCommitStore{monshi.NewMemStorage()})
	d := define(t, rt, "stale", trailSrc)
	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := dana.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, err = dana.Submit(ctx(), "application", res.Working)
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("Submit = %v, want STALE_REVISION", err)
	}
	if got, want := err.Error(), "revision 42, not 0"; !strings.Contains(
		got, want) {
		t.Errorf("Submit refused with %q, want a sentence containing %q",
			got, want)
	}
	var ce *monshi.CaseError
	if errors.As(err, &ce) && ce.Cause != nil {
		t.Errorf("a guard refusal carried Cause %v, want none: the host"+
			" did not fail, it disagreed", ce.Cause)
	}
	rev, err := dana.Revision(ctx())
	if err != nil || rev != 0 {
		t.Fatalf("a refused transaction still moved the case: %d (%v)",
			rev, err)
	}
}

// downStore is a backend with an outage: once armed, every call returns
// the host's own error — which is not the engine's to re-word. The
// lifecycle runs un-armed so the handles are real; the verbs under test
// are then read against a store that has simply stopped answering.
type downStore struct {
	monshi.Storage
	down  error
	armed *bool
}

func (s downStore) Create(c context.Context, rec monshi.CaseRecord) (
	monshi.CaseID, error) {
	if *s.armed {
		return 0, s.down
	}
	return s.Storage.Create(c, rec)
}

func (s downStore) Load(c context.Context, id monshi.CaseID) (
	monshi.CaseRecord, error) {
	if *s.armed {
		return monshi.CaseRecord{}, s.down
	}
	return s.Storage.Load(c, id)
}

func (s downStore) Draft(c context.Context, id monshi.CaseID,
	user monshi.UserID) (monshi.WorkingRecord, bool, error) {
	if *s.armed {
		return monshi.WorkingRecord{}, false, s.down
	}
	return s.Storage.Draft(c, id, user)
}

func (s downStore) Save(c context.Context, id monshi.CaseID,
	user monshi.UserID, expected uint64, w monshi.WorkingRecord) error {
	if *s.armed {
		return s.down
	}
	return s.Storage.Save(c, id, user, expected, w)
}

func (s downStore) Discard(c context.Context, id monshi.CaseID,
	user monshi.UserID, expected uint64) error {
	if *s.armed {
		return s.down
	}
	return s.Storage.Discard(c, id, user, expected)
}

func (s downStore) Commit(c context.Context, id monshi.CaseID,
	tx monshi.Transaction) (monshi.Receipt, error) {
	if *s.armed {
		return monshi.Receipt{}, s.down
	}
	return s.Storage.Commit(c, id, tx)
}

func (s downStore) History(c context.Context, id monshi.CaseID, cursor uint64,
	limit int) ([]monshi.HistoryEntry, error) {
	if *s.armed {
		return nil, s.down
	}
	return s.Storage.History(c, id, cursor, limit)
}

// TestEveryVerbReportsTheOutage runs each exported operation over a
// backend whose every call fails, and requires the same answer from all
// of them: DESIGN §9's one category reserved for somebody else's
// explanation, with `errors.Is` still reaching the backend's own error
// through Cause. The translation table has no gaps a host could trip
// into.
func TestEveryVerbReportsTheOutage(t *testing.T) {
	down := errors.New("network went away")
	armed := false
	rt := monshi.NewRuntimeWithStorage(downStore{
		Storage: monshi.NewMemStorage(), down: down, armed: &armed})
	d := define(t, rt, "down", trailSrc)
	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create before the outage: %v", err)
	}
	armed = true
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"Create", func() error {
			_, e := rt.Create(ctx(), d.ID(),
				trailOwner)
			return e
		}},
		{"Open", func() error {
			_, e := rt.Open(ctx(), dana.ID(), trailOwner)
			return e
		}},
		{"Stage", func() error { _, e := dana.Stage(ctx()); return e }},
		{"Revision", func() error { _, e := dana.Revision(ctx()); return e }},
		{"View", func() error { _, e := dana.View(ctx()); return e }},
		{"Update", func() error {
			_, e := dana.Update(ctx(), 0,
				monshi.Set("application.kind", monshi.Tag("PERSON")))
			return e
		}},
		{"Submit", func() error {
			_, e := dana.Submit(ctx(), "application", 1)
			return e
		}},
		{"Transition", func() error {
			_, e := dana.Transition(ctx(), "filed",
				1)
			return e
		}},
		{"Review", func() error {
			_, e := dana.Review(ctx(), "application",
				monshi.ReviewApprove, 1, "x")
			return e
		}},
		{"Sign", func() error {
			_, e := dana.Sign(ctx(), "application.submitted",
				1, "x")
			return e
		}},
		{"History", func() error {
			_, e := dana.History(ctx(), 0, 0)
			return e
		}},
		{"Rebase", func() error { _, e := dana.Rebase(ctx()); return e }},
		{"Discard", func() error { return dana.Discard(ctx(), 1) }},
	} {
		err := tc.run()
		if !monshi.Failed(err, monshi.StorageFailure) {
			t.Errorf("%s = %v, want STORAGE_FAILURE", tc.name, err)
			continue
		}
		if !errors.Is(err, down) {
			t.Errorf("%s did not carry the host's error as Cause", tc.name)
		}
	}
}

// rottenStore hands back a State that is not a snapshot: the bytes the
// engine reads are the host's own prose, not what this package wrote.
// The one read that stores the case answers honestly — a store that
// fails its own first look could not have stored anything — and every
// later read is garbage. The adapter must say *this package* cannot
// read them, as a store failure, and never panic and never render:
// §8's promise about the field ("produced and read only by this
// package") read from the host's seat.
type rottenStore struct {
	monshi.Storage
	reads int
}

func (s *rottenStore) Load(c context.Context, id monshi.CaseID) (
	monshi.CaseRecord, error) {
	rec, err := s.Storage.Load(c, id)
	if err != nil {
		return rec, err
	}
	s.reads++
	if s.reads > 1 {
		rec.State = []byte("not a snapshot — a host's own sentence")
	}
	return rec, nil
}

func TestForeignBytesAreAStoreFailureNotACrash(t *testing.T) {
	rt := monshi.NewRuntimeWithStorage(
		&rottenStore{Storage: monshi.NewMemStorage()})
	d := define(t, rt, "rotten", trailSrc)
	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"View", func() error { _, e := dana.View(ctx()); return e }},
		{"Stage", func() error { _, e := dana.Stage(ctx()); return e }},
		{"Update", func() error {
			_, e := dana.Update(ctx(), 0,
				monshi.Set("application.kind", monshi.Tag("PERSON")))
			return e
		}},
	} {
		err := tc.run()
		if !monshi.Failed(err, monshi.StorageFailure) {
			t.Errorf("%s over foreign bytes = %v, want STORAGE_FAILURE",
				tc.name, err)
			continue
		}
		var ce *monshi.CaseError
		if errors.As(err, &ce) && ce.Cause == nil {
			t.Errorf("%s hid the codec's complaint: Cause is nil", tc.name)
		}
	}
}

// rottenTrail lets the whole lifecycle run and then rewrites the first
// trail entry with its own bytes, so the *history* read meets the
// boundary instead of the snapshot read.
type rottenTrail struct{ monshi.Storage }

func (s rottenTrail) History(_ context.Context, id monshi.CaseID,
	cursor uint64, limit int) ([]monshi.HistoryEntry, error) {
	page, err := s.Storage.History(ctx(), id, cursor, limit)
	if err != nil {
		return nil, err
	}
	if len(page) > 0 {
		page[0] = []byte("not a commit")
	}
	return page, nil
}

func TestAForeignTrailEntryIsAlsoAStoreFailure(t *testing.T) {
	rt := monshi.NewRuntimeWithStorage(rottenTrail{monshi.NewMemStorage()})
	d := define(t, rt, "rot2", trailSrc)
	dana, err := rt.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := dana.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := dana.Submit(ctx(), "application", res.Working); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// The case reads normally: the snapshot is the engine's own bytes.
	if v, err := dana.View(ctx()); err != nil || v.Revision != 1 {
		t.Fatalf("View = %+v (%v), want the committed head 1", v, err)
	}
	_, err = dana.History(ctx(), 0, 0)
	if !monshi.Failed(err, monshi.StorageFailure) {
		t.Fatalf("History over a rewritten entry = %v, want STORAGE_FAILURE"+
			" — the store failed to give back what it was handed", err)
	}
	// And a *Rebase* asks the same seam for the unseen window: a host
	// that rewrote its trail must be refused there too, not merged from
	// what it cannot mean. The draft has to be behind the head for the
	// question to be asked at all — §5.8's current draft needs no window.
	registrar := mustTrailOpen(t, rt, dana.ID(), trailRuler)
	head, _ := registrar.Revision(ctx())
	if _, err := registrar.Transition(ctx(), "filed", head); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	v, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if _, err := dana.Update(ctx(), v.Working,
		monshi.Add("application.notes")); err != nil {
		t.Fatalf("drafting behind a rewritten trail: %v", err)
	}
	if _, err = dana.Rebase(ctx()); !monshi.Failed(err, monshi.StorageFailure) {
		t.Fatalf("Rebase over a rewritten trail = %v, want STORAGE_FAILURE",
			err)
	}
}

// liarStore says a draft exists when none was ever written, and forgetful
// says none exists when one was: both abuse the one fact the contract
// gives the store to own.
type liarStore struct {
	monshi.Storage
	garbage bool // the reported copy holds bytes this package never wrote
	forget  bool // every Draft says "no copy", even after a real Save
}

func (s liarStore) Draft(_ context.Context, id monshi.CaseID,
	user monshi.UserID) (monshi.WorkingRecord, bool, error) {
	if s.forget {
		return monshi.WorkingRecord{}, false, nil
	}
	return monshi.WorkingRecord{State: []byte("not a draft")}, true, nil
}

// TestADraftThatIsNotWhatItSaid is the two-faced host read from both
// sides. A `Draft` that reports a copy holding the host's own bytes is a
// store failure the moment the engine reaches for them; a `Draft` that
// forgets a copy it really has arrives at the same sentence the in-memory
// store writes for an actor who genuinely has none — because a store's
// memory, like its disk, can only ever be answered as "the copy is not
// here".
func TestADraftThatIsNotWhatItSaid(t *testing.T) {
	rt := monshi.NewRuntimeWithStorage(
		liarStore{Storage: monshi.NewMemStorage(), garbage: true})
	d := define(t, rt, "liar", trailSrc)
	_, err := rt.Create(ctx(), d.ID(), trailOwner)
	if !monshi.Failed(err, monshi.StorageFailure) {
		t.Fatalf("Create over a lying Draft = %v, want the garbage bytes"+
			" to fail as the store failing, not as a crash or a render", err)
	}

	rtF := monshi.NewRuntimeWithStorage(
		liarStore{Storage: monshi.NewMemStorage(), forget: true})
	d = define(t, rtF, "amnesic", trailSrc)
	dana, err := rtF.Create(ctx(), d.ID(), trailOwner)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = dana.Update(ctx(), 0,
		monshi.Set("application.kind", monshi.Tag("PERSON")))
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("Update over a forgotten draft = %v, want the no-copy"+
			" sentence", err)
	}
	if !strings.Contains(err.Error(), "no working copy to write over") {
		t.Errorf("Update refused with %q, want the in-memory store's own"+
			" words for an absent copy", err)
	}
}

// TestMemStorageKeepsTheContractsArithmetic is the contract observed
// directly through the exported store, with no runtime in sight: absent
// copies only initialize under a zero expected, guarded saves renumber,
// stale guards answer the held number, and a commit rebases a draft that
// descends from the revision it replaces — and only such a draft.
func TestMemStorageKeepsTheContractsArithmetic(t *testing.T) {
	s := monshi.NewMemStorage()
	id, err := s.Create(ctx(), monshi.CaseRecord{Definition: "x"})
	if err != nil || id == 0 {
		t.Fatalf("Create = {%d, %v}, want a nonzero allocated ID", id, err)
	}
	if _, have, err := s.Draft(ctx(), id, "dana"); have || err != nil {
		t.Fatalf("Draft of a new case = {%v, %v}, want absent and nil",
			have, err)
	}
	if err := s.Save(ctx(), id, "dana", 3, monshi.WorkingRecord{}); !errors.Is(
		err, monshi.ErrNoDraft) {
		t.Fatalf("an expected 3 over no copy = %v, want ErrNoDraft", err)
	}
	if err := s.Save(ctx(), id, "dana", 0,
		monshi.WorkingRecord{Base: 0}); err != nil {
		t.Fatalf("the engine's init = %v, want nil", err)
	}
	// The init landed as given, and the first guarded write renumbers:
	// the data is the caller's, the arithmetic is the store's.
	if w, have, err := s.Draft(ctx(), id, "dana"); !have || err != nil ||
		w.Revision != 0 {
		t.Fatalf("the installed init = %+v ({%v, %v}), want revision 0",
			w, have, err)
	}
	if err := s.Save(ctx(), id, "dana", 0, monshi.WorkingRecord{Base: 0,
		Revision: 99}); err != nil {
		t.Fatalf("Save = %v, want nil", err)
	}
	if w, _, _ := s.Draft(ctx(), id, "dana"); w.Revision != 1 {
		t.Errorf("a guarded save installed revision %d, want the store's"+
			" own expected+1: 1", w.Revision)
	}
	if err := s.Save(ctx(), id, "dana", 7, monshi.WorkingRecord{}); !func() bool {
		var st *monshi.Stale
		return errors.As(err, &st) && st.Have == 1
	}() {
		t.Errorf("a wrong expected = %v, want *Stale{Have: 1}", err)
	}
	// The commit's rebase rule, in both arms: dana's copy descends from 0,
	// the transaction replaces 0, and the base follows. Then a second
	// transaction whose author is not dana leaves her copy where it was.
	if rc, err := s.Commit(ctx(), id, monshi.Transaction{Actor: "dana",
		Expected: 0, Entry: monshi.HistoryEntry("entry-1"),
		Head: monshi.CaseRecord{Revision: 1}}); err != nil ||
		rc.Revision != 1 || rc.Draft != 1 {
		t.Fatalf("Commit = {%+v, %v}, want receipt {1 1}: the landed"+
			" number and the committer's copy after its rebase", rc, err)
	}
	if w, _, _ := s.Draft(ctx(), id, "dana"); w.Base != 1 || w.Revision != 1 {
		t.Errorf("the committer's copy came back {base %d, rev %d}, want"+
			" {1, 1}: the base moves, the content's number stays",
			w.Base, w.Revision)
	}
	rc, err := s.Commit(ctx(), id, monshi.Transaction{Actor: "other",
		Expected: 1, Entry: monshi.HistoryEntry("entry-2"),
		Head: monshi.CaseRecord{Revision: 2}})
	if err != nil || rc.Revision != 2 || rc.Draft != 0 {
		t.Fatalf("Commit = {%+v, %v}, want {2, 0}", rc, err)
	}
	if w, _, _ := s.Draft(ctx(), id, "dana"); w.Base != 1 {
		t.Errorf("a stranger's commit rebased dana to base %d, want 1:"+
			" the rule is the committer's own copy, and only one that"+
			" descends from the replaced revision", w.Base)
	}
	if _, err := s.Commit(ctx(), id, monshi.Transaction{Actor: "dana",
		Expected: 9}); err == nil {
		t.Error("a transaction expected 9 against head 2 succeeded")
	}
	// The trail's numbers are the transaction's own: dense, and paged by
	// arithmetic rather than a search — from the host's seat, `cursor` is
	// a revision and `limit` 0 says "to the head".
	for rev := uint64(3); rev <= 5; rev++ {
		if _, err := s.Commit(ctx(), id, monshi.Transaction{Actor: "dana",
			Expected: rev - 1, Entry: monshi.HistoryEntry(
				fmt.Sprintf("entry-%d", rev)),
			Head: monshi.CaseRecord{Revision: rev}}); err != nil {
			t.Fatalf("Commit %d: %v", rev, err)
		}
	}
	// The other half of the committer rule: the loop's three commits were
	// authored by dana too, but her copy descended from 1 and each
	// transaction replaced a later revision — so only the first commit
	// moved the base, and a store that rebased blindly would show it here.
	if w, _, _ := s.Draft(ctx(), id, "dana"); w.Base != 1 || w.Revision != 1 {
		t.Errorf("commits over a newer head left dana at {base %d rev %d},"+
			" want {1 1}: the rebase needs the copy to descend from the"+
			" revision the transaction replaced", w.Base, w.Revision)
	}
	page, err := s.History(ctx(), id, 2, 2)
	if err != nil || len(page) != 2 || string(page[0]) != "entry-2" ||
		string(page[1]) != "entry-3" {
		t.Fatalf("History(2, 2) = %q (%v), want entry-2 and entry-3",
			page, err)
	}
	all, err := s.History(ctx(), id, 0, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("History(0, 0) = %d entries (%v), want all five", len(all),
			err)
	}
	tail, err := s.History(ctx(), id, 4, 0)
	if err != nil || len(tail) != 2 || string(tail[1]) != "entry-5" {
		t.Fatalf("History(4, 0) = %q (%v), want the last two", tail, err)
	}
	if err := s.Discard(ctx(), id, "dana", 0); !func() bool {
		var st *monshi.Stale
		return errors.As(err, &st) && st.Have == 1
	}() {
		t.Errorf("Discard with a stale expected = %v, want *Stale{Have: 1}",
			err)
	}
	if err := s.Discard(ctx(), id, "dana", 1); err != nil {
		t.Fatalf("Discard = %v, want nil", err)
	}
	if _, have, err := s.Draft(ctx(), id, "dana"); have || err != nil {
		t.Errorf("after Discard, Draft = {%v, %v}, want absent", have, err)
	}
	if err := s.Discard(ctx(), id, "dana", 0); !errors.Is(err,
		monshi.ErrNoDraft) {
		t.Errorf("Discarding an absent copy = %v, want ErrNoDraft: the"+
			" engine reads that as its own rule, not a host failure", err)
	}
}

// blobStore is the "separate binary abstraction" DESIGN §8 sketches by
// name, implemented the way a host would: content the case record does
// not carry, reached by an opaque reference. The engine never calls it;
// the test exists so that the interface is proven implementable, which is
// all §8 promises for it today.
type blobStore struct{ keep map[string][]byte }

func (b blobStore) Put(_ context.Context, r io.Reader) (monshi.BinaryRef,
	error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	ref := "blob-" + strconv.Itoa(len(b.keep))
	b.keep[ref] = data
	return monshi.BinaryRef(ref), nil
}

func (b blobStore) Get(_ context.Context, ref monshi.BinaryRef) (
	io.ReadCloser, error) {
	data, ok := b.keep[string(ref)]
	if !ok {
		return nil, errors.New("no such blob")
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

var _ monshi.BinaryStore = (*blobStore)(nil)

func TestBinaryStoreIsImplementable(t *testing.T) {
	b := blobStore{keep: map[string][]byte{}}
	ref, err := b.Put(ctx(), strings.NewReader("scan"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	f, err := b.Get(ctx(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != "scan" {
		t.Fatalf("Get returned %q (%v), want the bytes Put held", got, err)
	}
}
