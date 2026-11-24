package monshi

// Sign is DESIGN §6.4's attestation about content the case already holds: an
// actor's statement that a form, as committed, is theirs. It is the fourth
// operation that commits, the second whose commit says nothing about data, and
// the reason §5.5's `require` line has all three of the conjuncts it states.
//
// The two halves of what DESIGN §4.2 promises are separable, and only one of
// them is this engine's. The *authentication* half — the cryptographic or
// biometric act, the witness, the printed image — belongs to the host and its
// verifier (§1's rule that the engine holds no secret material, §4.2's sentence
// that a signature image alone is not proof). What the case holds is the fact
// that an authorized hand attested to a revision: who, which content version,
// and the reference the caller handed §5.10 to point at. A verifier that wants
// the artifact asks the host for it by that reference, and a case whose table
// carried a blob would be a case that had forgotten which side of the seam
// proof lives on.
//
// The other half — validity for current content — is entirely this engine's, and
// the choice `Review` made in §5.9 is the choice made here, for the same reason:
// a signature is erased rather than marked stale. §5.6's effect clears the entry
// when the content it attested to moves, so "is there a signature standing for
// what the case holds?" has one answer, is answered by looking for the entry, and
// cannot be answered by a verdict that outlived its content. What §4.2 calls the
// retained historical record is the `chgSign` entry, which no erasure touches.
//
// So no content digest is stored, and that is a decision rather than an
// omission. A digest would answer the question one array read already answers —
// §5.6's rule is what makes an entry's existence mean "this content", so the
// bytes beside it could only agree with the entry or contradict it — and it would
// be the engine's own hash of the case's data, a thing no consumer asked for,
// computed per commit and compared per read. §3.2's preorder gives the erasure an
// index, and an index is cheaper than a fingerprint by the whole cost of
// computing one.
//
// The guard order is §5.4's and §5.5's, repeated from review.go for the reason
// that file gives: two questions about the definition come first, the observed
// revision is the only refusal that returns a result, and what the signature
// attests to is checked after the caller's freshness of view is known. A
// signature is asked about the committed case and never about a draft, which is
// why every read here is against `rec.commit`: the content a signer vouches for
// is the content the case holds, and §4.2 says so twice — in the sentence about
// binding to submitted content, and in the sentence that says a content change
// ends the signature.
//
// Two things stay on the record. `Sign` runs no `Validate` hook, because the
// content a signature binds is committed content and §5.4's gates already ran
// against every byte of it: re-asking a host whether content the case accepted
// is acceptable would make an attestation depend on when it was asked rather than
// on what it attests to. And DESIGN §10 milestone 4's verifier interface is not
// this file: §9 keeps that decision where the design document keeps it.

import (
	"context"
	"slices"
)

// SignatureInfo is one signature field's report about the attestation the case
// holds for it (DESIGN §6.1's signature state, RUNTIME §5.10). Like
// `ReviewInfo` it is a value whose zero says there is nothing to report, because
// most nodes are not signature fields at all, and a field the case has never
// seen answers the same way as one whose signature was erased.
//
// `By` is the actor, and `At` the revision the signature's own commit produced —
// §5.9's convention for a decision, restated here rather than shared, because
// the reason is the same and the two tables are not. `Evidence` is what the host
// handed with the signature, returned as it was given and empty when the caller
// handed nothing: the engine kept a reference and asked nothing to be true of it.
type SignatureInfo struct {
	By       UserID // whose hand attested; "" when nothing stands
	At       uint64 // the revision this signature's own commit produced
	Evidence string // the caller's reference to what was signed with
}

// SignResult is what a signature reached. It carries the list §4.2 calls "fully
// submitted" because that is the one thing a signer can be told to fix: a
// `Review` refuses a caller who is not asked to decide, and a `Sign` refuses a
// caller whose form is not ready — which is a fact about fields and units, and
// has paths attached. The signature's own absence is not among them: signing is
// how this call answers that question, and a refusal that listed it would tell
// the caller to do the thing they were doing.
//
// `Affected` names the attested field, §5.3's same rule asked of the one
// entry this commit writes: the field's rendering moved and its dependents
// with it. Nil on an already-standing signature and on every refusal.
type SignResult struct {
	Revision uint64
	Stage    string
	Missing  []string // §5.4's content gaps and §5.5's undecided units
	Affected []string
}

