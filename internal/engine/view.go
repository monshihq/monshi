package engine

import (
	"context"
	"strings"

	"github.com/monshihq/monshi/internal/program"
)

// ViewResult is one permission-filtered reading of a case. The three numbers
// describe the snapshot it was taken from, and every node in `Roots` is part
// of it.
type ViewResult struct {
	// Revision identifies the committed snapshot used for this view. Pass it as
	// observed to Transition; an intervening commit makes it stale.
	Revision uint64

	// Stage is the committed stage used to resolve every node's permissions.
	Stage string

	// Working is the draft revision to pass as base to Update. It is distinct
	// from the committed Revision and is zero when no draft exists.
	Working uint64

	// Roots contains the selected subtrees in caller order. Duplicate or nested
	// selections appear once. With no selections, return all visible top-level
	// forms in declaration order.
	//
	// Omit a selection if its guard or an ancestor's guard is false, or if
	// neither it nor its descendants are visible to the actor.
	Roots []*ViewNode
}

// ViewNode is one node of a view: a declaration this actor may see, at the
// address where its value — or its instances — live.
type ViewNode struct {
	// Path is an absolute address accepted by View and Update. Instance paths
	// contain stable row IDs, as in application.holders.42.who.
	Path string

	// Kind describes the declaration, not the stored value. For example, an
	// options field has Kind "options" while its selected value has KindText.
	Kind string

	// Multiple distinguishes a collection from a group, even when the
	// collection has no rows. Its children represent instances and carry stable
	// row addresses.
	Multiple bool

	// Value is what this actor's draft holds here. The test for "has a value"
	// is `Value.Kind() == KindNone`, not a separate flag: `Empty` clears a
	// cell and keeps the field, so "no value" is a fact the value
	// itself carries, in the same terms `Set` and `Empty` are stated. For a
	// form, a group or a collection as declared there is no value at all —
	// those are structure — and only an instance node of a repeated *field*
	// carries one.
	Value Value

	// Required reports whether the declaration's requirement holds in this
	// draft. For a collection, the requirement is for at least one row.
	Required bool

	// CanEdit reports the effective edit permission at this node. A container
	// can grant edit to its subtree without itself holding a writable value.
	CanEdit bool

	// CanSubmit is the answer `Submit` will give for the form this node lives
	// in. It is read from this node's own mask rather than from a walk up to
	// the form. `TestSubmitMaskIsTheFormsMaskEverywhere` holds that equivalence
	// open.
	//
	// Every node of one form thus carries the same value, and the reason it is
	// on every node rather than only the root is that the node a client holds
	// is usually a field and the question it is asking is about a button.
	CanSubmit bool

	// Reviewable is the answer `Review` will give for the node this one's
	// decision would be made at: the same mask, the same roles, the same stage.
	// It is read at this node rather than at the governing unit. A node nobody
	// may review has the same value as the form it sits in when the grant was
	// written above it, because the fold is what put the bit there.
	Reviewable bool

	// Review reports the verdict on the committed content of the governing
	// unit. Value can include draft edits the reviewer has never seen.
	//
	// A zero value means no `verifiable` declaration governs this node — an
	// empty `Unit` says so, and no field of a plain form has a verdict to
	// report. What a node never reports is a *stale* verdict.
	Review ReviewInfo

	// Signable is the answer `Sign` will give for this node. Only a signature
	// field can carry it, because only one can be signed — and unlike
	// `Reviewable`, it says nothing about whether the field's own authority
	// conjunct holds: the second half is about *who* the definition opened the
	// field to, which a view cannot answer for an actor who is not the one
	// asking.
	Signable bool

	// Signature is the attestation the case holds for this signature field,
	// read from the committed snapshot for the reason: a hand attests to the
	// content that is committed, and a signature beside a draft would be a
	// statement about content nobody has published. A zero value means the
	// field holds none.
	Signature SignatureInfo

	// Options holds the choices returned by the options provider, in provider
	// order. A binding belongs to the declaration: fetch it once per view and
	// share the result across repeated rows.
	Options []string

	// Problems holds validation refusals for this node's value. Each problem
	// names the node's own path.
	//
	// Note what they are not: the completeness, which is what `Required` and an
	// unset `Value` say here, and which `Submit` reports as `Missing`. A blank
	// required field is one heading in the view and one in the commit refusal.
	// A cell the actor may not see has no entry, and not because one was thrown
	// away: the walk that gathers these is filtered by the same array that
	// decides which nodes exist, so a hook's sentence about a withheld value is
	// never computed.
	Problems []FieldProblem

	// Children are the nodes below this one, in declaration order, after
	// the pruning and this actor's permissions. For a collection it is the
	// instances, in row order, and each instance node's own Children are its
	// members; a collection with no rows has none and `Required` says whether
	// that is a gap.
	Children []*ViewNode
}

