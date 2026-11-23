package monshi

// The edit operation (DESIGN §6.2): a batch of working edits, applied to
// one actor's working copy, or not at all.
//
// Three claims hold this together, and each is a rule someone could break by
// accident in a later slice:
//
//   - **Atomic.** An edit lands on a candidate the store handed out as a
//     copy, and that copy reaches the store only through a guarded save. A
//     refusal anywhere in the batch — a path that resolves to nothing, a
//     field this actor may not edit, a value whose class the field does not
//     take, a hook that fails — leaves the working copy exactly as it was.
//     That is DESIGN §9's "either the allowed atomic state transition or no
//     persistent effect", and it is free here because store.go already
//     clones on the way out.
//
//   - **Authority comes from committed data only.** The role set is folded
//     from the committed snapshot, never from the candidate (§4.3), so an
//     actor who writes themselves into a role-typed field gains nothing until
//     that value is committed. The masks themselves depend on the stage and
//     the roles and on no value at all (§3.5), which is why one fold serves
//     the whole batch.
//
//   - **Order is the caller's.** Edits are applied in the order given, and a
//     later edit resolves against the state the earlier ones left. That is
//     what makes §6.2's "changes such as province and city are evaluated
//     together" a claim about the engine rather than about a client: the
//     batch arrives as one state, not as one write at a time.
//
// What a successful batch reports is the copy's new revision, the paths it
// created, and the set whose *view* may have changed: §5.3's closure over the
// dependency adjacency, which `affected.go` walks once the batch is finished
// and which `ChangeResult.Affected` renders. Validation errors are the one
// remaining field of §6.2's response: they need §5.2's `satisfied` over that
// same closure, and arrive with §5.4's completeness pass.

