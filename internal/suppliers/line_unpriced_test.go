package suppliers

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// TestATypeOfAnyAuthorIsNotPricedFromTheCatalog: a type that names no author it serves may sell any author's models,
// at prices of its own (docs/domains/suppliers-direct.md「聚合商按自己的价」): its endpoint of a model the catalog
// prices has an unknown cost when connected and after a new ratio, and is not routed.
func TestATypeOfAnyAuthorIsNotPricedFromTheCatalog(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	w.provider.set("sk-good-key", providerKey{models: []string{"stone"}})
	line := w.connect("aggregate", "", "sk-good-key")
	endpoint := w.endpoint(line, "stone")
	for _, step := range []string{"connected", "a ratio set"} {
		if step == "a ratio set" {
			if err := w.setRatio(line, "0.9"); err != nil {
				t.Fatal(err)
			}
		}
		if got := w.text("SELECT cost_state || ' ' || (cost_schedule_id IS NULL)::text FROM suppliers.endpoints WHERE id = $1", endpoint); got != "unknown true" {
			t.Errorf("%s: the endpoint's cost is %s, want unknown with no plan", step, got)
		}
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(endpoint); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("acquiring the endpoint: %v, want it out of routing", err)
	}
}

// TestAWithdrawnPriceTakesTheEndpointOutOfRouting: once the catalog withdraws the reference price in the line's
// currency, keeping one in another, the line's endpoint of the model has no price of its own: pricing the line again,
// by a re-projection or a new ratio, makes its cost unknown, and it is not routed, rather than keeping the withdrawn
// price's plan.
func TestAWithdrawnPriceTakesTheEndpointOutOfRouting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		reprice func(*pastedWorld, uuid.UUID) error
	}{
		{"re-projected", func(w *pastedWorld, _ uuid.UUID) error {
			return (&lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}).reproject(w.t.Context())
		}},
		{"a ratio set", func(w *pastedWorld, line uuid.UUID) error { return w.setRatio(line, "0.9") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newPastedWorld(t)
			w.provider.set("sk-good-key", providerKey{models: []string{"stone"}})
			line := w.connect("pasted", "", "sk-good-key")
			endpoint := w.endpoint(line, "stone")
			w.withdrawUSD(w.models["stone"])
			if err := tc.reprice(w, line); err != nil {
				t.Fatal(err)
			}
			if got := w.text("SELECT cost_state FROM suppliers.endpoints WHERE id = $1", endpoint); got != "unknown" {
				t.Errorf("the endpoint's cost is %s, want unknown", got)
			}
			if err := w.svc.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := w.svc.Acquire(endpoint); !errors.Is(err, ErrNoEndpoint) {
				t.Errorf("acquiring the endpoint: %v, want it out of routing", err)
			}
		})
	}
}

// withdrawUSD keeps the model's price in CNY only: the price source chosen again with no USD schedule withdraws that.
func (w *pastedWorld) withdrawUSD(model catalog.ModelID) {
	w.t.Helper()
	ctx, yuan := w.t.Context(), jsonx.Raw(strings.Replace(stonePrice, "USD", "CNY", 1))
	evidence := jsonx.Raw(`{"url":"https://example.com/pricing"}`)
	if _, err := w.cat.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: w.op(), Model: model, Source: catalog.SourceManual, Schedule: yuan, Evidence: evidence,
	}); err != nil {
		w.t.Fatal(err)
	}
	snap := w.snap()
	view, err := w.cat.Model(ctx, model)
	if err != nil {
		w.t.Fatal(err)
	}
	bases := map[string]uuid.UUID{}
	for _, c := range []string{"USD", "CNY"} {
		ref, _ := snap.ReferencePrice(model, c)
		bases[c] = ref.ID
	}
	if _, err := w.cat.SelectPriceSource(ctx, catalog.SelectPriceSourceRequest{
		Op: w.op(), Model: model, Rev: view.Rev, Source: catalog.SourceManual, Bases: bases,
		Prices: []catalog.SourcePrice{{Schedule: yuan, Evidence: evidence}},
	}); err != nil {
		w.t.Fatal(err)
	}
	if _, ok := w.snap().ReferencePrice(model, "USD"); ok {
		w.t.Fatal("the USD price is still in force")
	}
}