// View reads the case as one actor sees it.
//
// Each path selects a subtree, including its descendants, and the selections
// are merged and returned in the order they were given. What the actor may not
// see is not in the answer.
//
// Checks run in this order, and the order is the one the invariant asks for:
//
//   - the case loads, and an unknown case is NOT_FOUND, as every operation
//     says it, from the store;
//   - each path resolves, and a path that names no declaration is NOT_FOUND
//     with the text in it, because a mistyped path is about the definition the
//     host holds, not about case data;
//   - a resolved path the actor may not see is ACCESS_DENIED — the refusal
//     comes before the walk, so a caller with no grant anywhere under a path
//     is not told what the walk would have found there;
//   - then, and only then, the subtree is read: presence and permissions
//     folded once per call, values from the draft, options fetched per field,
//     validation asked once per selected root.
//
// A view is refused outright in three cases only: the case or definition is
// unreadable, a path names nothing the caller may see, and the hook contract
// breaks — a missing `Options` provider, or a `Validate` hook that returns
// something its type cannot hold or that rewrites a stored value. Those are
// `HOOK_FAILURE`, and they refuse rather than render a form whose choices are
// absent or whose errors are unknown, because an execution
// failure is an error and not silent success.
func (c Case) View(ctx context.Context, paths ...string) (ViewResult, error) {
	refuse := c.refusing("view")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return ViewResult{}, err
	}
	img := d.img
	res := ViewResult{
		Revision: rec.revision,
		Stage:    img.StageName(rec.commit.stage),
	}

	// One draft, read under one lock, for every node of the answer.
	draft, working, err := c.w.cases.viewed(ctx, refuse, c.actor.User, img)
	if err != nil {
		return ViewResult{}, err
	}
	res.Working = working

	v := &viewWalk{
		ctx:      ctx,
		refuse:   refuse,
		w:        c.w,
		img:      img,
		s:        &draft,
		roles:    rolesHeld(img, c.actor, rec.commit.s),
		stage:    rec.commit.stage,
		reviews:  rec.commit.reviews,
		sigs:     rec.commit.sigs,
		problems: map[string][]FieldProblem{},
		opts:     map[program.HookID][]string{},
	}
	v.vis = viewable(img, v.roles, v.stage)
	v.hook = c.pinned(img, rec, v.s, v.roles, working, v.vis)

	if len(paths) == 0 {
		// The whole case is one scope, so it gets one validation walk over the
		// root. The walk is skipped when nothing at all is visible: a host
		// hook is code the definition cannot be blamed for being slow, and a
		// caller with no grant anywhere in the case is not owed an answer that
		// costs one.
		if v.vis[0] {
			if err := v.check(0); err != nil {
				return ViewResult{}, err
			}
			// One root per top-level form, in declaration order (the list
			// order, for the same reason: a client that diffs two views by
			// path needs the two to agree about what comes first). `node`
			// applies both prune rules, which is why a form nobody granted is
			// simply not in the list.
			for _, n := range img.ChildrenOf(0) {
				root, err := v.node(n, nil)
				if err != nil {
					return ViewResult{}, err
				}
				if root != nil {
					res.Roots = append(res.Roots, root)
				}
			}
		}
		return res, nil
	}

	// Merge before building: a selection contained in an earlier one is
	// dropped rather than rendered twice, and a selection that contains an
	// earlier one replaces it, so the caller gets the widest subtree it asked
	// for at the position it asked for it. Address text is the key because it
	// is unique per (declaration, instance) — declaration paths are unique in
	// a definition, and the ID inside them says which row.
	var kept []selection
	for _, p := range paths {
		r, ok := v.s.resolve(p)
		if !ok || !v.vis[r.slot] {
			// Two refusals, told apart by whether case data was consulted
			// (the walk fails at a name or at a row), and the one that
			// would have to read a row the caller cannot see is the one that
			// says nothing about it.
			if !ok && v.vis[r.slot] {
				return ViewResult{}, refuse.now(NotFound,
					"no path is "+quote(p))
			}
			return ViewResult{}, refuse.now(AccessDenied,
				"view is not granted at "+quote(p))
		}
		kept = addSelection(kept, selection{
			slot:   r.slot,
			prefix: r.path(),
			addr:   v.s.addr(r.slot, r.path()),
		})
	}

	for _, sel := range kept {
		// The scope's validation report is asked for before the first node of
		// it is built, so a hook that cannot answer does not leave a half
		// render on the screen. Both prune rules are asked once, in `node`:
		// this walk needs no copy of them, because the absent subtree is
		// already skipped by the span jump inside `validate`, and an
		// ungranted selection was refused on the way in.
		if err := v.check(sel.slot); err != nil {
			return ViewResult{}, err
		}
		root, err := v.node(sel.slot, sel.prefix)
		if err != nil {
			return ViewResult{}, err
		}
		if root != nil {
			res.Roots = append(res.Roots, root)
		}
	}
	return res, nil
}

