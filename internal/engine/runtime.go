package engine

import (
	"context"

	"github.com/monshihq/monshi/internal/program"
)

// CaseID names one case. The store issues them and 0 means "no such
// case", because a runtime never derives a key from the content it reads
// (§3.1) and a host's identifier has to stay opaque here.
type CaseID uint64

func (id CaseID) String() string { return "case " + dec(uint64(id)) }

// UserID names one actor in the host's own vocabulary. A role-typed
// field's value *is* such an identity (SYNTAX §5.2-T4), so the value is
// text: interpretation never asks anything of it but equality.
//
// An empty UserID is "nobody", and nobody matches nothing. A field that
// was never set carries no value at all, and RUNTIME §5.2 is just as clear
// that a field a host *cleared* holds an empty value rather than an absent
// one — so on both readings of "empty", allowing it to name a user would
// hand whatever role that field confers to whoever has no identity. That
// is a default-deny answer, which is the family §7's rules are in; a
// host's own identifier space is not this package's to validate.
type UserID string

func (u UserID) String() string { return quote(string(u)) }

// Actor is who is asking: an authenticated identity plus the primary role
// the host asserts (DESIGN §4.3). Core does not authenticate — it
// believes the identity it is given and derives everything else from
// committed data, which is why a role assigned by an uncommitted edit
// confers nothing (§4.3, SYNTAX §5.5-P6).
type Actor struct {
	User UserID

	// PrimaryRole is the role name the host authenticated, or "" for an
	// actor holding no role in this domain. A name the definition does not
	// declare confers nothing rather than failing the request: default-deny
	// already refuses whatever the actor cannot reach (SYNTAX §5.6-P1), and
	// a host's directory is not this package's to validate.
	PrimaryRole string
}

// wiring is what a handle resolves through: the definition registry, the
// store, and the host's hook functions. It is immutable — the three fields
// are set once by NewRuntime and never reassigned — which is what makes
// DESIGN §6's "no engine pointer, no independently mutable state" literally
// true. A handle can look things up and cannot write anything the runtime
// itself would not see. Each field's *contents* change, through its own
// lock and only through an explicit act, so a reloaded definition or a
// newly registered hook reaches handles opened before it.
type wiring struct {
	defs  *registry
	cases caseBackend
	hooks *hookSet
}

// Runtime binds a definition registry, case backend, and host hooks.
type Runtime struct {
	w *wiring
}

// NewRuntime assembles an empty runtime: no definitions, no cases, no
// hooks.
func NewRuntime() *Runtime {
	return &Runtime{w: &wiring{
		defs:  newRegistry(),
		cases: newCaseStore(),
		hooks: newHookSet(),
	}}
}

// Register adds an already-loaded definition to the registry, refusing a
// duplicate ID. Replacing is a separate act — Replace — because a reloaded
// definition reinterprets every case that names it (DESIGN §3), and a host
// that means to swap one under live cases should have to say so.
func (r *Runtime) Register(d *Definition) error {
	return r.insert(newRefuser("register", 0), d, false)
}

// Replace registers a definition under its ID whether or not one is
// there: the host's explicit reload (DESIGN §3 — Monshi watches no files).
// Existing cases keep their data and are read through the new definition
// from their next operation; nothing is rewritten (invariant 8).
//
// A case whose stored data was built under the old definition is refused
// with SCHEMA_MISMATCH rather than reinterpreted into nonsense: the slot
// numbering a snapshot depends on is the definition's, so a mismatch is
// heard as a mismatch and not answered by guessing (DESIGN §3 checks
// compatibility when it is necessary, which is here). Replacing with the
// *same* definition ID from a source that did not change is always safe;
// a host that changes a definition in place decides whether its existing
// cases are re-created or migrated.
func (r *Runtime) Replace(d *Definition) error {
	return r.insert(newRefuser("replace", 0), d, true)
}

