package monshi

// The derivation (RUNTIME §5.11; DESIGN §5.2, §10's historical replay).
//
// DESIGN §5.1 says a commit records new values and never old ones, and §5's
// model is therefore a linear sequence of forward diffs replayed onto an empty
// case rather than a stack of reversible patches. This file is that claim
// executed: everything else in the package *assumes* it. §4.2's erasures say a
// reset is not recorded because a replay derives it; §5.6's verdict entries say
// they exist because history is where an erased decision and an invalidated
// attestation survive; §5.9 and §5.10 each decline a `Stale` flag on the same
// argument — that the state erases what moved and the trail keeps the account.
// None of those sentences is checkable until something folds the entries back
// into a state, so this is the slice that makes them claims rather than hopes.
//
// The fold is deliberately not a second interpreter. It writes each entry's
// payload into a state, then hands the entry list to `applyCommitEffects` — the
// same driver, with the same effects list, that every real commit ran. That is
// what makes a derived state agree with a stored one by construction instead of
// by two implementations agreeing: a rule §4.2 adds to a commit is a rule the
// replay inherits the same day, and a replay that forgot an effect would fail
// the equivalence test rather than silently disagree with the present.
//
// Three things the fold needs and one it cannot have:
//
//   - The base is the *created* case, not the empty one. Creating a case
//     materializes its declared defaults (frame.go says why: an undefaults case
//     is not an empty case), and a default nobody overwrote never appears in an
//     entry. So replay starts where `create` started, from the definition alone
//     — which also fixes the honest limit of the derivation, stated below.
//   - The stage comes from the record and is never derived. A transition commit
//     carries no content at all, so the only place "revision 7 was at
//     review" lives is the field store.go records it in. Replaying entries
//     without that field lands a case at its initial stage with all the right
//     values, which is the failure this note exists to prevent.
//   - Rows are restored before the values inside them, which the diff already
//     guarantees: `differ.set` appends a collection's `chgRows` entry and only
//     then walks the rows, and preorder puts an outer collection before an
//     inner one. So a fold never needs an entry it has not been given, and an
//     entry whose address the fold does not hold is a removal of a row that is
//     already gone — skipped, not failed.
//   - The instance counter cannot be recovered. §5.1 forbids reusing an ID, and
//     a draft that added a row and removed it before submitting advanced the
//     counter without changing any row set, so no entry says it happened
//     (submit.go takes the larger of the two counters for exactly that reason).
//     Replay therefore tracks the largest ID an entry *listed*: a derived state
//     never reissues an address a host may be holding, and it can sit behind a
//     case whose counter moved in an unrecorded way. Nothing here commits a
//     derived state, so the gap is inert today; it is the second reason §5.11's
//     exported read is a trail and not a state, and it is recorded rather than
//     smoothed over because a host that ever writes a replayed state back is a
//     host that deserves to have been told.
//
// What the derivation is *not* is a re-execution of the checks. DESIGN §5.2 is
// explicit that replay never reruns hooks or revalidates old commits, §7 states
// it for this package, and §5.4's no-hooks-on-a-transition rule gives the
// reason: a hook is host code, and host code that answers differently last
// quarter is a different answer rather than a different case. So nothing in
// `replayTo` can fail, take a context, or call a hook.

// replayTo derives the committed state one case's history describes at
// revision `to`, given the entries in order and the definition they were
// written under. `to` of 0 is the case as created; a `to` past the head
// returns the head's state, since there is no other answer for a revision that
// never happened.
func replayTo(img *image, hist []commitRecord, to uint64) committedState {
	cs := committedState{s: newState(img), stage: img.initial}
	cs.s.applyDefaults()
	for _, rec := range hist {
		if rec.revision > to {
			break // the trail is dense and ordered, so nothing later matters
		}
		for _, ch := range rec.changes {
			switch ch.kind {
			case chgValue:
				// The cell, at the address the entry names — or nowhere, when
				// the row it lived in was removed by the same commit. A payload
				// that is unset writes an unset cell, which is §5.1's `Empty`
				// effect and the reason a removal is a change and not an
				// absence of one.
				if c, ok := cs.s.cellAt(ch.slot, ch.path); ok {
					c.set(ch.v)
				}
			case chgRows:
				cs.s.restore(ch.slot, ch.path, ch.ids)
			case chgReview:
				if cs.reviews == nil {
					cs.reviews = reviewTable(img, nil)
				}
				cs.reviews[ch.slot] = derivedDecision(rec, ch)
			case chgSign:
				if cs.sigs == nil {
					cs.sigs = signTable(img, nil)
				}
				cs.sigs[ch.slot] = derivedSignature(rec, ch)
			}
		}
		// §4.2's erasures, run by their real driver. A record writes content or
		// a verdict and never both — Submit and its diff are the content
		// commits, `Review` and `Sign` each write exactly one entry of their own
		// kind, and `Transition` writes none — so this call either does the
		// erasing a content commit did or says nothing, exactly as it did the
		// first time.
		applyCommitEffects(img, &cs, rec.changes, commitEffects)
		cs.stage = rec.stage
	}
	return cs
}