// check asks the scope's `Validate` hooks once and indexes their answers by
// address, so the walk that builds the nodes can hand each one the sentences
// about its own value. The filter it passes is this actor's visibility, which
// is what makes that line per actor: a rule the host wrote is not asked about a
// field the caller may not see, so neither its problems nor its failures can
// name a path that would disclose one.
//
// Two consequences of reusing Submit's walk rather than writing a second one
// are worth naming. A selection addressed to one instance asks the hook about
// its siblings too, because `validate` is a subtree walk and not a per-row one;
// those answers are dropped by never being looked up, since the nodes built for
// the caller's address are the only ones that ask. And a walk over a whole form
// costs one hook call per stored value, not one per node rendered beside it.
//
// A hook that misbehaves is an error and not a report, exactly as it is at
// commit: the fixed-point rule refuses the whole operation, and a view that
// rendered a form while pretending not to know its hook is broken would be a
// quieter version of the bug the rule exists to catch.
func (v *viewWalk) check(root program.NodeID) error {
	probs, err := v.w.validate(v.ctx, v.refuse, v.hook, root)
	if err != nil {
		return err
	}
	for _, pb := range probs {
		v.problems[pb.Path] = append(v.problems[pb.Path], pb)
	}
	return nil
}

// selection is one merged root of a view.
type selection struct {
	slot   program.NodeID
	prefix instPath
	addr   string
}

// addSelection inserts sel, keeping caller order and dropping overlaps: a
// selection contained in one already kept is not rendered twice, and one that
// contains kept selections absorbs them — so the answer is the widest subtree
// the caller asked for, listed in the order the caller asked for it, with an
// absorbing selection after the roots that remain.
//
// The two tests are separate loops on purpose. The first answer can return the
// caller's slice untouched, and the second writes into its backing array; merged
// into one pass, an early return would hand back a list it had already
// shortened, which is a wrong answer no test would think to ask for.
func addSelection(kept []selection, sel selection) []selection {
	for _, k := range kept {
		if contained(k.addr, sel.addr) {
			return kept // an ancestor the caller already named renders it
		}
	}
	out := kept[:0]
	for _, k := range kept {
		if contained(sel.addr, k.addr) {
			continue
		}
		out = append(out, k)
	}
	return append(out, sel)
}

// contained reports whether outer selects inner — the same node, or an
// ancestor of it. The dot matters: "application.parties.1" does not lie under
// "application.parties.12", and two addresses that differ only by where the
// instance ID ends are different rows.
func contained(outer, inner string) bool {
	return inner == outer ||
		strings.HasPrefix(inner, outer+".")
}

