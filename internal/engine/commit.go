package engine

import (
	"slices"
	"time"

	"github.com/monshihq/monshi/internal/program"
)

// committedBy is what a committing operation tells the store about itself and
// the content it is publishing: who acted, which operation, and the forward
// diff that says what the commit changed. The store supplies the timestamp and
// the revision (§3.1's split, applied to history: the caller's words are data,
// the numbering is the store's).
type committedBy struct {
	by      UserID
	op      commitOp
	changes []commitChange
}

// commitEffect is §5.6's rule about what a commit *erases*, stated once as a
// shape: something a commit's diff removes from the tables that sit beside the
// content, because the content they speak about moved. The language has two —
// the verification reset review.go writes and the signature invalidation
// sign.go writes — and both are the same three questions, which is why the
// store runs them rather than each operation remembering them.
//
// `reach` asks what one diff entry removes, in this effect's terms: it maps the
// entry to the slots whose fact it erases and adds those not already counted,
// consulting the state as the effects have left it so far. Answering it is one
// or two array reads and no walk, which is the whole reason §3.2's lowering
// stamps `verif` and the owning `form` onto the slot: "what stops being true
// when this moved" is a question the definition can answer before any case
// exists. An effect that says nothing about an entry says nothing about the
// commit, and a commit that reaches nothing copies nothing — the property §8
// prices, and the reason `reach` is told whether the fact is standing at all.
//
// `open` then makes the table the removals write into, and `erase` removes one
// fact by naming its slot. The pair is separated on purpose: copying is a
// per-commit decision and clearing is a per-fact one, so a diff that moves the
// same form twice pays for one table rather than two, and an effect with
// nothing to remove hands the stored slice forward untouched.
type commitEffect interface {
	reach(img *program.Program, next *committedState, ch commitChange,
		hits []program.NodeID) []program.NodeID
	open(img *program.Program, next *committedState)
	erase(next *committedState, target program.NodeID)
}

// commitEffects are the effects every commit pays for. That there is one list
// and not one per operation is the point: a `Submit` that erased verifications
// but not signatures would leave a case whose attestation outlived the content
// it attested to, and no read of the state could tell that had happened. The
// store runs the list and no caller hands it one, which is what makes §5.6's
// rules a rule rather than a convention; the driver below takes the list as a
// parameter so that a test can ask one effect to do one thing.
var commitEffects = []commitEffect{verifReset{}, sigReset{}}

// applyCommitEffects runs the effects over the diff a commit is writing, on the
// snapshot the commit is about to install. It mutates `next` in place: `next` is
// the caller's own copy of the case's state, and the replace-never-mutate rule
// is kept by `open`, which hands back a fresh table and never edits the stored
// one.
//
// It answers with everything it erased. §5.6's effects remove facts the diff
// does not record — a reset writes no entry beside the content that caused it —
// and §5.3's walk is what turns those removals into paths a client can re-ask
// about, so the caller of a content commit needs the list even though history
// does not. The two effects' targets can name the same slot only by the
// caller's seed doing it twice, which is what a mark array is for.
func applyCommitEffects(img *program.Program, next *committedState,
	changes []commitChange, effects []commitEffect) []program.NodeID {
	var erased []program.NodeID
	for _, e := range effects {
		var hits []program.NodeID
		for _, ch := range changes {
			if ch.kind != chgValue && ch.kind != chgRows {
				// Not content. §5.1 counts a decision and a signature among
				// the semantic committed changes, and both are recorded, but
				// neither is the kind of change §5.6's rules are stated about:
				// a commit whose changes are verdicts erases no verdict.
				// Asking the kinds positively here is what lets an effect skip
				// the question, and what makes a kind history learns to record
				// later safe — an effect asked about a kind it does not
				// understand would be erasing a fact from an entry about
				// something else.
				continue
			}
			hits = e.reach(img, next, ch, hits)
		}
		if len(hits) == 0 {
			continue
		}
		e.open(img, next)
		for _, n := range hits {
			e.erase(next, n)
		}
		erased = append(erased, hits...)
	}
	return erased
}

// addHit collects a removal target the caller has not counted yet. Both effects
// need the dedup — a diff that moves three fields under one unit removes one
// fact, and a rebase that commits four fields of one form invalidates that
// form's signature once — and the lists are the size of a form's declarations,
// so a linear scan is the cheap answer and a set would be the expensive one.
func addHit(hits []program.NodeID, n program.NodeID) []program.NodeID {
	if !slices.Contains(hits, n) {
		hits = append(hits, n)
	}
	return hits
}

// committed is what a successful commit hands back: the entry the store
// appended and the snapshot it installed, both by value. The operations need
// the pair because §5.6's effects run here and not in the caller — a submit
// cannot know from its own `next` whether the commit erased a decision — and
// §6's observer contract says the notification carries *that* state, the one
// the commit left, at the number the commit produced. Handing values out is
// safe for the standing reason (the store aliases, never mutates): `post`'s
// state and tables are replaced by the next commit and never written through
// this copy, and `rec.changes` is the caller's own diff.
type committed struct {
	rec  commitRecord
	post committedState
	copy uint64 // the committer's own copy number, as this commit left it

	// erased is what §5.6's effects removed on the way in: the units whose
	// decisions the commit invalidated and the fields whose attestations it
	// undid. No entry says any of it — the state keeps only what stands, and
	// the trail is the account of what stood — so the commit result's
	// affected set (§5.3, §5.4) is where a caller learns of it.
	erased []program.NodeID
}

// prepareCommit computes the same history entry and content invalidations for
// both backends. The caller owns the candidate; publication and the revision
// guard are one atomic backend operation.
func prepareCommit(img *program.Program, head *committedState, revision uint64, at time.Time, by committedBy) (commitRecord, []program.NodeID) {
	erased := applyCommitEffects(img, head, by.changes, commitEffects)
	return commitRecord{revision: revision, at: at, by: by.by, op: by.op, stage: head.stage, changes: by.changes}, erased
}
