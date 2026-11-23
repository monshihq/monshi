package monshi

// The commit path (DESIGN §6.3; RUNTIME §5.4).
//
// A submit is the one operation that publishes content, so it is the one
// place the whole §5.4 list runs: who is allowed to commit, whether the copy
// being committed is the copy the caller is holding, whether the form is
// complete, whether its values still pass the hooks as they are today, and
// whether any of it actually differs from what the case already says.
//
// Four rules hold this together, and each could be broken by a change that
// looks perfectly reasonable:
//
//   - **The caller sends a form and a number, and nothing else.** DESIGN
//     §6.3 is explicit: the server already holds the working data, so no
//     content arrives with a submit and none is trusted if it did. The base
//     revision is therefore *read* from the copy the store has, not passed in
//     — a client that reported its own base would be reporting a fact the
//     store knows better, and §6.3's check would be one the caller chose.
//   - **Authority comes from committed data only**, as in an `Update`: the
//     role set folds from the committed snapshot (§4.3), so the draft being
//     submitted cannot confer the right to submit itself.
//   - **One form's subtree is the whole scope.** Completeness, validation
//     and the diff are limited to the submitted form (§6.3), and the
//     snapshot the commit writes is the *old* one with that subtree replaced
//     rather than the working copy — another form's unsubmitted draft stays
//     unsubmitted. That is why a graft and not a swap is the commit's half
//     of the transaction.
//   - **No content means no commit.** An empty diff is a success that writes
//     nothing (DESIGN §6.3's "no meaningless commit"), and it is reported
//     with the revision it did not move, so a host comparing numbers learns
//     what happened without a second field saying so.
//
// What this file does *not* do is the verification and signature half of
// §5.4: `verif` units are not reset and `sigEntry` invalidations are not
// computed, because nothing can approve or sign a unit until Review and Sign
// exist to do it (DESIGN §10's milestone 5). A submit therefore commits
// content today and will commit resets alongside it then; the diff, the
// history entry and the transaction are already shaped to carry that, since
// a reset is an effect on the same slots.

import (
	"context"
	"strings"
)

// CommitResult is what a submit reports: the revision the case reached, the
// stage it is still at, and — when nothing was committed — the two lists
// that say why.
//
// `Revision` and `Stage` are filled on every path that returns, refusal
// included, because both are facts the caller did not have to ask about: a
// host that submitted at a stale revision learns from the same value what
// number to reload at. `Missing` and `Invalid` are the field-level list
// DESIGN §9 keeps out of the error categories: an incomplete or invalid form
// is refused with INVALID_OPERATION *and* answers with the paths, so a host
// renders the message without parsing one. On a successful submit both are
// empty, and on a no-op submit so is the commit.
type CommitResult struct {
	// Revision is the case's committed revision after this call: advanced by
	// one by a commit that wrote something, and unchanged otherwise (§5.1's
	// rule that a revision is the count of commits).
	Revision uint64

	// Stage is the stage the case sits at. Submit never moves it (§4.4: only
	// `accept` and `reject` change state), so the value is the one the case
	// was at — reported because §6.1's view carries it and a host refreshes a
	// form header from here.
	Stage string

	// Missing holds the paths §5.4's completeness walk found with no value, in
	// declaration order, one entry per thing the form is short of: a field, an
	// instance's member at that instance's address, or a collection with no
	// rows. They are editable paths — each names its instances by their stable
	// IDs — so a client can send the fix straight back to `Update`.
	Missing []string

	// Invalid holds the values the definition's `Validate` hooks refused at
	// this candidate, each with the path it lives at. Empty on a submit that
	// committed.
	Invalid []FieldProblem
}

