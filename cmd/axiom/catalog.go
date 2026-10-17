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
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// operation reads one request of the file and runs it.
type operation func(ctx context.Context, c *catalog.Catalog, f *catalog.Fetcher, actor audit.Actor, doc []byte) (catalog.Result, error)

// run adapts an admin operation of the catalog: the file holds the request as JSON, in which a member the request does
// not name is an error. do is a method expression such as (*catalog.Catalog).SaveAuthor.
func run[R any, P interface {
	*R
	SetActor(audit.Actor)
}](do func(*catalog.Catalog, context.Context, R) (catalog.Result, error)) operation {
	return func(ctx context.Context, c *catalog.Catalog, _ *catalog.Fetcher, actor audit.Actor, doc []byte) (catalog.Result, error) {
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
	// The dataset's entry gives the prices, so the request names the model and the dataset, and the command fetches.
	"select-dataset-source": func(ctx context.Context, c *catalog.Catalog, f *catalog.Fetcher, actor audit.Actor, doc []byte) (catalog.Result, error) {
		var r catalog.SelectDatasetSourceRequest
		if err := jsonx.UnmarshalKnown(doc, &r); err != nil {
			return catalog.Result{}, err
		}
		r.SetActor(actor)
		fetched, err := c.Fetch(ctx, f, r.Source)
		if err != nil {
			return catalog.Result{}, err
		}
		return c.SelectDatasetSource(ctx, fetched, r)
	},
}

// catalogCommand runs `axiom catalog <operation> <file>`, `axiom catalog sync` or `axiom catalog check` in the platform
// scope against AXIOM_DATABASE_URL. The platform scope is built here, in cmd/, and nowhere a request can reach. The
// actor is the command itself until the operator's identity is defined (Issue #23).
func catalogCommand(ctx context.Context, environ, args []string, out io.Writer) error {
	usage := fmt.Errorf("usage: axiom catalog sync | check | <operation> <file>; operations: %v", slices.Sorted(maps.Keys(catalogOperations)))
	switch {
	case len(args) == 1 && args[0] == "sync":
		return withCatalog(ctx, environ, func(c *catalog.Catalog, f *catalog.Fetcher) error { return syncCommand(ctx, c, f, out) })
	case len(args) == 1 && args[0] == "check":
		return withCatalog(ctx, environ, func(c *catalog.Catalog, f *catalog.Fetcher) error { return checkCommand(ctx, c, f, out) })
	case len(args) != 2:
		return usage
	}
	do, ok := catalogOperations[args[0]]
	if !ok {
		return fmt.Errorf("unknown operation %q; %w", args[0], usage)
	}
	doc, err := os.ReadFile(args[1]) //nolint:gosec // the operator names the request file on their own machine
	if err != nil {
		return err
	}
	return withCatalog(ctx, environ, func(c *catalog.Catalog, f *catalog.Fetcher) error {
		res, err := do(ctx, c, f, audit.System("catalog.cli"), doc)
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		return writeLine(out, map[string]any{"change": res.Change.String(), "subject": res.Subject, "replayed": res.Replayed})
	})
}

// withCatalog opens the catalog on the database and a fetcher for the datasets, and closes them after do.
func withCatalog(ctx context.Context, environ []string, do func(*catalog.Catalog, *catalog.Fetcher) error) error {
	env := config.New(environ)
	databaseURL := env.Required(pg.DatabaseURLVar)
	startupTimeout := env.Duration(startupTimeoutVar, defaultStartupTimeout)
	if err := env.Problems(); err != nil {
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
	client, err := egress.New(egress.Direct)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return do(c, catalog.NewFetcher(client))
}

func writeLine(out io.Writer, v any) error {
	line, err := jsonx.MarshalCanonical(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", line)
	return err
}

// syncCommand runs one round over each dataset, as the scheduler will, and writes a line per model: published,
// unchanged, or held with its reason and the schedule it would take.
func syncCommand(ctx context.Context, c *catalog.Catalog, f *catalog.Fetcher, out io.Writer) error {
	reports, err := c.SyncAll(ctx, f, true)
	held := 0
	for _, report := range reports {
		for _, r := range report.Results {
			if r.Outcome == catalog.OutcomeHeld {
				held++
			}
			if werr := writeLine(out, map[string]any{"kind": "sync", "source": report.Source, "result": r}); werr != nil {
				return werr
			}
		}
	}
	if err != nil {
		return err
	}
	return writeLine(out, map[string]any{"kind": "summary", "held": held})
}

// checkedAuthors are the vendors of the first-version acceptance check (Issue #24).
var checkedAuthors = []string{"openai", "anthropic", "google", "deepseek"}

// checkCommand is the first-version acceptance check, read-only: a line for each model of the vendors, whether the
// price of each model that follows a dataset is the dataset's now, and which text models OpenRouter lists for the
// vendors that the catalog does not know. It fails when it found anything. It cannot know which models a vendor sells,
// but OpenRouter's list is the nearest to that; the vendor's page is read by a person.
func checkCommand(ctx context.Context, c *catalog.Catalog, f *catalog.Fetcher, out io.Writer) error {
	problems := 0
	report := func(v any, bad bool) error {
		if bad {
			problems++
		}
		return writeLine(out, v)
	}
	checks, err := c.Check(ctx, checkedAuthors)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if err := report(map[string]any{"kind": "model", "check": check}, len(check.Problems) > 0); err != nil {
			return err
		}
	}
	for _, src := range []catalog.PriceSource{catalog.SourceLiteLLM, catalog.SourceOpenRouter} {
		if err := checkDataset(ctx, c, f, src, report); err != nil {
			return err
		}
	}
	if err := writeLine(out, map[string]any{"kind": "summary", "problems": problems}); err != nil {
		return err
	}
	if problems > 0 {
		return fmt.Errorf("the check found %d problems", problems)
	}
	return nil
}

// checkDataset reports, for one dataset, which followers' prices differ from its entries. A dataset that cannot be read
// is a problem reported on a line, so the other dataset is still checked. OpenRouter's list also gives the models the
// catalog lacks.
func checkDataset(ctx context.Context, c *catalog.Catalog, f *catalog.Fetcher, src catalog.PriceSource, report func(any, bool) error) error {
	fetched, err := c.Fetch(ctx, f, src)
	var sync catalog.SyncReport
	if err == nil {
		sync, err = c.SyncDataset(ctx, fetched, false)
	}
	if err != nil {
		return report(map[string]any{"kind": "dataset", "source": src, "error": err.Error()}, true)
	}
	for _, r := range sync.Results {
		if err := report(map[string]any{"kind": "dataset", "source": src, "result": r}, r.Outcome != catalog.OutcomeUnchanged); err != nil {
			return err
		}
	}
	if src != catalog.SourceOpenRouter {
		return nil
	}
	entries, err := catalog.ParseOpenRouter(fetched.Body)
	if err != nil {
		return err
	}
	if err := c.Refresh(ctx); err != nil {
		return err
	}
	missing, err := c.Unrecognized(entries, checkedAuthors)
	if err != nil {
		return err
	}
	for _, key := range missing {
		if err := report(map[string]any{"kind": "unrecognized", "openrouter": key}, true); err != nil {
			return err
		}
	}
	return nil
}
