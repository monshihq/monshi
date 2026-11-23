package monshi

// The view (DESIGN §6.1; RUNTIME §5.7): the one read that says what an actor
// sees, in the shape a client renders.
//
// Every other operation here is a write with an answer attached. This is the
// read, and what holds it together is that it has almost no semantics of its
// own. Each fact a node reports is one the engine already needed elsewhere:
// presence and requiredness are §5.2's two lines, answered by `reached` and
// `asked`; the permission bits are §3.5's mask fold, the same call `Update`
// judges an edit by and `Submit` judges a form by; the values are the actor's
// draft, which is what §5.4's completeness walk and §6's re-check at commit
// already read; and the paths are §5.1's addresses, spelled by the same `addr`
// that writes a gap report. So the view is those walks generalised from "is
// this case finished" to "show this case", and that is why it is a subtree
// walk over the image rather than a second place the language is interpreted:
// a client that disagrees with the engine about what a field means should be
// a bug in this file, never a drift between two files.
//
// Five decisions are worth their reasons:
//
//   - **The draft for data, the commit for authority.** §6.1's values are what
//     the actor is looking at, so they come from their working copy — a view
//     of committed content would hide the edits the client is displaying. The
//     masks do not: roles are folded from the committed snapshot and the stage
//     is the committed one (§4.3, invariant 6), so a draft that writes itself
//     into a role-typed field sees no new permission until somebody commits
//     it. The same split, asked in the same order, is what every committing
//     operation already does.
//   - **Nothing is created.** A read that found no working copy does not
//     allocate one; store.go's `viewed` says why that is a rule and not
//     tidiness. An actor who has never edited the case is shown the committed
//     content, which is the content their draft would have been cloned from,
//     with the revision 0 that `Update` documents for a copy not yet made —
//     so the number in the result is the number to send back.
//   - **Pruned, not flagged.** A node whose `present` guard answered false is
//     not in the view, and a node this actor may not see is not in the view.
//     §6.1 lists presence among the facts a view carries; the way it carries
//     it is by not containing the node, which is exactly what §5.2 says an
//     absent node is. A `Present: false` field would invite a client to render
//     what the engine just declined to include, and an invisible node with a
//     flag on it would be a disclosure of the shape of what is hidden.
//   - **Emptiness is not absence.** The one place this walk refuses to prune
//     is an unset cell: §5.1 keeps a field cleared with `Empty`, §5.2 makes it
//     fail `required`, and a form that omitted every blank box would report
//     what has been typed rather than what is left to do. Guard false and
//     value unset are different facts in the language, so they are different
//     outcomes here.
//   - **One snapshot, one hook call per question.** The walk reads one state
//     for its whole answer (§6.1's "one consistent working snapshot"), and the
//     `Options` provider for a field runs once per call however many instances
//     display it. Both are the same promise: a view whose rows disagree about
//     when they were read is not a view of anything.
//
// What this file did not give, and now does, is the two verdicts a case holds
// about its content: a node reports the `verifiable` declaration that governs
// it and what the case decided about that (RUNTIME §5.9), and a signature field
// reports who attested to the content the case holds and what reference the
// attestation came with (§5.10). Both are read from the committed snapshot while
// the values beside them are read from this actor's draft, which is the split
// §6.1 states and §5.7 draws: the verdicts are about the case, the draft is about
// what somebody would like it to be.
//
// Neither reports staleness, and neither could. §4.2's rules are that a content
// commit erases the verdict it invalidates, and an erased verdict is not in the
// table this walk reads, so a moved case shows the *absence* of a decision or an
// attestation and the account of what was there is history's (§5.6). What the
// node reports about the declaration itself is the kind the author spelled, so a
// signature field arrives as `"signature"` whether or not anything has been
// signed in it.

import (
	"context"
	"strings"
)

