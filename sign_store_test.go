package monshi

// In-package tests for where a signature lives and what erases it (RUNTIME
// §5.10). What a host observes — that a signature commits, which refusal
// arrives with which category, that a transition asks for one — is asserted
// externally in sign_test.go. This file asserts the five things no host call
// reaches: the signature table's allocation and replacement, §5.6's invalidation
// as the store applies it, the shape of a `chgSign` entry and what that shape
// costs the merge, the gap list §5.5's third conjunct builds, and the lowering
// that makes all of it one array read.

import (
	"strconv"
	"testing"
)

// sigSrc states each of §4.2's signature shapes with one declaration: a role
// form (`submitted`), an assigned form (`approved_by`, resolved against the
// role-typed `reviewer`), a required one a stage's `require` can ask for
// (`countersigned`), one in another form (`archive.sealed`, for the claim that a
// commit reaches only its own form's signatures), and a `multiple` group whose
// rows move beneath them all.
//
// `application` deliberately carries no `verifiable`, and that is a fixture
// decision rather than a gap: F3 ties a `review` grant to a verifiable target,
// so a definition about attestations that also verified would need a judgement
// in every readiness test. sign_test.go uses a definition where both verdicts
// are in play.
//
// `Bearer` exists to hold `allow sign all`, the one target shape P3 cannot
// check: the case root carries the bit, every slot beneath it inherits it, and
// so the test for "is this a signature field" is the runtime's and not the
// front end's.
const sigSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
        allow view, edit, submit archive;
        allow sign application.submitted;
    }
    Manager {
        allow view all;
        allow sign application.approved_by;
    }
    Registrar {
        allow view all;
        allow sign application.countersigned;
        allow sign archive.sealed;
    }
    Bearer {
        allow view all;
        allow sign all;
    }
}
forms {
    application {
        kind      : legal_form required;
        full_name : string required;
        reviewer  : Manager;

        pages multiple {
            note : string;
        }

        submitted     : signature(Applicant);
        approved_by   : signature(.reviewer);
        countersigned : signature(Registrar) required;
    }

    archive {
        filed_at : string;
        sealed   : signature(Registrar);
    }
}
stages {
    draft {
        allow Registrar accept, reject;
        accept filed;
    }

    filed {
        require application;
    }
}
`

var (
	sigApplicant = Actor{User: "dana", PrimaryRole: "Applicant"}
	sigOther     = Actor{User: "zoe", PrimaryRole: "Applicant"}
	sigRegistrar = Actor{User: "elliot", PrimaryRole: "Registrar"}
	sigBearer    = Actor{User: "cal", PrimaryRole: "Bearer"}
)

// sigCase is a runtime, its image and one actor's handle, in the shape
// review_store_test.go's `reviewCase` gives: the image because every claim here
// is about a specific slot, the handle because a signature is made through an
// operation.
func sigCase(t *testing.T, actor Actor) (*Runtime, *image, Case) {
	t.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLowerSource(t, sigSrc)}
	if err := rt.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return rt, d.img, c
}

// sigOpened is the same case in a second actor's hands, which is what most of
// §4.2's signature rules are about: two hands, one form.
func sigOpened(t *testing.T, rt *Runtime, id CaseID, actor Actor) Case {
	t.Helper()
	c, err := rt.Open(ctx(), id, actor)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

// sigFilled is a host's cycle, which every signature test here needs first: a
// signature attests to committed content, and the readiness gate (§4.2) is the
// difference between countersigning a form and countersigning a blank page. The
// numbers are read from the store rather than remembered, so a test cannot pass
// by tracking revisions wrongly: rebase, save a batch at the copy's own
// revision, commit it, and report what the case reached.
func sigFilled(t *testing.T, rt *Runtime, img *image, c Case,
	edits ...Edit) uint64 {
	t.Helper()
	if _, err := c.Rebase(ctx()); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	w, err := rt.w.cases.working(ctx(), newRefuser(update, c.id), c.actor.User, img)
	if err != nil {
		t.Fatalf("working: %v", err)
	}
	base := mustUpdate(t, c, w.rev, edits...)
	res, err := c.Submit(ctx(), "application", base)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return res.Revision
}

// typed is the content the two required fields ask for, committed.
func typed(t *testing.T, rt *Runtime, img *image, c Case) uint64 {
	t.Helper()
	return sigFilled(t, rt, img, c, Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Ada")))
}

// signedBy is the table attestations at `fields` would have stored, built by
// hand so the invalidation tests start from signatures rather than from the
// operation that makes them.
func signedBy(img *image, fields ...nodeID) []sigState {
	tbl := signTable(img, nil)
	for i, n := range fields {
		tbl[n] = sigState{by: "dana", at: uint64(i + 1),
			ref: "envelope-" + dec(uint64(i+1))}
	}
	return tbl
}

// invalidatedBy runs §5.6's signature erasure over a diff the way the store
// does, and hands back the table it produced. The list is one effect rather than
// the package's two, so that a test about a signature is not also a test about a
// decision.
func invalidatedBy(img *image, have []sigState,
	changes []commitChange) []sigState {
	next := committedState{sigs: have}
	applyCommitEffects(img, &next, changes, []commitEffect{sigReset{}})
	return next.sigs
}

// setValue writes one cell of a state's row, which is how the two scans below
// get content into a state without an operation: the questions they answer are
// about what a state holds, and a `Submit` would answer them with a second rule
// attached.
func setValue(t *testing.T, s *state, n nodeID, v slotValue) {
	t.Helper()
	c, ok := s.cellOf(n)
	if !ok {
		t.Fatalf("%s is not a cell", s.img.path(n))
	}
	c.set(v)
}

func TestAnUnsignedCaseHoldsNoSignatureTable(t *testing.T) {
	rt, img, dana := sigCase(t, sigApplicant)
	// The state is created empty rather than allocated per form: a case nobody
	// has signed holds nil, and `sigAt` is the only place allowed to say what
	// that means. A definition with three signature fields and no attestation
	// costs nothing, which is §3.9's rule about what indexes by data applied to
	// a table that would have indexed by nothing.
	rec := stored(t, rt, dana)
	if rec.commit.sigs != nil {
		t.Errorf("a new case holds a %d-entry table, want nil",
			len(rec.commit.sigs))
	}
	field := slotAt(t, img, "application.submitted")
	if sigAt(nil, field) != (sigState{}) {
		t.Error("an absent entry does not read as unsigned")
	}
	// The range is the other half of the answer, and it is a property of the
	// definition: what a case holds about a form's signatures is bounded by
	// what that form declares, never by how many fields it has.
	if got := img.sigSlotsOf(img.slots[field].form); len(got) != 3 {
		t.Errorf("application groups %d signature fields, want 3", len(got))
	}
}

func TestASignatureReplacesTheTableAndNotTheEntry(t *testing.T) {
	rt, img, dana := sigCase(t, sigApplicant)
	app := slotAt(t, img, "application.submitted")
	rev := typed(t, rt, img, dana)
	res, err := dana.Sign(ctx(), "application.submitted", rev, "envelope-1")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if res.Revision != rev+1 {
		t.Errorf("the signature reached revision %d, want %d", res.Revision,
			rev+1)
	}
	have := stored(t, rt, dana).commit.sigs
	if have == nil {
		t.Fatal("a signed case holds no table")
	}
	g := sigAt(have, app)
	if g.by != "dana" || g.at != res.Revision || g.ref != "envelope-1" {
		t.Errorf("the table holds %q@%d %q, want dana@%d envelope-1",
			g.by, g.at, g.ref, res.Revision)
	}
	// The bound content is `at-1` by convention, and the convention is what
	// makes it safe to store three things rather than four: the revision the
	// attestation names as its own is the one that committed it, so the version
	// it speaks about is the one it was built against, and §5.6's erasure is
	// what keeps the two from drifting apart.
	if g.at != rev+1 {
		t.Errorf("the entry stands for content at revision %d, want %d",
			g.at-1, rev)
	}
	// The store's replace-never-mutate rule in the direction a reader cares
	// about: the copy a second commit builds is not the array the first handed
	// out, so a caller holding the old one keeps the old answer.
	tbl := signTable(img, have)
	if &tbl[0] == &have[0] {
		t.Error("the copy shares the stored array")
	}
	tbl[app] = sigState{}
	if sigAt(stored(t, rt, dana).commit.sigs, app).at == 0 {
		t.Error("writing the copy reached the stored table")
	}
}

func TestASecondSignatureReplacesTheFirst(t *testing.T) {
	rt, img, dana := sigCase(t, sigApplicant)
	app := slotAt(t, img, "application.submitted")
	rev := typed(t, rt, img, dana)
	if _, err := dana.Sign(ctx(), "application.submitted", rev, "one"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// §4.2's "at most one effective signature per content version": a second
	// hand on one field is not a second entry. The field is open to a role
	// rather than to a person, so whoever of that role signs is the hand the
	// attestation names — and replacement is what makes `signature(Role)` a
	// quorum of one instead of a race for whoever asked first. The displaced
	// signature is not withdrawn by this: it is history's to report.
	zoe := sigOpened(t, rt, dana.ID(), sigOther)
	res, err := zoe.Sign(ctx(), "application.submitted", rev+1, "two")
	if err != nil {
		t.Fatalf("replacement sign: %v", err)
	}
	tbl := stored(t, rt, dana).commit.sigs
	if len(tbl) != len(img.slots) {
		t.Errorf("the table holds %d entries, want %d slots", len(tbl),
			len(img.slots))
	}
	g := sigAt(tbl, app)
	if g.by != "zoe" || g.ref != "two" || g.at != res.Revision {
		t.Errorf("the field holds %q@%d with %q, want zoe@%d two", g.by,
			g.at, g.ref, res.Revision)
	}
	// Both acts are in the record, because the erasure is of the effective
	// attestation and not of the account of it: the state holds one signature
	// and history holds two, which is the half of §4.2 a table of verdicts
	// cannot state on its own.
	h := stored(t, rt, dana).hist
	if len(h) != 3 || h[1].op != opSign || h[2].op != opSign {
		t.Fatalf("history is %d entries, want two sign commits after the "+
			"submit", len(h))
	}
	if h[1].by != "dana" || h[2].by != "zoe" {
		t.Errorf("history says %q then %q, want dana then zoe", h[1].by,
			h[2].by)
	}
	if img.slots[app].sig == 0 {
		t.Fatal("a signature field carries no signature reference")
	}
}

func TestASignCommitDoesNotInvalidateItself(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application.submitted")
	off := slotAt(t, img, "application.countersigned")
	have := signedBy(img, app)
	// The entry a signature writes is about a verdict and not about content, so
	// §5.6's driver must not hand it to the effect that erases verdicts: a
	// commit that invalidated the attestation it was making would sign nothing,
	// and the kind filter is asked before any effect runs rather than inside
	// one, so a third rule about non-content entries is safe by construction.
	got := invalidatedBy(img, have, []commitChange{
		{kind: chgSign, slot: off, v: slotValue{set: true, s: "x"}}})
	if sigAt(got, app).at == 0 {
		t.Error("a signature commit erased the form's standing attestation")
	}
	if &got[0] != &have[0] {
		t.Error("a commit that erased nothing copied the table anyway")
	}
	// The same claim through the store, where it is a rule rather than a
	// function: two fields of one form, both signed, both standing. §4.2's
	// "multiple signatures are unordered" means one hand's act is not another
	// hand's content change — and a reset that read a signature as content
	// would make the second erase the first, which is the difference between a
	// countersignature and a replacement.
	rt, _, dana := sigCase(t, sigApplicant)
	rev := typed(t, rt, img, dana)
	elliot := sigOpened(t, rt, dana.ID(), sigRegistrar)
	if _, err := dana.Sign(ctx(), "application.submitted", rev, "one"); err != nil {
		t.Fatalf("Sign(submitted): %v", err)
	}
	res, err := elliot.Sign(ctx(), "application.countersigned", rev+1, "two")
	if err != nil {
		t.Fatalf("Sign(countersigned): %v", err)
	}
	tbl := stored(t, rt, dana).commit.sigs
	if sigAt(tbl, app).ref != "one" || sigAt(tbl, off).ref != "two" {
		t.Errorf("the second signature left %q and %q", sigAt(tbl, app).ref,
			sigAt(tbl, off).ref)
	}
	if sigAt(tbl, app).at == res.Revision {
		t.Error("the first signature was rewritten by the second commit")
	}
}

func TestAContentCommitInvalidatesItsForm(t *testing.T) {
	rt, img, dana := sigCase(t, sigApplicant)
	app := slotAt(t, img, "application.submitted")
	name := slotAt(t, img, "application.full_name")
	rev := typed(t, rt, img, dana)
	if _, err := dana.Sign(ctx(), "application.submitted", rev, "one"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	have := stored(t, rt, dana).commit.sigs
	// The mapping the index is built for: one field of the form moves, and the
	// attestation that bound it is gone — author and reference with it, because
	// a state holding both the erased signature and its bytes would answer "is
	// this signed" twice, and §4.2's "the historical record is retained" is a
	// sentence about history.
	got := invalidatedBy(img, have, []commitChange{
		{kind: chgValue, slot: name, v: txt("Grace")}})
	if st := sigAt(got, app); st != (sigState{}) {
		t.Errorf("the invalidation left %q@%d %q standing", st.by, st.at,
			st.ref)
	}
	// And the same through the store: the content commits, and a read of the
	// case finds nothing where the attestation was.
	next := sigFilled(t, rt, img, dana,
		Set("application.full_name", Text("Grace")))
	if sigAt(stored(t, rt, dana).commit.sigs, app).at != 0 {
		t.Error("a commit kept the signature of the content it replaced")
	}
	// What it does not erase is the record, which is where the retained
	// signature lives: three commits, the sign entry among them.
	h := stored(t, rt, dana).hist
	if len(h) != 3 || h[1].op != opSign || h[1].by != "dana" ||
		h[2].op != opSubmit {
		t.Fatalf("history is %d entries: %v %v %v, want submit, sign, submit",
			len(h), h[0].op, h[1].op, h[2].op)
	}
	if h[2].revision != next {
		t.Errorf("the content commit is at revision %d, want %d", h[2].revision,
			next)
	}
}

func TestARowCommitInvalidatesTheFormItMovedUnder(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application.submitted")
	pages := slotAt(t, img, "application.pages")
	// A row appearing is content, and §5.1 counts it as such: the attestation
	// bound a form whose pages have since changed. The diff entry names the
	// collection's declaration rather than any row, so the anchor on that
	// declaration is what finds the signatures — which is why the stamp is
	// asked of every slot and not only of fields.
	got := invalidatedBy(img, signedBy(img, app), []commitChange{
		{kind: chgRows, slot: pages, v: slotValue{set: true, i: 1}}})
	if sigAt(got, app).at != 0 {
		t.Error("a committed row left the form's signature standing")
	}
	if img.slots[pages].form != img.slots[app].form {
		t.Errorf("the collection is anchored to form %d, its field to %d",
			img.slots[pages].form, img.slots[app].form)
	}
}

func TestACommitInAnotherFormSharesTheTable(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application.submitted")
	sealed := slotAt(t, img, "archive.sealed")
	file := slotAt(t, img, "archive.filed_at")
	tbl := signedBy(img, app)
	tbl[sealed] = sigState{by: "elliot", at: 2, ref: "wax"}
	// Two forms, two attestations, one diff entry in the second. The table is
	// shared, so the erasure that does happen must copy — and the form that was
	// not touched keeps its entry untouched, which is the claim: the reach is
	// bounded by the anchor and not by a walk that could stop early.
	got := invalidatedBy(img, tbl, []commitChange{
		{kind: chgValue, slot: file, v: txt("2026-01-01")}})
	if &got[0] == &tbl[0] {
		t.Error("a commit that erased an attestation wrote through the table")
	}
	if sigAt(got, sealed).at != 0 {
		t.Error("the touched form kept its attestation")
	}
	if sigAt(got, app) != sigAt(tbl, app) {
		t.Errorf("the untouched form's entry changed from %+v to %+v",
			sigAt(tbl, app), sigAt(got, app))
	}
}

func TestOneCommitInvalidatesEverySignatureOfItsForm(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application.submitted")
	off := slotAt(t, img, "application.countersigned")
	note := slotAt(t, img, "application.approved_by")
	pages := slotAt(t, img, "application.pages")
	tbl := signedBy(img, app, off, note)
	// Three entries, one form, and a diff that mentions two of them: one copy,
	// and every standing attestation in the form gone — including the field no
	// entry names, because §4.2 binds a signature to the form and not to the
	// row that moved. The dedup is what keeps one copy out of three, and the
	// same diff over the erased table is the claim in reverse: the copy is a
	// consequence of an erasure, not a cost of walking the range.
	got := invalidatedBy(img, tbl, []commitChange{
		{kind: chgValue, slot: app, v: slotValue{set: true, i: 1}},
		{kind: chgValue, slot: off, v: slotValue{set: true, i: 1}},
		{kind: chgRows, slot: pages, v: slotValue{set: true, i: 2}},
	})
	if &got[0] == &tbl[0] {
		t.Fatal("three erased attestations copied no table")
	}
	for _, n := range []nodeID{app, off, note} {
		if sigAt(got, n).at != 0 {
			t.Errorf("%s still signed at %d", img.path(n), sigAt(got, n).at)
		}
	}
	again := invalidatedBy(img, got, []commitChange{
		{kind: chgValue, slot: app, v: slotValue{set: true, i: 2}}})
	if &again[0] != &got[0] {
		t.Error("erasing nothing standing copied the table")
	}
}

func TestTheUnsignedScanAsksOnlyWhatTheDefinitionRequires(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application")
	off := slotAt(t, img, "application.countersigned")
	s := newState(img)
	s.applyDefaults()

	// The first version of this scan listed every unsigned signature field, and
	// a stage whose form declared an optional attestation could then never be
	// reached. SYNTAX §5.6 says a `require` asks that *required* signatures are
	// valid, so the predicate is the definition's `required` attribute — the
	// same `asked` the completeness walk uses, which is what makes `required`
	// mean one thing on a signature field and no more on it than elsewhere.
	gaps := unsignedSigs(&s, nil, app)
	if len(gaps) != 1 || gaps[0].kind != gapSig ||
		gaps[0].path != "application.countersigned" {
		t.Fatalf("the scan asks %+v, want the one required field", gaps)
	}
	have := signTable(img, nil)
	have[off] = sigState{by: "elliot", at: 1}
	if got := unsignedSigs(&s, have, app); len(got) != 0 {
		t.Errorf("a signed required field still asks %+v", got)
	}
	// An optional field is absent from the list whether or not it is signed,
	// and the two optional fields are absent in the same way: `asked` answers
	// "does the definition require this" and a signature field without a
	// `required` answers no.
	sub := slotAt(t, img, "application.submitted")
	both := signTable(img, have)
	both[sub] = sigState{}
	if got := unsignedSigs(&s, both, app); len(got) != 0 {
		t.Errorf("an unsigned optional field arrived in the list: %+v", got)
	}
	// A form with no required signature is an empty list rather than a walk:
	// the range is the signatures, never the fields, so the conjunct costs a
	// definition that does not use it one loop over nothing.
	if got := unsignedSigs(&s, nil, slotAt(t, img, "archive")); len(got) != 0 {
		t.Errorf("archive asks %+v, want nothing", got)
	}
}

func TestTheThreeConjunctsArriveInOrder(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application")
	s := newState(img)
	s.applyDefaults()

	// §5.5's `require` is one sentence with three conjuncts, and the remedy for
	// each is a different call: `Update` for a value, `Review` for a judgement,
	// `Sign` for an attestation. The gap list is built in exactly that order,
	// which is also the order a client can work through it, and the three scans
	// are the same three the destination asks.
	var all []gap
	all = append(all, s.incomplete(app)...)
	all = append(all, pendingUnits(&s, nil, app)...)
	all = append(all, unsignedSigs(&s, nil, app)...)
	if len(all) != 3 {
		t.Fatalf("an empty application asks %+v, want two fields and one "+
			"signature", all)
	}
	if all[2].kind != gapSig || all[2].path != "application.countersigned" {
		t.Errorf("the last gap is %+v, want the signature", all[2])
	}
	for _, g := range all[:2] {
		if g.kind == gapSig {
			t.Errorf("the attestation arrived before the content: %+v", all)
		}
	}
	// The counts are what the sentence is built from, and they must stay apart:
	// a refusal that said "3 fields with no value" about a signature would send
	// a user back to a form they had already finished.
	var fields, units, sigs int
	for _, g := range all {
		switch g.kind {
		case gapVerif:
			units++
		case gapSig:
			sigs++
		default:
			fields++
		}
	}
	if got := requireSentence("filed", fields, units, sigs); got !=
		`"filed" still requires 2 fields with no value, and 1 signature` {
		t.Errorf("requireSentence = %s", got)
	}
	// The two-part sentence §5.5 shipped with is this function with the third
	// count empty, which is what makes the third conjunct a widening of the
	// same wording rather than a new shape of message — and SYNTAX §7's
	// conformance example is the reason that has to be true.
	for _, want := range [][4]string{
		{"2", "1", "0",
			`"x" still requires 2 fields with no value, and 1 approval`},
		{"1", "1", "0", `"x" still requires 1 field with no value, and 1 approval`},
		{"0", "2", "0", `"x" still requires 2 approvals`},
		{"3", "0", "0", `"x" still requires 3 fields with no value`},
		{"0", "0", "1", `"x" still requires 1 signature`},
		{"1", "1", "1", `"x" still requires 1 field with no value, 1 approval, ` +
			`and 1 signature`},
	} {
		f, _ := strconv.Atoi(want[0])
		u, _ := strconv.Atoi(want[1])
		g, _ := strconv.Atoi(want[2])
		if got := requireSentence("x", f, u, g); got != want[3] {
			t.Errorf("requireSentence(%d, %d, %d) = %s, want %s", f, u, g,
				got, want[3])
		}
	}
}

func TestARequiredSignatureIsNotAValueGap(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	s := newState(img)
	s.applyDefaults()
	app := slotAt(t, img, "application")
	setValue(t, &s, slotAt(t, img, "application.kind"),
		slotValue{set: true, i: 1})
	setValue(t, &s, slotAt(t, img, "application.full_name"), txt("Ada"))

	// §4.2's "excluding signature fields" read as the whole reason this walk
	// must skip them: `Update` refuses to write a signature, `Sign` requires
	// the form to be complete first, and a gap only `Sign` can answer would make
	// a required signature a form nobody can ever submit — and so nobody can
	// ever sign. The three rules are one cycle with no way out of it, which is
	// why the exclusion is in the completeness walk rather than in a message
	// that explains the deadlock after it happens.
	for _, g := range s.incomplete(app) {
		if g.kind == gapSig || g.path == "application.countersigned" {
			t.Fatalf("completeness asks a signature as a value: %+v",
				s.incomplete(app))
		}
	}
	if got := s.incomplete(app); len(got) != 0 {
		t.Errorf("a filled form still asks %+v", got)
	}
	// Yet the attribute is not meaningless: `required` asks everything of the
	// scan that gates a stage and nothing of the scan that lists fields, which
	// is what gives a signature field the same attribute every other field has
	// rather than a signature-specific word for the same intent.
	if !s.asked(slotAt(t, img, "application.countersigned")) {
		t.Error("`required` on a signature field asks nothing of the definition")
	}
	sub := slotAt(t, img, "application.submitted")
	if s.asked(sub) {
		t.Error("an optional signature is reported as required")
	}
}

func TestTheSignatureGroupIsADenseRangePerForm(t *testing.T) {
	img := mustLowerSource(t, sigSrc)
	app := slotAt(t, img, "application")
	archive := slotAt(t, img, "archive")
	fields := img.sigSlotsOf(img.slots[app].form)
	want := []string{"application.submitted", "application.approved_by",
		"application.countersigned"}
	if len(fields) != len(want) {
		t.Fatalf("application groups %d signature fields, want %d",
			len(fields), len(want))
	}
	// Declaration order, which is the order §5.5's `Missing` list reports them
	// in and the order a form renders its boxes: preorder numbering would put
	// `countersigned` last by accident of slot order and then the two would
	// disagree.
	for i, n := range fields {
		if img.path(n) != want[i] {
			t.Errorf("field %d = %s, want %s", i, img.path(n), want[i])
		}
	}
	if got := img.sigSlotsOf(img.slots[archive].form); len(got) != 1 ||
		img.path(got[0]) != "archive.sealed" {
		t.Errorf("archive groups %v, want its one field", got)
	}
	// The case root has an empty range, which is what lets the invalidation
	// index by the anchor read from any slot without asking whether that slot
	// was in a form at all: `form` 0 is a valid index and not an error.
	if len(img.sigSlotsOf(0)) != 0 {
		t.Error("the case root groups signature fields")
	}
	// The anchor is stamped over the whole subtree: a form, a field, a
	// collection's declaration, a group's member and a signature field all
	// answer one ordinal, which is the property §5.6's mapping and `owner` both
	// rest on — and a second form answers a different one.
	for _, path := range []string{"application", "application.full_name",
		"application.pages", "application.pages.note",
		"application.countersigned"} {
		if img.slots[slotAt(t, img, path)].form != img.slots[app].form {
			t.Errorf("%s carries form %d, want %d", path,
				img.slots[slotAt(t, img, path)].form, img.slots[app].form)
		}
	}
	if img.slots[slotAt(t, img, "archive.filed_at")].form ==
		img.slots[app].form {
		t.Error("two forms share one anchor")
	}
	if img.owner(slotAt(t, img, "application.pages.note")) != app {
		t.Error("`owner` no longer answers through the anchor")
	}
	if img.owner(0) != 0 {
		t.Error("the case root does not own itself")
	}
}

func TestASignatureEntryIsNotAMergeableChange(t *testing.T) {
	rt, img, dana := sigCase(t, sigApplicant)
	app := slotAt(t, img, "application.submitted")
	rev := typed(t, rt, img, dana)
	if _, err := dana.Sign(ctx(), "application.submitted", rev, "one"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec := stored(t, rt, dana)
	last := rec.hist[len(rec.hist)-1]
	if len(last.changes) != 1 || last.changes[0].kind != chgSign {
		t.Fatalf("a sign commit wrote %+v, want one signature entry",
			last.changes)
	}
	ch := last.changes[0]
	if ch.slot != app || !ch.v.set || ch.v.s != "one" {
		t.Errorf("the entry says %s holds %+v, want the field and its "+
			"evidence", img.path(ch.slot), ch.v)
	}
	// An instance address is what tells a merge where a value lives, and a
	// signature field has none: S1 puts it in a form, so the entry's path is
	// empty and its payload carries the reference and nothing else. A merge that
	// read this entry as content would be grafting a verdict into a draft.
	if ch.path != nil || len(ch.ids) != 0 {
		t.Errorf("the entry carries an address %v and rows %v, want neither",
			ch.path, ch.ids)
	}
	// Who signed is the record's answer and not the entry's — §5.9's discipline
	// for a decision, restated for an attestation so that the entry and the
	// commit cannot disagree about whose hand it was.
	if last.op != opSign || last.by != "dana" || last.revision != rev+1 {
		t.Errorf("the record says op %v by %q at %d, want sign by dana at %d",
			last.op, last.by, last.revision, rev+1)
	}

	// And the merge that reads a diff for content finds nothing at a signature
	// slot. A second actor's signature is the unseen commit here, which is the
	// shape the hazard has: an attestation arriving in a draft as a value would
	// be a cell the definition says no `Update` may write, and a conflict
	// reported for it would ask a user to reconcile two hands over a form
	// neither of them edited.
	elliot := sigOpened(t, rt, dana.ID(), sigRegistrar)
	if _, err := elliot.Sign(ctx(), "application.countersigned", rev+1,
		"seal"); err != nil {
		t.Fatalf("second signature: %v", err)
	}
	mustUpdate(t, dana, rev, Set("application.full_name", Text("Grace")))
	res, err := dana.Rebase(ctx())
	if err != nil {
		t.Fatalf("Rebase over two signature commits: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("a signature commit clashed: %v", res.Conflicts)
	}
	draft := draftState(t, dana, img)
	if v := draft.cellValue(app, nil); v.set {
		t.Errorf("a signature arrived in a draft as %+v", v)
	}
	if v := draft.cellValue(slotAt(t, img, "application.full_name"), nil); v.s !=
		"Grace" {
		t.Errorf("the rebase lost the draft's own edit, leaving %+v", v)
	}
}
