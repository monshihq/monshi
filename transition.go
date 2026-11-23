package monshi

// Moving a case between stages (DESIGN §6.4; RUNTIME §5.5).
//
// A transition is the second operation that commits, and it differs from a
// submit in the one way that reaches every rule below: it changes no content.
// What it publishes is a stage. That is why it reads the *committed* state and
// not the caller's draft (DESIGN §6.4's first sentence), why the number it is
// given is the case's own revision rather than a working copy's, why its
// history entry carries no diff yet does carry a stage that differs from the
// entry before it, and why it is the first commit whose result has something to
// say about *paths*: moving the stage moves every stage-scoped grant, so the
// set of paths this actor may see changes with no value having moved.
//
// Five decisions here are worth their reasons:
//
//   - **One number, and it is the case's.** DESIGN §6.4 is explicit that these
//     operations "operate on committed state, not drafts", so there is no
//     working revision to check and no candidate to graft: the actor's copy,
//     if they have one, is left exactly as it was — even its base, unless it
//     happened to stand on the very revision this commit replaces, which is
//     the only case in which §6.3's rebase may follow (§5.5's reason that rule
//     cannot be unconditional).
//     Freshness is asked once, of the case, and it refuses *even if the commit
//     changed an apparently unrelated form* — the caller is asserting they
//     looked at this case, and a case that is not the one they looked at is
//     not the case they may act on (invariant 5).
//   - **Permission, then freshness, then content** — the order §5.4 shipped
//     with, for the same reason. The grant is a fact about the definition and
//     the actor's committed roles, so it is known without reading anything the
//     actor may not see; reporting a stale revision or an unmet guard to an
//     actor the definition refuses would trade a `null` for the case's
//     revision traffic. Nothing in DESIGN or RUNTIME §7 states that as a rule
//     — §7 is about containment and bounding, not disclosure, and the one
//     written disclosure rule is invariant 7's, about `View` — so this is the
//     engine's own hardening rather than a citation, and the refusals below
//     are the only place it shows.
//   - **`reject` skips the destination's `require` and nothing else** (DESIGN
//     §4.4). The roles, the edge and its guard are all still asked, because
//     "no" is as much a decision as "yes" and an actor who may not say it may
//     not say it. What is skipped is *destination completeness*, which is the
//     asymmetry the whole section turns on: a case that cannot go forward is
//     exactly the case somebody must be able to send back.
//   - **A refusal names paths and never a value.** The destination's `require`
//     answers with `Missing`, whose entries are declaration paths, so the
//     operation discloses definition structure and not case content. Filtering
//     that list by the actor's `view` masks would be the consistent move only
//     if presence were confidential, and it is not: F5's pruning already
//     answers a guard by *absence* in every `View`, so a condition's outcome is
//     visible in the language by design and a gap list adds nothing to it.
//     Per-node confidentiality is §6.1's job, as invariant 7 says.
//   - **No hooks are re-asked.** §5.5's line asks whether the required forms
//     are `satisfied`, which is §5.2's engine-level answer — present,
//     required, non-empty — and not §6's question about whether a value is
//     acceptable. Submit is where a stored value is re-asked (RUNTIME §5.4),
//     and it asked on the way in. A transition that ran hooks here would
//     invent a third place a commit can fail for a reason no field the actor
//     can reach explains.
//
// What this file does not do is the other two conjuncts of §5.5's require line
// — `verif` units APPROVED and signatures valid. Nothing approves or signs
// until `Review` and `Sign` exist (DESIGN §10's milestone 5), so there is
// nothing yet for a transition to withhold a stage change over; RUNTIME §5.5
// says the same from the other side.

import "context"