// derivedDecision is the state entry one recorded decision stands for: the verdict
// the commit produced, by the hand the record names, at the revision the record
// is. `at` is not stored in the entry and needs not be — a commit's own number
// is the number `Review` predicted for it, which is what §5.9's "the revision
// this decision committed at" rests on, and the equivalence test is the proof.
//
// A revoked decision derives to the zero state and not to a pending mark by the
// revoker: the table answers who stands behind a *standing* verdict, and the
// revoker is history's to name. That is the same asymmetry §5.9 states about a
// reset — the state keeps the fact, the trail keeps the account — reached here
// from the other direction, by a caller who chose to write an entry for a
// retraction rather than erase one.
func derivedDecision(rec commitRecord, ch commitChange) reviewState {
	if uint8(ch.v.i) == verifPending {
		return reviewState{}
	}
	return reviewState{code: uint8(ch.v.i), by: rec.by,
		at: rec.revision, note: ch.v.s}
}

// derivedSignature is the state entry one recorded attestation stands for, and
// it needs no such qualification as `derivedDecision` does: there is no
// revocation of a signature (§5.10's reason), so every entry means the same
// thing — this hand, this reference, this content — and the only way a signature
// leaves the state is §4.2 erasing it, which the driver above does and this
// function never has to know about.
func derivedSignature(rec commitRecord, ch commitChange) sigState {
	return sigState{by: rec.by, at: rec.revision, ref: ch.v.s}
}

// restore puts one collection's rows at the address a `chgRows` entry names, so
// that the set is exactly the IDs the entry lists and in that order. A row the
// case still has keeps its values: an entry says which instances exist and never
// what is inside one that went on existing, because the values that moved are
// entries of their own.
//
// A row that does not exist yet is created *empty*, and that is the load-bearing
// choice. `addAt` materializes a new row's declared defaults, and the diff for
// the same commit emits an entry for every one of them — `img.same` compares a
// value's presence first, so a default in a row the committed state had no
// address for is a difference (value.go). So the values a derived row must hold
// are all coming, and creating the row empty rather than defaulting it keeps the
// fold from applying a definition's defaults twice: once here and once through
// the entries that recorded them.
//
// The counter moves before anything else does, including in the two cases that
// write no rows. §5.1 forbids reusing an instance ID, and an ID an entry *named*
// was issued — including one that lived inside a row the same commit removed. A
// fold that forgot those would answer a later `Add` with an address some host may
// still be holding from an older page of the same trail, which is the one mistake a
// derived state could make that no read can undo.
func (s *state) restore(coll nodeID, path instPath, ids []instanceID) {
	for _, id := range ids {
		if id > s.seq {
			s.seq = id // the largest address ever named, never one reused
		}
	}
	set, ok := s.rowsBelow(coll, path, len(ids) > 0)
	if !ok || set == nil {
		// Either the address is not data — the outer instance was removed, and the
		// rows that lived inside it went with it, which is the answer and not a
		// failure to record one — or it is a valid address whose set was never
		// made, and an empty list asks for nothing. Both write nothing, and
		// neither is a reason to stop folding.
		return
	}
	next := make([]instRow, 0, len(ids))
	index := make(map[instanceID]int, len(ids))
	for _, id := range ids {
		row := instRow{id: id, row: make(stateView, s.img.rowLen(coll))}
		if k, had := set.find(id); had {
			row = set.rows[k] // kept rows keep their content (§5.1's rule)
		}
		index[id] = len(next)
		next = append(next, row)
	}
	set.rows, set.index = next, index
}
