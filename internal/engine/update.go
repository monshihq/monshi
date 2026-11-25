package engine

import (
	"context"

	"github.com/monshihq/monshi/internal/program"
)

// ChangeResult is what an accepted batch says about the working copy.
type ChangeResult struct {
	// Working is the copy's revision after this batch, the value the next
	// batch — or a Submit — presents as the one it observed (§6.2).
	Working uint64

	// Created holds the path of every instance `Add` made, spelled as the
	// caller's own text plus the engine's new ID: "application.shareholders"
	// becomes "application.shareholders.42". It is what makes the row
	// addressable, which is the reason §5.1's `Add` takes no value.
	Created []string

	// Affected holds the declaration paths whose *view* may have changed,
	// which is what lets a client refresh a subtree it never wrote: DESIGN
	// §6.2's "changes such as province and city are evaluated together", and
	// the reason a created instance is spelled with its ID in `Created` but
	// appears here as its declaration. A group path stands for its subtree, no
	// path in the list contains another, and the order is declaration order —
	// RUNTIME §5.3 and affected.go say what the walk marks, and why it is a
	// superset rather than an exact diff.
	Affected []string
}

// Update applies edits to this actor's working copy and reports the copy's
// new revision. `base` is the working revision the caller observed — 0 for a
// copy it has never written — and a copy that has moved on is refused rather
// than merged: two sessions of one actor share one copy (invariant 4), so
// quietly overwriting the other's work would be this package destroying data
// it was never shown. §6.2's guard, and §6.5's rebase is where a stale copy
// becomes recoverable.
//
// Nothing is saved until every edit is settled, so a refusal leaves the copy
// untouched. The save is guarded again against a session that wrote between
// the check and the save, and reports the same STALE_REVISION.
func (c Case) Update(ctx context.Context, base uint64, edits ...Edit) (
	ChangeResult, error) {
	refuse := c.refusing("update")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return ChangeResult{}, err
	}
	if len(edits) == 0 {
		// Invariant 9's discipline. An operation asked to change nothing is
		// not asked to guess what was meant, and a batch that arrived empty is
		// almost always a slice the host built wrong — which is worth one
		// refusal now rather than one quiet no-op per request.
		return ChangeResult{}, refuse.now(InvalidOp,
			"the batch names no edit")
	}

	// The candidate is a copy, so every write below is one the store never
	// sees unless the batch survives to the save. `d.img` is the image the
	// case was just proved readable through, and store.go interprets the copy
	// with it, which is §3's reinterpretation applied to a working state.
	w, err := c.w.cases.working(ctx, refuse, c.actor.User, d.img)
	if err != nil {
		return ChangeResult{}, err
	}
	if w.rev != base {
		return ChangeResult{}, refuse.staleCopy(w.rev, base)
	}

	// Roles are folded from the *committed* snapshot. Not an optimization: an
	// edit that assigns a role-typed field would otherwise confer the role
	// inside the same batch that wrote it (§4.3, T4).
	img := d.img
	roles := rolesHeld(img, c.actor, rec.commit.s)

	// The snapshot every host function this batch calls is handed (§6,
	// DESIGN §7). It is built once for the batch, over the draft the edits land
	// in and the committed tables and roles beside it, with no visibility fold:
	// an edit is gated per path by `edit` (§4.3), which is a stricter question
	// than the view's `view`, so a hook's read here is the node's own bit.
	at := c.pinned(img, rec, &w.s, roles, w.rev, nil)
	fan := newFanout(img)
	var out ChangeResult
	for _, e := range edits {
		created, err := c.apply(ctx, refuse, img, at, e, fan)
		if err != nil {
			return ChangeResult{}, err
		}
		if created != "" {
			out.Created = append(out.Created, created)
		}
	}

	// The closure runs on the finished candidate: a guard's operand is whatever
	// the row holds after `settle` replaced what the host asked for, which is
	// DESIGN §7's "re-evaluate dependencies after *normalization*". The walk
	// reads no values, only the adjacency, so it needs nothing from here but
	// the write set — and it waits for the save, because a batch the store
	// refuses should not have paid for a closure nobody is told about.
	rev, err := c.w.cases.saveWorking(ctx, refuse, c.actor.User, base, w, 0)
	if err != nil {
		return ChangeResult{}, err
	}
	// The store did the numbering: what the caller presented as observed is
	// the guard, and the revision that comes back is the answer (§3.1).
	out.Working = rev
	out.Affected = fan.affected()
	return out, nil
}

