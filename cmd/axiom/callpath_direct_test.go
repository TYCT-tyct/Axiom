package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers"
)

// directCost is what the recorded Messages sample's 25 input and 15 output tokens cost at the model's official price,
// servedReference's three and fifteen dollars a million tokens, in billionths of a dollar.
const directCost = 25*3_000 + 15*15_000

// TestADirectSupplierServesAtTheOfficialPrice: a provider's own API connected with a pasted key serves a call with no
// balance to read, through the provider's own key header, and the call costs the model's official price: the supplier's
// cost row holds it at a unit value of 1, with no request id to wait for a charge, and the client is charged the sale
// price as for any supplier.
func TestADirectSupplierServesAtTheOfficialPrice(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	// The fixture's relay stops, so a call can only be served by the direct API.
	if err := pgtest.Run(t.Context(), w.database.Superuser, "UPDATE suppliers.suppliers SET status = 'paused'"); err != nil {
		t.Fatal(err)
	}
	stream, err := os.ReadFile(filepath.Join("..", "..", "internal", "protocol", "testdata", "messages", "text", "upstream.sse"))
	if err != nil {
		t.Fatal(err)
	}
	provider := newFakeProvider(t, string(stream), "claude-opus-5-5")
	w.trusted = append(w.trusted, provider.server.Certificate())
	supplier := w.connectDirect(provider)
	w.price()
	n := w.start()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })

	r := w.call(n, "/v1/messages", w.richKey, messagesCall)
	if r.status != http.StatusOK {
		t.Fatalf("Messages: status %d: %s", r.status, r.body)
	}
	call := provider.sent()[len(provider.sent())-1]
	if call.path != "/v1/messages" || call.auth != "sk-good-direct-key|" {
		t.Errorf("the provider was sent %s with %q, want /v1/messages with the key in x-api-key alone", call.path, call.auth)
	}
	id := r.header.Get("X-Generation-Id")
	w.checkSettled(r, messagesCharge)
	w.checkBalanced()
	if got := w.query(`SELECT supplier_id::text || ' ' || estimated_native::text || ' ' || cost_flag || ' ' || coalesce(charge_ref, '-') || ' ' ||
		conversion_state FROM suppliers.attempt_costs WHERE request_id::text = $1`, id); got != supplier+" 0.0003 complete - same_currency" {
		t.Errorf("the supplier's cost row is %q, want the direct API's, at the official price, with no request id", got)
	}
	var logged string
	waitUntil(t, 5*time.Second, func() bool {
		logged = w.query(`SELECT coalesce(max(price_nano)::text || ' ' || max(cost_nano)::text, '') FROM observability.call_log WHERE id::text = $1`, id)
		return logged != ""
	})
	if want := strconv.Itoa(messagesCharge) + " " + strconv.Itoa(directCost); logged != want {
		t.Errorf("the call log holds %q, want the sale price and the official price %q", logged, want)
	}
}

// connectDirect connects the provider as a compatible Anthropic API with one key, through the suppliers module as the
// admin handler does, and returns the supplier's ID.
func (w *callWorld) connectDirect(provider *fakeProvider) string {
	w.t.Helper()
	ctx := w.t.Context()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := cat.Refresh(ctx); err != nil {
		w.t.Fatal(err)
	}
	snap, err := cat.Current()
	if err != nil {
		w.t.Fatal(err)
	}
	admin, err := suppliers.NewAdmin(suppliers.AdminConfig{
		Keys: masterKeys(w.t), Egress: egress.NewForTesting(egresstest.Loopback(w.t, provider.server.Certificate())),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	origin, err := suppliers.NormalizeOrigin(provider.server.URL)
	if err != nil {
		w.t.Fatal(err)
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		w.t.Fatal(err)
	}
	r := suppliers.DirectRequest{
		Kind: "compatible", Name: "Harbor", Origin: origin, Family: "anthropic", Currency: usd,
		Keys: []string{"sk-good-direct-key"}, MaxConcurrency: 4, Reason: "a direct source",
	}
	var plan *suppliers.DirectPlan
	if err := w.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		plan, err = admin.PlanDirect(ctx, tx, r)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := admin.CheckDirect(ctx, plan); err != nil {
		w.t.Fatal(err)
	}
	var made suppliers.Connected
	if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		made, err = admin.CreateDirect(ctx, tx, audit.System("callpath.test"), snap, plan)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	if made.Supplier == nil {
		w.t.Fatalf("the key did not pass: %+v", made.Checks)
	}
	return made.Supplier.ID.String()
}