// viewWalk carries what every node of one call needs to answer, so that the
// fold over roles and the permission array are computed once rather than per
// node — the point about a walk that re-derives its own preconditions.
type viewWalk struct {
	ctx    context.Context
	refuse refuser
	w      *wiring
	img    *program.Program
	s      *state // the actor's draft, or the commit where there is none
	roles  []program.RoleID
	stage  program.StageID
	vis    []bool

	// reviews and sigs are the committed verdict tables, and the two fields
	// here that are not read from `s`: a decision and an attestation are facts
	// about the case and not about this actor's draft. Each
	// is nil when nothing of its kind has ever been recorded, which `reviewAt`
	// reads as "everything pending" and `sigAt` as "nothing signed".
	reviews []reviewState
	sigs    []sigState

	// hook is the snapshot handed to every host function this walk calls. One
	// for the walk rather than one per call: its state, mask and tables are
	// fixed before its first node is built, so a choice list and a validation
	// report are read from one answer, which is the same soundness `opts` buys
	// for the fetch count.
	hook CaseContext

	problems map[string][]FieldProblem
	opts     map[program.HookID][]string
}

// visible fills one flag per slot: this actor may see something at this node
// or below it. It is computed by walking the preorder range backwards, which
// works because a node's children always have higher ordinals than it,
// so the answer beneath a node is ready before the node is asked and the whole
// pass costs one scan of the slots plus one pass over the child edges.
//
// The descendant half is not decoration, and it is the reason a view is not a
// per-node permission lookup. `view` on a group's members has to be reachable
// through the group, and the alternative — a container nobody may view carrying
// content somebody may — is a node that cannot explain where its own child came
// from.
//
// `Rebase` folds the same array because a conflict report is a
// disclosure of the same kind as a rendered node: the two operations are
// required to answer "may this actor see this slot" identically, and one
// function is what makes that true rather than two walkers agreeing by
// luck.
func viewable(img *program.Program, roles []program.RoleID, stage program.StageID) []bool {
	vis := make([]bool, img.SlotCount())
	for i := img.SlotCount() - 1; i >= 0; i-- {
		n := program.NodeID(i)
		if img.Effective(roles, stage, n)&program.PermView != 0 {
			vis[i] = true
			continue
		}
		for _, c := range img.ChildrenOf(n) {
			if vis[c] {
				vis[i] = true
				break
			}
		}
	}
	return vis
}

// node renders the subtree at n. The prefix carries one stable row ID per
// enclosing collection, so instances of the same declaration retain distinct
// addresses.
func (v *viewWalk) node(n program.NodeID, prefix instPath) (*ViewNode, error) {
	img := v.img
	// Prune before reading fields or calling providers. This applies to a
	// directly selected root just as it does to a child reached during
	// traversal.
	if !v.vis[n] || !v.s.reached(n) {
		return nil, nil
	}
	sl := img.Slot(n)
	out := &ViewNode{
		Path:      v.s.addr(n, prefix),
		Kind:      v.kindOf(n),
		Multiple:  sl.Coll != 0,
		Required:  v.s.asked(n),
		CanEdit:   img.MayEdit(v.roles, v.stage, n),
		CanSubmit: img.MaySubmit(v.roles, v.stage, n),
		// The unit test is `verif == n` and not `verif != 0`. `Review` refuses
		// both. A *row* of the unit is the same slot at a different address.
		Reviewable: sl.Verif == n && !endsAt(prefix, n) &&
			img.MayReview(v.roles, v.stage, n),
		Review: reviewInfoOf(img, v.reviews, n),
		// Two of `Reviewable`'s three tests are not asked here, and both are
		// the doing: a signature field is a form's direct member, so this
		// node's slot *is* the declaration (no governing unit to settle) and no
		// address of it is ever an instance's (no row to decline). That test is
		// load-bearing rather than tidy: `allow sign all` is legal, its bit
		// reaches every slot by inheritance, and without it a definition would
		// report half its fields as signable.
		Signable:  sl.Sig != 0 && img.MaySign(v.roles, v.stage, n),
		Signature: signatureInfoOf(img, v.sigs, n),
	}
	if sl.Kind == program.KindField {
		// The draft's cell, at this instance. A form, a group and a
		// collection-as-declared hold no value, and `cellValue` answers that
		// with an unset cell rather than a guess, so there is nothing to test
		// beyond the kind.
		if sv := v.s.cellValue(n, prefix); sv.Set {
			out.Value = load(img, n, sv)
		}
	}
	opts, err := v.optionsFor(n)
	if err != nil {
		return nil, err
	}
	out.Options = opts
	out.Problems = v.problems[out.Path]

	kids, err := v.children(n, prefix)
	if err != nil {
		return nil, err
	}
	out.Children = kids
	return out, nil
}

