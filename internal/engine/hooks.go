package engine

import (
	"context"
	"sync"

	"github.com/monshihq/monshi/internal/program"
)

// normFunc is the engine's name for one of the two halves of a `Validate`
// hook — the normalization step, spelled by the host-facing contract as
// `ValidateFunc` (context.go). What is this file's business is what comes
// back: because it is a `Value` and not a row payload, a hook speaks the
// boundary's language rather than the image's, and the caller settles it
// through the same `img.store` that settled the input. That is not
// belt-and-braces: a hook that returns the wrong class, or a decimal its
// type cannot hold, is a host bug with the row as its blast radius, so §6's
// "stores the normalized value for all later checks" means *after the same
// check*, never instead of it.
type normFunc = ValidateFunc

// optsFunc is the same alias for a choice provider, whose host-facing name is
// `OptionsFunc`. The rules that live here rather than there are about
// absence: a failure is heard as `HOOK_FAILURE` about the binding, and a
// *missing* provider refuses the same way, because SYNTAX says a missing hook
// is a runtime error and not silent success and DESIGN §7 says an options
// field with nobody to ask has no list to invent. The list itself is display
// data, never stored and never asked by a condition (§3.3, E2).
type optsFunc = OptionsFunc

type hookSet struct {
	mu       sync.RWMutex
	byType   map[string]normFunc
	byPath   map[string]optsFunc
	onCommit []OnCommitFunc
}

func newHookSet() *hookSet {
	return &hookSet{
		byType: map[string]normFunc{},
		byPath: map[string]optsFunc{},
	}
}

// bindType installs a normalizer for one custom type's name, replacing
// whatever was there. Replacing rather than refusing a duplicate is the
// host's convenience: a registry that could not be pointed at a new function
// would leave no way to fix a hook without a new runtime.
func (h *hookSet) bindType(name string, fn normFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byType[name] = fn
}

// bindPath installs a choice provider for one field path, with the same
// replace-not-refuse rule.
func (h *hookSet) bindPath(name string, fn optsFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byPath[name] = fn
}

// bindCommit appends one observer, skipping a nil: §6's list answers "who is
// told when a commit lands", and a nil entry would be an empty seat at it.
func (h *hookSet) bindCommit(fn OnCommitFunc) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onCommit = append(h.onCommit, fn)
}

// clearCommit empties the observer list. Replacing the whole list rather than
// one entry is honest about what this seam is: an unkeyed host wiring, where
// entry-by-entry surgery would mean the runtime invented keys §6 says only the
// definition's two bindings have.
func (h *hookSet) clearCommit() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onCommit = nil
}

// commitFns hands out the header under the read lock. Safe to call after the
// lock drops for the standing replace-never-mutate reason: an append either
// copies the array or writes past a length this reader's header does not
// reach, and a clear replaces the header rather than emptying the array.
func (h *hookSet) commitFns() []OnCommitFunc {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.onCommit
}

// notify runs §6's `OnCommit` observers for one durable commit, at the tail
// of the operation that made it — after the store's lock is released, which is
// how DESIGN §7's "never inside a long-held storage transaction" lands in an
// in-memory store: the transaction is a few array swaps, and the rule is about
// not calling host code while holding it, so an observer may re-enter the case
// (read it, even write a fresh batch against the new revision) without meeting
// a lock the engine still holds.
//
// The order is the contract's, and it is not the design's: `Commit` carries
// the store's own `at`, and observers registered earlier are called earlier,
// because a host that put its audit before its fan-out meant that. An
// unwired host pays one nil-slice read here and nothing else — no fold, no
// render, no snapshot — which is what keeps §1's minimal-dependency promise
// true of a feature a deployment does not use.
//
// The record and the snapshot are cut from the same fold: the roles are the
// committing actor's, re-derived from the state *this* commit installed — a
// submit that assigns a role has granted it by the time the observer is told —
// and the visibility array is the one `commitView` renders through, so an
// entry the record does not name is a path the context answers as absent. That
// is §7's property 7 with a notification as its subject: the observer is host
// code, and it sees what its caller saw, no more.
func (c Case) notify(ctx context.Context, img *program.Program, rec caseHead,
	got committed) {
	fns := c.w.hooks.commitFns()
	if len(fns) == 0 {
		return
	}
	roles := rolesHeld(img, c.actor, got.post.s)
	vis := viewable(img, roles, got.post.stage)
	com, _ := commitView(img, got.rec, roles, got.post.stage, vis)
	at := c.pinnedPost(img, rec, got, roles, vis)
	for _, fn := range fns {
		fn(ctx, at, com)
	}
}

