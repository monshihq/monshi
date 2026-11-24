package monshi

// The snapshot a hook is handed (RUNTIME §6; DESIGN §7).
//
// DESIGN §7 calls `CaseContext` "a read-only, stable snapshot view" that gives
// a host "case identity, actor, stage, values, reviews, signatures and
// effective roles without … mutation or storage privileges", and adds that it
// "is a logical interface, not necessarily a copy of the state." The second
// half is what makes this type cheap, and the store is what makes it true:
// `working`, `draft` and `viewed` each clone on the way out (store.go), so the
// state an operation holds is already private to it. Nothing else writes those
// bytes, before the operation ends or after it, which means a context can be
// the pointers the caller already had rather than a snapshot of its own, and a
// host that keeps one past the call is keeping a *finished* state — inert,
// readable, and safe to read from another goroutine. DESIGN §7's "no hook
// result may be cached across a commit: the context it saw is the snapshot, not
// a lease" is therefore a property of the shape rather than a rule this package
// has to police, and the test that says so retains a context and reads it from
// a goroutine after the batch returned.
//
// What the shape does not decide by itself is whose permission gates a read.
// §6 states the rule about a hook's *output* — "a host rule is not entitled to
// run over content the caller could not render" — and §7's property 7 is that
// nothing is disclosed without effective `view`. A context that answered an
// unfiltered path would be a disclosure channel wearing a host's function, and
// a wider one than §5.11's trail ever was, because it takes arbitrary text. So
// a read is answered at the nodes the caller could have rendered: the caller's
// own visibility fold where it computed one, and the same reachability
// otherwise, which is what lets one rule serve an edit, a submit and a view
// instead of three that happen to agree today. `Value`'s answer for a path the
// caller may not render is the same one it gives for a cell that holds nothing:
// absence, and not a refusal.

import "context"

// CaseContext is what a `Validate` or `Options` hook is handed — and what an
// `OnCommit` observer is handed after the commit — the snapshot the operation
// was already reading, pinned to the actor it is reading for.
//
// It has no methods that write, and it holds no handle: a host cannot reach
// `Update` from inside a hook, which is DESIGN §7's "hooks are read-only with
// respect to case state" (invariant 9) enforced by the type rather than by
// advice. A zero value is inert — every read answers the way an unseen one
// does — because a host that stores a `CaseContext` in a struct gets the zero
// value whether it meant to or not.
type CaseContext struct {
	img     *image
	s       *state // the operation's own clone: the draft, or the commit
	reviews []reviewState
	sigs    []sigState // the committed verdict tables, never the draft's
	roles   []roleID   // folded from committed data (§4.3, T4)
	stage   stageID    // where the case stands, not where the draft imagines
	only    []bool     // the caller's visibility fold, nil when it has none
	id      CaseID
	def     string
	actor   Actor
	rev     uint64 // the committed head this snapshot was cut at
	work    uint64 // the copy's number, 0 when there is no copy
}

// pinned is the one construction, and every caller already holds all six
// arguments: `Case.Update` and `Case.Submit` pass the candidate and no fold,
// `Case.View` passes the draft it just cloned and the fold it computed for the
// same walk. Nothing here copies state — that is §7's "not necessarily a copy"
// arriving as a parameter list rather than as a caveat.
//
// `work` is the copy's number as the operation found it, not as it will save
// it: a hook that runs during a batch sees a snapshot whose `Working` is the
// revision the caller presented, because the batch is not the store's yet.
func (c Case) pinned(img *image, rec storedCase, s *state, roles []roleID,
	work uint64, only []bool) CaseContext {
	return CaseContext{
		img:     img,
		s:       s,
		reviews: rec.commit.reviews,
		sigs:    rec.commit.sigs,
		roles:   roles,
		stage:   rec.commit.stage,
		only:    only,
		id:      c.id,
		def:     rec.def,
		actor:   c.actor,
		rev:     rec.revision,
		work:    work,
	}
}

