package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func setExchange(ctx context.Context, r *rig, in billing.ExchangeInput) (row billing.BillingExchangeRate, err error) {
	err = r.db.Tx(ctx, platformScope, func(tx pg.Tx) (err error) {
		row, err = billing.SetExchangeRate(ctx, tx, audit.System("gateway.test"), in)
		return err
	})
	return row, err
}

// payInCNY makes the suppliers settle in CNY, so that every route needs a CNY to USD rate.
func payInCNY(t *testing.T, r *rig) {
	t.Helper()
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.suppliers SET settlement_currency='CNY'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.suppliers.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// republishRate commits a new rate and publishes a snapshot that routes new requests with it.
func republishRate(ctx context.Context, r *rig, e routing.Endpoint, preset routing.Preset) error {
	next, err := setExchange(ctx, r, billing.ExchangeInput{
		Base: "CNY", Quote: "USD", Numerator: "20", Denominator: "71", Kind: "adjustment", ExpectedVersion: 1, Reason: "changed after reservation",
	})
	if err != nil {
		return err
	}
	e.Exchange = next.Basis()
	snap, err := r.router.NewSnapshot(time.Now(), []routing.Endpoint{e}, []routing.Preset{preset})
	if err != nil {
		return err
	}
	r.router.Replace(snap)
	return nil
}

type costBasis struct {
	charged, recomputed int64
	rate                uuid.UUID
	profit              *int64
}

func readCostBasis(t *testing.T, r *rig, id uuid.UUID) (c costBasis) {
	t.Helper()
	err := r.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(sum(e.amount),0)::bigint FROM billing.entries e
		JOIN billing.transactions t ON t.id=e.transaction_id WHERE t.request_id=$1 AND e.book='supplier_cost'`, id).Scan(&c.charged); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), "SELECT exchange_rate_id FROM suppliers.attempt_costs WHERE request_id=$1", id).Scan(&c.rate); err != nil {
			return err
		}
		// Every factor comes from the stored basis, even though the current FX row has changed.
		if err := tx.QueryRow(t.Context(), `SELECT ceil(a.estimated_native * v.value *
		CASE WHEN a.conversion_from = fx.base THEN fx.numerator::numeric / fx.denominator
		ELSE fx.denominator::numeric / fx.numerator END * 1000000000)::bigint
		FROM suppliers.attempt_costs a JOIN suppliers.unit_values v ON v.id=a.unit_value_id
		JOIN billing.exchange_rates fx ON fx.id=a.exchange_rate_id WHERE a.request_id=$1`, id).Scan(&c.recomputed); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT profit FROM billing.reservations WHERE request_id=$1", id).Scan(&c.profit)
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExchangeReservationReachesTheActualCostAndProfit(t *testing.T) {
	r, _ := costedRig(t)
	e := r.endpoints["alpha-1"]
	payInCNY(t, r)
	first, err := setExchange(t.Context(), r, billing.ExchangeInput{
		Base: "CNY", Quote: "USD", Numerator: "10", Denominator: "71", Kind: "initial", Reason: "test reservation freeze",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.suppliers.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, err := r.suppliers.Acquire(uuid.UUID(e.ID))
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	e.Exchange = first.Basis()
	preset := routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}
	r.install(preset, e)
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(receipt)
	sample, _ := sample(t, protocol.Messages)
	up.setScript(costedScript(sample, true))
	updated := make(chan error, 1)
	var once sync.Once
	// Admission has committed. Publish a new rate before the very first send.
	up.beforeAcquire = func(suppliers.EndpointID) {
		once.Do(func() { updated <- republishRate(t.Context(), r, e, preset) })
	}
	account := r.funded(1e12)
	for i, want := range []int64{184, 367} {
		r.do("POST", "/v1/messages", account.key, strings.NewReader(stream("acme/alpha")))
		if i == 0 {
			select {
			case err := <-updated:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("the request never attempted its reserved route")
			}
		}
		got := readCostBasis(t, r, r.lastCall().ID)
		if got.charged != want || got.recomputed != got.charged || got.profit == nil || *got.profit != -want {
			t.Fatalf("call %d: cost %d want %d profit %v", i, got.charged, want, got.profit)
		}
		if (got.rate == first.ID) != (i == 0) {
			t.Fatalf("call %d used wrong rate %s", i, got.rate)
		}
	}
	r.ledgerBalanced()
}

func TestMissingExchangeMakesGatewayProfitUnknown(t *testing.T) {
	r, _ := costedRig(t)
	payInCNY(t, r)
	a, err := r.suppliers.Acquire(uuid.UUID(r.endpoints["alpha-1"].ID))
	if err != nil {
		t.Fatal(err)
	}
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(a.Done())
	sample, _ := sample(t, protocol.Messages)
	up.setScript(costedScript(sample, true))
	account := r.funded(1e12)
	r.do("POST", "/v1/messages", account.key, strings.NewReader(stream("acme/alpha")))
	if r.count("SELECT count(*) FROM billing.reservations WHERE state='settled' AND profit IS NULL") != 1 {
		t.Fatal("gateway discarded unknown conversion")
	}
	if r.count("SELECT count(*) FROM suppliers.attempt_costs WHERE conversion_state='missing_rate' AND cost_flag='complete'") != 1 {
		t.Fatal("conversion destroyed native completeness")
	}
}
