package monshi

import (
	"context"
	"io"

	"github.com/monshihq/monshi/internal/engine"
)

// CaseID is an opaque store-issued case identifier. Zero denotes no case.
type CaseID = engine.CaseID

// UserID is an actor's opaque identity. The empty identity holds no assigned role.
type UserID = engine.UserID

// Actor is the authenticated identity and primary role supplied by the host.
type Actor = engine.Actor

// Runtime owns the definition registry, case backend, and hook bindings. It is safe for concurrent use.
type Runtime engine.Runtime

// NewRuntime creates an empty runtime with a native in-memory case store.
func NewRuntime() *Runtime { return (*Runtime)(engine.NewRuntime()) }

// Define loads and registers a definition, refusing duplicate IDs.
func (r *Runtime) Define(id string, src io.Reader) (*Definition, error) {
	d, err := Load(id, src)
	if err != nil {
		return nil, err
	}
	if err = r.Register(d); err != nil {
		return nil, err
	}
	return d, nil
}

// Register installs an already compiled definition, refusing duplicate IDs.
func (r *Runtime) Register(d *Definition) error {
	return (*engine.Runtime)(r).Register((*engine.Definition)(d))
}

// Replace installs a definition under its ID. Open handles resolve it on their next operation.
func (r *Runtime) Replace(d *Definition) error {
	return (*engine.Runtime)(r).Replace((*engine.Definition)(d))
}

// Definition returns the registered definition and whether it exists.
func (r *Runtime) Definition(id string) (*Definition, bool) {
	d, ok := (*engine.Runtime)(r).Definition(id)
	return (*Definition)(d), ok
}

// Create creates a case at its initial stage and opens a working copy for the actor.
func (r *Runtime) Create(ctx context.Context, definition string,
	actor Actor) (Case, error) {
	c, err := (*engine.Runtime)(r).Create(ctx, definition, actor)
	return Case(c), err
}

// Open resolves a case and opens its working copy for the actor.
func (r *Runtime) Open(ctx context.Context, id CaseID, actor Actor) (Case, error) {
	c, err := (*engine.Runtime)(r).Open(ctx, id, actor)
	return Case(c), err
}

// RegisterValidate binds a validator for the named custom type.
func (r *Runtime) RegisterValidate(typeName string, fn ValidateFunc) {
	(*engine.Runtime)(r).RegisterValidate(typeName, engine.ValidateFunc(fn))
}

// RegisterOptions binds an options provider to a field path.
func (r *Runtime) RegisterOptions(fieldPath string, fn OptionsFunc) {
	(*engine.Runtime)(r).RegisterOptions(fieldPath, engine.OptionsFunc(fn))
}

// RegisterOnCommit adds a post-commit observer.
func (r *Runtime) RegisterOnCommit(fn OnCommitFunc) {
	(*engine.Runtime)(r).RegisterOnCommit(engine.OnCommitFunc(fn))
}

// ClearOnCommit removes all post-commit observers.
func (r *Runtime) ClearOnCommit() { (*engine.Runtime)(r).ClearOnCommit() }