// normalizer answers for one binding the image carries. A binding with no
// registered function is not an error: a custom type is a name for a type
// expression (SYNTAX §2), and `money : decimal(14, 2)` obliges nobody to
// write a hook. §6's "missing required hooks reject the update" is about a
// hook the definition *requires*, and in this language an attribute names no
// function, so nothing does — which is why the unbound case is identity and
// not a refusal. A binding of the wrong kind — a field path, which belongs
// to `provider` — answers not-found the same way, because the two vocabularies
// address different questions and the split maps hold that by structure.
func (h *hookSet) normalizer(img *program.Program, id program.HookID) (normFunc, bool) {
	kind, name, bound := img.BindingText(id)
	if !bound || kind != program.HookByType {
		return nil, false
	}
	h.mu.RLock()
	fn := h.byType[name]
	h.mu.RUnlock()
	return fn, fn != nil
}

// provider answers for an `options` field's path binding. The missing case
// is *not* identity: DESIGN §7's rule is per kind precisely because an
// options field with no provider has no answer, so the caller refuses (the
// refusal is `view`'s to build, from the binding's own name).
func (h *hookSet) provider(img *program.Program, id program.HookID) (optsFunc, bool) {
	kind, name, bound := img.BindingText(id)
	if !bound || kind != program.HookByPath {
		return nil, false
	}
	h.mu.RLock()
	fn := h.byPath[name]
	h.mu.RUnlock()
	return fn, fn != nil
}

// settle is §6's normalization chain for one assigned value, and the only
// place a hook's output is allowed to touch a row. It takes the value as the
// host wrote it and returns the payload to store: with no binding, or no
// function registered for it, that is the settling of the input, which costs
// one map lookup and no second conversion.
//
// The hook is handed the cell's own address — `at`'s state spelled at `n` and
// `p`, the same text `Update` takes back — because §7 wants a normalizer to be
// able to consider its dependencies, and a value with no address cannot say
// which row it belongs to. The address is only computed when there is a
// function to hand it to: an unbound assignment pays for the lookup and
// nothing else, which is what keeps this the cheapest hook call site. And for
// a field outside a collection the address is the declaration's own interned
// path, so even a bound assignment builds no text — a cell inside a row is the
// one shape that has to be spelled.
//
// The binding consulted is the field's *type*'s — `Validate` by custom type
// (DESIGN §7). A slot's own `hook` is deliberately not consulted here: an
// options field's binding is `Options` by path, which answers "which values
// may this hold" for a view and is the view's to call. Conflating the two
// would mean a dropdown's option list being run as a normalizer, which is
// not a thing any definition asked for.
//
// A hook's output goes through the same `img.store` as its input, and the two
// failures are reported apart on purpose. A caller who assigned the wrong kind
// of value made a request the engine would not carry out, and gets
// INVALID_OPERATION about its own path. A hook that returned something the
// field's type cannot hold broke a rule the *host* was relying on, and gets
// HOOK_FAILURE naming the binding: blaming the caller for a function it did
// not write would send it to debug the wrong half of its own code.
//
// `refuse` names the operation the refusal is about, for the reason store.go
// gives its own: a host reads `CaseError.Op` to find the call, and a hook
// failure heard during a submit that said "update" would send it to the wrong
// line of its own code. It arrives bound rather than as a word the hook layer
// re-types, which is how `validate` below answers in the same voice.
func (w *wiring) settle(ctx context.Context, refuse refuser, img *program.Program,
	n program.NodeID, p instPath, at CaseContext, in Value) (program.Cell, error) {
	out, bad := store(img, n, in)
	if bad != "" {
		return program.Cell{}, refuse.now(InvalidOp, bad)
	}
	fn, hook := w.forType(img, n)
	if fn == nil {
		return out, nil
	}
	normalized, err := fn(ctx, at, at.addrOf(n, p), in)
	if err != nil {
		return program.Cell{}, refuse.caused(HookFailure,
			"the Validate hook for "+quote(img.BindingName(hook))+
				" refused the value", err)
	}
	out, bad = store(img, n, normalized)
	if bad != "" {
		return program.Cell{}, refuse.now(HookFailure, "the Validate hook for "+
			quote(img.BindingName(hook))+
			" returned a value its type does not hold: "+bad)
	}
	return out, nil
}

// forType is the `Validate` binding a slot's type carries, resolved to a
// function: the answer `settle` and `validate` both need, and the reason
// they cannot disagree about which fields have a hook at all. A nil function
// means identity — no binding, a binding with nothing registered (§6), or a
// field with no type because it confers a role.
func (w *wiring) forType(img *program.Program, n program.NodeID) (normFunc, program.HookID) {
	sl := img.Slot(n)
	if sl.Role != 0 {
		return nil, 0
	}
	hook := img.Type(sl.Type).Hook
	fn, have := w.hooks.normalizer(img, hook)
	if !have {
		return nil, hook
	}
	return fn, hook
}

