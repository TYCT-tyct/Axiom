// Package registry lists the adapters the program carries: the only list of channel types
// (docs/domains/suppliers-direct.md「渠道类型」). adapters and tables below are the only place that names them all, one
// package a line, so removing an adapter is deleting its package and its line, and removing one row of a table is
// deleting the row: `just adapters-removable` checks that nothing else needs a package. There is no init()
// self-registration, which would put an adapter in the program by merely importing it.
package registry

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/anthropic"
	"axiom/internal/suppliers/internal/azure"
	"axiom/internal/suppliers/internal/bedrock"
	"axiom/internal/suppliers/internal/compatible"
	"axiom/internal/suppliers/internal/cpa"
	"axiom/internal/suppliers/internal/declared"
	"axiom/internal/suppliers/internal/newapi"
	"axiom/internal/suppliers/internal/sub2api"
	"axiom/internal/suppliers/internal/vendors"
	"axiom/internal/suppliers/internal/vertex"
)

var adapters = []adapter.Adapter{
	anthropic.New(),
	azure.New(),
	bedrock.New(),
	compatible.New(),
	cpa.New(),
	newapi.New(),
	sub2api.New(),
	vertex.New(),
}

// tables are packages that carry a table of adapters, one a row.
var tables = [][]adapter.Adapter{
	vendors.New(),
}

// Registry finds an adapter by the kind stored in suppliers.kind.
type Registry struct {
	byKind map[adapter.Kind]adapter.Adapter
}

// Default is the registry of the adapters this program carries: the built-in kinds, then the declarations in the
// directory AXIOM_SUPPLIER_ADAPTERS names. Declarations are read and checked here, at startup, so a bad file stops the
// node before it can serve a supplier it cannot talk to.
func Default() (*Registry, error) {
	declaredAdapters, err := declared.LoadEnv()
	if err != nil {
		return nil, err
	}
	return New(append(slices.Concat(append([][]adapter.Adapter{adapters}, tables...)...), declaredAdapters...)...)
}

// New refuses what would fail later in a request: a kind that cannot be stored, two adapters of one kind, a rate
// bucket that can never admit a request, or a type a request could not be checked against.
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
		if err := checkType(a); err != nil {
			return nil, fmt.Errorf("adapter %q: type %w", kind, err)
		}
		r.byKind[kind] = a
	}
	return r, nil
}

// checkType checks an adapter's description, and that it says the mode the adapter's interfaces are of.
func checkType(a adapter.Adapter) error {
	t := TypeOf(a)
	if t.Kind != a.Kind() {
		return fmt.Errorf("describes kind %q", t.Kind)
	}
	_, direct := a.(adapter.Direct)
	_, cloud := a.(adapter.Cloud)
	if direct != (t.Mode == adapter.ModeDirect) || cloud != (t.Mode == adapter.ModeCloud) {
		return fmt.Errorf("mode %q is not the mode of the adapter's interfaces", t.Mode)
	}
	if cloud {
		if _, signs := a.Calls().(adapter.Signer); !signs {
			return errors.New("is a cloud platform's whose calls are not signed")
		}
	}
	return t.Check()
}

// TypeOf is the type an adapter describes, or the relay type of its kind for one that does not.
func TypeOf(a adapter.Adapter) adapter.Type {
	if d, ok := a.(adapter.Described); ok {
		return d.Type()
	}
	return adapter.Relay(a.Kind())
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

// Types are the registered types in kind order.
func (r *Registry) Types() []adapter.Type {
	out := make([]adapter.Type, 0, len(r.byKind))
	for _, k := range r.Kinds() {
		out = append(out, TypeOf(r.byKind[k]))
	}
	return out
}