// pinnedPost cuts the snapshot an `OnCommit` observer is handed: the state the
// store *installed*, not one the operation prepared. That difference is the
// whole reason it exists — §5.6's effects erase verdicts inside the store's
// commit, so the caller's `next` can be a state the commit did not leave. Every
// field here is what the commit produced: `rev` is the number the entry got,
// the tables are post-erasure, and `work` is the committer's copy as the same
// locked act left it. `only` is the fold the dispatcher rendered the record
// through, so an observer's read of a path and its copy of the entry naming
// that path answer with one mask — §6's agreement rule again, one construction
// away.
func (c Case) pinnedPost(img *image, rec storedCase, got committed,
	roles []roleID, only []bool) CaseContext {
	return CaseContext{
		img:     img,
		s:       &got.post.s,
		reviews: got.post.reviews,
		sigs:    got.post.sigs,
		roles:   roles,
		stage:   got.post.stage,
		only:    only,
		id:      c.id,
		def:     rec.def,
		actor:   c.actor,
		rev:     got.rec.revision,
		work:    got.copy,
	}
}

// seen is §7's property 7 asked of a host function: the read answers at the
// nodes this caller could have *rendered*, which is §5.7's rule and nothing
// looser. It is the caller's own visibility fold when the caller computed one,
// and §5.7's reachability otherwise — the node's own `view` bit, or a
// descendant's — because a read that has no fold still has to answer the way
// the same actor's view would, and the two are not allowed to disagree about
// who may see what.
//
// The distinction only bites on a container. A value lives at a leaf, and a
// leaf's fold is its own bit: §3.5's inheritance is top-down, so nothing below
// a leaf can grant it. That is what closes the hole the rule exists to close —
// a grant that reaches one member of a row leaves its siblings unreachable, and
// a host asking for one of those by path is answered with absence, the same
// answer §5.11 gives for a withheld trail entry. What it deliberately does not
// hide is a unit's verdict showing on a container the caller *can* render: a
// form node carries its decision in `ViewNode.Review`, and a hook reading that
// path is reading what the caller was already shown.
//
// The cost is one array read where there is a fold, which is the view's case
// and its cheapest; an edit or a submit pays one pass over the node's
// preorder range, which is what `effective` already charges §5.9's and
// §5.10's gates for a single path. A hook that reads a hundred fields pays a
// hundred of those, and folding an array per hook call to answer one question
// would make the cheapest hook the most expensive read in the package.
//
// Every caller has already answered for the zero snapshot: a read that gets
// this far holds a state, and `pinned` builds no state without the image it
// was resolved against, so the walks below are guarded once rather than twice.
func (x CaseContext) seen(n nodeID) bool {
	if x.only != nil {
		return x.only[n]
	}
	lo, hi := x.img.subtree(n)
	for m := nodeID(lo); m < nodeID(hi); m++ {
		if x.img.effective(x.roles, x.stage, m)&permView != 0 {
			return true
		}
	}
	return false
}

// addrOf spells the cell a hook is being asked about, in the same text
// `Value` takes and `Update` takes back. It is a method on the snapshot rather
// than a parameter the caller already has because that is what keeps the two
// vocabularies one: the address a normalizer is handed, the address a provider
// is bound at, and the address a later `FieldProblem` reports all come from one
// state, so a host that lines a refusal up against the value it was shown is
// matching strings it did not have to build itself.
func (x CaseContext) addrOf(n nodeID, p instPath) string {
	if x.s == nil {
		return ""
	}
	return x.s.addr(n, p)
}

// Case is the case this snapshot was cut from.
func (x CaseContext) Case() CaseID { return x.id }

// Definition is the name of the definition governing it. It is a name and not
// a `*Definition` on purpose: the compiled definition is a host's object, and
// handing one to a hook would hand it a way to walk the image unmasked.
func (x CaseContext) Definition() string { return x.def }

// Actor is who is asking, as the operation was told — the identity a role-typed
// field confers a role *to*, and the primary role the host asserted (DESIGN
// §4.3). A hook that logs or delegates needs this and cannot derive it from
// data, because two users of one case share one draft by design (invariant 4).
func (x CaseContext) Actor() Actor { return x.actor }

// Stage is the case's current stage name, read from the committed snapshot.
// A draft cannot move a stage, so this is one answer for every hook of a call.
func (x CaseContext) Stage() string {
	if x.img == nil {
		return ""
	}
	return x.img.stageName(x.stage)
}

// Revision is the committed head this snapshot was cut at — the number §5.9's
// and §5.10's verdicts are stamped against, and the one `Submit` guards.
func (x CaseContext) Revision() uint64 { return x.rev }

// Working is the copy's number, or 0 when the actor has no draft. It is
// `ViewResult.Working` for the same snapshot, and see `pinned` for why a hook
// during a batch sees the number the batch started at.
func (x CaseContext) Working() uint64 { return x.work }