// insert is the registry write both Register and Replace need: one
// refusal shape, one detail naming the operation that asked.
func (r *Runtime) insert(refuse refuser, d *Definition, replace bool) error {
	if d == nil {
		return refuse.now(InvalidOp, "no definition was given")
	}
	return r.w.defs.add(refuse, d, replace)
}

// Definition returns the registered definition with that ID.
func (r *Runtime) Definition(id string) (*Definition, bool) {
	return r.w.defs.get(id)
}

// Create makes a case of a registered definition and returns it opened for
// the actor (DESIGN §3: Create selects the definition by ID). The new case
// sits at the definition's initial stage at revision 0 with its declared
// defaults materialized into its data (§5.1), and no commit is appended —
// that is what makes the first successful operation revision 1 (DESIGN §5,
// invariant 2).
func (r *Runtime) Create(ctx context.Context, definition string,
	actor Actor) (Case, error) {
	d, ok := r.w.defs.get(definition)
	if !ok {
		return Case{}, newRefuser("create", 0).now(InvalidOp,
			"no definition is registered as "+quote(definition))
	}
	id, err := r.w.cases.create(ctx, newRefuser("create", 0), d)
	if err != nil {
		return Case{}, err
	}
	c := Case{id: id, def: d.ID(), actor: actor, w: r.w}
	// The case is now open for this actor, which is DESIGN §5's trigger for a
	// working state — the same act `Open` performs, for the same reason: an
	// edit that gets refused afterwards must not leave behind a copy the
	// caller never saw created (`Open` below says why that matters).
	if _, err := r.w.cases.working(ctx, c.refusing("create"), actor.User,
		d.img); err != nil {
		return Case{}, err
	}
	return c, nil
}

// Open resolves a case through the definition ID its own record stores
// (DESIGN §3) and hands back a handle. Nothing case-shaped is read into
// it: frames come from the store on each operation, so the handle stays a
// handle — and an unknown case, or one whose definition is not registered,
// fails here rather than at first use.
func (r *Runtime) Open(ctx context.Context, id CaseID, actor Actor) (Case, error) {
	c := Case{id: id, actor: actor, w: r.w}
	d, rec, err := c.look(ctx, c.refusing("open"))
	if err != nil {
		return Case{}, err
	}
	// The handle carries the definition the *record* names, not the one the
	// caller would have guessed: that is DESIGN §3's "Open resolves it
	// through the case record's stored definition ID", and it is why a host
	// that lost track of which case is which still reads the right schema.
	c.def = rec.def

	// DESIGN §5 says a working state is first created when the user opens the
	// case, so this is where the copy appears — not at the first edit. The
	// difference is not bookkeeping: an operation that then *refuses* has
	// changed nothing it was shown, because the copy it wrote on already
	// existed (§9's no-persistent-effect guarantee, which an edit cannot make
	// true for itself).
	if _, err := r.w.cases.working(ctx, c.refusing("open"), actor.User,
		d.img); err != nil {
		return Case{}, err
	}
	return c, nil
}

// Case is a handle on one case for one actor: an ID, the definition that
// governs it, the caller, and the runtime's immutable wiring. Four words
// and no mutable state (DESIGN §6). Copying it is free and means the same
// thing, and two handles for one actor read and edit one working copy.
type Case struct {
	id    CaseID
	def   string
	actor Actor
	w     *wiring
}

// ID is the case this handle addresses.
func (c Case) ID() CaseID { return c.id }

// Actor is the caller this handle acts as.
func (c Case) Actor() Actor { return c.actor }

// Definition returns the definition governing this case. The second
// answer is false when the case is unknown or its definition is not
// registered — the same refusal every operation on this handle returns.
//
// It resolves the name through the registry on every call and keeps no
// compiled handle of its own, so a Replace is visible here immediately
// (DESIGN §3). The *name* is what the handle carries, because a case's
// definition is fixed when the case is created: Open read it from the
// record, Create was told it, and no operation changes it. A zero handle —
// the value a host holds if it ignored Create's error — reports false
// rather than dereferencing the wiring it never got.
func (c Case) Definition() (*Definition, bool) {
	if c.w == nil {
		return nil, false
	}
	return c.w.defs.get(c.def)
}

