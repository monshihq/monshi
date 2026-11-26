package engine

import "sync"

// registry is the definition registry. Its contents change only
// through an explicit act on Runtime, so the value a handle carries — a
// pointer to it — is immutable even while the map under it is not. A
// Definition never changes after Load; "reloading" replaces the entry.
type registry struct {
	mu   sync.RWMutex
	defs map[string]*Definition
}

func newRegistry() *registry { return &registry{defs: map[string]*Definition{}} }

func (g *registry) get(id string) (*Definition, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	d, ok := g.defs[id]
	return d, ok
}

// add installs a definition, refusing a duplicate unless the caller asked
// to replace. Duplicate IDs are rejected because `Create` selects by ID:
// two definitions under one name would make a case's record ambiguous about
// which one governs its data, and that is a host mistake worth stopping
// rather than resolving. `refuse` names the refusal after the act that asked,
// because Register and Replace report the same collision differently.
func (g *registry) add(refuse refuser, d *Definition, replace bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.defs[d.ID()]; dup && !replace {
		return refuse.now(InvalidOp,
			"definition "+quote(d.ID())+" is already registered")
	}
	g.defs[d.ID()] = d
	return nil
}
