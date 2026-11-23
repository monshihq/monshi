package monshi

// Review is DESIGN §6.4's decision about content the case already holds:
// approve, reject, or revoke a `verifiable` declaration. It is the third
// operation that commits and the first whose commit says nothing about data —
// which is why so much of this file is about where a decision is *stored*.
//
// A decision is about the committed case, so nothing here reads, writes or
// consults a draft. The roles are folded from the committed snapshot like
// every other authority question (invariant 6), the target path is resolved
// against the committed state rather than the caller's copy, and the review
// table lives beside the snapshot instead of inside it (store.go's comment on
// `committedState` gives the three consequences: `Update` cannot write one,
// `Rebase` has nothing to merge, and a working copy does not carry one).
//
// The guard order is §5.4's and §5.5's, and it is repeated here rather than
// re-derived because the three operations must not disagree about when a
// caller learns what the case is doing. Two questions about the *definition*
// come first — the path, and whether this actor may decide at all — because a
// wrong revision number cannot change either answer, and an unauthorized
// caller must not be consoled with the case's revision traffic (§7's property
// 7). The observed-revision guard comes next, before anything that reads the
// case's decisions, and it is the only refusal that returns a result: naming
// the revision to reload at is the whole content of that one message. After
// the guard come the rules about the decision itself.
//
// What a commit *erases* is §5.6's rule, run at last: a committed content
// change puts its verifiable units back to PENDING, and `resetVerifs` is the
// consumer that indexing was built for — one `slots[n].verif` read per changed
// entry, no walk, no dependency graph. It belongs to the commit and not to a
// read because §4.2 says so ("are part of the commit"), and because a view
// that computed staleness at render time would be answering a different
// question every time the mask changed.
//
// Two things this file does not do, both on the record. `ReviewResult`
// carries no `Affected` set: DESIGN §6.4 promises one for every committing
// operation, and RUNTIME §9 defers the decision to wherever `OnCommit` makes
// it, because a list nobody consumes is a list whose shape is guesswork. And
// the `chgReview` entry a decision writes is not read back by anything yet:
// DESIGN §10's historical replay does not exist, so the snapshot answers what
// a unit's state is, and the entry is written because a history that cannot
// say who decided what is a history milestone 5 would have to rewrite.

import (
	"context"
	"slices"
	"strings"
)

// ReviewState is a verifiable unit's effective review state (DESIGN §4.2).
// The three words are the design document's, and `ReviewPending` is also what
// a unit whose content moved is now: a reset is not a third state, it is the
// absence of a decision.
type ReviewState string

const (
	ReviewPending  ReviewState = "PENDING"
	ReviewApproved ReviewState = "APPROVED"
	ReviewRejected ReviewState = "REJECTED"
)

// ReviewAction is what a `Review` call asks the case to do. The words are
// §4.2's three — approval, rejection with a required comment, revocation —
// and they are deliberately not the three states: `ReviewRevoke` produces
// `ReviewPending`, and a caller who could write a state directly could set
// PENDING over somebody else's decision, which is the one thing revocation is
// restricted away from below.
type ReviewAction string

const (
	ReviewApprove ReviewAction = "APPROVE"
	ReviewReject  ReviewAction = "REJECT"
	ReviewRevoke  ReviewAction = "REVOKE"
)

// ReviewInfo is one node's report about the verifiable unit that governs it
// (DESIGN §6.1's verification state, RUNTIME §5.7). It is a value and not a
// pointer because most nodes have no unit to report, and a zero `ReviewInfo`
// says exactly that: `Unit` empty means no declaration in this actor's view
// governs this node, and `State` says nothing about anything.
//
// `At` is the revision this decision's own commit produced, and not the
// revision of the content approved. The difference is load-bearing: a review
// commit advances the case's number, so a mark that recorded the content
// version would be a statement about a revision that no longer exists as
// "current" the moment the approval lands, and a second reviewer reading it
// would conclude the case had moved when only decisions had. Staleness is
// §5.6's reset's job — content moves, the unit goes back to PENDING — and not
// a comparison of two numbers.
type ReviewInfo struct {
	Unit    string      // the governing verifiable declaration, "" if none
	State   ReviewState // its effective state
	By      UserID      // who decided; empty when pending
	At      uint64      // the revision this decision committed at
	Comment string      // a rejection's reason, and a note beside the rest
}

