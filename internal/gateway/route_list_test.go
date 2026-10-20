package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/suppliers"
)

// The model list in OpenRouter's shape is built by the routing snapshot's own build, from the same reading of the catalog
// and the sale prices: this file is about that one reading, so that the price a client reads is the price a call is
// charged. What the entries carry is openrouter_test.go's; what gets into the snapshot is route_test.go's.

// listed is the model's entry in the list the last build published.
func (f *routeFixture) listed(id catalog.ModelID) (orModel, bool) {
	l := f.models.published()
	for i := range l.models {
		if l.models[i].model == id {
			return l.models[i], true
		}
	}
	return orModel{}, false
}

// TestRouteBuildPricesTheListWithTheSnapshotsPrices: the model list and the routing snapshot are one build's, made from
// one reading of the sale prices, so the price a client reads is the price the endpoint the snapshot plans to charges —
// the same price version. A reading that changes publishes both, and a build that fails publishes neither.
func TestRouteBuildPricesTheListWithTheSnapshotsPrices(t *testing.T) {
	f := newRouteFixture(t)
	f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
	check := func(step string, sold billing.Sold) {
		t.Helper()
		e, ok := f.listed(f.served)
		if !ok {
			t.Fatalf("%s: the list does not show the served model", step)
		}
		want, ok := sold.Schedule.BaseRate(catalog.ItemInput)
		if !ok || e.prompt != want || e.Pricing.Prompt != want.String() {
			t.Errorf("%s: the list shows %v, want the sale price %v", step, e.prompt, want)
		}
		plan, wait, err := f.router.Plan(call(f.served))
		if err != nil || wait != nil || len(plan.Entries) != 1 {
			t.Fatalf("%s: plan = %d entries, %v, %v; want the one endpoint", step, len(plan.Entries), wait, err)
		}
		if got := plan.Entries[0].Price.Version; got != sold.Version {
			t.Errorf("%s: routing charges price version %s, the list was built at %s: the two readings drifted apart",
				step, got, sold.Version)
		}
	}
	previous := f.prices[f.served]
	initial := previous
	check("the first build", previous)
	// The next reading prices the model again: one build publishes the routing snapshot and the list from it, so the two
	// changes are one, not two readings that drift apart between rebuilds.
	raised, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000009","output":"0.000010"}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.prices[f.served] = billing.Sold{Version: uuid.NewV7(), Schedule: raised}
	check("a price change before the next build", previous)
	snap, list, count, err := f.deps.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check("a completed build before publication", previous)
	f.deps.Publish(snap, list, count)
	check("the build after the price changed", f.prices[f.served])
	if e, _ := f.listed(f.served); e.prompt.String() != "0.000009" {
		t.Errorf("the list shows %v after the price changed, want 0.000009", e.prompt)
	}
	// A build that fails publishes neither: the list keeps the price of the snapshot routing still plans with.
	before, _ := f.listed(f.served)
	previous = f.prices[f.served]
	f.prices[f.served] = billing.Sold{Version: uuid.NewV7(), Schedule: initial.Schedule}
	f.deps.UnknownItems = func(context.Context) ([]billing.UnknownItem, error) {
		return nil, errors.New("the database is gone after reading new prices")
	}
	if _, _, _, err := f.deps.Build(t.Context()); err == nil {
		t.Fatal("a build with a failed source succeeded")
	}
	if after, _ := f.listed(f.served); after.prompt != before.prompt {
		t.Errorf("a failed build changed the list's price from %v to %v", before.prompt, after.prompt)
	}
	check("after the failed build", previous)
}

func TestOpenRouterRequestsDoNotReadPrices(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	var reads atomic.Int32
	d := RouteDeps{
		Catalog: r.catalog, Router: r.router, Models: r.models, Quality: quality.New(nil), Logger: slog.New(slog.DiscardHandler),
		Endpoints: func(context.Context) ([]suppliers.Routable, error) {
			return []suppliers.Routable{routable(r.alpha, protocol.Messages)}, nil
		},
		Prices: func(context.Context) (map[catalog.ModelID]billing.Sold, error) {
			if reads.Add(1) > 1 {
				return nil, errors.New("price storage unavailable")
			}
			return r.prices(), nil
		},
		UnknownItems: func(context.Context) ([]billing.UnknownItem, error) { return nil, nil },
	}
	snap, list, count, err := d.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d.Publish(snap, list, count)
	for range 2 {
		if ids := r.orIDs(a); len(ids) != 1 || ids[0] != "acme/alpha" {
			t.Fatalf("published list = %v, want acme/alpha", ids)
		}
		if got := r.orModel(a, "acme/alpha").Pricing["prompt"]; got != "0.000001" {
			t.Fatalf("published detail price = %q", got)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("list/detail requests made %d additional price reads", got-1)
	}
}