// TransitionResult is what a transition reports: the revision the case
// reached, the stage it is at now, the paths this actor's view of the case is
// answered differently about, and — when the move was refused — the content
// the destination still asks for.
//
// `Revision` and `Stage` are filled from the moment the caller has proved
// entitled — the grant held, the number matched — through every later answer,
// refusal included, which is the division `CommitResult` draws and for the
// same reason: an `ACCESS_DENIED` that arrived beside the case's revision and
// stage would answer the question that refusal was written not to answer,
// while a refusal about the *case* is one the caller is standing inside. On
// the paths that return no result the two are zero, and a host that needs the
// position after `ACCESS_DENIED` reads it from its next `Open`.
//
// `Stage` is the stage the case sits at after this call, which on a refusal is
// the one it sat at before.
//
// `Affected` is §5.3's answer for an operation whose batch wrote nothing: the
// paths whose view moved because the masks the definition resolves for this
// actor moved with the stage (§3.5's stage overlays). It is the acting
// actor's list, for the same reason `ChangeResult.Affected` is — a second
// actor's answer is a second list, and whom to notify is DESIGN §7's
// `OnCommit` question rather than a field of this result (affected.go).
type TransitionResult struct {
	// Revision is the case's committed revision after this call. A transition
	// always commits — a stage move is a semantic change (§5.1), so there is
	// no empty-diff no-op here — and so this is always the revision the
	// operation produced.
	Revision uint64

	// Stage is the stage the case is at: the destination on success, the
	// stage it was already at on a refusal.
	Stage string

	// Affected holds the paths whose visibility changed for this actor when
	// the stage moved, in declaration order and collapsed the way §5.3
	// collapses: no entry contains another, so a group that changed stands
	// for its subtree. A path appears whether the move gained it or took it
	// away, because the list answers "which views are now rendered
	// differently". Empty when no overlay any held role resolves differs
	// between the two stages, which is the common case — transition
	// permissions are not per-node (SYNTAX P1) — and not a failure to
	// compute.
	Affected []string

	// Missing holds what the destination's `require` still wants, in the same
	// shape and with the same meaning as `CommitResult.Missing`: editable
	// paths, one entry per gap, in declaration order across the required
	// forms. Empty on a transition that committed.
	Missing []string
}