// ReviewResult is what a review reached. It is the smallest of the committing
// operations' results on purpose: the case's revision and stage are facts the
// caller did not have to ask about and cannot get any other way, and the
// decision itself is something the caller just made.
type ReviewResult struct {
	Revision uint64
	Stage    string
}

// reviewState is the stored half of the same fact, sized for a table with one
// entry per slot. The zero value is PENDING with nobody's name on it, which is
// what lets a case that has never been reviewed hold a nil table: the absent
// entry and the pending entry are the same answer, and `reviewAt` is the only
// place that gets to say so.
type reviewState struct {
	code uint8
	by   UserID
	at   uint64
	note string
}

const (
	verifPending uint8 = iota
	verifApproved
	verifRejected
)

// state renders a stored decision as the word a host compares against.
func (r reviewState) state() ReviewState {
	switch r.code {
	case verifApproved:
		return ReviewApproved
	case verifRejected:
		return ReviewRejected
	}
	return ReviewPending
}

// Review decides a `verifiable` declaration: `ReviewApprove` and
// `ReviewReject` record this actor's judgement of the content the case holds
// now, and `ReviewRevoke` takes back the actor's own decision (§4.2). A
// rejection must say why, because a state with no reason attached is not
// something a second reviewer can act on; a revocation must say nothing,
// because it leaves no decision standing to attach a note to.
//
// `observed` is the committed revision the caller was shown, in the same sense
// as `Transition`'s (§6.4): a decision is about the whole case the reviewer
// read, so any other commit that moved the number is refused even when it
// touched an unrelated form.
//
// Repeating a decision that already stands — same action, same author, same
// comment — is success that commits nothing, for §6.3's reason stated for a
// submit: the caller's request is already true, and a revision that counts
// commits must not count this one. The three facts that make a decision
// *this* one are compared and not merely the state, because a second
// reviewer's approval is a new fact the case did not have: exactly one
// decision is active per unit, so it replaces the first one and says in
// history that somebody else agreed.
func (c Case) Review(ctx context.Context, target string, act ReviewAction,
	observed uint64, comment string) (ReviewResult, error) {
	refuse := c.refusing("review")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return ReviewResult{}, err
	}
	img := d.img

	// Resolution and authority, both asked of the committed state, and the
	// two refusals told apart the way §5.7 tells them apart: a path that does
	// not resolve is only a `NOT_FOUND` when this actor may decide at the node
	// the walk stopped at, because the walk fails at a *name* against the
	// definition and at an instance against somebody's data (§3.9), and the
	// second is a fact about the case.
	roles := img.rolesHeld(c.actor, rec.commit.s)
	r, ok := rec.commit.s.resolve(target)
	granted := img.mayReview(roles, rec.commit.stage, r.slot)
	if !ok || !granted {
		if !ok && granted {
			return ReviewResult{}, refuse.now(NotFound,
				"no path is "+quote(target))
		}
		return ReviewResult{}, refuse.now(AccessDenied,
			"review is not granted at "+quote(target))
	}

	// The named declaration has to be a unit that can be decided, and only the
	// *governing* one: F3 gives a nested `verifiable` declaration an ancestor,
	// and P2's "review targets are verifiable declarations" is the same fact
	// read from the other side. A refusal that named the caller's path alone
	// would leave the host to discover by trial what it can decide, so the
	// unit that answers is named with it.
	unit, sl := r.slot, img.slots[r.slot]
	if sl.verif != unit {
		if sl.verif == 0 {
			return ReviewResult{}, refuse.now(InvalidOp,
				quote(img.path(unit))+" declares nothing verifiable to decide")
		}
		return ReviewResult{}, refuse.now(InvalidOp,
			quote(img.path(unit))+" is not the unit to decide; "+
				quote(img.path(sl.verif))+" governs it")
	}
	// F4's "reviewed as a whole, not per member" read as a refusal: a path
	// through a row addresses one instance, and a host that believed it had
	// approved one holder would be wrong in a way no later answer corrects.
	if r.depth != 0 {
		return ReviewResult{}, refuse.now(InvalidOp,
			quote(target)+" names a row, and a review decides "+
				quote(img.path(unit))+" as a whole")
	}

	if observed != rec.revision {
		res := ReviewResult{Revision: rec.revision,
			Stage: img.stageName(rec.commit.stage)}
		return res, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+
				dec(observed))
	}
	res := ReviewResult{Revision: rec.revision,
		Stage: img.stageName(rec.commit.stage)}

	switch act {
	case ReviewApprove, ReviewReject, ReviewRevoke:
	default:
		return res, refuse.now(InvalidOp,
			quote(string(act))+" is not a review decision")
	}
	note := strings.TrimSpace(comment) == ""
	if act == ReviewReject && note {
		return res, refuse.now(InvalidOp, "a rejection of "+
			quote(img.path(unit))+" must say why")
	}
	if act == ReviewRevoke && !note {
		return res, refuse.now(InvalidOp, "a revocation leaves no decision"+
			" standing, so its comment would be nobody's")
	}

	cur := reviewAt(rec.commit.reviews, unit)
	if act == ReviewRevoke {
		// §4.2's restriction, stated as the two ways it can fail: nothing is
		// decided (so there is nothing to take back), or somebody else decided
		// it (so this actor is not the author). The second is a permission
		// answer and the first is not: `ACCESS_DENIED` here would tell a
		// caller the unit has a decision when it has none, and that is a fact
		// about the case a refusal should not volunteer.
		if cur.code == verifPending {
			return res, refuse.now(InvalidOp, "nothing is decided at "+
				quote(img.path(unit))+" to revoke")
		}
		if cur.by != c.actor.User {
			return res, refuse.now(AccessDenied, "the decision at "+
				quote(img.path(unit))+" is "+quote(string(cur.by))+
				"'s, and only its author may revoke it")
		}
	}

	next := reviewState{}
	if act == ReviewApprove || act == ReviewReject {
		code := verifApproved
		if act == ReviewReject {
			code = verifRejected
		}
		// The revision predicted here is the one `commit` is about to
		// produce, and the store's guard is what makes the prediction exact:
		// it refuses anything that did not arrive at the number this call
		// observed, so a concurrent commit loses rather than mislabels. The
		// alternative — reading the number back — cannot work, because the
		// table is part of the state being committed.
		next = reviewState{code: code, by: c.actor.User,
			at: rec.revision + 1, note: comment}
	}
	if next.code == cur.code && next.by == cur.by && next.note == cur.note {
		return res, nil // the same decision by the same hand: nothing to add
	}

	tbl := reviewTable(img, rec.commit.reviews)
	tbl[unit] = next
	rev, err := c.w.cases.commit(refuse, img, rec.revision,
		committedState{s: rec.commit.s, stage: rec.commit.stage,
			reviews: tbl},
		committedBy{by: c.actor.User, op: opReview,
			changes: []commitChange{{kind: chgReview, slot: unit,
				v: slotValue{set: true, i: int64(next.code),
					s: next.note}}}})
	if err != nil {
		// Reachable only through a concurrent commit, which the store answers
		// in its own voice for the reason submit.go gives. Nothing the caller
		// was told about a revision this call did not reach belongs in the
		// result.
		return ReviewResult{}, err
	}
	res.Revision = rev
	return res, nil
}