// apply settles one edit against the candidate and tells the fanout what it
// wrote. The order inside it is §6's re-evaluation order: resolve, check the
// structure the verb was given, check authority, then write, normalizing as the
// write happens. What it does not do is re-evaluate anything the write reaches
// — that is the batch's job rather than the edit's, because §6.2's whole point
// is that a guard reading two fields in one batch is asked once, after both
// landed (RUNTIME §5.3).
//
// The state it writes and the snapshot a hook reads are the same object: the
// edit takes `at` instead of a state pointer, so there is no way for the write
// to land somewhere the normalizer was not shown. That is the property §7's
// "candidate-state context" wants — an earlier edit of the same batch is
// visible to a later one's hook, a later edit is not — and stating it as a
// parameter rather than as a comment is what keeps it true when the batch
// grows a step.
func (c Case) apply(ctx context.Context, refuse refuser, img *program.Program,
	at CaseContext, e Edit, fan *fanout) (string, error) {
	s := at.s
	// An edit that names no path is a batch element nobody filled in, and it
	// is worth saying so about the edit rather than letting §3.9's walk answer
	// that no declaration is reached: the difference is between a host whose
	// own request is malformed and one whose path has gone stale, and only one
	// of those is fixed by looking at the definition.
	if e.path == "" {
		return "", refuse.now(InvalidOp, "an edit names no path")
	}
	r, ok := s.resolve(e.path)
	if !ok {
		return "", refuse.now(NotFound,
			"no declaration is reached by path "+quote(e.path))
	}
	n := r.slot
	sl := img.Slot(n)

	// The verbs differ in what they address, which is the one structural
	// question resolution cannot answer: `Set` and `Empty` want a value,
	// `Add` and `Remove` want an instance of a collection. `hasValue` is the
	// answer to the first and §5.1's stable-ID addressing to the second, so
	// a message can name the shape the caller's text actually reached.
	switch e.kind {
	case editSet, editEmpty:
		if !r.hasValue {
			return "", refuse.now(InvalidOp,
				quote(e.path)+" "+whyNoValue(img, n))
		}
	case editAdd:
		if bad := whyNotInstance(sl); bad != "" {
			return "", refuse.now(InvalidOp, quote(e.path)+" "+bad)
		}
	case editRemove:
		if sl.Coll == 0 {
			return "", refuse.now(InvalidOp, quote(e.path)+
				" is not a multiple declaration, and remove drops an instance "+
				"rather than a declaration")
		}
	}

	// Authority: `edit` on what the path reached, at this stage, for the roles
	// held from committed data. An `Add` or `Remove` is granted on the
	// collection's own slot — the group, not its members — which is the same
	// shape the language spells: `edit application.shareholders`.
	if !img.MayEdit(at.roles, at.stage, n) {
		return "", refuse.now(AccessDenied,
			"editing "+quote(e.path)+" is not granted at stage "+
				quote(img.StageName(at.stage)))
	}

	switch e.kind {
	case editEmpty:
		r.cell.clear()
		fan.wroteField(n)
		return "", nil
	case editSet:
		v, err := c.w.settle(ctx, refuse, img, n, r.path(), at, e.v)
		if err != nil {
			return "", err
		}
		r.cell.set(v)
		fan.wroteField(n)
		return "", nil
	case editAdd:
		// The row's defaults materialize without a per-field permission test,
		// and that is not an oversight: nothing in them came from this actor.
		// A default is the definition's content, landed in the instance the
		// caller asked for (§5.1, invariant 8), and the fields a caller may
		// not edit are exactly the ones it cannot set afterwards either.
		id, _, ok := s.addAt(n, r.path())
		if !ok {
			// Resolution reached the collection, so the only way this fails is
			// a state that cannot hold the row — a defect, not a request, and
			// reported as one so §10's rule about not laundering a runtime bug
			// into a refusal keeps holding past the seam.
			return "", refuse.now(SchemaMismatch,
				quote(e.path)+" reaches a collection this state cannot add to")
		}
		fan.wroteRows(n)
		return e.path + "." + dec(uint64(id)), nil
	case editRemove:
		// The last step names the instance to drop; the rows it lives in are
		// addressed by everything before it. §3.9's walk guarantees the
		// pairing for a path that entered a collection, and a path that ended
		// at the collection itself has no such step — which is what this
		// distinguishes, rather than assuming the caller meant the last row.
		steps := r.path()
		if len(steps) == 0 || steps[len(steps)-1].coll != n {
			return "", refuse.now(InvalidOp,
				quote(e.path)+" names a collection, not an instance of one")
		}
		if !s.removeAt(n, steps[:len(steps)-1], steps[len(steps)-1].id) {
			// Belt and braces, and a defect rather than a request: the ID in
			// the last step is one resolution just found a row for, so the
			// only way this fails is the two walks disagreeing about the same
			// state. §10's rule against laundering a runtime bug into a
			// refusal is why this is not reported as NOT_FOUND, which is what
			// a host that really did name a stale instance hears.
			return "", refuse.now(SchemaMismatch,
				quote(e.path)+" names an instance this state does not hold")
		}
		fan.wroteRows(n)
		return "", nil
	}
	return "", refuse.now(InvalidOp, "an edit with no verb")
}

// whyNoValue says, for a slot resolution found but `Set` or `Empty` cannot
// write, which of the three shapes has no value: structure, a collection, or a
// member that needs an instance named. It is a sentence rather than a category
// because the category is the same INVALID_OPERATION either way and only the
// remedy differs — and a host that has to guess which of its paths was wrong
// is reading a refusal that could have said it.
func whyNoValue(img *program.Program, n program.NodeID) string {
	sl := img.Slot(n)
	switch {
	case sl.Coll != 0:
		// A repeated group is structure too, and saying "edit its fields"
		// there would send the host to the one verb that cannot help it.
		return "is a multiple declaration, whose values live one per " +
			"instance: add one, then address it by its ID"
	case sl.Kind != program.KindField:
		return "is a group, which is structure and has no value: edit its fields"
	default:
		return "is inside a collection, so the path has to name an instance of it"
	}
}

// whyNotInstance is the same idea for `Add`: which shape cannot take a new
// instance, in the words that say what to do instead.
//
// Both shapes `multiple` is legal on take one (SYNTAX §4), and that is not an
// extension of anything: a repeated *field*'s instances are values of that
// field, held in rows of its own, which is why §5.1's row range begins at the
// slot itself for a field and at its first descendant for a group. So `Add` is
// the only door to a multi-valued field's first value. What an earlier version
// of this sentence offered instead — `Set` at a path naming a fresh ID — cannot
// work: §3.9's walk refuses an instance ID with no row behind it, so a host
// told to do that holds a remedy it can never use. One test and one message is
// the honest shape, and `value_test.go` keeps the two addable shapes beside it.
func whyNotInstance(sl program.Slot) string {
	if sl.Coll == 0 {
		return "is not a multiple declaration, so it has no instances to add"
	}
	return ""
}