// Stage answers the case's current stage name. `Transition` is the operation
// that changes it (DESIGN §6.4), and no other answer a caller holds can move
// it: a submit grafts content and keeps the stage it found (RUNTIME §5.1).
func (c Case) Stage(ctx context.Context) (string, error) {
	_, rec, err := c.look(ctx, c.refusing("stage"))
	if err != nil {
		return "", err
	}
	return rec.commit.stageName(), nil
}

// Revision is the case's committed revision: the value a Submit, Review,
// Sign or Transition must present as the base it observed (DESIGN §6.4,
// invariant 5).
func (c Case) Revision(ctx context.Context) (uint64, error) {
	_, rec, err := c.look(ctx, c.refusing("revision"))
	if err != nil {
		return 0, err
	}
	return rec.revision, nil
}

// look resolves the handle against the store: the case's record and the
// definition it names. An unknown case is NOT_FOUND; a case whose
// definition is not registered is SCHEMA_MISMATCH, because the definition
// is what makes the stored data mean anything (DESIGN §3). Neither returns
// a half-read state, and neither retries with a fallback: a runtime never
// re-runs the front end to recover (RUNTIME §1).
//
// The record is returned by value, which is what makes the snapshot's
// scalars one consistent read rather than a race across the store's lock.
//
// `refuse` is the calling operation's own refuser, and this is the reason the
// type exists rather than a nicety: a host reads `CaseError.Op` to learn which
// call refused, so a shared lookup reporting "open" while the host called
// `Stage` would send it to the wrong line of its own code. Passing the binding
// down is also what lets the store's refusals — an unknown case, a stale
// copy — be answered in the caller's voice without any of them naming an
// operation it never saw.
//
// ctx is the caller's, and the store now reads it too: the seam gained one
// parameter per method with milestone 4 so that a host's backend can cancel
// and time out (DESIGN §8), and the in-memory path discards it.
func (c Case) look(ctx context.Context, refuse refuser) (
	*Definition, caseHead, error) {
	// A zero Case is what a host holds if it ignored Create's error. It is
	// not a handle on anything, and the answer is the refusal the store
	// already gives for an ID it never issued — not a nil dereference inside
	// a package the host cannot step through.
	if c.w == nil {
		return nil, caseHead{}, refuse.now(NotFound,
			"this handle was never opened from a runtime")
	}
	rec, err := c.w.cases.load(ctx, refuse)
	if err != nil {
		return nil, caseHead{}, err
	}
	d, ok := c.w.defs.get(rec.def)
	if !ok {
		return nil, caseHead{}, refuse.now(SchemaMismatch,
			"definition "+quote(rec.def)+
				" governs this case and is not registered")
	}
	// A reload replaces the definition, not the data (DESIGN §3): the same
	// stored values are interpreted through the newly loaded image. That is
	// meaningful only while the ordinal space is the one the case was
	// written into, so a definition whose shape moved is refused, and one
	// whose shape held is re-pointed. compat.go says what "shape" means and
	// what is deliberately not part of it.
	if img := rec.commit.s.img; img != d.img {
		if detail, ok := program.DataDrift(img, d.img); !ok {
			return nil, caseHead{}, refuse.now(SchemaMismatch, detail)
		}
		rec.commit.s.reinterpret(d.img)
	}
	return d, rec, nil
}

// reinterpret points a state at the definition its case is now read
// through. The caller has proved the two images agree on the ordinal space;
// this is a pointer swap on a value the store handed out as a copy, so it
// writes no case data (invariant 8) and is what DESIGN §3's "data is
// interpreted using the newly loaded definition" costs at runtime: nothing
// until a reload actually happened, because image identity answers the
// common case in one comparison (§7.4).
func (s *state) reinterpret(img *program.Program) { s.img = img }