// reviewAt reads one unit's decision. An entry past the table's end is
// pending, and that is not a shortcut: the table is allocated to the slot
// count on the first decision anybody makes, so a short table means no
// decision has ever been recorded and every unit is being asked about before
// it has been decided.
func reviewAt(have []reviewState, unit nodeID) reviewState {
	if int(unit) >= len(have) {
		return reviewState{}
	}
	return have[unit]
}

// reviewTable copies a case's stored review table so that it can be replaced,
// sizing the copy to the definition's slots. Sizing to the slots rather than
// to the count of units is what makes `reviewAt`'s index the slot's own number
// and not a lookup, and a case with one verifiable form pays len(slots) words
// for its first decision: dense, because §3.9's rule about what indexes by
// data has no exception here and a map would be the exception.
func reviewTable(img *image, have []reviewState) []reviewState {
	next := make([]reviewState, len(img.slots))
	copy(next, have)
	return next
}

// resetVerifs applies §4.2's rule to the diff a commit is about to write:
// every verifiable unit whose content moved goes back to PENDING, and the
// entries that say so are the ones the commit already made for §5.6's other
// consumers. The mapping is one array read per entry — `slots[n].verif` is the
// governing unit F3 settled at lowering — which is what makes this the effect
// that costs nothing on a commit that changed nothing under a unit.
//
// Two kinds are skipped, and both skips are load-bearing. `chgReview` is not
// content: a commit whose changes are decisions must not reset the unit it
// just decided, which would unmake the act the entry records. A rows entry is
// content like any other — a row appearing or vanishing under a verifiable
// group is the group's content changing — so it is *not* skipped, and its
// slot is the collection's declaration, whose `verif` is the unit that
// governs it.
//
// The table is copied once and only if something resets, so a commit that
// moves content under no verifiable unit hands the stored slice forward
// unchanged, and the store's replace-never-mutate rule is what makes sharing
// it with readers safe in that case.
func resetVerifs(img *image, have []reviewState,
	changes []commitChange) []reviewState {
	var hit []nodeID
	for _, ch := range changes {
		if ch.kind == chgReview {
			continue
		}
		unit := img.slots[ch.slot].verif
		if unit == 0 || reviewAt(have, unit).code == verifPending {
			continue // no unit, or nothing standing that a reset would change
		}
		if !slices.Contains(hit, unit) {
			hit = append(hit, unit)
		}
	}
	if len(hit) == 0 {
		return have
	}
	next := reviewTable(img, have)
	for _, unit := range hit {
		next[unit] = reviewState{}
	}
	return next
}