// Transition moves this case along one declared edge (DESIGN §6.4).
//
// `dest` names the destination stage, and the caller names it rather than
// choosing among alternatives because ST3 guarantees at most one edge to any
// destination: `(current, dest)` is a lookup and never a search with ambiguity
// (§5.5). `observed` is the case's committed revision as the actor was shown
// it — the number `Revision` returned, not a working copy's — and any commit
// since then refuses this call.
//
// The checks run in the order RUNTIME §5.5 lists them with one inversion: the
// revision guard moves ahead of the guard and the require walk, because both
// of those read case content and content the caller has not seen is not
// content it can be told to fix. Refusing a transition on a condition the
// caller's snapshot does not contain would send it a message about a case it
// cannot look at; staleness is the answer that is actionable either way.
//
// Nothing is written until the whole list passes, and the write is one call:
// a refused transition leaves the case, every working copy and the history
// exactly as they were.
func (c Case) Transition(ctx context.Context, dest string,
	observed uint64) (TransitionResult, error) {
	refuse := c.refusing("transition")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return TransitionResult{}, err
	}
	img := d.img
	to, ok := img.stageByName(dest)
	if !ok {
		return TransitionResult{}, refuse.now(NotFound,
			"no stage is named "+quote(dest))
	}
	from := rec.commit.stage
	st := &img.stages[from]
	e, ok := st.edgeTo(to)
	if !ok {
		// The destination exists and no edge leads to it from here. The
		// caller may also have mistyped, but the engine cannot tell a typo
		// from a wish to be elsewhere, and what it can say is a fact: no
		// transition is declared. The category is about state rather than
		// about a name, because the name it was given does resolve.
		return TransitionResult{}, refuse.now(InvalidOp,
			"no transition is declared from "+quote(img.stageName(from))+
				" to "+quote(dest))
	}

	// Roles from the committed snapshot, as every committing operation folds
	// them (§4.3). A transition cannot assign a role, so the draft is not
	// merely untrustworthy here — it is irrelevant, which is the clearest
	// statement of why nothing in this operation reads a working copy.
	roles := img.rolesHeld(c.actor, rec.commit.s)
	granted := st.mayAccept(roles)
	kind := "accept"
	if e.reject {
		granted = st.mayReject(roles)
		kind = "reject"
	}
	if !granted {
		return TransitionResult{}, refuse.now(AccessDenied,
			"an "+kind+" toward "+quote(dest)+
				" is not granted at stage "+quote(img.stageName(from)))
	}

	// DESIGN §6.4's guard, and invariant 5's: a stale observed revision cannot
	// transition. The store's own commit guard repeats this under the lock
	// below; this one exists so the refusal arrives before any content is
	// read, and with the case's number in it.
	if observed != rec.revision {
		return TransitionResult{}, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+
				dec(observed))
	}

	res := TransitionResult{Revision: rec.revision, Stage: img.stageName(from)}

	// The edge's condition, over the committed frame. An edge guard was
	// lowered like any other condition (§3.4), so this is one closure call on
	// dense slots, and it is the only reason a transition reads case data at
	// all. Note what is *not* involved: edge guards are absent from the §5.3
	// adjacency on purpose, so nothing here walks a fanout — the guard is
	// asked at the moment of the transition and nowhere else.
	if e.guard != 0 && !img.cond(e.guard)(rec.commit.s.row) {
		return res, refuse.now(InvalidOp,
			"the "+kind+" toward "+quote(dest)+" is declared with a "+
				"condition the committed content does not meet")
	}

	// ST4's destination requirement, and only for an accept (§4.4). The
	// question is §5.4's question — the same `incomplete` walk, so a form that
	// a submit would refuse to publish is a form a transition cannot require
	// — asked of the *committed* state: a draft cannot satisfy a stage, and
	// the content the reviewer is vouching for is the content the case holds.
	if !e.reject {
		var gaps []gap
		for _, form := range img.requires(to) {
			gaps = append(gaps, rec.commit.s.incomplete(form)...)
		}
		if len(gaps) > 0 {
			res.Missing = gapPaths(gaps)
			return res, refuse.now(InvalidOp,
				quote(dest)+" still requires "+count(len(gaps),
					"field", "fields")+" with no value")
		}
		// §5.5's remaining two conjuncts — every `verif` unit in a required
		// form APPROVED, every signature valid for current content — are
		// milestone 5's, with `Review` and `Sign` (DESIGN §10). They join as
		// further gaps on this same list rather than as new shapes: the
		// question is already "what does the destination still want".
	}

	// The commit. Nothing is grafted, because nothing moved: the new snapshot
	// *is* the old one, with the stage beside it rather than inside it
	// (committedState holds the stage precisely so that a stage change needs
	// no new state). The store's replace-never-mutate rule is what makes
	// handing the same state forward safe, and it is why a transition is the
	// one commit that copies no case data.
	//
	// `changes` is empty and that is not a no-op: §5.1 counts the stage among
	// the semantic committed changes, and commitRecord carries the stage of
	// the entry for exactly the replay that has to land the case where it is.
	rev, err := c.w.cases.commit(refuse, rec.revision,
		committedState{s: rec.commit.s, stage: to},
		committedBy{by: c.actor.User, op: opTransition})
	if err != nil {
		// A concurrent commit between the guard above and this one, which the
		// store answers in its own voice for the reason submit.go gives.
		return TransitionResult{}, err
	}
	res.Revision = rev
	res.Stage = img.stageName(to)
	// The actor's roles are unchanged by the move — a transition assigns
	// nothing — so the two stages are the only difference in what §3.5's
	// function is handed, and the answer needs no state.
	res.Affected = img.viewChanges(roles, from, to)
	return res, nil
}
