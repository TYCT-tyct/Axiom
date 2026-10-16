// Package registry lists the adapters the program carries. adapters below is the only place that names them all, one
// line each, so removing an adapter is deleting its package and its line: `just adapters-removable` checks that
// nothing else needs it. There is no init() self-registration, which would put an adapter in the program by merely
// importing it.
package registry

import (
	"fmt"
	"maps"
	"slices"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/newapi"
	"axiom/internal/suppliers/internal/sub2api"
)

var adapters = []adapter.Adapter{
	newapi.New(),
	sub2api.New(),
}

// Registry finds an adapter by the kind stored in suppliers.kind.
type Registry struct {
	byKind map[adapter.Kind]adapter.Adapter
}

// Default is the registry of the adapters this program carries.
func Default() (*Registry, error) { return New(adapters...) }

// New refuses what would fail later in a request: a kind that cannot be stored, two adapters of one kind, or a rate
// bucket that can never admit a request.
func New(list ...adapter.Adapter) (*Registry, error) {
	r := &Registry{byKind: make(map[adapter.Kind]adapter.Adapter, len(list))}
	for _, a := range list {
		kind := a.Kind()
		if !kind.Valid() {
			return nil, fmt.Errorf("adapter kind %q: must be lower-case letters and digits, starting with a letter", kind)
		}
		if _, dup := r.byKind[kind]; dup {
			return nil, fmt.Errorf("adapter kind %q: registered twice", kind)
		}
		if err := adapter.ValidateBuckets(a.Buckets()); err != nil {
			return nil, fmt.Errorf("adapter %q: %w", kind, err)
		}
		r.byKind[kind] = a
	}
	return r, nil
}

// Get returns the adapter of kind.
func (r *Registry) Get(kind adapter.Kind) (adapter.Adapter, bool) {
	a, ok := r.byKind[kind]
	return a, ok
}

// Has reports whether an adapter of kind is registered.
func (r *Registry) Has(kind adapter.Kind) bool { _, ok := r.byKind[kind]; return ok }

// Kinds lists the registered kinds in order.
func (r *Registry) Kinds() []adapter.Kind { return slices.Sorted(maps.Keys(r.byKind)) }
