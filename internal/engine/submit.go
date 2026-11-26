package engine

import (
	"context"
	"strings"

	"github.com/monshihq/monshi/internal/program"
)

// CommitResult is what a submit reports: the revision the case reached, the
// stage it is still at, and — when nothing was committed — the two lists
// that say why.
//
// `Revision` and `Stage` are filled on every path that returns, refusal
// included, because both are facts the caller did not have to ask about: a host
// that submitted at a stale revision learns from the same value what number to
// reload at. On a successful submit both are empty, and on a no-op submit so is
// the commit.
type CommitResult struct {
	// Revision is the case's committed revision after this call: advanced by
	// one by a commit that wrote something, and unchanged otherwise (the rule
	// that a revision is the count of commits).
	Revision uint64

	// Stage is the stage the case sits at. Submit never moves it (only
	// `accept` and `reject` change state), so the value is the one the case
	// was at — reported because the view carries it and a host refreshes a
	// form header from here.
	Stage string

	// Missing holds the paths the completeness walk found with no value, in
	// declaration order, one entry per thing the form is short of: a field, an
	// instance's member at that instance's address, or a collection with no
	// rows. They are editable paths — each names its instances by their stable
	// IDs — so a client can send the fix straight back to `Update`.
	Missing []string

	// Invalid holds the values the definition's `Validate` hooks refused at
	// this candidate, each with the path it lives at. Empty on a submit that
	// committed.
	Invalid []FieldProblem

	// Affected is the walk over what this commit moved: the submitted
	// entries, the decisions and attestations the effects erased beside
	// them — the facts the diff does not record and history will not show as
	// removals — and, when the commit conferred or withdrew one of the
	// submitter's own roles, the subtrees whose *visibility* moved with it.
	// Declaration-addressed, sorted, prefix-free, and a superset by the same
	// right the batch list claims: a client refreshes these paths after the
	// commit and asks nothing else why. Nil on a submit that committed
	// nothing and on every refusal. What the list cannot answer is which
	// *other* actors a role commit just changed the view of; that question is
	// per reader, and it belongs to the observer seam.
	Affected []string
}

// Submit commits one form of this actor's working copy.
//
// `form` names a top-level form, and `working` is the revision of the copy
// the form lives in — one per `(case, user)`, and distinct from the case's
// committed revision, which is the number this call reads from the store
// rather than the one it is told. A copy that has moved is refused rather
// than merged, and so is a copy that descends from a revision the case no
// longer has: the second is the guard against applying a stale prepared
// diff, and it is what makes a concurrent commit by somebody else a refusal
// instead of a silent overwrite.
//
// Check permission first so an unauthorized caller cannot discover the working
// revision. Then check the draft revision and base before running validation on
// its content.
//
// Nothing is written until the whole list passes, and the write is one call: a
// refused submit leaves the working copy exactly where it was, at the revision
// the caller already held.
func (c Case) Submit(ctx context.Context, form string,
	working uint64) (CommitResult, error) {
	refuse := c.refusing("submit")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return CommitResult{}, err
	}
	img := d.img
	n, err := topForm(img, refuse, form)
	if err != nil {
		return CommitResult{}, err
	}

	// Roles from the committed snapshot. A submit is the operation where
	// getting this wrong would matter most: the draft is exactly the content
	// that assigns a role field, and a submit that read roles from it would let
	// an actor grant themselves the right to publish the grant.
	roles := rolesHeld(img, c.actor, rec.commit.s)
	if !img.MaySubmit(roles, rec.commit.stage, n) {
		return CommitResult{}, refuse.now(AccessDenied,
			"submitting "+quote(form)+" is not granted at stage "+
				quote(img.StageName(rec.commit.stage)))
	}

	w, err := c.w.cases.working(ctx, refuse, c.actor.User, img)
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

	// The snapshot the re-ask is handed. It is the *candidate* rather than the
	// committed state, which is the same frame the requiredness walk reads,
	// and it carries no visibility fold: a submit reports every problem in the
	// subtree it publishes, not the subset of them the actor could render.
	at := c.pinned(img, rec, &w.s, roles, w.rev, nil)

	// The content checks, both over the submitted subtree alone. They run
	// against the *candidate*, which is what the evaluation frame is: a
	// requiredness or a hook asked of the committed state would report a form
	// the caller never presented, and the two states agree inside this subtree
	// by the time the commit grafts it (below), so nothing that reaches history
	// is checked anywhere else.
	res := CommitResult{Revision: rec.revision, Stage: rec.commit.stageName()}
	gaps := w.s.incomplete(n)
	if len(gaps) > 0 {
		res.Missing = gapPaths(gaps)
	}
	problems, err := c.w.validate(ctx, refuse, at, n)
	if err != nil {
		// A broken hook is not a field validation error. Return HOOK_FAILURE
		// without attributing the failure to a value the actor entered.
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
	// form's draft out of it.
	lo, hi := img.Subtree(n)
	next := rec.commit.s.clone()
	next.graft(&w.s, lo, hi)
	// Instance IDs are never reused within a case, so the sequence
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
		// An unchanged form needs no commit. Return the current revision and
		// leave history untouched.
		return res, nil
	}

	out, err := c.w.cases.commit(ctx, refuse, img, rec.revision,
		committedState{s: next, stage: rec.commit.stage,
			reviews: rec.commit.reviews,
			sigs:    rec.commit.sigs},
		committedBy{by: c.actor.User, op: opSubmit, changes: changes})
	if err != nil {
		// Reachable only through a concurrent commit between the guard above
		// and this one. It is reported unchanged, in the store's voice: an
		// operation that retried the check would be claiming a consistency it
		// does not hold the lock for.
		return CommitResult{}, err
	}
	res.Revision = out.rec.revision

	// The affected set runs on the installed state and the erased targets,
	// after the store's yes for the reason update.go gives its own walk: a
	// closure is news about a commit, and a refused one has none to carry.
	// The role term is the one a batch never had — committing a role-typed
	// field moves the function for the actor who committed it, and this
	// is the only operation whose own data can do that.
	fan := newFanout(img)
	seedCommit(fan, out.rec.changes, out.erased)
	if now := rolesHeld(img, c.actor, out.post.s); !roleSetsMatch(roles, now) {
		for _, n := range img.RoleChanges(roles, now, out.post.stage) {
			fan.reask(n)
		}
	}
	res.Affected = fan.affected()
	c.notify(ctx, img, rec, out)
	return res, nil
}

// topForm resolves the name a submit presents to the slot of the top-level
// form it names, refusing anything else with the category that says which
// kind of mistake it was.
//
// Submit accepts a form name. A dotted path is an address for an edit, not a
// misspelled top-level form.
func topForm(img *program.Program, refuse refuser, name string) (program.NodeID, error) {
	if strings.Contains(name, ".") {
		return 0, refuse.now(InvalidOp,
			"a submit names one top-level form, and "+quote(name)+
				" is a path: submit the form that contains it")
	}
	for _, n := range img.FormSlots() {
		if img.Name(img.Slot(n).Name) == name {
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