// pendingUnits lists the verifiable units inside one form that the case has
// not approved: §5.5's second conjunct of a destination's `require`, reported
// as gaps on the same list §5.4's completeness walk produces, because the
// question is already "what does the destination still want".
//
// Only *governing* units are listed (F3), so one decision is named once and a
// rejected unit cannot arrive under two paths. The walk honors §5.2's
// pruning exactly as `incomplete` does: a unit inside a subtree the effective
// state does not reach is not a question the destination can ask.
func pendingUnits(s *state, reviews []reviewState, form nodeID) []gap {
	img := s.img
	var out []gap
	lo, hi := img.subtree(form)
	for n := lo; n < hi; n++ {
		sl := &img.slots[n]
		if sl.flags&flagVerifiable == 0 || sl.verif != n {
			continue
		}
		if !s.present(n) {
			n += nodeID(sl.span) - 1
			continue
		}
		if reviewAt(reviews, n).code == verifApproved {
			continue
		}
		out = append(out, gap{path: img.path(n), kind: gapVerif})
	}
	return out
}

// reviewInfoOf is the node's report: the unit that governs it, and what the
// case has decided about that unit. Values come from the caller's draft and
// this comes from the committed snapshot, which is §6.1's split stated in the
// one place where it is most visible: a form can hold a value somebody is
// still typing beside an APPROVED mark that is about what the case holds, and
// both statements are true because they are about different content.
func reviewInfoOf(img *image, reviews []reviewState, n nodeID) ReviewInfo {
	unit := img.slots[n].verif
	if unit == 0 {
		return ReviewInfo{}
	}
	r := reviewAt(reviews, unit)
	return ReviewInfo{Unit: img.path(unit), State: r.state(), By: r.by,
		At: r.at, Comment: r.note}
}