// Roles names every role this actor holds for this case: the primary role plus
// the ones a *committed* role-typed field confers (SYNTAX §5.2-T4), which is
// the set the masks in this snapshot are computed from. An edit that assigns a
// role confers nothing here, because the fold is taken from the committed
// state and not from the draft (§4.3) — and a hook is the most likely place in
// a host to guess wrong about that, so the answer is the folded set rather
// than the data behind it.
func (x CaseContext) Roles() []string {
	if x.img == nil {
		return nil
	}
	out := make([]string, 0, len(x.roles))
	for _, r := range x.roles {
		out = append(out, x.img.roleName(r))
	}
	return out
}

// Value reads one cell of the snapshot, at the address §5.1 spells and
// `Update` accepts. `false` covers three facts the caller cannot usefully
// separate: the path reaches no declaration, the node is not this actor's to
// see, and the cell holds nothing. The second is why the answer is not an
// error — §7's property 7 makes a withheld value indistinguishable from an
// absent one everywhere else in this package, and a hook is not an exception
// to that rule; it is the reason the rule exists.
//
// A `Validate` hook is handed the value it is being asked about, so reading its
// own path answers with what the cell held *before* this write: the hook's
// output is what gets stored, and until it returns there is nothing to read.
// That is the snapshot's own discipline rather than a wart — §6's chain runs
// apply, normalize, store — and it is why the proposed value arrives as an
// argument instead of being left for a hook to find.
func (x CaseContext) Value(path string) (Value, bool) {
	if x.s == nil {
		return Value{}, false
	}
	r, ok := x.s.resolve(path)
	if !ok || !r.hasValue || !x.seen(r.slot) {
		return Value{}, false
	}
	v := x.s.cellValue(r.slot, r.path())
	if !v.set {
		return Value{}, false
	}
	return x.img.load(r.slot, v), true
}

// Review answers the decision that stands about the unit governing this path,
// in the shape `ViewNode.Review` reports it — so a hook and a form cannot
// disagree about what was decided, and §5.9's rule that the table's zero value
// is the whole answer holds here too. `false` says the path is invisible or
// reaches no decided unit; a revoked decision answers `true` with `PENDING`,
// because "nothing stands" is a fact about the unit rather than a lack of one.
func (x CaseContext) Review(path string) (ReviewInfo, bool) {
	if x.s == nil {
		return ReviewInfo{}, false
	}
	r, ok := x.s.resolve(path)
	if !ok || !x.seen(r.slot) || x.img.slots[r.slot].verif == 0 {
		return ReviewInfo{}, false
	}
	return reviewInfoOf(x.img, x.reviews, r.slot), true
}

// Signature answers the attestation standing on a signature field, as
// `ViewNode.Signature` reports it, and with the same `false` for a path that is
// withheld or is not a signature field at all. An erased attestation is the
// zero value with `true`: §5.10's point that a field left signable and a field
// never signed are one fact about the *state*, with the difference belonging to
// history (§5.11) rather than to a hook.
func (x CaseContext) Signature(path string) (SignatureInfo, bool) {
	if x.s == nil {
		return SignatureInfo{}, false
	}
	r, ok := x.s.resolve(path)
	if !ok || !x.seen(r.slot) || x.img.slots[r.slot].sig == 0 {
		return SignatureInfo{}, false
	}
	return signatureInfoOf(x.img, x.sigs, r.slot), true
}

// ValidateFunc is a `Validate` hook: the value a host assigned, the address it
// was assigned at, and the snapshot around it, in exchange for the value the
// engine stores and re-asks (DESIGN §7, RUNTIME §6).
//
// The two contexts are different things and both are needed. Go's `ctx` is
// cancellation and deadline — the argument §10's contract requires of anything
// that may do I/O, which for a hook means an RPC-backed host function — and
// `at` is the case. DESIGN §7's sketch names only the case one; §10's rule is
// that the design records what shipped.
//
// A non-nil error means the value is refused. Which refusal that is depends on
// the call site, and that is the whole of §7's tri-state today: an error
// against a value being written abandons the batch with `HOOK_FAILURE`, because
// nothing was stored and the actor is the one to hear about it, while the same
// error against a *stored* value at submit or in a view is a `FieldProblem`
// about that field, because a read cannot un-store it. Giving a hook the word
// for "invalid, and keep it" is a decision about what an accepted `Update`
// reports, and it is RUNTIME §9's to make with the result contract rather than
// here.
type ValidateFunc func(ctx context.Context, at CaseContext, path string,
	v Value) (Value, error)

