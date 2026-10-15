// Package suppliers is the module that knows who Axiom buys model calls from: the suppliers and their credentials,
// groups, models and endpoints, and the adapters that talk to each kind of supplier system. What is here is the
// foundation: the tables and the adapter contract. Syncing, reconciling and calling come on top (README.md).
package suppliers

import (
	"context"
	"fmt"
	"strings"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// Module is the module's migrations, for cmd/ to give to pg.Migrate and pg.DB.Check.
func Module() pg.Module { return pg.Module{Name: "suppliers", Migrations: store.Migrations()} }

// CheckAdapters refuses to start when the program carries no adapter for a supplier that is still in use: its
// requests could not be sent and its sync could not run, which would surface later as failures that look like the
// supplier's. scope must see every supplier, so it is the platform scope.
func CheckAdapters(ctx context.Context, db *pg.DB, scope pg.Scope) error {
	r, err := registry.Default()
	if err != nil {
		return fmt.Errorf("adapters: %w", err)
	}
	return checkAdapters(ctx, db, scope, r)
}

func checkAdapters(ctx context.Context, db *pg.DB, scope pg.Scope, r *registry.Registry) error {
	var orphans []string
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		live, err := store.New().ListLiveSuppliers(ctx, tx)
		if err != nil {
			return err
		}
		for _, s := range live {
			if !r.Has(adapter.Kind(s.Kind)) {
				orphans = append(orphans, fmt.Sprintf("%q (kind %q)", s.Name, s.Kind))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check the suppliers' adapters: %w", err)
	}
	if len(orphans) > 0 {
		return fmt.Errorf("suppliers with no adapter in this program: %s; this program has %q: restore the adapter, or remove the supplier",
			strings.Join(orphans, ", "), r.Kinds())
	}
	return nil
}