// ViewResult is one permission-filtered reading of a case. The three numbers
// describe the snapshot it was taken from, and every node in `Roots` is part
// of it.
type ViewResult struct {
	// Revision is the case's committed revision as this call read it. It is
	// the number a `Transition` wants as `observed`, so a host can act on
	// what it displayed — but holding it is no lease (§7's rule that a
	// snapshot is not a lease applies to a view like any other read), and a
	// commit between this call and that one is answered by STALE_REVISION.
	Revision uint64

	// Stage is the stage the case sits at, and the stage every node's
	// permission was answered at. §3.5's masks are a function of roles and
	// stage, so a view without its stage cannot explain why a field is on
	// screen or off it. Submit and Transition report the same string.
	Stage string

	// Working is the revision of the draft these values came from, and so the
	// number to pass `Update` as `base`. Zero means the actor has no working
	// copy and these are committed values, which is what §6.2 says to send
	// for a copy not yet made. It is reported beside `Revision` because a
	// draft view is the one place a client needs both: the one it may
	// overwrite, and the one it would have to rebase against.
	Working uint64

	// Roots holds one node per selection, in the order the caller listed the
	// paths, each with its whole subtree beneath it. Overlapping selections
	// are merged (§6.1): a path inside another selected path is dropped,
	// because the ancestor already renders it, and naming one path twice
	// selects it once. With no paths the result is every top-level form the
	// actor may see, in declaration order — the entire permission-filtered
	// view, which is what "no paths" means in §6.1 rather than an error: the
	// case root is what `all` denotes (§3.6), so there is an unambiguous
	// thing to return. §6.2 refuses an empty batch because it would change
	// nothing and say nothing; an empty list here says "everything".
	//
	// A selection is missing from this list in two cases and both are a fact
	// about §5.2 rather than a refusal: the node's own guard or an ancestor's
	// answered false, or the actor may see neither it nor anything under it.
	Roots []*ViewNode
}