// OptionsFunc is an `Options` hook: the declared path it is bound at, and the
// snapshot, in exchange for the texts the field may hold.
//
// The path is the *declaration's* address, with no instance in it, and a
// provider's answer is fetched once per binding per view for the reason §5.7
// gives: the binding is a field's and so is its list, and a two-hundred-row
// collection is one question with two hundred renderings of the answer. A hook
// that needs the row to answer is being asked a per-row question, which is
// SYNTAX's F6 uniformity this language does not have — so the snapshot here is
// the case's, not an instance's, and `Value` reads what the form holds at
// paths the caller names.
type OptionsFunc func(ctx context.Context, at CaseContext,
	path string) ([]string, error)

// RegisterValidate installs the normalizer for one custom type's name,
// replacing whatever was bound there (RUNTIME §6, DESIGN §7's lookup rule).
//
// It cannot refuse a name no definition declares, and it does not try: a
// runtime holds no definition at the moment a host calls this, and §3.8's
// binding is a key rather than a symbol, so the check that would be needed is
// the one every case operation already pays when it resolves the image. What
// the key costs when it is wrong is stated by the two absence rules §6 keeps
// apart on purpose — an unbound `Validate` is identity, and a host that
// mistyped one gets unchanged values, while an unbound `Options` provider is
// `HOOK_FAILURE` naming the binding on the first view that needs the list.
func (r *Runtime) RegisterValidate(typeName string, fn ValidateFunc) {
	r.w.hooks.bindType(typeName, fn)
}

// RegisterOptions installs the choice provider for one field path, with the
// same replace-not-refuse rule and the same reason: the binding is the text a
// definition declared, and the runtime is not the place that knows whether a
// definition uses it.
func (r *Runtime) RegisterOptions(fieldPath string, fn OptionsFunc) {
	r.w.hooks.bindPath(fieldPath, fn)
}

// OnCommitFunc is the observer §6 and DESIGN §7 describe: it is told that a
// commit happened, with the record the store actually wrote and a snapshot of
// the state that commit left.
//
// It returns nothing, and that absence is the contract. DESIGN's sketch ends
// "→ observer result", but the only fact the runtime could act on is the
// commit's outcome, and the design forbids an observer from changing it: a
// result the engine must ignore is a result the signature should not carry. A
// void function states "never vetoes" in the type, the way `CaseContext`'s
// missing methods state invariant 9. What an observer does with a delivery
// failure — retry, queue, alert — is the host's, and DESIGN §7's outbox lives
// there: the engine holds no queue and runs no retry, because a runtime that
// delivered reliably would be a service, and this package is not one.
//
// An observer that panics propagates to the caller *after* the commit is
// durable: the act stands, and the panic is the host's bug to hear about.
// Nothing recovers it, because deciding what a broken host function means is
// exactly the policy this seam refuses to own.
type OnCommitFunc func(ctx context.Context, at CaseContext, com Commit)

// RegisterOnCommit appends one observer to the runtime's list; they are called
// in registration order, once per durable commit, by every operation that
// commits (RUNTIME §6 — an `Update` never commits, and a `Submit` whose form
// already held the content commits nothing and notifies nobody). A nil function
// is not added: an observer that observes nothing has no place in a list whose
// order is a host's own.
//
// It is a list and not a key, and the reason is §6's closing rule: the image
// records bindings only for the hooks a definition declares, and no declaration
// names an observer. An observer dispatches on `at.Definition()` if it cares
// which case moved — the same text §3.8 interned, and no new key the runtime
// could get wrong.
func (r *Runtime) RegisterOnCommit(fn OnCommitFunc) {
	r.w.hooks.bindCommit(fn)
}

// ClearOnCommit empties the observer list — the replace-not-refuse convenience
// the keyed registrations get by overwriting their map entry, stated for a list
// that has no key to overwrite. A deployment that wired an observer it must fix
// or stop mid-run does not need a new runtime for it, and cases opened before
// the call speak through the emptied list like any other registration change.
func (r *Runtime) ClearOnCommit() {
	r.w.hooks.clearCommit()
}