// validate is §6.3's "final validation using current rules/hooks": the
// reached fields of one subtree, each stored value run through the binding
// its type declares, and every refusal collected as a problem about that
// field.
//
// Why the engine asks a question it asked when the value was written: the
// rules are host code and the host may have changed them, and DESIGN §3's
// answer about a reloaded definition is that old content becomes *invalid*,
// not incompatible. Submit is the operation entitled to find that out, and
// the only one that acts on it — an edit that stored a value under a hook
// that accepted it was not wrong, and §5.2's `required` still cannot see a
// value a hook later came to refuse.
//
// Unset values are not asked about. `Empty` leaves a cell with no value, and
// E6 makes a comparison over absent data unsatisfied rather than invalid, so
// the hook's silence about nothing is the definition's: §5.4's completeness
// pass is what reports an empty required field, and reporting it twice under
// two different headings would make a host guess which one its UI shows.
//
// The two ways a hook can misbehave are refused rather than collected, and
// they are §6's `HOOK_FAILURE` for the same reason `settle` gives it: a hook
// that returns a value its type cannot hold, or one that rewrites a value the
// row already holds, is host code breaking the contract the engine stores on.
// The second is the one worth stating, because it is the alternative to
// quietly committing something the actor never presented: §6's chain says a
// normalized value is what "all later checks" see, and Submit *is* that later
// check, so a hook that is not a fixed point on its own output has a bug to
// fix, and a refusal naming the binding is where it learns so.
//
// The caller's filter rides in the snapshot as `only`, and the two callers
// differ for one reason. Submit's carries none: the actor presenting a form has
// the grant the rules are being asked about, so every reached field of that
// subtree is its business. The view's carries the slots this actor may see,
// because a host's rule is not entitled to run over content the caller could
// not render — both as work and as disclosure. A problem about a withheld field
// would be a message naming a value the caller may not see, and a hook that
// broke the fixed-point rule there would refuse the whole view with that path
// in the sentence. Neither is a fact about what the caller is allowed to know,
// so neither is computed. The filter is applied here rather than left to
// `CaseContext`'s own mask rule because a walk that reports paths must not even
// ask about one it will not report.
func (w *wiring) validate(ctx context.Context, refuse refuser, at CaseContext,
	root program.NodeID) ([]FieldProblem, error) {
	img, s, only := at.img, at.s, at.only
	var out []FieldProblem
	lo, hi := img.Subtree(root)
	for n := lo; n < hi; n++ {
		if !s.present(n) {
			n += program.NodeID(img.Slot(n).Span) - 1 // §5.2's pruning, as §5.4's walk
			continue
		}
		if only != nil && !only[n] {
			continue // §6.1's "validation errors" line is a per-actor answer
		}
		sl := img.Slot(n)
		switch {
		case sl.Kind != program.KindField:
			continue
		case sl.Coll != 0 && img.RowLo(n) != n:
			// A repeated group's own slot holds rows, not a value; its
			// members are walked as the slots they are. A repeated *field*
			// does have a value per row (§5.1), so it is not skipped.
			continue
		case sl.Type != 0 && img.Type(sl.Type).Base == program.BaseSignature:
			// A signature's content is `sigEntry`'s story (§3.6) and the
			// boundary has no class for it: `Sign` asks a signature
			// whether it is valid, and this walk does not guess at it.
			continue
		}
		fn, hook := w.forType(img, n)
		if fn == nil {
			continue
		}
		binding := img.BindingName(hook)
		for _, p := range s.pathsOf(n) {
			v := s.cellValue(n, p)
			if !v.Set {
				continue
			}
			// The address is computed once per cell here rather than on the
			// two paths that report it, because the hook is handed the same
			// text: a problem about a value and the question about it are
			// stated in one vocabulary, which is what lets a host line a
			// refusal up with the field it was asked about.
			addr := s.addr(n, p)
			in := load(img, n, v)
			got, err := fn(ctx, at, addr, in)
			if err != nil {
				out = append(out, FieldProblem{
					Path: addr, Code: invalidValue,
					Msg: "the Validate hook for " + quote(binding) +
						" refused this value: " + err.Error()})
				continue
			}
			back, bad := store(img, n, got)
			if bad != "" {
				return nil, refuse.now(HookFailure, "the Validate hook for "+
					quote(binding)+
					" returned a value its type does not hold: "+bad)
			}
			if !img.Same(n, v, back) {
				return nil, refuse.now(HookFailure, "the Validate hook for "+
					quote(binding)+" rewrote a stored value, at "+addr)
			}
		}
	}
	return out, nil
}
