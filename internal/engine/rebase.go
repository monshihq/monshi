package engine

import "context"

// RebaseResult is what a rebase says about the working copy afterwards.
type RebaseResult struct {
	// Working is the copy's revision after the rebase. A rebase that merged
	// anything writes the copy, so the number moves — and it must: a second
	// session holding the old one would otherwise be able to save a batch
	// built from the *unsynchronized* draft over the merge, which is
	// invariant 4's failure with a nicer name. It is the value the caller's
	// next `Update` presents as `base`, exactly as §6.2 says.
	Working uint64

	// Base is the committed revision the copy now descends from: the case's
	// own revision, which is the number the copy did not have before and the
	// one §5.4's submit guard reads from the store. It is reported because the
	// question after a rebase is always "can I submit now", and the honest
	// answer is a number rather than a second read.
	Base uint64

	// Conflicts names, in the order the merge found them, the things both the
	// draft and the newer commits changed, at the addresses §5.1 gives them.
	// The list arrives beside a `CONFLICT` refusal and never beside a success:
	// an empty list is a success's whole answer, and a host that has to ask
	// whether a success still had work left is reading two sources of truth.
	Conflicts []string
}

// Rebase pulls the newer committed content into this actor's working copy,
// keeping the edits the copy holds (DESIGN §6.5). It takes no revision number
// because it guards nothing the caller could know better: the copy's own base
// is where the comparison starts and the case's current revision is where it
// ends, both read from the store that owns them.
//
// An actor with no copy is already synchronized — a copy is cloned from the
// committed state when it is first made (§5.7's `working` creates it from the
// snapshot the case is at), so the absence of one means there is nothing to
// bring forward, and a copy whose base is the case's revision needs no
// merge. Both answer success with nothing written, because §6.5's
// "synchronization is mandatory before a subsequent submit" is a claim
// about the state a submit will find and not about the work
// this call had to do. Neither makes a copy either: a read that allocated
// per-user state would be `viewed`'s problem in §5.7's guise, and a rebase has
// the same shape.
func (c Case) Rebase(ctx context.Context) (RebaseResult, error) {
	refuse := c.refusing("rebase")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return RebaseResult{}, err
	}
	img := d.img

	w, have, err := c.w.cases.draft(ctx, refuse, c.actor.User, img)
	if err != nil {
		return RebaseResult{}, err
	}
	if !have {
		return RebaseResult{Base: rec.revision}, nil
	}
	if w.base == rec.revision {
		return RebaseResult{Working: w.rev, Base: w.base}, nil
	}

	// The commits this copy has not seen, and the ones it has: history is
	// dense (§5.6) — the entry at index i is the commit that produced
	// revision i+1 — so a slice split at the copy's base is the whole of what
	// the merge reads, in both directions. A rebase that asked the question
	// any other way would be re-deriving what the store already recorded.
	// The trail is asked through the seam rather than read off the loaded
	// record because the exported contract's `Load` answers a case, not its
	// log: a durable store should not pay for a trail this call may not need
	// (the draft in front of it may already be current), and `history` is
	// the same read `History` exports.
	if w.base > rec.revision {
		return RebaseResult{}, refuse.now(StaleRevision, "the draft advanced beyond the observed case revision")
	}
	all, err := c.historyThrough(ctx, refuse, 1, rec.revision, 0)
	if err != nil {
		return RebaseResult{}, err
	}
	split := w.base // historyThrough verified the dense, one-based sequence.

	// Roles from the committed snapshot, as every operation folds them (§4.3),
	// and used here for the mask alone: a rebase grants nothing and refuses
	// nothing on authority, because it writes only the caller's own draft.
	roles := rolesHeld(img, c.actor, rec.commit.s)
	m := rebaser{img: img, vis: viewable(img, roles, rec.commit.stage)}
	m.run(&rec.commit.s, all[:split], all[split:], &w)

	if len(m.clashes) > 0 {
		// Nothing is saved, which is the whole of §6.5's "a failed/conflicting
		// rebase leaves the previous working state intact" and the reason the
		// merge runs on a clone at all. `Base` stays the copy's own: the
		// caller is no more synchronized than it was, and a result implying
		// otherwise would be a lie about the one number this operation exists
		// to fix.
		paths, veiled := m.report()
		out := RebaseResult{Working: w.rev, Base: w.base, Conflicts: paths}
		return out, refuse.now(Conflict, rebaseRefusal(paths, veiled))
	}

	// `base` and `rev` are the store's to set, so the state handed over
	// carries only the merged data (§6.2's division); `rec.revision` is the
	// lineage this call exists to move.
	rev, err := c.w.cases.saveWorking(ctx, refuse, c.actor.User, w.rev,
		workingState{s: m.out}, rec.revision)
	if err != nil {
		return RebaseResult{}, err
	}
	return RebaseResult{Working: rev, Base: rec.revision}, nil
}

// Discard drops this actor's working copy (DESIGN §6.5). `expected` is the
// working revision the caller observed, and it is not decoration: the copy
// this call destroys is one another session of the same actor may have written
// a moment ago, and an unguarded drop would be this package deleting work it
// was never shown.
//
// The copy is removed and not reset, which is what makes §6.5's "a new working
// copy starts from the latest committed state on next access" true by
// construction: `working` clones from the committed snapshot it finds, so the
// next edit gets a draft of the case as it is now with no residue of what was
// thrown away.
//
// Nothing is committed and nothing is remembered. Invariant 2 counts commits,
// and a discarded draft was never content, so the case's revision is exactly
// where it was — which is the difference between this operation and `Submit`,
// which ends with a number that moved.
func (c Case) Discard(ctx context.Context, expected uint64) error {
	refuse := c.refusing("discard")
	if _, _, err := c.look(ctx, refuse); err != nil {
		return err
	}
	return c.w.cases.discard(ctx, refuse, c.actor.User, expected)
}
