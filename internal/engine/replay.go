package engine

import "github.com/monshihq/monshi/internal/program"

// replayTo derives the committed state one case's history describes at
// revision `to`, given the entries in order and the definition they were
// written under. `to` of 0 is the case as created; a `to` past the head
// returns the head's state, since there is no other answer for a revision that
// never happened.
func replayTo(img *program.Program, hist []commitRecord, to uint64) committedState {
	cs := committedState{s: newState(img), stage: img.InitialStage()}
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
	if ch.decision == verifPending {
		return reviewState{}
	}
	return reviewState{code: ch.decision, by: rec.by,
		at: rec.revision, note: ch.note}
}

// derivedSignature is the state entry one recorded attestation stands for, and
// it needs no such qualification as `derivedDecision` does: there is no
// revocation of a signature (§5.10's reason), so every entry means the same
// thing — this hand, this reference, this content — and the only way a signature
// leaves the state is §4.2 erasing it, which the driver above does and this
// function never has to know about.
func derivedSignature(rec commitRecord, ch commitChange) sigState {
	return sigState{by: rec.by, at: rec.revision, ref: ch.note}
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
func (s *state) restore(coll program.NodeID, path instPath, ids []instanceID) {
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
		row := instRow{id: id, row: make(program.Row, s.img.RowLen(coll))}
		if k, had := set.find(id); had {
			row = set.rows[k] // kept rows keep their content (§5.1's rule)
		}
		index[id] = len(next)
		next = append(next, row)
	}
	set.rows, set.index = next, index
}