// ViewNode is one node of a view: a declaration this actor may see, at the
// address where its value — or its instances — live. The fields are the
// metadata §6.1 requires and nothing else, on the theory that a client which
// needs a copy of the DSL to render a view has not been given a view.
type ViewNode struct {
	// Path is the node's absolute address, and it is spelled to be sent
	// back: it resolves through §3.9's walk, so `Update` accepts it and
	// `View` accepts it again. Members of an instance are addressed by that
	// instance's stable ID (§5.1) — "application.holders.42.who" — which is
	// what makes a diff of two views by path meaningful.
	Path string

	// Kind is the *declaration's* vocabulary: "form", "group", "role", or
	// the value class the definition spelled ("string", "decimal", "options",
	// "enum", "signature", …). It is deliberately not `Value.Kind`, which
	// names the class of the data: an options field's Kind is "options" while
	// its value reads as KindText, because §3.3 says an options value *is* the
	// chosen option's text and the list of choices is not data the row holds.
	// The two vocabularies differ at exactly that one point.
	Kind string

	// Multiple reports the declaration said `multiple`, so the node's
	// children are instances, each addressed by its ID, and `Add` can give it
	// another (§5.1). A group without it has the same children always. It is
	// part of the node because a client that cannot tell an empty collection
	// from an empty optional group cannot offer to add the first row, and
	// §6.1 says the client should not have to read the DSL to find out.
	Multiple bool

	// Value is what this actor's draft holds here. The test for "has a value"
	// is `Value.Kind() == KindNone`, not a separate flag: `Empty` clears a
	// cell and keeps the field (§5.1), so "no value" is a fact the value
	// itself carries, in the same terms `Set` and `Empty` are stated. For a
	// form, a group or a collection as declared there is no value at all —
	// those are structure — and only an instance node of a repeated *field*
	// carries one.
	Value Value

	// Required reports whether the definition asks for something here, which
	// is `required` or a `required[cond]` whose condition holds (§5.2). It is
	// asked of the draft, because it is the draft the client is completing.
	// For a collection it means "at least one instance" (SYNTAX §4), so a
	// required empty collection is Required with no Children — the whole of
	// what §5.4's report says about it, in the shape a form can render.
	Required bool

	// CanEdit is the answer `Update` will give an edit that names this node:
	// the same mask, the same roles, the same stage (§4.3). It is the mask at
	// this node, which is what §3.5's fold down the scope chain put there — so a
	// grant written on the form is true here and beneath it — and on a container
	// it reports what the definition granted rather than promising that the
	// container holds a value to write. §5.1 says it does not.
	CanEdit bool

	// CanSubmit is the answer `Submit` will give for the form this node lives
	// in. It is read from this node's own mask rather than from a walk up to
	// the form, and that is a rule rather than a shortcut: SYNTAX P3 makes a
	// `submit` grant's target a top-level form and nothing else — no field, no
	// group, and no `deny` at one either — so the only submit bits in the
	// table arrive through §3.5's fold, which is total, and a member's mask
	// therefore carries its form's bit and no bit of its own.
	// `TestSubmitMaskIsTheFormsMaskEverywhere` holds that equivalence open,
	// because the cheap reading of P3 depends on it.
	//
	// Every node of one form thus carries the same value, and the reason it is
	// on every node rather than only the root is that the node a client holds
	// is usually a field and the question it is asking is about a button.
	CanSubmit bool

	// Reviewable is the answer `Review` will give for the node this one's
	// decision would be made at: the same mask, the same roles, the same stage
	// (§4.3). It is read at this node rather than at the governing unit, which
	// is what makes it a promise and not a hint — the path a host holds is the
	// path a host sends, and RUNTIME §5.9 resolves the permission question at
	// exactly that slot. A node nobody may review has the same value as the
	// form it sits in when the grant was written above it, because §3.5's fold
	// is what put the bit there.
	Reviewable bool

	// Review is what the case has decided about the `verifiable` declaration
	// that governs this node, and it is read from the *committed* snapshot
	// while `Value` above is read from this actor's draft (RUNTIME §5.7's
	// split, and §4.2's rule that a decision is about committed content). The
	// two can disagree in the one way that is true rather than misleading: an
	// APPROVED mark beside values this actor has not submitted says the case
	// was approved as it stands, and this draft is a proposal about it.
	// `ReviewInfo.At` is the number to quote when the host decides which of
	// the two to show.
	//
	// A zero value means no `verifiable` declaration governs this node — an
	// empty `Unit` says so, and no field of a plain form has a verdict to
	// report. What a node never reports is a *stale* verdict, because §4.2
	// clears a unit back to PENDING when its content commits: a moved case
	// shows the absence of a decision, and §5.9 gives the reason that is the
	// honest rendering.
	Review ReviewInfo

	// Signable is the answer `Sign` will give for this node: the mask, the
	// roles and the stage §5.10 asks, at the slot a host would send. Only a
	// signature field can carry it, because only one can be signed — and
	// unlike `Reviewable`, it says nothing about whether the field's own
	// authority conjunct holds: S3's second half is about *who* the definition
	// opened the field to, which a view cannot answer for an actor who is not
	// the one asking.
	Signable bool

	// Signature is the attestation the case holds for this signature field,
	// read from the committed snapshot for §4.2's reason: a hand attests to the
	// content that is committed, and a signature beside a draft would be a
	// statement about content nobody has published. A zero value means the field
	// holds none, which covers the field never having been signed and the
	// signature a content commit invalidated in one answer; §5.10 says why the
	// two are the same fact rather than two facts with one rendering, and why
	// the difference is history's to state.
	Signature SignatureInfo

	// Options is the choice list this field's `Options` hook supplied, in the
	// order the host returned it. Non-nil only for a declared `options`
	// field: an enum's choices are declared data and arrive as `Kind`
	// "enum", and a string has none. The same list is shown by every instance
	// of a repeated field, which is the one instance of a §6.1 hook whose
	// answer is not per-instance — a provider is bound by field path, so
	// until a hook can see a row (DESIGN §7's `CaseContext`) it cannot have
	// been asked about one.
	Options []string

	// Problems holds the `Validate` refusals about the value at this node,
	// with the node's own path on each entry. §6.1 wants them beside the
	// values, and they are the same `FieldProblem` a submit reports: a view
	// that invented its own shape for a hook's sentence would be a second
	// vocabulary for one fact.
	//
	// Note what they are not: §5.4's completeness, which is what `Required`
	// and an unset `Value` say here, and which `Submit` reports as `Missing`.
	// A blank required field is one heading in the view and one in the commit
	// refusal, and both are the engine's §5.2 answer rather than a hook's.
	// A cell the actor may not see has no entry, and not because one was
	// thrown away: the walk that gathers these is filtered by the same array
	// that decides which nodes exist, so a hook's sentence about a withheld
	// value is never computed.
	Problems []FieldProblem

	// Children are the nodes below this one, in declaration order, after
	// §5.2's pruning and this actor's permissions. For a collection it is the
	// instances, in row order, and each instance node's own Children are its
	// members; a collection with no rows has none and `Required` says whether
	// that is a gap.
	Children []*ViewNode
}