// sigState is the stored form of an attestation, one entry per signature field
// in a table dense by slot. The zero value is no signature, which is what lets an
// unsigned case hold a nil table: the absent entry and the unsigned field are the
// same answer, and `sigAt` is the only place that gets to say so.
//
// There is no `valid` bit and no code for "invalidated". §5.6's effect is what
// invalidates, and it does so by removing the entry, so an entry's presence *is*
// validity for current content and nothing else is representable. A third state
// would be a record of a signature that no longer means anything, and the history
// already holds that.
type sigState struct {
	by  UserID
	at  uint64
	ref string
}

// Sign attests to one signature field: `target` names the field, `observed` is
// the committed revision the signer was shown, in the same sense as `Review`'s,
// and `evidence` is a reference to what the host's authentication produced — a
// document, an envelope, a ceremony ID. The case keeps the caller's own words
// about that artifact and interprets nothing (§1's minimal surface: the engine
// stores a reference and asks for nothing), and a blank reference is accepted for
// the same reason: the language has no expression for one, whether a signing
// ceremony leaves an artifact is the deployment's convention, and refusing a
// signature for want of a pointer would be inventing a rule §4.2 does not state.
//
// What must be true first is what the signature would attest to: the form's
// committed content is complete (§5.4's walk) and every verifiable unit inside it
// stands APPROVED (§5.5's second conjunct). Those are DESIGN §6.4's "fully
// submitted/valid with approved applicable verification units", asked of the case
// and not of any draft, because a signature bound to content nobody committed
// would be one statement about two things.
//
// A signature already standing for this same content, this same evidence, by this
// same hand is success that commits nothing (§6.3's rule, as §5.9 states it). A
// different hand on the same field arrives at replacement, which is what §4.2's
// "at most one effective signature per content version" is the shape of: the
// entry says whose attestation now stands, and the record of the first is
// history's. Revocation is not offered, because §4.2 lists it for decisions and
// not for signatures — and taking back an attestation without withdrawing the act
// that made it is a statement this engine cannot make on a host's behalf.
func (c Case) Sign(ctx context.Context, target string, observed uint64,
	evidence string) (SignResult, error) {
	refuse := c.refusing("sign")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return SignResult{}, err
	}
	img := d.img

	// Resolution and authority, both asked of the committed state, and the two
	// refusals told apart the way §5.7 tells them apart. The mask is asked at the
	// field's own slot: P3 puts a `sign` grant's target on a signature field, so
	// the bit anywhere else arrived by inheritance — and `all` is inheritance
	// from the case root, which is why the shape question is asked below instead
	// of being assumed away by the grant.
	roles := img.rolesHeld(c.actor, rec.commit.s)
	r, ok := rec.commit.s.resolve(target)
	granted := img.maySign(roles, rec.commit.stage, r.slot)
	if !ok || !granted {
		if !ok && granted {
			return SignResult{}, refuse.now(NotFound,
				"no path is "+quote(target))
		}
		return SignResult{}, refuse.now(AccessDenied,
			"sign is not granted at "+quote(target))
	}

	// Two questions about the definition, both before the case is consulted about
	// its content: what this path names has to be a signature field, and a
	// signature field has to be reached by a path that names no row.
	field, sl := r.slot, img.slots[r.slot]
	if sl.sig == 0 {
		return SignResult{}, refuse.now(InvalidOp,
			quote(img.path(field))+" declares no signature to make")
	}
	// S1 read from the runtime side. The front end cannot be fooled into putting
	// a signature field inside a `multiple` group, so this is the absence of a
	// feature and not a hole in one: a path through a row would ask the engine to
	// attest to one instance of a collection, and §4.2 binds a signature to *form*
	// content, which is the whole form and not a member of it.
	if r.depth != 0 {
		return SignResult{}, refuse.now(InvalidOp,
			quote(target)+" names a row, and a signature attests to "+
				quote(img.path(img.owner(field)))+" as a whole")
	}

	if observed != rec.revision {
		res := SignResult{Revision: rec.revision,
			Stage: img.stageName(rec.commit.stage)}
		return res, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+
				dec(observed))
	}
	res := SignResult{Revision: rec.revision,
		Stage: img.stageName(rec.commit.stage)}

	// S3's authority, which is the mask's second conjunct and not a restatement
	// of it: `signature(Role)` is open to a holder of that role, and
	// `signature(field)` is open to the user that field's committed value names.
	// A definition may grant `sign` to a role the signature is not open to — the
	// two are declared in different statements — so the mask answers "was this
	// hand granted the act" and this answers "is this the hand". Both read the
	// committed fold, so an actor who reaches a role through a committed
	// role-typed field holds it here as everywhere else (T4).
	e := img.sigs[sl.sig]
	if e.assigned != 0 {
		named := rec.commit.s.cellValue(e.assigned, nil)
		if !named.set || UserID(named.s) != c.actor.User {
			return res, refuse.now(AccessDenied, quote(img.path(field))+
				" is open to the user named at "+quote(img.path(e.assigned))+
				", and this actor is not that user")
		}
	} else if !slices.Contains(roles, e.role) {
		return res, refuse.now(AccessDenied, quote(img.path(field))+
			" is open to a holder of "+
			quote(img.roleName(e.role))+", and this actor is not one")
	}

	// What the signature would attest to. Both halves are already written for
	// §5.5's `require` — the same walk, the same table, the same order — so a form
	// a transition would find incomplete is a form that cannot be signed, and a
	// form whose units are undecided is a form whose signature would be a claim
	// about a judgement nobody made.
	form := img.owner(field)
	gaps := rec.commit.s.incomplete(form)
	gaps = append(gaps, pendingUnits(&rec.commit.s, rec.commit.reviews,
		form)...)
	if len(gaps) > 0 {
		res.Missing = gapPaths(gaps)
		return res, refuse.now(InvalidOp, notSignable(img.path(form), gaps))
	}

	next := sigState{by: c.actor.User, at: rec.revision + 1, ref: evidence}
	cur := sigAt(rec.commit.sigs, field)
	if cur.by == next.by && cur.ref == next.ref {
		// The same hand, the same evidence: already true, and a revision that
		// counts commits must not count this one. `at` is not compared, because it
		// is this entry's own number and not a fact about the attestation — a
		// signature repeated with a different reference says something the case did
		// not hold, exactly as §5.9 says of a decision repeated with a different
		// comment.
		return res, nil
	}

	tbl := signTable(img, rec.commit.sigs)
	tbl[field] = next
	out, err := c.w.cases.commit(refuse, img, rec.revision,
		committedState{s: rec.commit.s, stage: rec.commit.stage,
			reviews: rec.commit.reviews, sigs: tbl},
		committedBy{by: c.actor.User, op: opSign,
			changes: []commitChange{{kind: chgSign, slot: field,
				v: slotValue{set: true, s: evidence}}}})
	if err != nil {
		// Reachable only through a concurrent commit, which the store answers in
		// its own voice for the reason submit.go gives. Nothing the caller was
		// told about a revision this call did not reach belongs in the result.
		return SignResult{}, err
	}
	res.Revision = out.rec.revision
	// §5.9's pair again: one entry, the field's own slot, and the walk that
	// says what else its rendering reached.
	fan := newFanout(img)
	seedCommit(fan, out.rec.changes, out.erased)
	res.Affected = fan.affected()
	c.notify(ctx, img, rec, out)
	return res, nil
}

