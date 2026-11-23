package monshi_test

// Signature seen from outside the module (DESIGN §4.2, §6.4; RUNTIME §5.10):
// what a host observes when an authorized hand attests to content the case
// already holds — which refusal arrives with which category, what the view says
// about the attestation, what erases it, and what a stage asks of it.
//
// What a host cannot observe is in the in-package file: the signature table's
// allocation, the shape of the history entry, the merge filter that keeps an
// attestation out of a rebase, and the index that makes the erasure one array
// read.
//
// The fixture is the shape a document review actually has: an applicant who may
// type and may attest to his own filing, a registrar who may decide and may
// countersign, two managers of whom the case names one, and a `Bearer` whose
// `allow sign all` exists to prove that a grant is not an identity. Most of
// these claims are about two or three people holding one case, so most of them
// need more than one handle.

import (
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

const sealSrc = `
types {
    legal_form : enum(PERSON, COMPANY);
}
roles {
    Applicant {
        allow view, edit, submit application;
        allow sign application.submitted;
    }
    Manager {
        allow view all;
        allow sign application.approved_by;
    }
    Registrar {
        allow view all;
        allow review application;
        allow sign application.countersigned;
    }
    Bearer {
        allow view all;
        allow sign all;
    }
}
forms {
    application verifiable {
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
	sealing   = monshi.Actor{User: "dana", PrimaryRole: "Applicant"}
	sealAgain = monshi.Actor{User: "zoe", PrimaryRole: "Applicant"}
	sealFirst = monshi.Actor{User: "bo", PrimaryRole: "Manager"}
	sealOther = monshi.Actor{User: "cy", PrimaryRole: "Manager"}
	sealRuler = monshi.Actor{User: "elliot", PrimaryRole: "Registrar"}
	sealAll   = monshi.Actor{User: "cal", PrimaryRole: "Bearer"}
)

// signing is a runtime, its definition and five handles on one fresh case: the
// applicant's, because content has to exist before an attestation can be about
// it; the registrar's, because this form verifies itself and so needs a
// judgement before a signature; two managers, because the interesting claim
// about `signature(.reviewer)` is which of them it opens the field to; and the
// `sign all` actor, whose grant proves the grant is not the identity.
func signing(t *testing.T) (
	*monshi.Runtime, monshi.Case, monshi.Case, monshi.Case, monshi.Case,
	monshi.Case) {
	t.Helper()
	rt := monshi.NewRuntime()
	d := define(t, rt, "deed", sealSrc)
	dana, err := rt.Create(ctx(), d.ID(), sealing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handles := []monshi.Case{dana}
	for _, a := range []monshi.Actor{sealRuler, sealFirst, sealOther, sealAll} {
		c, err := rt.Open(ctx(), dana.ID(), a)
		if err != nil {
			t.Fatalf("Open(%s): %v", a.User, err)
		}
		handles = append(handles, c)
	}
	return rt, handles[0], handles[1], handles[2], handles[3], handles[4]
}

// content is the two required fields, committed, and the revision afterwards:
// the thing a signature is required to be about.
func content(t *testing.T, dana monshi.Case) uint64 {
	t.Helper()
	draft(t, dana,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	return committed(t, dana, "application")
}

// approved is what a signature on this form attests to: content the case holds
// and a judgement somebody made about it. §4.2's readiness test asks both, so a
// test that wants to reach the interesting part of `Sign` builds the whole of
// it, and a test that is about the readiness gate stops before this.
func approved(t *testing.T, dana, elliot monshi.Case) uint64 {
	t.Helper()
	rev := content(t, dana)
	res, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		"")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	return res.Revision
}

// shown is the attestation the case reports for one field, which is the only way
// a host can read it: the table is not the interface.
func shown(t *testing.T, c monshi.Case, path string) monshi.SignatureInfo {
	t.Helper()
	v, err := c.View(ctx(), path)
	if err != nil {
		t.Fatalf("View(%s): %v", path, err)
	}
	return node(t, v, path).Signature
}

// whole is the view every node assertion below is made against: a host renders a
// form, not a slot, and `Signable` is a fact about what a form puts on screen.
func whole(t *testing.T, c monshi.Case) monshi.ViewResult {
	t.Helper()
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	return v
}

func TestSignAttestsToCommittedContent(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	rev := approved(t, dana, elliot)
	res, err := dana.Sign(ctx(), "application.submitted", rev, "envelope-7")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if res.Revision != rev+1 {
		t.Errorf("the attestation reached revision %d, want %d", res.Revision,
			rev+1)
	}
	// A signature is a commit that says nothing about data, so the stage does
	// not move and the content is where it was. The one thing that changed is
	// whose hand stands behind the form, and the view reports it on the field
	// the definition declared rather than on the form the attestation is about.
	if res.Stage != "draft" {
		t.Errorf("the attestation moved the stage to %q, want draft",
			res.Stage)
	}
	got := shown(t, dana, "application.submitted")
	if got.By != "dana" || got.At != res.Revision || got.Evidence !=
		"envelope-7" {
		t.Errorf("the view reports %q@%d %q, want dana@%d envelope-7", got.By,
			got.At, got.Evidence, res.Revision)
	}
	// `At` is the revision the signature's own commit produced, so the content
	// it attests to is the version before it: §5.9's convention for a decision,
	// restated for an attestation, because a host that read the number as the
	// version it vouched for would show a signature one revision late.
	v, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Revision != res.Revision {
		t.Errorf("the case is at %d and the attestation says %d", v.Revision,
			got.At)
	}
	// A field nobody attested to reports the zero value rather than an error or
	// an absent node — the same answer as one whose attestation was erased,
	// which §5.10 states as a rule about what "stale" is not.
	if z := shown(t, dana, "application.approved_by"); z !=
		(monshi.SignatureInfo{}) {
		t.Errorf("an unsigned field reports %+v, want the zero value", z)
	}
	// And the attestation does not make the form complete: `required` on a
	// signature is a question the stage asks, never a value cell, so the node
	// still carries no value of any kind.
	n := node(t, v, "application.submitted")
	if n.Value.Kind() != monshi.KindNone {
		t.Errorf("an attested field holds a value of kind %v", n.Value.Kind())
	}
}

func TestSignIsRefusedUntilTheFormIsDecided(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	// Content the case holds, and no judgement yet. This form verifies itself,
	// so §4.2's "approved applicable verification units" is the conjunct that
	// stops an attestation about a form the registrar has not read, and the
	// path it names is the unit rather than the field — which is what makes the
	// refusal actionable. The attestation this call would write is not among
	// them, because signing is how this call answers that question.
	rev := content(t, dana)
	res, err := dana.Sign(ctx(), "application.submitted", rev, "now")
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("signing an undecided form = %v, want INVALID_OPERATION", err)
	}
	if !strings.Contains(err.Error(), "1 approval") {
		t.Errorf("the refusal says %q, want the missing approval counted", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "application" {
		t.Errorf("Missing = %s, want the one unit", paths(res.Missing,
			"application"))
	}
	v, err := dana.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Revision != rev {
		t.Errorf("a refusal advanced the case to %d, want %d", v.Revision, rev)
	}
	if z := shown(t, dana, "application.submitted"); z !=
		(monshi.SignatureInfo{}) {
		t.Errorf("the refusal left %+v standing", z)
	}
	// The same call after the decision is what the refusal was about.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		""); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if _, err := dana.Sign(ctx(), "application.submitted", rev+1,
		"now"); err != nil {
		t.Errorf("signing after the approval = %v", err)
	}
}

func TestSignIsRefusedWhileRequiredContentIsMissing(t *testing.T) {
	_, dana, _, _, _, _ := signing(t)
	// The draft is not the case. §4.2 binds a signature to committed content, so
	// an actor who has typed both required fields and not submitted them is
	// attesting to a form the case does not hold, and the refusal says so about
	// the *committed* state — every required field, not the one still blank on
	// the screen. A `Sign` that read the caller's own draft would let a hand
	// vouch for bytes nobody published, and each actor's signature would be
	// about a different form.
	draft(t, dana,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	res, err := dana.Sign(ctx(), "application.submitted", 0, "x")
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("signing an incomplete form = %v, want INVALID_OPERATION",
			err)
	}
	if len(res.Missing) != 3 || res.Missing[0] != "application.kind" ||
		res.Missing[1] != "application.full_name" ||
		res.Missing[2] != "application" {
		t.Errorf("Missing = %s, want both fields then the unit, from the "+
			"committed state", paths(res.Missing, "application.kind",
			"application.full_name", "application"))
	}
	// Both conjuncts of readiness fail here and they are counted apart in the
	// message, because the errands are apart: "fill this in" is the applicant's
	// and "wait for the registrar" is nobody's until the form is whole.
	if !strings.Contains(err.Error(), "2 required fields with no value") ||
		!strings.Contains(err.Error(), "1 approval") {
		t.Errorf("the refusal says %q, want both kinds of gap counted", err)
	}
	for _, p := range res.Missing {
		if p == "application.submitted" {
			t.Errorf("Missing asks the attestation this call is making: %v",
				res.Missing)
		}
	}
	// The same call after the content is published reaches the next conjunct,
	// which is what says the first refusal was about content and not about the
	// caller's right to make it.
	at := committed(t, dana, "application")
	if _, err := dana.Sign(ctx(), "application.submitted", at,
		"x"); !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("signing decided-free content = %v", err)
	} else if !strings.Contains(err.Error(), "1 approval") ||
		strings.Contains(err.Error(), "required field") {
		t.Errorf("the refusal says %q, want the approval and no field", err)
	}
}

func TestAContentCommitErasesTheAttestation(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	rev := approved(t, dana, elliot)
	if _, err := dana.Sign(ctx(), "application.submitted", rev,
		"envelope-7"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// §4.2's other half, observed as a host would: the form's content moves and
	// the attestation that bound the previous version is gone from the view.
	// Not flagged, not stale, not kept beside a note that it no longer applies —
	// gone, which is what makes "is this signed" have one answer.
	draft(t, dana, monshi.Set("application.full_name", monshi.Text("Ada L")))
	after := committed(t, dana, "application")
	if got := shown(t, dana, "application.submitted"); got !=
		(monshi.SignatureInfo{}) {
		t.Errorf("the content commit left %q@%d %q standing", got.By, got.At,
			got.Evidence)
	}
	// The decision is erased by the same rule and reported the same way, which
	// is the point of making both an erasure rather than a verdict with a flag
	// on it: the view of a form somebody edited holds two absences, and the
	// account of what was there is the history's.
	v, err := elliot.View(ctx(), "application")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, v, "application").Review; got.State !=
		monshi.ReviewPending {
		t.Errorf("the content commit left the decision at %q", got.State)
	}
	// What the erasure costs is the stage: this form's required countersignature
	// is standing no more than the applicant's was, and the destination asks
	// again — in the same words it used before anybody signed, which is the
	// claim that the erasure is a rule and not a rendering.
	res, err := elliot.Transition(ctx(), "filed", after)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("the transition over erased attestations = %v, want a "+
			"refusal", err)
	}
	// Two erasures arrive on one list, in the order a client can work through
	// them: the decision §5.6's other effect reset, then the attestation this
	// one cleared. A destination that asked only the second would move to a
	// stage whose form the registrar has not read.
	if len(res.Missing) != 2 || res.Missing[0] != "application" ||
		res.Missing[1] != "application.countersigned" {
		t.Errorf("Missing = %s, want the unit then the countersignature",
			paths(res.Missing, "application", "application.countersigned"))
	}
	if !strings.Contains(err.Error(), "1 approval") ||
		!strings.Contains(err.Error(), "1 signature") {
		t.Errorf("the refusal says %q, want both conjuncts counted", err)
	}
}

func TestSubmitTreatsARequiredSignatureAsNoGap(t *testing.T) {
	_, dana, _, _, _, _ := signing(t)
	// §4.2's "excluding signature fields", seen from the one operation that
	// would otherwise deadlock on it: only `Sign` writes an attestation, `Sign`
	// requires the form to be complete first, and so a required signature
	// counted as a field with no value would make a form nobody could ever
	// submit and therefore nobody could ever sign. This fixture's
	// `countersigned` is `required` and unsigned throughout.
	w := draft(t, dana,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")))
	res, err := dana.Submit(ctx(), "application", w)
	if err != nil {
		t.Fatalf("Submit with a required signature outstanding = %v", err)
	}
	if len(res.Missing) != 0 {
		t.Errorf("Submit asks %s, want nothing", paths(res.Missing))
	}
	// The attestation is not a value either: a field the definition says no
	// `Update` may write arrives in the view as its declared kind with nothing
	// in it, which is what tells a client to put a signing box there and not a
	// text field.
	v, err := dana.View(ctx(), "application.countersigned")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	n := node(t, v, "application.countersigned")
	if n.Kind != "signature" {
		t.Errorf("the field's kind is %q, want signature", n.Kind)
	}
	if !n.Required {
		t.Error("`required` on a signature field asks nothing of the view")
	}
	if n.Value.Kind() != monshi.KindNone {
		t.Errorf("an unsigned field holds a value of kind %v", n.Value.Kind())
	}
	if err := updateFails(t, dana, n.Path); !monshi.Failed(err,
		monshi.InvalidOp) {
		t.Errorf("writing a signature field = %v, want INVALID_OPERATION", err)
	}
}

// updateFails is the other half of "this is not a value": `Update` refuses to
// write a signature field, which is what makes the completeness walk's exclusion
// a rule about the data rather than a courtesy.
func updateFails(t *testing.T, c monshi.Case, path string) error {
	t.Helper()
	v, err := c.View(ctx())
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	_, err = c.Update(ctx(), v.Working, monshi.Set(path, monshi.Text("x")))
	return err
}

func TestTransitionRefusesForWantOfARequiredSignature(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	// The conjunct §5.5 has stated since milestone 3 and paid for with
	// milestone 5's currency: content complete, the unit approved, and the
	// destination still refuses, because the form's required attestation does
	// not stand. The gap arrives on the same list the fields and the units
	// arrive on, last, in the order a client can act on it.
	at := approved(t, dana, elliot)
	res, err := elliot.Transition(ctx(), "filed", at)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("a transition over an unsigned required signature = %v, want "+
			"INVALID_OPERATION", err)
	}
	if !strings.Contains(err.Error(), "1 signature") {
		t.Errorf("the refusal says %q, want the attestation counted", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "application.countersigned" {
		t.Errorf("Missing = %s, want the required field", paths(res.Missing,
			"application.countersigned"))
	}
	// An optional attestation on the same form gates nothing: two of this
	// form's three signature fields are still unsigned when the destination is
	// reached below, and the definition's `required` is the only difference
	// between them and the one that asks.
	if _, err := elliot.Sign(ctx(), "application.countersigned", at,
		"seal"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	out, err := elliot.Transition(ctx(), "filed", at+1)
	if err != nil {
		t.Fatalf("transition after the countersignature = %v", err)
	}
	if out.Stage != "filed" {
		t.Errorf("the case reached %q, want filed", out.Stage)
	}
	if got := shown(t, dana, "application.countersigned"); got.By != "elliot" {
		t.Errorf("the attestation the destination asked for reads %q", got.By)
	}
}

func TestTransitionAsksNothingOfAnInvalidatedSignature(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	at := approved(t, dana, elliot)
	if _, err := elliot.Sign(ctx(), "application.countersigned", at,
		"seal"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// The destination asked, got, and now the content moves under the
	// attestation. A transition that read the number in the table as permission
	// would let a signature outlive the content it bound, which is the failure
	// mode §4.2 names, and this is the only test of it from the stage machine's
	// side.
	draft(t, dana, monshi.Set("application.full_name", monshi.Text("Ada Two")))
	rev := committed(t, dana, "application")
	if z := shown(t, dana, "application.countersigned"); z !=
		(monshi.SignatureInfo{}) {
		t.Fatalf("the content commit left the attestation standing: %+v", z)
	}
	res, err := elliot.Transition(ctx(), "filed", rev)
	if !monshi.Failed(err, monshi.InvalidOp) {
		t.Fatalf("transition over an erased signature = %v, want a refusal",
			err)
	}
	// Both verdicts the commit erased are asked for, in the order a client can
	// act on them: the decision §5.6's other effect reset, then this one. The
	// destination does not know or care which rule cleared which.
	if len(res.Missing) != 2 || res.Missing[0] != "application" ||
		res.Missing[1] != "application.countersigned" {
		t.Errorf("Missing = %s, want the unit then the erased field",
			paths(res.Missing, "application", "application.countersigned"))
	}
	if !strings.Contains(err.Error(), "1 signature") ||
		!strings.Contains(err.Error(), "1 approval") {
		t.Errorf("the refusal says %q, want the wording it had before", err)
	}
	// Re-deciding and re-signing is the whole remedy, and the second attestation
	// binds the new content: the case is not stuck at a stage whose requirement
	// was erased by a rule it also states. The two calls are the same two the
	// case needed the first time, which is what makes an erasure cheaper to the
	// host than a verdict with a flag on it — nothing has to be re-explained.
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		""); err != nil {
		t.Fatalf("re-approving = %v", err)
	}
	if _, err := elliot.Sign(ctx(), "application.countersigned", rev+1,
		"seal-2"); err != nil {
		t.Fatalf("re-signing = %v", err)
	}
	out, err := elliot.Transition(ctx(), "filed", rev+2)
	if err != nil {
		t.Fatalf("transition after re-signing = %v", err)
	}
	if out.Stage != "filed" {
		t.Errorf("the case reached %q, want filed", out.Stage)
	}
}

func TestASignatureIsAQuorumOfOne(t *testing.T) {
	rt, dana, elliot, _, _, _ := signing(t)
	at := approved(t, dana, elliot)
	if _, err := dana.Sign(ctx(), "application.submitted", at,
		"first"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// A field open to a role is open to whoever holds it, so a second applicant
	// is not a second signature: §4.2's "at most one effective signature per
	// content version" is a statement about the state, and the state's answer
	// after both hands is one name. Refusing the second hand would lock the
	// field to whoever asked first, and there is no revocation for a signature
	// to undo that.
	zoe, err := rt.Open(ctx(), dana.ID(), sealAgain)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := zoe.Sign(ctx(), "application.submitted", at+1, "second")
	if err != nil {
		t.Fatalf("a second hand on one field = %v", err)
	}
	got := shown(t, dana, "application.submitted")
	if got.By != "zoe" || got.Evidence != "second" || got.At != res.Revision {
		t.Errorf("the field reports %q@%d %q, want zoe@%d second", got.By,
			got.At, got.Evidence, res.Revision)
	}
	// The replaced attestation is not a stale one either: the view holds one
	// answer for the field, and the record of the first is the history's.
	v, err := dana.View(ctx(), "application.submitted")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if s := node(t, v, "application.submitted").Signature; s.By != "zoe" {
		t.Errorf("the node reports two hands: %+v", s)
	}
}

func TestTheSameHandSigningAgainCommitsNothing(t *testing.T) {
	_, dana, elliot, _, _, _ := signing(t)
	at := approved(t, dana, elliot)
	res, err := dana.Sign(ctx(), "application.submitted", at, "envelope")
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// §6.3's rule that a commit must say something, applied to a verdict: the
	// same hand attesting to the same content with the same reference is already
	// the case's answer, so the call succeeds and the number does not move. A
	// double-click is why this matters, and a revision that counted it would be
	// a count of clicks.
	again, err := dana.Sign(ctx(), "application.submitted", res.Revision,
		"envelope")
	if err != nil {
		t.Fatalf("a repeated signature = %v, want success", err)
	}
	if again.Revision != res.Revision {
		t.Errorf("the repeat reached revision %d, want the unchanged %d",
			again.Revision, res.Revision)
	}
	// A different reference is a different statement — it is what the host says
	// the ceremony produced — so it commits, at the same hand and the same
	// content. §5.9 says the same about a decision repeated with a different
	// comment.
	diff, err := dana.Sign(ctx(), "application.submitted", res.Revision,
		"envelope-copy")
	if err != nil {
		t.Fatalf("a signature with another reference = %v", err)
	}
	if diff.Revision != res.Revision+1 {
		t.Errorf("a new reference reached %d, want %d", diff.Revision,
			res.Revision+1)
	}
	if got := shown(t, dana, "application.submitted"); got.Evidence !=
		"envelope-copy" {
		t.Errorf("the field reports %q, want the new reference", got.Evidence)
	}
}

func TestAGrantIsNotAnIdentity(t *testing.T) {
	_, dana, elliot, bo, _, cal := signing(t)
	at := approved(t, dana, elliot)
	// Two conjuncts answer "may this hand sign here": the definition's `sign`
	// grant at the field's slot, and the field's own statement about whose
	// attestation it wants. `Bearer` holds `allow sign all`, so the first is
	// satisfied at every signature field in the case, and it is the second that
	// refuses him below — which is the difference between this engine checking
	// what a definition says and the language meaning something the engine could
	// not check.
	_, err := cal.Sign(ctx(), "application.submitted", at, "")
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Fatalf("a `sign all` holder signing a field open to Applicants = "+
			"%v, want ACCESS_DENIED", err)
	}
	if !strings.Contains(err.Error(), "Applicant") {
		t.Errorf("the refusal says %q, want the role the field is open to",
			err)
	}
	// The grant's absence is the other refusal, and the two are told apart the
	// way §5.7 tells a withheld path from a refused one: the registrar holds no
	// grant on the applicant's field, and is told that rather than that it does
	// not exist.
	if _, err := elliot.Sign(ctx(), "application.submitted", at,
		""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("a registrar with no grant on this field = %v, want "+
			"ACCESS_DENIED", err)
	}
	// The assigned form's turn: `signature(.reviewer)` is open to the user the
	// committed field names, and this actor holds the grant and names nobody, so
	// the mask admits him and the entry does not. `bo` is refused below because
	// the reviewer field is still empty — an unassigned field names nobody,
	// which is not the same as naming everybody.
	_, err = bo.Sign(ctx(), "application.approved_by", at, "note")
	if !monshi.Failed(err, monshi.AccessDenied) {
		t.Fatalf("signing a field whose role-typed source is empty = %v, "+
			"want ACCESS_DENIED", err)
	}
	if !strings.Contains(err.Error(), "named at") {
		t.Errorf("the refusal says %q, want the field it reads the name from",
			err)
	}
}

func TestAnAssignedSignatureFollowsTheNamedUser(t *testing.T) {
	_, dana, elliot, bo, cy, _ := signing(t)
	draft(t, dana,
		monshi.Set("application.kind", monshi.Tag("PERSON")),
		monshi.Set("application.full_name", monshi.Text("Ada")),
		monshi.Set("application.reviewer", monshi.User("bo")))
	at := committed(t, dana, "application")
	res, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, at,
		"")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	at = res.Revision

	// A role-typed field's content is content, and the consequence is the
	// interesting one: `signature(.reviewer)` is open to the user that field
	// names in the committed case, so the attestation is bo's while the form
	// says bo — and both men hold the same `sign` grant, so nothing about the
	// definition distinguishes them.
	if _, err := bo.Sign(ctx(), "application.approved_by", at,
		"note"); err != nil {
		t.Fatalf("the named reviewer signing = %v", err)
	}
	if got := shown(t, dana, "application.approved_by"); got.By != "bo" {
		t.Errorf("the field reports %q, want the named reviewer bo", got.By)
	}
	// cy holds the grant and is not the name. The refusal says so rather than
	// pretending the path does not exist: an actor the definition opened the
	// field to, in a role the definition granted, is entitled to the reason.
	if _, err := cy.Sign(ctx(), "application.approved_by", at+1,
		""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("an unnamed manager signing = %v, want ACCESS_DENIED", err)
	}
	// Reassigning the reviewer is a content commit, so it erases the attestation
	// by §4.2's rule and moves the authority by the same content: the signature
	// the first reviewer's identity authorized is gone, and the second may now
	// give it. This is not a rule about signatures — it is §4.2's rule applied to
	// a form whose content includes who is in charge of it.
	//
	// The commit erases the decision too, and one diff entry is what both effects
	// answer to: a form whose reviewer changed has different content, and neither
	// verdict is about the content the case holds now.
	draft(t, dana, monshi.Set("application.reviewer", monshi.User("cy")))
	rev := committed(t, dana, "application")
	if z := shown(t, dana, "application.approved_by"); z !=
		(monshi.SignatureInfo{}) {
		t.Errorf("reassigning the reviewer left %q's attestation standing",
			z.By)
	}
	// The displaced hand is refused by the identity conjunct and not by the
	// readiness gate, because whose attestation a field takes is asked before
	// what it would attest to: a refusal that reached the pending decision first
	// would tell bo to wait for the registrar instead of telling him the field
	// is not his.
	if _, err := bo.Sign(ctx(), "application.approved_by", rev,
		""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("the displaced reviewer signing again = %v, want "+
			"ACCESS_DENIED", err)
	}
	if _, err := elliot.Review(ctx(), "application", monshi.ReviewApprove, rev,
		""); err != nil {
		t.Fatalf("re-approving: %v", err)
	}
	if _, err := cy.Sign(ctx(), "application.approved_by", rev+1,
		"note"); err != nil {
		t.Fatalf("the new reviewer signing = %v", err)
	}
	if got := shown(t, dana, "application.approved_by"); got.By != "cy" {
		t.Errorf("the field reports %q, want cy", got.By)
	}
}

func TestSignRefusesTheWrongShapeOfPath(t *testing.T) {
	_, dana, _, _, _, cal := signing(t)
	// Three refusals, in the order §5.4 and §5.5 state them: definition
	// questions first, then the caller's freshness of view, then the case's own
	// answers. `cal` holds `allow sign all`, so the grant is never what refuses
	// him, and what does is a question about the shape of what he named.
	if _, err := cal.Sign(ctx(), "nowhere.at.all", 0,
		""); !monshi.Failed(err, monshi.NotFound) {
		t.Errorf("a path the case does not have, for an actor granted `sign "+
			"all` = %v, want NOT_FOUND", err)
	}
	// The same absent path for an actor with no grant there answers
	// ACCESS_DENIED, which is §5.7's rule that a refusal the caller is not
	// entitled to make must not leak the shape of the case.
	if _, err := dana.Sign(ctx(), "application.nope", 0,
		""); !monshi.Failed(err, monshi.AccessDenied) {
		t.Errorf("a path the case does not have, for an actor with no grant "+
			"there = %v, want ACCESS_DENIED", err)
	}
	// The shape question the mask cannot answer on its own: `allow sign all`
	// puts the bit at the case root and every slot inherits it, so the only
	// thing between a grant that means "every signature in the case" and one
	// that means "every field in the case" is the runtime asking whether this
	// slot declares a signature at all.
	if _, err := cal.Sign(ctx(), "application.full_name", 0,
		""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("signing a field that declares no signature = %v, want "+
			"INVALID_OPERATION", err)
	} else if !strings.Contains(err.Error(), "no signature") {
		t.Errorf("the refusal says %q, want the shape named", err)
	}
	// A form is a content unit and not a signature field, and the same question
	// answers it: the path resolves, the grant holds, and what is named is not
	// something a hand attests to.
	if _, err := cal.Sign(ctx(), "application", 0,
		""); !monshi.Failed(err, monshi.InvalidOp) {
		t.Errorf("signing a form = %v, want INVALID_OPERATION", err)
	}
	// A stale observation is the only refusal that returns a result, because it
	// is the only one that says something about the case rather than about the
	// request: the caller is told where the case is, in the shape they will use
	// when they come back.
	res, err := cal.Sign(ctx(), "application.submitted", 40, "")
	if !monshi.Failed(err, monshi.StaleRevision) {
		t.Fatalf("a stale observation = %v, want STALE_REVISION", err)
	}
	if res.Revision == 40 || res.Stage != "draft" {
		t.Errorf("the refusal reports %d at %q, want the case's own numbers",
			res.Revision, res.Stage)
	}
	if len(res.Missing) != 0 {
		t.Errorf("a freshness refusal listed %s, want nothing",
			paths(res.Missing))
	}
}