import "context"

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
	d, rec, err := c.look(ctx, "update")
	if err != nil {
		return ChangeResult{}, err
	}
	if len(edits) == 0 {
		// Invariant 9's discipline. An operation asked to change nothing is
		// not asked to guess what was meant, and a batch that arrived empty is
		// almost always a slice the host built wrong — which is worth one
		// refusal now rather than one quiet no-op per request.
		return ChangeResult{}, refused("update", InvalidOp, c.id,
			"the batch names no edit")
	}

	// The candidate is a copy, so every write below is one the store never
	// sees unless the batch survives to the save. `d.img` is the image the
	// case was just proved readable through, and store.go interprets the copy
	// with it, which is §3's reinterpretation applied to a working state.
	w, err := c.w.cases.working("update", c.id, c.actor.User, d.img)
	if err != nil {
		return ChangeResult{}, err
	}
	if w.rev != base {
		return ChangeResult{}, staleWorking("update", c.id, w.rev, base)
	}

	// Roles are folded from the *committed* snapshot. Not an optimization: an
	// edit that assigns a role-typed field would otherwise confer the role
	// inside the same batch that wrote it (§4.3, T4).
	img := d.img
	roles := img.rolesHeld(c.actor, rec.commit.s)
	fan := newFanout(img)
	var out ChangeResult
	for _, e := range edits {
		created, err := c.apply(ctx, img, &w.s, roles, rec.commit.stage, e, fan)
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
	rev, err := c.w.cases.saveWorking("update", c.id, c.actor.User, base, w)
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
func (c Case) apply(ctx context.Context, img *image, s *state,
	roles []roleID, stage stageID, e Edit, fan *fanout) (string, error) {
	// An edit that names no path is a batch element nobody filled in, and it
	// is worth saying so about the edit rather than letting §3.9's walk answer
	// that no declaration is reached: the difference is between a host whose
	// own request is malformed and one whose path has gone stale, and only one
	// of those is fixed by looking at the definition.
	if e.path == "" {
		return "", refused("update", InvalidOp, c.id, "an edit names no path")
	}
	r, ok := s.resolve(e.path)
	if !ok {
		return "", refused("update", NotFound, c.id,
			"no declaration is reached by path "+quote(e.path))
	}
	n := r.slot
	sl := &img.slots[n]

	// The verbs differ in what they address, which is the one structural
	// question resolution cannot answer: `Set` and `Empty` want a value,
	// `Add` and `Remove` want an instance of a collection. `hasValue` is the
	// answer to the first and §5.1's stable-ID addressing to the second, so
	// a message can name the shape the caller's text actually reached.
	switch e.kind {
	case editSet, editEmpty:
		if !r.hasValue {
			return "", refused("update", InvalidOp, c.id,
				quote(e.path)+" "+whyNoValue(img, n))
		}
	case editAdd:
		if bad := whyNotInstance(img, sl); bad != "" {
			return "", refused("update", InvalidOp, c.id, quote(e.path)+" "+bad)
		}
	case editRemove:
		if sl.coll == 0 {
			return "", refused("update", InvalidOp, c.id, quote(e.path)+
				" is not a multiple declaration, and remove drops an instance "+
				"rather than a declaration")
		}
	}

	// Authority: `edit` on what the path reached, at this stage, for the roles
	// held from committed data. An `Add` or `Remove` is granted on the
	// collection's own slot — the group, not its members — which is the same
	// shape the language spells: `edit application.shareholders`.
	if !img.mayEdit(roles, stage, n) {
		return "", refused("update", AccessDenied, c.id,
			"editing "+quote(e.path)+" is not granted at stage "+
				quote(img.stageName(stage)))
	}

	switch e.kind {
	case editEmpty:
		r.cell.clear()
		fan.wroteField(n)
		return "", nil
	case editSet:
		v, err := c.w.settle(ctx, "update", img, c.id, n, e.v)
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
			return "", refused("update", SchemaMismatch, c.id,
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
			return "", refused("update", InvalidOp, c.id,
				quote(e.path)+" names a collection, not an instance of one")
		}
		if !s.removeAt(n, steps[:len(steps)-1], steps[len(steps)-1].id) {
			// Belt and braces, and a defect rather than a request: the ID in
			// the last step is one resolution just found a row for, so the
			// only way this fails is the two walks disagreeing about the same
			// state. §10's rule against laundering a runtime bug into a
			// refusal is why this is not reported as NOT_FOUND, which is what
			// a host that really did name a stale instance hears.
			return "", refused("update", SchemaMismatch, c.id,
				quote(e.path)+" names an instance this state does not hold")
		}
		fan.wroteRows(n)
		return "", nil
	}
	return "", refused("update", InvalidOp, c.id, "an edit with no verb")
}

// whyNoValue says, for a slot resolution found but `Set` or `Empty` cannot
// write, which of the three shapes has no value: structure, a collection, or a
// member that needs an instance named. It is a sentence rather than a category
// because the category is the same INVALID_OPERATION either way and only the
// remedy differs — and a host that has to guess which of its paths was wrong
// is reading a refusal that could have said it.
func whyNoValue(img *image, n nodeID) string {
	sl := &img.slots[n]
	switch {
	case sl.coll != 0:
		// A repeated group is structure too, and saying "edit its fields"
		// there would send the host to the one verb that cannot help it.
		return "is a multiple declaration, whose values live one per " +
			"instance: add one, then address it by its ID"
	case sl.kind != kindField:
		return "is a group, which is structure and has no value: edit its fields"
	default:
		return "is inside a collection, so the path has to name an instance of it"
	}
}

// whyNotInstance is the same idea for `Add`: which shape cannot take a new
// instance, in the words that say what to do instead.
func whyNotInstance(img *image, sl *nodeSlot) string {
	switch {
	case sl.coll == 0:
		return "is not a multiple declaration, so it has no instances to add"
	case sl.kind != kindGroup:
		// A repeated *field* is its own row: §5.1's instances of it are
		// values of that field, so the way to make one is `Set` on a path
		// naming an instance, and `Add` on the collection of a repeated group
		// is the verb that creates the row a repeated field lives in.
		return "is a repeated field, whose instances are values: set one at " +
			"an instance path rather than adding one here"
	}
	return ""
}