// notSignable states what the signature would have been attesting to, in the two
// kinds of thing §4.2 asks for and counted apart for §5.5's reason: "fill this
// in" and "wait for the registrar" are different errands, and a caller who was
// told one number for both would send a user back to a form they had already
// finished. The paths are in the result; this is the log line.
func notSignable(form string, gaps []gap) string {
	var fields, units int
	for _, g := range gaps {
		if g.kind == gapVerif {
			units++
			continue
		}
		fields++
	}
	var parts []string
	if fields > 0 {
		parts = append(parts, count(fields, "required field",
			"required fields")+" with no value")
	}
	if units > 0 {
		parts = append(parts, count(units, "approval", "approvals")+
			" outstanding")
	}
	return quote(form) + " is not ready to be signed: " + andList(parts)
}

// sigAt reads one field's attestation. As with `reviewAt`, an entry past the
// table's end is no signature: the table is allocated to the slot count on the
// first signature anybody makes, so a short table means nothing has been signed.
func sigAt(have []sigState, field nodeID) sigState {
	if int(field) >= len(have) {
		return sigState{}
	}
	return have[field]
}

// signTable copies a case's stored signature table so that it can be replaced,
// sized to the definition's slots so that `sigAt`'s index is the field's own
// number. Only the slots §3.2's numbering puts in a form's signature range are
// ever read, so the density costs len(slots) entries and buys one array index per
// question — the trade review.go's `reviewTable` states, for the same reason:
// §3.9's rule about what indexes by data has no exception here.
func signTable(img *image, have []sigState) []sigState {
	next := make([]sigState, len(img.slots))
	copy(next, have)
	return next
}