func TestTheViewReportsWhatMayBeSignedAndWhatWas(t *testing.T) {
	_, dana, elliot, bo, _, cal := signing(t)
	// `Signable` is the answer `Sign` will give at this address for this actor:
	// the mask, the shape, and nothing else. The field's own statement about
	// whose attestation it wants is about a person, and a view is not asked to
	// guess which person is reading it — so `cal`'s grant reaches every
	// signature field and no ordinary one, while the conjunct that will refuse
	// him is not reported at all.
	v := whole(t, cal)
	for _, path := range []string{"application.submitted",
		"application.approved_by", "application.countersigned",
		"archive.sealed"} {
		if n := node(t, v, path); !n.Signable {
			t.Errorf("%s is not signable for a `sign all` holder, and the "+
				"mask is the whole of what the view answers", path)
		}
	}
	// The assertion the shape check exists for: `all` reaches a value field too,
	// and a view that asked only the mask would report half a form as
	// signable.
	for _, path := range []string{"application", "application.full_name",
		"application.pages", "application.kind", "archive",
		"archive.filed_at"} {
		if n := node(t, v, path); n.Signable {
			t.Errorf("%s reports itself signable, and it declares no "+
				"signature to make", path)
		}
	}
	// The flag answered for the actors the definition actually opened each
	// field to, which is the mask's own shape rather than the root's: three
	// fields, three grantees, and an applicant with one act available.
	dv := whole(t, dana)
	if n := node(t, dv, "application.submitted"); !n.Signable {
		t.Error("the applicant may not sign his own filing")
	}
	for _, path := range []string{"application.approved_by",
		"application.countersigned", "application.full_name"} {
		if n := node(t, dv, path); n.Signable {
			t.Errorf("%s reports itself signable to the applicant", path)
		}
	}
	if n := node(t, whole(t, bo), "application.approved_by"); !n.Signable {
		t.Error("a manager may not sign the field the grant names")
	}
	if n := node(t, whole(t, elliot), "application.countersigned"); !n.Signable {
		t.Error("the registrar may not countersign")
	}

	// The attestation itself is read from the committed case while the values
	// beside it come from the caller's draft, which is the split §6.1 states: a
	// hand vouches for what is published, and somebody may be editing what is
	// not. Neither statement is wrong, because they are about different content.
	at := approved(t, dana, elliot)
	if _, err := dana.Sign(ctx(), "application.submitted", at,
		"envelope-7"); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	draft(t, dana, monshi.Set("application.full_name", monshi.Text("Not Yet")))
	dr, err := dana.View(ctx(), "application")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got := node(t, dr, "application.full_name").Value.Text(); got !=
		"Not Yet" {
		t.Errorf("the draft reads %q, want the caller's own uncommitted value",
			got)
	}
	got := node(t, dr, "application.submitted").Signature
	if got.By != "dana" || got.Evidence != "envelope-7" {
		t.Errorf("the draft's view lost the attestation: %+v", got)
	}
	// Committing that draft is what the attestation was about, and it goes — the
	// same rule seen from the other side of the seam, with no flag on the node
	// to say which kind of absence it is.
	committed(t, dana, "application")
	after, err := dana.View(ctx(), "application.submitted")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	n := node(t, after, "application.submitted")
	if n.Signature != (monshi.SignatureInfo{}) {
		t.Errorf("the node still reports %+v", n.Signature)
	}
	if !n.Signable {
		t.Error("an erased attestation made the field un-signable")
	}
}
