package engine

import (
	"context"

	"github.com/monshihq/monshi/internal/program"
)

// CaseContext is what a `Validate` or `Options` hook is handed — and what an
// `OnCommit` observer is handed after the commit — the snapshot the operation
// was already reading, pinned to the actor it is reading for.
//
// It has no methods that write, and it holds no handle: a host cannot reach
// `Update` from inside a hook. A zero value is inert — every read answers the
// way an unseen one does — because a host that stores a `CaseContext` in a
// struct gets the zero value whether it meant to or not.
type CaseContext struct {
	img     *program.Program
	s       *state // the operation's own clone: the draft, or the commit
	reviews []reviewState
	sigs    []sigState       // the committed verdict tables, never the draft's
	roles   []program.RoleID // folded from committed data
	stage   program.StageID  // where the case stands, not where the draft imagines
	only    []bool           // the caller's visibility fold, nil when it has none
	id      CaseID
	def     string
	actor   Actor
	rev     uint64 // the committed head this snapshot was cut at
	work    uint64 // the copy's number, 0 when there is no copy
}

// pinned is the one construction, and every caller already holds all six
// arguments: `Case.Update` and `Case.Submit` pass the candidate and no fold,
// `Case.View` passes the draft it just cloned and the fold it computed for the
// same walk.
//
// `work` is the copy's number as the operation found it, not as it will save
// it: a hook that runs during a batch sees a snapshot whose `Working` is the
// revision the caller presented, because the batch is not the store's yet.
func (c Case) pinned(img *program.Program, rec caseHead, s *state, roles []program.RoleID,
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
// whole reason it exists — the effects erase verdicts inside the store's
// commit, so the caller's `next` can be a state the commit did not leave. Every
// field here is what the commit produced: `rev` is the number the entry got,
// the tables are post-erasure, and `work` is the committer's copy as the same
// locked act left it. `only` is the fold the dispatcher rendered the record
// through, so an observer's read of a path and its copy of the entry naming
// that path answer with one mask — the agreement rule again, one construction
// away.
func (c Case) pinnedPost(img *program.Program, rec caseHead, got committed,
	roles []program.RoleID, only []bool) CaseContext {
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

// seen checks whether the actor could see the node in a view of this snapshot.
// Reuse the visibility mask when the caller has already computed one.
//
// The distinction only bites on a container. A value lives at a leaf, and a
// leaf's fold is its own bit: the inheritance is top-down, so nothing below a
// leaf can grant it. That is what closes the hole the rule exists to close.
// What it deliberately does not hide is a unit's verdict showing on a container
// the caller *can* render: a form node carries its decision in
// `ViewNode.Review`, and a hook reading that path is reading what the caller
// was already shown.
//
// With a precomputed view mask, each read costs one lookup. During Update and
// Submit, checking the node's subtree avoids building a full-case mask for a
// hook that may read only one field.
//
// Every caller has already answered for the zero snapshot: a read that gets
// this far holds a state, and `pinned` builds no state without the image it
// was resolved against, so the walks below are guarded once rather than twice.
func (x CaseContext) seen(n program.NodeID) bool {
	if x.only != nil {
		return x.only[n]
	}
	lo, hi := x.img.Subtree(n)
	for m := program.NodeID(lo); m < program.NodeID(hi); m++ {
		if x.img.Effective(x.roles, x.stage, m)&program.PermView != 0 {
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
func (x CaseContext) addrOf(n program.NodeID, p instPath) string {
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
// field confers a role *to*, and the primary role the host asserted. A hook
// that logs or delegates needs this and cannot derive it from data, because two
// users of one case share one draft by design.
func (x CaseContext) Actor() Actor { return x.actor }

// Stage is the case's current stage name, read from the committed snapshot.
// A draft cannot move a stage, so this is one answer for every hook of a call.
func (x CaseContext) Stage() string {
	if x.img == nil {
		return ""
	}
	return x.img.StageName(x.stage)
}

// Revision returns the committed revision used by the operation that supplied
// this context.
func (x CaseContext) Revision() uint64 { return x.rev }

// Working is the copy's number, or 0 when the actor has no draft. It is
// `ViewResult.Working` for the same snapshot, and see `pinned` for why a hook
// during a batch sees the number the batch started at.
func (x CaseContext) Working() uint64 { return x.work }

// Roles names every role this actor holds for this case: the primary role plus
// the ones a *committed* role-typed field confers, which is
// the set the masks in this snapshot are computed from. An edit that assigns a
// role confers nothing here, because the fold is taken from the committed
// state and not from the draft — and a hook is the most likely place in
// a host to guess wrong about that, so the answer is the folded set rather
// than the data behind it.
func (x CaseContext) Roles() []string {
	if x.img == nil {
		return nil
	}
	out := make([]string, 0, len(x.roles))
	for _, r := range x.roles {
		out = append(out, x.img.RoleName(r))
	}
	return out
}

// Value reads the cell at path. It returns false for an unknown path, a hidden
// node, or an unset cell, so a hook cannot use it to distinguish hidden data
// from missing data.
//
// A `Validate` hook is handed the value it is being asked about, so reading its
// own path answers with what the cell held *before* this write: the hook's
// output is what gets stored, and until it returns there is nothing to read.
// That is the snapshot's own discipline rather than a wart — the chain runs
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
	if !v.Set {
		return Value{}, false
	}
	return load(x.img, r.slot, v), true
}

// Review answers the decision that stands about the unit governing this path,
// in the shape `ViewNode.Review` reports it — so a hook and a form cannot
// disagree about what was decided, and the rule that the table's zero value
// is the whole answer holds here too. `false` says the path is invisible or
// reaches no decided unit; a revoked decision answers `true` with `PENDING`,
// because "nothing stands" is a fact about the unit rather than a lack of one.
func (x CaseContext) Review(path string) (ReviewInfo, bool) {
	if x.s == nil {
		return ReviewInfo{}, false
	}
	r, ok := x.s.resolve(path)
	if !ok || !x.seen(r.slot) || x.img.Slot(r.slot).Verif == 0 {
		return ReviewInfo{}, false
	}
	return reviewInfoOf(x.img, x.reviews, r.slot), true
}

// Signature answers the attestation standing on a signature field, as
// `ViewNode.Signature` reports it, and with the same `false` for a path that is
// withheld or is not a signature field at all. An erased attestation is the
// zero value with `true`: the point that a field left signable and a field
// never signed are one fact about the *state*, with the difference belonging to
// history rather than to a hook.
func (x CaseContext) Signature(path string) (SignatureInfo, bool) {
	if x.s == nil {
		return SignatureInfo{}, false
	}
	r, ok := x.s.resolve(path)
	if !ok || !x.seen(r.slot) || x.img.Slot(r.slot).Sig == 0 {
		return SignatureInfo{}, false
	}
	return signatureInfoOf(x.img, x.sigs, r.slot), true
}

// ValidateFunc is a `Validate` hook: the value a host assigned, the address it
// was assigned at, and the snapshot around it, in exchange for the value the
// engine stores and re-asks.
//
// ctx carries the caller's cancellation and deadline. at provides read-only
// access to the case as this actor sees it.
//
// A validation error during Update rejects the batch. During Submit or View,
// rejecting an already stored value produces a FieldProblem at its path
// instead.
type ValidateFunc func(ctx context.Context, at CaseContext, path string,
	v Value) (Value, error)

// OptionsFunc is an `Options` hook: the declared path it is bound at, and the
// snapshot, in exchange for the texts the field may hold.
//
// The path is the *declaration's* address, with no instance in it. A hook that
// needs the row to answer is being asked a per-row question.
type OptionsFunc func(ctx context.Context, at CaseContext,
	path string) ([]string, error)

// RegisterValidate installs the normalizer for one custom type's name,
// replacing whatever was bound there.
//
// It cannot refuse a name no definition declares, and it does not try: a
// runtime holds no definition at the moment a host calls this, and the binding
// is a key rather than a symbol, so the check that would be needed is the one
// every case operation already pays when it resolves the image.
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

// OnCommitFunc observes a durable commit and its resulting case snapshot.
//
// It returns nothing, and that absence is the contract. A void function states
// "never vetoes" in the type. What an observer does with a delivery failure —
// retry, queue, alert — is the host's, and the outbox lives there: the engine
// holds no queue and runs no retry, because a runtime that delivered reliably
// would be a service, and this package is not one.
//
// An observer that panics propagates to the caller *after* the commit is
// durable: the act stands, and the panic is the host's bug to hear about.
// Nothing recovers it, because deciding what a broken host function means is
// exactly the policy this seam refuses to own.
type OnCommitFunc func(ctx context.Context, at CaseContext, com Commit)

// RegisterOnCommit appends one observer to the runtime's list; they are called
// in registration order, once per durable commit, by every operation that
// commits (an `Update` never commits, and a `Submit` whose form
// already held the content commits nothing and notifies nobody). A nil function
// is not added: an observer that observes nothing has no place in a list whose
// order is a host's own.
//
// It is a list and not a key, and the reason is the closing rule: the image
// records bindings only for the hooks a definition declares, and no declaration
// names an observer. An observer dispatches on `at.Definition()` if it cares
// which case moved.
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