// sigReset is §4.2's other erasure: a content change invalidates the effective
// signature but retains the historical record. The erasure is the invalidation,
// and the record it retains is the `chgSign` entry in history, which nothing here
// touches.
//
// The mapping is two array reads per entry. `slots[n].form` says whose content
// moved — the ordinal §3.2's lowering stamps over the form's preorder range, which
// is why no request-time answer walks a parent chain — and `sigSlotsOf` says which
// signature fields that form declares, in declaration order, as a dense range. A
// form that declares none has an empty range, so a definition that does not use
// signatures costs two reads and no copy, and the range's size bounds the work of
// one that does: the question is answered by the signatures a form has and never
// by its fields.
//
// A role-typed field's content is content, which is worth saying because the
// consequence is the interesting one: reassigning `application.reviewer`
// invalidates the signature that field's value was authorized to make, since the
// person named is now somebody else. That is not a rule this effect adds; it is
// §4.2's rule applied to a form whose content includes who is in charge of it.
type sigReset struct{}

func (sigReset) reach(img *image, next *committedState, ch commitChange,
	hits []nodeID) []nodeID {
	for _, field := range img.sigSlotsOf(img.slots[ch.slot].form) {
		if sigAt(next.sigs, field).at == 0 {
			continue // nothing standing that an invalidation would change
		}
		hits = addHit(hits, field)
	}
	return hits
}

func (sigReset) open(img *image, next *committedState) {
	next.sigs = signTable(img, next.sigs)
}

// erase is the invalidation. An erased entry is not a signature with a flag on
// it: §4.2's "at most one effective signature per content version" is a statement
// about what may exist, and what may not exist is not stored.
func (sigReset) erase(next *committedState, field nodeID) {
	next.sigs[field] = sigState{}
}

// unsignedSigs lists the signature fields of one form that its `required`
// attributes ask for and no attestation answers: §5.5's third conjunct, SYNTAX
// §5.6's "its required signatures are valid", reported on the same gap list
// §5.4's completeness walk produces, for §5.9's reason — the destination's
// question is already "what does this form still owe", and a client handed two
// lists has to join them itself.
//
// The scan reads the form's signature range and asks two questions of each field:
// whether the definition requires it, with the same `asked` predicate the
// completeness walk uses — which is what `signature(Role) required` means, and
// what makes an optional attestation cost nothing to a transition — and whether
// one stands. It is short by construction rather than by pruning: S1 puts the
// fields directly in the form, so there is no row to visit and no subtree to skip,
// and SYNTAX §4 takes away the three attributes that could make a signature
// field's presence conditional.
//
// A signature that stands is valid for current content by §5.6's rule, so this
// asks about presence and never about staleness, and an invalidated signature
// arrives here as the absence that §5.10 says it is.
func unsignedSigs(s *state, sigs []sigState, form nodeID) []gap {
	img := s.img
	var out []gap
	for _, field := range img.sigSlotsOf(img.slots[form].form) {
		if !s.asked(field) {
			continue // an optional attestation is not a question to withhold
			// a stage over
		}
		if sigAt(sigs, field).at != 0 {
			continue
		}
		out = append(out, gap{path: img.path(field), kind: gapSig})
	}
	return out
}

// signatureInfoOf is the node's report about its own signature field. It is
// review.go's `reviewInfoOf` mirrored, and read from the same place — the
// committed snapshot, while the values beside it come from the caller's draft —
// which is why a view can show an attestation next to content somebody is still
// editing and be right about both: they are statements about different content.
func signatureInfoOf(img *image, sigs []sigState, n nodeID) SignatureInfo {
	if img.slots[n].sig == 0 {
		return SignatureInfo{}
	}
	g := sigAt(sigs, n)
	return SignatureInfo{By: g.by, At: g.at, Evidence: g.ref}
}
