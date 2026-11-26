package engine

import "context"

// RebaseResult is what a rebase says about the working copy afterwards.
type RebaseResult struct {
	// Working is the copy's revision after the rebase. A rebase that merged
	// anything writes the copy, so the number moves — and it must: a second
	// session holding the old one would otherwise be able to save a batch built
	// from the *unsynchronized* draft over the merge.
	Working uint64

	// Base is the committed revision the copy now descends from: the case's
	// own revision, which is the number the copy did not have before and the
	// one the submit guard reads from the store. It is reported because the
	// question after a rebase is always "can I submit now", and the honest
	// answer is a number rather than a second read.
	Base uint64

	// Conflicts names, in the order the merge found them. The list arrives
	// beside a `CONFLICT` refusal and never beside a success: an empty list is
	// a success's whole answer, and a host that has to ask whether a success
	// still had work left is reading two sources of truth.
	Conflicts []string
}

// Rebase pulls the newer committed content into this actor's working copy,
// keeping the edits the copy holds. It takes no revision number
// because it guards nothing the caller could know better: the copy's own base
// is where the comparison starts and the case's current revision is where it
// ends, both read from the store that owns them.
//
// With no draft, or with one already based on the observed revision, there is
// nothing to merge. Neither case writes a working copy.
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

	// Stop history at the head whose content and permissions we read above. A
	// newer commit belongs to the next rebase. The trail is dense, so the
	// draft's base splits the prefix it saw from the changes it must merge.
	if w.base > rec.revision {
		return RebaseResult{}, refuse.now(StaleRevision, "the draft advanced beyond the observed case revision")
	}
	all, err := c.historyThrough(ctx, refuse, 1, rec.revision, 0)
	if err != nil {
		return RebaseResult{}, err
	}
	split := w.base // historyThrough verified the dense, one-based sequence.

	// Use committed roles to mask conflict paths. Rebase writes only this
	// actor's draft.
	roles := rolesHeld(img, c.actor, rec.commit.s)
	m := rebaser{img: img, vis: viewable(img, roles, rec.commit.stage)}
	m.run(&rec.commit.s, all[:split], all[split:], &w)

	if len(m.clashes) > 0 {
		// On conflict, leave the original draft intact. Report its existing
		// base and reveal only the paths the actor may see.
		paths, veiled := m.report()
		out := RebaseResult{Working: w.rev, Base: w.base, Conflicts: paths}
		return out, refuse.now(Conflict, rebaseRefusal(paths, veiled))
	}

	// `base` and `rev` are the store's to set, so the state handed over
	// carries only the merged data (the division); `rec.revision` is the
	// lineage this call exists to move.
	rev, err := c.w.cases.saveWorking(ctx, refuse, c.actor.User, w.rev,
		workingState{s: m.out}, rec.revision)
	if err != nil {
		return RebaseResult{}, err
	}
	return RebaseResult{Working: rev, Base: rec.revision}, nil
}

// Discard drops this actor's working copy. `expected` is the
// working revision the caller observed, and it is not decoration: the copy
// this call destroys is one another session of the same actor may have written
// a moment ago, and an unguarded drop would be this package deleting work it
// was never shown.
//
// The copy is removed and not reset.
//
// Nothing is committed and nothing is remembered.
func (c Case) Discard(ctx context.Context, expected uint64) error {
	refuse := c.refusing("discard")
	if _, _, err := c.look(ctx, refuse); err != nil {
		return err
	}
	return c.w.cases.discard(ctx, refuse, c.actor.User, expected)
}
