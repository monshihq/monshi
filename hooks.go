package monshi

// The hook execution contract (RUNTIME §6; DESIGN §7).
//
// A `Validate` hook is host code, and what the image records about it is a
// *binding* — `hookKey` of two kinds, interned at lowering from a custom
// type's name or a field's path — so nothing in a definition can name a
// function, choose one by data, or reach one it did not declare. The
// engine's job is the mapping §6 describes: key to function, in a registry
// this package owns.
//
// Only the `Validate` half is mapped here. The `Options` binding arrives
// with the view, because the two differ in more than signature:
// normalization runs inside an edit and its result is stored, while options
// are fetched on demand for a view and gated by the same `view` permission
// as the values beside them (§6). They join this table when that lands, and
// the lookup below is already keyed by kind, so a path binding can never be
// mistaken for a type one.
//
// Registration itself is not exported yet. A hook's signature carries a
// read-only `CaseContext` (DESIGN §7), and exporting the seam before that
// type exists would fix a signature the next milestone has to break. Until
// then this is an in-package registry, and the code that reads it —
// `settle` below, which an edit calls per assignment, and `validate`, which
// Submit calls per stored value — is written as the whole §6 contract:
// looked up by the binding the definition declared, run before the value is
// stored, its output checked by the same test the input passed, and a
// failure heard as a failure.

import (
	"context"
	"sync"
)

// normFunc is a `Validate` hook's normalization step: it takes the value a
// host assigned and returns the value the engine stores and re-checks.
// Because what comes back is a `Value` and not a row payload, a hook speaks
// the boundary's language rather than the image's, and the caller settles it
// through the same `img.store` that settled the input. That is not
// belt-and-braces: a hook that returns the wrong class, or a decimal its
// type cannot hold, is a host bug with the row as its blast radius, so §6's
// "stores the normalized value for all later checks" means *after the same
// check*, never instead of it.
type normFunc func(ctx context.Context, v Value) (Value, error)

// hookBinding is the key a host would register against, spelled in text
// rather than in ordinals. Interning is per definition — two files that both
// declare `money` give it different `stringID`s — so a table keyed by the
// interned id would need one entry per compilation of the same name. The
// name is what the host has, and §3.9's rule that a name is a key only at
// the seam is exactly what this is.
type hookBinding struct {
	kind uint8 // hookByType or hookByPath
	name string
}

type hookSet struct {
	mu    sync.RWMutex
	byKey map[hookBinding]normFunc
}

func newHookSet() *hookSet {
	return &hookSet{byKey: map[hookBinding]normFunc{}}
}

// bind installs a normalizer for one binding, replacing whatever was there.
// Replacing rather than refusing a duplicate is the host's convenience: a
// registry that could not be pointed at a new function would leave no way to
// fix a hook without a new runtime.
func (h *hookSet) bind(kind uint8, name string, fn normFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byKey[hookBinding{kind: kind, name: name}] = fn
}

// bindingName renders what a definition declared a hook against — a custom
// type's name or a field's path — which is the only name a host's log line has
// for the function. It reads the image's own table rather than the caller's
// text, so the refusal about a hook says what the definition said.
func (img *image) bindingName(id hookID) string {
	if id == 0 || int(id) >= len(img.hooks) {
		return ""
	}
	return img.name(img.hooks[id].name)
}

// normalizer answers for one binding the image carries. A binding with no
// registered function is not an error: a custom type is a name for a type
// expression (SYNTAX §2), and `money : decimal(14, 2)` obliges nobody to
// write a hook. §6's "missing required hooks reject the update" is about a
// hook the definition *requires*, and in this language an attribute names no
// function, so nothing does — which is why the unbound case is identity and
// not a refusal.
func (h *hookSet) normalizer(img *image, id hookID) (normFunc, bool) {
	if id == 0 || int(id) >= len(img.hooks) {
		return nil, false
	}
	key := img.hooks[id]
	h.mu.RLock()
	fn := h.byKey[hookBinding{kind: key.kind, name: img.name(key.name)}]
	h.mu.RUnlock()
	return fn, fn != nil
}

// settle is §6's normalization chain for one assigned value, and the only
// place a hook's output is allowed to touch a row. It takes the value as the
// host wrote it and returns the payload to store: with no binding, or no
// function registered for it, that is the settling of the input, which costs
// one map lookup and no second conversion.
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
func (w *wiring) settle(ctx context.Context, refuse refuser, img *image,
	n nodeID, in Value) (slotValue, error) {
	out, bad := img.store(n, in)
	if bad != "" {
		return slotValue{}, refuse.now(InvalidOp, bad)
	}
	fn, hook := w.forType(img, n)
	if fn == nil {
		return out, nil
	}
	binding := quote(img.bindingName(hook))
	normalized, err := fn(ctx, in)
	if err != nil {
		return slotValue{}, refuse.caused(HookFailure,
			"the Validate hook for "+binding+" refused the value", err)
	}
	out, bad = img.store(n, normalized)
	if bad != "" {
		return slotValue{}, refuse.now(HookFailure, "the Validate hook for "+
			binding+" returned a value its type does not hold: "+bad)
	}
	return out, nil
}

// forType is the `Validate` binding a slot's type carries, resolved to a
// function: the answer `settle` and `validate` both need, and the reason
// they cannot disagree about which fields have a hook at all. A nil function
// means identity — no binding, a binding with nothing registered (§6), or a
// field with no type because it confers a role.
func (w *wiring) forType(img *image, n nodeID) (normFunc, hookID) {
	sl := &img.slots[n]
	if sl.role != 0 {
		return nil, 0
	}
	hook := img.types[sl.typ].hook
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
func (w *wiring) validate(ctx context.Context, refuse refuser, img *image,
	s *state, root nodeID) ([]FieldProblem, error) {
	var out []FieldProblem
	lo, hi := img.subtree(root)
	for n := lo; n < hi; n++ {
		if !s.present(n) {
			n += nodeID(img.slots[n].span) - 1 // §5.2's pruning, as §5.4's walk
			continue
		}
		sl := &img.slots[n]
		switch {
		case sl.kind != kindField:
			continue
		case sl.coll != 0 && img.rowLo(n) != n:
			// A repeated group's own slot holds rows, not a value; its
			// members are walked as the slots they are. A repeated *field*
			// does have a value per row (§5.1), so it is not skipped.
			continue
		case sl.typ != 0 && img.types[sl.typ].base == baseSignature:
			// A signature's content is `sigEntry`'s story (§3.6) and the
			// boundary has no class for it: milestone 5 asks a signature
			// whether it is valid, and this walk does not guess at it.
			continue
		}
		fn, hook := w.forType(img, n)
		if fn == nil {
			continue
		}
		binding := img.bindingName(hook)
		for _, p := range s.pathsOf(n) {
			v := s.cellValue(n, p)
			if !v.set {
				continue
			}
			in := img.load(n, v)
			got, err := fn(ctx, in)
			if err != nil {
				out = append(out, FieldProblem{
					Path: s.addr(n, p), Code: invalidValue,
					Msg: "the Validate hook for " + quote(binding) +
						" refused this value: " + err.Error()})
				continue
			}
			back, bad := img.store(n, got)
			if bad != "" {
				return nil, refuse.now(HookFailure, "the Validate hook for "+
					quote(binding)+
					" returned a value its type does not hold: "+bad)
			}
			if !img.same(n, v, back) {
				return nil, refuse.now(HookFailure, "the Validate hook for "+
					quote(binding)+" rewrote a stored value, at "+
					s.addr(n, p))
			}
		}
	}
	return out, nil
}