// View reads the case as one actor sees it (DESIGN §6.1).
//
// Each path selects a subtree, including its descendants, and the selections
// are merged and returned in the order they were given. What the actor may not
// see is not in the answer, and is not said to be missing: invariant 7 is about
// disclosure, and "you cannot see that" already tells you there is a that.
//
// Checks run in this order, and the order is the one §7's invariant asks for:
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
// absent or whose errors are unknown, because DESIGN §7 says an execution
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
		Stage:    img.stageName(rec.commit.stage),
	}

	// One draft, read under one lock, for every node of the answer. This is
	// the snapshot §6.1 promises, and `viewed` is the half of it that keeps a
	// read from being a write.
	draft, working, err := c.w.cases.viewed(refuse, c.actor.User, img)
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
		roles:    img.rolesHeld(c.actor, rec.commit.s),
		stage:    rec.commit.stage,
		reviews:  rec.commit.reviews,
		sigs:     rec.commit.sigs,
		problems: map[string][]FieldProblem{},
		opts:     map[hookID][]string{},
	}
	v.vis = viewable(img, v.roles, v.stage)

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
			// One root per top-level form, in declaration order (§5.4's list
			// order, for the same reason: a client that diffs two views by
			// path needs the two to agree about what comes first). `node`
			// applies both prune rules, which is why a form nobody granted is
			// simply not in the list.
			for _, n := range img.childrenOf(0) {
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
			// (§3.9's walk fails at a name or at a row), and the one that
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
		// this walk needs no copy of them, because §5.2's absent subtree is
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
// about its own value (§6.1's "validation errors" line, and §6's chain run
// against the draft rather than against the commit). The filter it passes is
// this actor's visibility, which is what makes that line per actor: a rule the
// host wrote is not asked about a field the caller may not see, so neither its
// problems nor its failures can name a path that would disclose one.
//
// Two consequences of reusing Submit's walk rather than writing a second one
// are worth naming. A selection addressed to one instance asks the hook about
// its siblings too, because `validate` is a subtree walk and not a per-row one;
// those answers are dropped by never being looked up, since the nodes built for
// the caller's address are the only ones that ask. And a walk over a whole form
// costs one hook call per stored value, not one per node rendered beside it.
//
// A hook that misbehaves is an error and not a report, exactly as it is at
// commit: §6's fixed-point rule refuses the whole operation, and a view that
// rendered a form while pretending not to know its hook is broken would be a
// quieter version of the bug the rule exists to catch.
func (v *viewWalk) check(root nodeID) error {
	probs, err := v.w.validate(v.ctx, v.refuse, v.img, v.s, root, v.vis)
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
	slot   nodeID
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
// node — §8's point about a walk that re-derives its own preconditions.
type viewWalk struct {
	ctx    context.Context
	refuse refuser
	w      *wiring
	img    *image
	s      *state // the actor's draft, or the commit where there is none
	roles  []roleID
	stage  stageID
	vis    []bool

	// reviews and sigs are the committed verdict tables, and the two fields
	// here that are not read from `s`: a decision and an attestation are facts
	// about the case and not about this actor's draft (§4.2, §5.9, §5.10). Each
	// is nil when nothing of its kind has ever been recorded, which `reviewAt`
	// reads as "everything pending" and `sigAt` as "nothing signed".
	reviews []reviewState
	sigs    []sigState

	problems map[string][]FieldProblem
	opts     map[hookID][]string
}

// visible fills one flag per slot: this actor may see something at this node
// or below it. It is computed by walking the preorder range backwards, which
// works because a node's children always have higher ordinals than it (§3.2),
// so the answer beneath a node is ready before the node is asked and the whole
// pass costs one scan of the slots plus one pass over the child edges.
//
// The descendant half is not decoration, and it is the reason a view is not a
// per-node permission lookup. `view` on a group's members has to be reachable
// through the group, and the alternative — a container nobody may view
// carrying content somebody may — is a node that cannot explain where its own
// child came from. What invariant 7 forbids is disclosing content without a
// grant, and a container that grants nothing discloses nothing but its name,
// which the caller's definition already has. This is the same reading §4.3
// gives an ancestor's deny: the mask is asked at the node, never at its
// parent.
//
// `Rebase` folds the same array (§5.8) because a conflict report is a
// disclosure of the same kind as a rendered node: the two operations are
// required to answer "may this actor see this slot" identically, and one
// function is what makes that true rather than two walkers agreeing by
// luck.
func viewable(img *image, roles []roleID, stage stageID) []bool {
	vis := make([]bool, len(img.slots))
	for i := len(img.slots) - 1; i >= 0; i-- {
		n := nodeID(i)
		if img.effective(roles, stage, n)&permView != 0 {
			vis[i] = true
			continue
		}
		for _, c := range img.childrenOf(n) {
			if vis[c] {
				vis[i] = true
				break
			}
		}
	}
	return vis
}

// node builds one node and everything below it, or returns nil when §5.2 or
// this actor's permissions take it out of the view. `prefix` addresses the
// instance the node lives in, empty for content outside every collection, and
// it is the caller's path plus one step per collection the walk entered — so
// the same declaration is built once per row it has data in, at the address
// that row's ID gives it.
func (v *viewWalk) node(n nodeID, prefix instPath) (*ViewNode, error) {
	img := v.img
	// Both prune rules, asked before anything is read or fetched: §5.2 takes an
	// absent subtree out of the effective state, and invariant 7 takes an
	// ungranted one out of this actor's view. An options provider is not called
	// for either, and a node is never built in order to be thrown away, which
	// is why the tests are here rather than only in the caller that walks the
	// children — the root of a selection arrives through a different door.
	if !v.vis[n] || !v.s.reached(n) {
		return nil, nil
	}
	sl := &img.slots[n]
	out := &ViewNode{
		Path:      v.s.addr(n, prefix),
		Kind:      v.kindOf(n),
		Multiple:  sl.coll != 0,
		Required:  v.s.asked(n),
		CanEdit:   img.mayEdit(v.roles, v.stage, n),
		CanSubmit: img.maySubmit(v.roles, v.stage, n),
		// The unit test is `verif == n` and not `verif != 0`: F3 makes only
		// the outermost `verifiable` declaration govern, so a nested one is
		// not decidable either, and §4.3's promise that this flag answers the
		// operation would be false if a member or a governed group could say
		// it. `Review` refuses both (RUNTIME §5.9). A *row* of the unit is the
		// same slot at a different address, and the operation refuses that
		// address too — F4 reviews a collection as a whole — so the flag is the
		// declaration's node's and never an instance's.
		Reviewable: sl.verif == n && !endsAt(prefix, n) &&
			img.mayReview(v.roles, v.stage, n),
		Review: reviewInfoOf(img, v.reviews, n),
		// Two of `Reviewable`'s three tests are not asked here, and both are
		// S1's doing: a signature field is a form's direct member, so this
		// node's slot *is* the declaration (no governing unit to settle) and no
		// address of it is ever an instance's (no row to decline). What is left
		// is the two facts §4.3 promises the flag answers: the mask at this
		// slot, asked the way §5.10 asks it, and the test for being a signature
		// field at all. That test is load-bearing rather than tidy: `allow sign
		// all` is legal, its bit reaches every slot by inheritance, and without
		// it a definition would report half its fields as signable.
		Signable:  sl.sig != 0 && img.maySign(v.roles, v.stage, n),
		Signature: signatureInfoOf(img, v.sigs, n),
	}
	if sl.kind == kindField {
		// The draft's cell, at this instance. A form, a group and a
		// collection-as-declared hold no value, and `cellValue` answers that
		// with an unset cell rather than a guess, so there is nothing to test
		// beyond the kind.
		if sv := v.s.cellValue(n, prefix); sv.set {
			out.Value = img.load(n, sv)
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
func (v *viewWalk) children(n nodeID, prefix instPath) ([]*ViewNode, error) {
	img := v.img
	var out []*ViewNode
	if img.slots[n].coll != 0 && !endsAt(prefix, n) {
		set, ok := v.s.rowsBelow(n, prefix, false)
		if !ok || set == nil {
			return nil, nil // no address, or nobody has added a row
		}
		for _, row := range set.rows {
			// A row is data, so it is in the view: §5.1's `Remove` takes rows
			// away and `Empty` clears a value but keeps the element, and an
			// instance that was not rendered would make the first invisible
			// and the second indistinguishable from it.
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
	for _, c := range img.childrenOf(n) {
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
// `CaseContext` arrives (DESIGN §7) the key gains the instance and this
// sentence moves into the hook's doc; until then a per-row fetch would be
// asking one question two hundred times and pretending the answers were
// independent.
//
// The missing case is a refusal and not an empty list, which is the one place
// §6's two bindings disagree on purpose: `Validate` absent means identity,
// because the job it was hired for was to change the value and the unchanged
// value is what the type checker accepted; an `Options` provider absent means
// there is no list, because the job it was hired for was to answer and
// inventing silence is how a dropdown ends up empty in production. DESIGN §7
// states the rule; this is the only call site that can enforce it.
func (v *viewWalk) optionsFor(n nodeID) ([]string, error) {
	hook := v.img.slots[n].hook
	if hook == 0 {
		return nil, nil
	}
	if list, fetched := v.opts[hook]; fetched {
		return list, nil
	}
	path := v.img.name(v.img.slots[n].path)
	fn, have := v.w.hooks.provider(v.img, hook)
	if !have {
		return nil, v.refuse.now(HookFailure,
			"no Options hook is registered for "+quote(path))
	}
	list, err := fn(v.ctx, path)
	if err != nil {
		return nil, v.refuse.caused(HookFailure, "the Options hook for "+
			quote(path)+" refused this field", err)
	}
	v.opts[hook] = list
	return list, nil
}

// kindOf names the declaration in the client's vocabulary. The order is the
// one §3.3 states the facts in: a role-typed field has no value class at all,
// so it is asked about first, and only a field has a class to report.
func (v *viewWalk) kindOf(n nodeID) string {
	sl := &v.img.slots[n]
	switch {
	case sl.kind == kindForm:
		return "form"
	case sl.kind == kindGroup:
		return "group"
	case sl.role != 0:
		return "role"
	}
	return className(v.img.baseOf(n))
}

// withStep returns prefix plus one addressed instance. It copies rather than
// appending: a shared array would be correct only while every caller finishes
// with its index before a sibling writes it, and the whole point of an address
// in a view is that a host may keep it. The array is at most §3.9's depth
// bound, so the copy is bounded by a constant the definition cannot exceed.
func withStep(prefix instPath, st instStep) instPath {
	out := make(instPath, len(prefix)+1)
	copy(out, prefix)
	out[len(prefix)] = st
	return out
}

// endsAt reports whether prefix already addresses an instance of coll, which
// is what tells a collection node that it is being built as one row rather
// than as the declaration. The last step is the only candidate: §3.9's walk
// puts the innermost enclosing collection there, and a collection cannot
// address itself twice.
func endsAt(prefix instPath, coll nodeID) bool {
	return len(prefix) > 0 && prefix[len(prefix)-1].coll == coll
}