// Submit commits one form of this actor's working copy (DESIGN §6.3).
//
// `form` names a top-level form, and `working` is the revision of the copy
// the form lives in — one per `(case, user)`, and distinct from the case's
// committed revision, which is the number this call reads from the store
// rather than the one it is told. A copy that has moved is refused rather
// than merged, and so is a copy that descends from a revision the case no
// longer has: the second is §6.3's guard against applying a stale prepared
// diff, and it is what makes a concurrent commit by somebody else a refusal
// instead of a silent overwrite.
//
// Checks run in this order, and the order is a decision rather than an
// accident: permission comes before either revision, so an actor who cannot
// commit this form learns that and nothing about the copy's numbers. The
// alternative reveals the case's revision traffic to a caller the definition
// refuses, and a host cannot un-learn what it was told. RUNTIME §7 does not
// say this — its six items are about containment and bounds, and the one
// written disclosure rule is DESIGN's invariant 7, about `View` — so the
// ordering is this engine's own hardening, held to the same shape in
// transition.go. The remaining order is DESIGN §6.3's own: the copy's
// revision, then the base, then the content —
// a cheap freshness test before an expensive walk, and completeness before
// validation because a value nobody filled in has no hook worth calling.
//
// Nothing is written until the whole list passes, and the write is one call:
// a refused submit leaves the working copy exactly where it was, at the
// revision the caller already held (§9's "either the allowed atomic state
// transition or no persistent effect").
func (c Case) Submit(ctx context.Context, form string,
	working uint64) (CommitResult, error) {
	refuse := c.refusing("submit")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return CommitResult{}, err
	}
	img := d.img
	n, err := img.topForm(refuse, form)
	if err != nil {
		return CommitResult{}, err
	}

	// Roles from the committed snapshot, as §4.3 requires and as update.go
	// states the reason for. A submit is the operation where getting this
	// wrong would matter most: the draft is exactly the content that assigns
	// a role field, and a submit that read roles from it would let an actor
	// grant themselves the right to publish the grant.
	roles := img.rolesHeld(c.actor, rec.commit.s)
	if !img.maySubmit(roles, rec.commit.stage, n) {
		return CommitResult{}, refuse.now(AccessDenied,
			"submitting "+quote(form)+" is not granted at stage "+
				quote(img.stageName(rec.commit.stage)))
	}

	w, err := c.w.cases.working(refuse, c.actor.User, img)
	if err != nil {
		return CommitResult{}, err
	}
	if w.rev != working {
		return CommitResult{}, refuse.staleCopy(w.rev, working)
	}
	if w.base != rec.revision {
		return CommitResult{}, refuse.now(StaleRevision,
			"the working copy descends from revision "+dec(w.base)+
				", the case is at "+dec(rec.revision))
	}

	// §5.4's content checks, both over the submitted subtree alone. They run
	// against the *candidate*, which is what §6.2's evaluation frame is:
	// a requiredness or a hook asked of the committed state would report a
	// form the caller never presented, and the two states agree inside this
	// subtree by the time the commit grafts it (below), so nothing that
	// reaches history is checked anywhere else.
	res := CommitResult{Revision: rec.revision, Stage: rec.commit.stageName()}
	gaps := w.s.incomplete(n)
	if len(gaps) > 0 {
		res.Missing = gapPaths(gaps)
	}
	problems, err := c.w.validate(ctx, refuse, img, &w.s, n, nil)
	if err != nil {
		// A hook that misbehaved rather than refused: HOOK_FAILURE, and no
		// list to render, because the defect is in host code and not in any
		// field the actor can fix. §6's rule, stated for hooks as §9's is for
		// categories.
		return CommitResult{}, err
	}
	res.Invalid = problems
	if len(res.Missing) > 0 || len(res.Invalid) > 0 {
		return res, refuse.now(InvalidOp,
			notSubmittable(quote(form), res.Missing, res.Invalid))
	}

	// The content that commits: the committed snapshot with this form's
	// subtree replaced by the candidate's. Cloning first is what keeps
	// store.go's replace-never-mutate rule true of the snapshot a reader may
	// be holding, and grafting rather than swapping is what keeps another
	// form's draft out of it (§6.3).
	lo, hi := img.subtree(n)
	next := rec.commit.s.clone()
	next.graft(&w.s, lo, hi)
	// Instance IDs are never reused within a case (§5.1), so the sequence
	// advances with the content the candidate brought. Taking the larger of
	// the two rather than the candidate's alone is the honest form of the
	// rule: the guard above makes the candidate descend from this snapshot,
	// so the two can only differ one way today, and a commit path that
	// trusted that would be one rebase away from reissuing an ID a host has
	// a saved path for.
	if next.seq < w.s.seq {
		next.seq = w.s.seq
	}

	changes := diffForm(&rec.commit.s, &next, n)
	if len(changes) == 0 {
		// §6.3's no meaningless commit, and §5.1's consequence: a revision is
		// the count of commits, so a submit that changed nothing leaves the
		// number, the history and every other actor's staleness exactly as
		// they were. Success rather than a refusal, because the caller's
		// request — "this form is committed" — is now true.
		return res, nil
	}

	rev, err := c.w.cases.commit(refuse, rec.revision,
		committedState{s: next, stage: rec.commit.stage},
		committedBy{by: c.actor.User, op: opSubmit, changes: changes})
	if err != nil {
		// Reachable only through a concurrent commit between the guard above
		// and this one, which is the case §6.3's "reject rather than apply a
		// stale prepared diff" is written for. It is reported unchanged, in
		// the store's voice: an operation that retried the check would be
		// claiming a consistency it does not hold the lock for.
		return CommitResult{}, err
	}
	res.Revision = rev
	return res, nil
}

// topForm resolves the name a submit presents to the slot of the top-level
// form it names, refusing anything else with the category that says which
// kind of mistake it was.
//
// SYNTAX P3 confines a `submit` grant to a top-level form, so this is a
// lookup in the definition's form list and not a path walk: a name containing
// a dot is a *path*, which is what §6.2's edits take, and the two are the
// commonest confusion in an API that has both. Telling them apart is worth a
// branch, because the host's fix differs — one is a typo, the other is the
// wrong operation.
func (img *image) topForm(refuse refuser, name string) (nodeID, error) {
	if strings.Contains(name, ".") {
		return 0, refuse.now(InvalidOp,
			"a submit names one top-level form, and "+quote(name)+
				" is a path: submit the form that contains it")
	}
	for _, n := range img.forms {
		if img.name(img.slots[n].name) == name {
			return n, nil
		}
	}
	return 0, refuse.now(NotFound, "no form is named "+quote(name))
}

// notSubmittable is the one sentence a refused submit carries beside its
// lists: how many of each kind, and nothing about which, because `Missing`
// and `Invalid` answer that and a detail that repeated them would only be a
// second place to be wrong.
func notSubmittable(form string, missing []string, invalid []FieldProblem) string {
	var parts []string
	if n := len(missing); n > 0 {
		parts = append(parts, count(n, "required field", "required fields")+
			" with no value")
	}
	if n := len(invalid); n > 0 {
		parts = append(parts, count(n, "value", "values")+
			" its hooks refused")
	}
	return form + " cannot be committed: " + strings.Join(parts, ", ")
}

// count renders a length as a count and a noun, for the two nouns a submit
// refusal has. The plural is spelled out rather than formed because the
// words here are a host's UI copy in embryo and a rule for adding an "s" is
// a rule about English rather than about cases.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return dec(uint64(n)) + " " + many
}
