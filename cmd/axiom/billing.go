package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/config"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// billingOperation reads one request of the file and runs it in a platform-scope transaction.
type billingOperation func(ctx context.Context, tx pg.Tx, actor audit.Actor, doc []byte) error

// runBilling adapts an administrator's operation of billing, a package function taking the transaction, as run adapts
// the catalog's.
func runBilling[R any, P interface {
	*R
	SetActor(audit.Actor)
}](do func(context.Context, pg.Tx, R) error) billingOperation {
	return func(ctx context.Context, tx pg.Tx, actor audit.Actor, doc []byte) error {
		var r R
		if err := jsonx.UnmarshalKnown(doc, &r); err != nil {
			return err
		}
		P(&r).SetActor(actor)
		return do(ctx, tx, r)
	}
}

// billingOperations are what `axiom billing` runs until the admin interface exists (internal/billing/README.md「兜底价」).
var billingOperations = map[string]billingOperation{
	"save-fallbacks":     runBilling(billing.SaveFallbacks),
	"clear-unknown-item": runBilling(billing.ClearUnknownItem),
}

// billingLists are the read-only listings, each writing a line per row.
var billingLists = map[string]func(ctx context.Context, db *pg.DB, write func(any) error) error{
	"unknown-items": listUnknownItems,
}

// listUnknownItems writes them from one read: a row is a model's field, so they are few.
func listUnknownItems(ctx context.Context, db *pg.DB, write func(any) error) error {
	return db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		items, err := billing.UnknownItems(ctx, tx)
		for _, it := range items {
			if err == nil {
				err = write(it)
			}
		}
		return err
	})
}

// billingCommand runs `axiom billing <listing>` or `axiom billing <operation> <file>` in the platform scope against
// AXIOM_DATABASE_URL. The actor is the command itself until the operator's identity is defined (Issue #23).
func billingCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	usage := fmt.Errorf("usage: axiom billing <listing> | <operation> <file>; listings: %v; operations: %v",
		slices.Sorted(maps.Keys(billingLists)), slices.Sorted(maps.Keys(billingOperations)))
	if len(args) == 1 {
		list, ok := billingLists[args[0]]
		if !ok {
			return fmt.Errorf("unknown listing %q; %w", args[0], usage)
		}
		return withDatabase(ctx, environ, func(db *pg.DB) error {
			return list(ctx, db, func(row any) error { return writeLine(out, row) })
		})
	}
	if len(args) != 2 {
		return usage
	}
	do, ok := billingOperations[args[0]]
	if !ok {
		return fmt.Errorf("unknown operation %q; %w", args[0], usage)
	}
	doc, err := os.ReadFile(args[1]) //nolint:gosec // the operator names the request file on their own machine
	if err != nil {
		return err
	}
	return withDatabase(ctx, environ, func(db *pg.DB) error {
		return db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			if err := do(ctx, tx, audit.System("billing.cli"), doc); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			return writeLine(out, map[string]any{"done": args[0]})
		})
	})
}

// withDatabase runs use on the database.
func withDatabase(ctx context.Context, environ []string, use func(*pg.DB) error) error {
	env := config.New(environ)
	databaseURL := env.Required("AXIOM_DATABASE_URL")
	startupTimeout := env.Duration("AXIOM_STARTUP_TIMEOUT", defaultStartupTimeout)
	if err := env.Err(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	db, err := openDatabase(ctx, databaseURL, startupTimeout)
	if err != nil {
		return err
	}
	defer db.Close()
	return use(db)
}
