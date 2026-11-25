package engine

import (
	"context"
	"strings"

	"github.com/monshihq/monshi/internal/program"
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
//
// `Affected` is the unit's own path (§5.3's collapse of a re-asked subtree):
// a decision lands *on* the unit, the view renders it there, and a client
// that refreshes the unit refreshes every field the verdict speaks about.
// Nil on a no-op decision and on every refusal — the list follows the commit.
type ReviewResult struct {
	Revision uint64
	Stage    string
	Affected []string
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
	roles := rolesHeld(img, c.actor, rec.commit.s)
	r, ok := rec.commit.s.resolve(target)
	granted := img.MayReview(roles, rec.commit.stage, r.slot)
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
	unit, sl := r.slot, img.Slot(r.slot)
	if sl.Verif != unit {
		if sl.Verif == 0 {
			return ReviewResult{}, refuse.now(InvalidOp,
				quote(img.Path(unit))+" declares nothing verifiable to decide")
		}
		return ReviewResult{}, refuse.now(InvalidOp,
			quote(img.Path(unit))+" is not the unit to decide; "+
				quote(img.Path(sl.Verif))+" governs it")
	}
	// F4's "reviewed as a whole, not per member" read as a refusal: a path
	// through a row addresses one instance, and a host that believed it had
	// approved one holder would be wrong in a way no later answer corrects.
	if r.depth != 0 {
		return ReviewResult{}, refuse.now(InvalidOp,
			quote(target)+" names a row, and a review decides "+
				quote(img.Path(unit))+" as a whole")
	}

	if observed != rec.revision {
		res := ReviewResult{Revision: rec.revision,
			Stage: img.StageName(rec.commit.stage)}
		return res, refuse.now(StaleRevision,
			"the case is at revision "+dec(rec.revision)+", not "+
				dec(observed))
	}
	res := ReviewResult{Revision: rec.revision,
		Stage: img.StageName(rec.commit.stage)}

	switch act {
	case ReviewApprove, ReviewReject, ReviewRevoke:
	default:
		return res, refuse.now(InvalidOp,
			quote(string(act))+" is not a review decision")
	}
	note := strings.TrimSpace(comment) == ""
	if act == ReviewReject && note {
		return res, refuse.now(InvalidOp, "a rejection of "+
			quote(img.Path(unit))+" must say why")
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
				quote(img.Path(unit))+" to revoke")
		}
		if cur.by != c.actor.User {
			return res, refuse.now(AccessDenied, "the decision at "+
				quote(img.Path(unit))+" is "+quote(string(cur.by))+
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
	out, err := c.w.cases.commit(ctx, refuse, img, rec.revision,
		committedState{s: rec.commit.s, stage: rec.commit.stage,
			reviews: tbl, sigs: rec.commit.sigs},
		committedBy{by: c.actor.User, op: opReview,
			changes: []commitChange{{kind: chgReview, slot: unit,
				decision: next.code, note: next.note}}})
	if err != nil {
		// Reachable only through a concurrent commit, which the store answers
		// in its own voice for the reason submit.go gives. Nothing the caller
		// was told about a revision this call did not reach belongs in the
		// result.
		return ReviewResult{}, err
	}
	res.Revision = out.rec.revision
	// One entry seeds the walk, and the walk is the same rule as §5.4's: the
	// verdict re-asks its unit, and whatever a guard hangs off the unit's own
	// path rides along with it. No content moved, so no effect erased a fact
	// and no role set can have changed; the empty terms cost nothing.
	fan := newFanout(img)
	seedCommit(fan, out.rec.changes, out.erased)
	res.Affected = fan.affected()
	c.notify(ctx, img, rec, out)
	return res, nil
}

// reviewAt reads one unit's decision. An entry past the table's end is
// pending, and that is not a shortcut: the table is allocated to the slot
// count on the first decision anybody makes, so a short table means no
// decision has ever been recorded and every unit is being asked about before
// it has been decided.
func reviewAt(have []reviewState, unit program.NodeID) reviewState {
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
func reviewTable(img *program.Program, have []reviewState) []reviewState {
	next := make([]reviewState, img.SlotCount())
	copy(next, have)
	return next
}

// verifReset is §4.2's rule to the first of the two things that are not
// content: a committed content change puts every verifiable unit whose content
// moved back to PENDING. The mapping is one array read per entry —
// `slots[n].verif` is the governing unit F3 settled at lowering — which is what
// makes this the effect that costs nothing on a commit that changed nothing
// under a unit.
//
// Two kinds of entry are told apart, and the telling is the store's: §5.6's
// driver skips the entries that are not content before `reach` is asked, so a
// decision that reset its own unit cannot happen and neither can a signature
// that invalidated the content it stands on. A rows entry *is* content: a row
// appearing or vanishing under a verifiable group is that group's content
// changing, and its slot's `verif` is the unit that governs it.
type verifReset struct{}

// reach counts the unit one entry moves, and only if the unit has a decision
// standing: a commit that lands in a case nobody has reviewed has nothing to
// erase, and §5.6's rule must not become an allocation for the sake of writing
// zeroes over zeroes.
func (verifReset) reach(img *program.Program, next *committedState, ch commitChange,
	hits []program.NodeID) []program.NodeID {
	unit := img.Slot(ch.slot).Verif
	if unit == 0 || reviewAt(next.reviews, unit).code == verifPending {
		return hits // no unit, or nothing standing that a reset would change
	}
	return addHit(hits, unit)
}

// open sizes the writable table to the definition's slots, which is what makes
// `erase`'s index the unit's own number and not a lookup.
func (verifReset) open(img *program.Program, next *committedState) {
	next.reviews = reviewTable(img, next.reviews)
}

// erase is the reset: the zero value, which is PENDING with nobody's name on
// it. The decision itself is not rewritten anywhere else, because §5.9 keeps
// resets out of history — the entry that says who decided survives its own
// erasure, and replaying the entries between it and now is what says whether it
// still stands.
func (verifReset) erase(next *committedState, unit program.NodeID) {
	next.reviews[unit] = reviewState{}
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
func pendingUnits(s *state, reviews []reviewState, form program.NodeID) []gap {
	img := s.img
	var out []gap
	lo, hi := img.Subtree(form)
	for n := lo; n < hi; n++ {
		sl := img.Slot(n)
		if sl.Flags&program.FlagVerifiable == 0 || sl.Verif != n {
			continue
		}
		if !s.present(n) {
			n += program.NodeID(sl.Span) - 1
			continue
		}
		if reviewAt(reviews, n).code == verifApproved {
			continue
		}
		out = append(out, gap{path: img.Path(n), kind: gapVerif})
	}
	return out
}

// reviewInfoOf is the node's report: the unit that governs it, and what the
// case has decided about that unit. Values come from the caller's draft and
// this comes from the committed snapshot, which is §6.1's split stated in the
// one place where it is most visible: a form can hold a value somebody is
// still typing beside an APPROVED mark that is about what the case holds, and
// both statements are true because they are about different content.
func reviewInfoOf(img *program.Program, reviews []reviewState, n program.NodeID) ReviewInfo {
	unit := img.Slot(n).Verif
	if unit == 0 {
		return ReviewInfo{}
	}
	r := reviewAt(reviews, unit)
	return ReviewInfo{Unit: img.Path(unit), State: r.state(), By: r.by,
		At: r.at, Comment: r.note}
}
