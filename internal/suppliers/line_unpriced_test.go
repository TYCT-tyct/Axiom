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

// TestALineIsPricedOnlyFromItsAuthorsOwnPriceList: a direct line of the author's own API sells the author's model at
// the catalog's reference price only when that price came from the author's official price list (docs/domains/
// suppliers-direct.md「成本与线路倍率」). A model whose price another source gave (here typed in by a person; a dataset
// such as OpenRouter's lists one provider's price, often below the author's) has an unknown cost on the line, when
// connected, at a new ratio and when projected again, and is not routed; the model priced from the official list is.
func TestALineIsPricedOnlyFromItsAuthorsOwnPriceList(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	w.listFrom("pebble", stonePrice, catalog.SourceManual)
	w.provider.set("sk-good-key", providerKey{models: []string{"stone", "pebble"}})
	line := w.connect("pasted", "", "sk-good-key")
	want := map[string]string{"stone": "known", "pebble": "unknown"}
	for _, step := range []string{"connected", "a ratio set", "projected again"} {
		switch step {
		case "a ratio set":
			if err := w.setRatio(line, "0.9"); err != nil {
				t.Fatal(err)
			}
		case "projected again":
			if err := (&lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}).reproject(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.svc.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
		for model, state := range want {
			endpoint := w.endpoint(line, model)
			if got := w.text("SELECT cost_state FROM suppliers.endpoints WHERE id = $1", endpoint); got != state {
				t.Errorf("%s: %s's cost is %s, want %s", step, model, got, state)
			}
			if _, err := w.svc.Acquire(endpoint); errors.Is(err, ErrNoEndpoint) != (state == "unknown") {
				t.Errorf("%s: acquiring %s: %v", step, model, err)
			}
		}
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
	evidence := w.evidence(catalog.SourceOfficial, "CNY")
	if _, err := w.cat.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: w.op(), Model: model, Source: catalog.SourceOfficial, Schedule: yuan, Evidence: evidence,
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
		Op: w.op(), Model: model, Rev: view.Rev, Source: catalog.SourceOfficial, Bases: bases,
		Prices: []catalog.SourcePrice{{Schedule: yuan, Evidence: evidence}},
	}); err != nil {
		w.t.Fatal(err)
	}
	if _, ok := w.snap().ReferencePrice(model, "USD"); ok {
		w.t.Fatal("the USD price is still in force")
	}
}