// children returns the nodes below n: its instances, if it is a collection as
// declared; otherwise its declaration children. The two cases cannot share a
// loop because they answer different questions — one walks the data, in row
// order, and one walks the definition, in declaration order — and a collection
// instance's children are its members, which is the second case again with one
// more step in the address.
func (v *viewWalk) children(n program.NodeID, prefix instPath) ([]*ViewNode, error) {
	img := v.img
	var out []*ViewNode
	if img.Slot(n).Coll != 0 && !endsAt(prefix, n) {
		set, ok := v.s.rowsBelow(n, prefix, false)
		if !ok || set == nil {
			return nil, nil // no address, or nobody has added a row
		}
		for _, row := range set.rows {
			// Empty clears the value but keeps the row visible. Only Remove
			// drops its address from the view.
			child, err := v.node(n, withStep(prefix,
				instStep{coll: n, id: row.id}))
			if err != nil {
				return nil, err
			}
			if child != nil {
				out = append(out, child)
			}
		}
		return out, nil
	}
	for _, c := range img.ChildrenOf(n) {
		// Nothing is filtered here: `node` holds both prune rules, so the one
		// place they are stated is the one place they can be forgotten about.
		// What a caller may not see arrives as nil, and a nil is not a child.
		child, err := v.node(c, prefix)
		if err != nil {
			return nil, err
		}
		if child != nil {
			out = append(out, child)
		}
	}
	return out, nil
}

// optionsFor fetches a declared `options` field's choice list, once per field
// per call, and refuses when there is nobody to fetch it from.
//
// The cache is what keeps a collection honest: a field with two hundred rows
// is one hook call and two hundred renderings of the same list, because the
// binding is by field path and the provider was never offered a row. When
// `CaseContext` arrives the key gains the instance and this
// sentence moves into the hook's doc; until then a per-row fetch would be
// asking one question two hundred times and pretending the answers were
// independent.
//
// The missing case is a refusal and not an empty list, which is the one place
// the two bindings disagree on purpose: `Validate` absent means identity,
// because the job it was hired for was to change the value and the unchanged
// value is what the type checker accepted; an `Options` provider absent means
// there is no list, because the job it was hired for was to answer and
// inventing silence is how a dropdown ends up empty in production.
func (v *viewWalk) optionsFor(n program.NodeID) ([]string, error) {
	hook := v.img.Slot(n).Hook
	if hook == 0 {
		return nil, nil
	}
	if list, fetched := v.opts[hook]; fetched {
		return list, nil
	}
	path := v.img.Name(v.img.Slot(n).Path)
	fn, have := v.w.hooks.provider(v.img, hook)
	if !have {
		return nil, v.refuse.now(HookFailure,
			"no Options hook is registered for "+quote(path))
	}
	list, err := fn(v.ctx, v.hook, path)
	if err != nil {
		return nil, v.refuse.caused(HookFailure, "the Options hook for "+
			quote(path)+" refused this field", err)
	}
	v.opts[hook] = list
	return list, nil
}

// kindOf names the declaration in the client's vocabulary.
func (v *viewWalk) kindOf(n program.NodeID) string {
	sl := v.img.Slot(n)
	switch {
	case sl.Kind == program.KindForm:
		return "form"
	case sl.Kind == program.KindGroup:
		return "group"
	case sl.Role != 0:
		return "role"
	}
	return program.ClassName(v.img.BaseOf(n))
}

// withStep returns prefix plus one addressed instance. It copies rather than
// appending: a shared array would be correct only while every caller finishes
// with its index before a sibling writes it, and the whole point of an address
// in a view is that a host may keep it. The array is at most the depth
// bound, so the copy is bounded by a constant the definition cannot exceed.
func withStep(prefix instPath, st instStep) instPath {
	out := make(instPath, len(prefix)+1)
	copy(out, prefix)
	out[len(prefix)] = st
	return out
}

// endsAt reports whether prefix already addresses an instance of coll, which
// is what tells a collection node that it is being built as one row rather
// than as the declaration. The last step is the only candidate: the walk
// puts the innermost enclosing collection there, and a collection cannot
// address itself twice.
func endsAt(prefix instPath, coll program.NodeID) bool {
	return len(prefix) > 0 && prefix[len(prefix)-1].coll == coll
}
