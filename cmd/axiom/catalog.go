package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/config"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// operation reads one request of the file and runs it.
type operation func(ctx context.Context, c *catalog.Catalog, actor audit.Actor, doc []byte) (catalog.Result, error)

// run adapts an admin operation of the catalog: the file holds the request as JSON, in which a member the request does
// not name is an error. do is a method expression such as (*catalog.Catalog).SaveAuthor.
func run[R any, P interface {
	*R
	SetActor(audit.Actor)
}](do func(*catalog.Catalog, context.Context, R) (catalog.Result, error)) operation {
	return func(ctx context.Context, c *catalog.Catalog, actor audit.Actor, doc []byte) (catalog.Result, error) {
		var r R
		if err := jsonx.UnmarshalKnown(doc, &r); err != nil {
			return catalog.Result{}, err
		}
		P(&r).SetActor(actor)
		return do(c, ctx, r)
	}
}

// catalogOperations are the admin operations `axiom catalog` runs until the admin interface exists. Each is a file with
// the request of README's operations: who asks is this command, why and which attempt are in the file (internal/catalog/README.md「公开接口」).
var catalogOperations = map[string]operation{
	"save-author":         run((*catalog.Catalog).SaveAuthor),
	"create-model":        run((*catalog.Catalog).CreateModel),
	"add-name":            run((*catalog.Catalog).AddName),
	"update-spec":         run((*catalog.Catalog).UpdateSpec),
	"list-model":          run((*catalog.Catalog).ListModel),
	"retire":              run((*catalog.Catalog).Retire),
	"publish-price":       run((*catalog.Catalog).PublishPrice),
	"select-price-source": run((*catalog.Catalog).SelectPriceSource),
}

// catalogCommand runs `axiom catalog <operation> <file>` in the platform scope against AXIOM_DATABASE_URL and writes
// the result as one JSON line. The platform scope is built here, in cmd/, and nowhere a request can reach. The actor is
// the command itself until the operator's identity is defined (Issue #23).
func catalogCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: axiom catalog <operation> <file>; operations: %v", slices.Sorted(maps.Keys(catalogOperations)))
	}
	do, ok := catalogOperations[args[0]]
	if !ok {
		return fmt.Errorf("unknown operation %q; operations: %v", args[0], slices.Sorted(maps.Keys(catalogOperations)))
	}
	doc, err := os.ReadFile(args[1]) //nolint:gosec // the operator names the request file on their own machine
	if err != nil {
		return err
	}
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
	c, err := catalog.Open(db, pg.PlatformScope(), catalog.Config{})
	if err != nil {
		return err
	}
	res, err := do(ctx, c, audit.System("catalog.cli"), doc)
	if err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	line, err := jsonx.MarshalCanonical(map[string]any{"change": res.Change.String(), "subject": res.Subject, "replayed": res.Replayed})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", line)
	return err
}
