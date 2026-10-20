package gateway

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// republishRate commits a new rate and publishes a snapshot of the endpoints that routes new requests with it.
func republishRate(ctx context.Context, r *rig, preset routing.Preset, endpoints ...routing.Endpoint) error {
	next, err := setExchange(ctx, r, billing.ExchangeInput{
		Base: "CNY", Quote: "USD", Numerator: "20", Denominator: "71", Kind: "adjustment", ExpectedVersion: 1, Reason: "changed after reservation",
	})
	if err != nil {
		return err
	}
	for i := range endpoints {
		endpoints[i].Exchange = next.Basis()
	}
	snap, err := r.router.NewSnapshot(time.Now(), endpoints, []routing.Preset{preset})
	if err != nil {
		return err
	}
	r.router.Replace(snap)
	return nil
}

func requireRepublished(t *testing.T, updated <-chan error) {
	t.Helper()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("the request never attempted its reserved route")
	}
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
		once.Do(func() { updated <- republishRate(t.Context(), r, preset, e) })
	}
	account := r.funded(1e12)
	for i, want := range []int64{184, 367} {
		r.do("POST", "/v1/messages", account.key, strings.NewReader(stream("acme/alpha")))
		if i == 0 {
			requireRepublished(t, updated)
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

// A cost the usage did not fully support is an estimate, not a known cost, whatever it converted to: the call's profit
// is unknown, as it is for a missing rate. A cost from reported usage has one (the test above).
func TestEstimatedUsageMakesGatewayProfitUnknown(t *testing.T) {
	smp, _ := sample(t, protocol.Messages)
	start := strings.Replace(supplierStart(false), `{"input_tokens":`+strconv.Itoa(supplierInput)+`}`, `{}`, 1)
	for name, tc := range map[string]struct {
		script scripted
		flag   string
	}{
		"an output never reported": {costedScript(smp, false), "unpriced"},
		"no usage reported":        {serve(beat{data: start + events(smp, evBlock) + events(smp, evHello)}), "no_usage"},
	} {
		t.Run(name, func(t *testing.T) {
			r, receipt := costedRig(t)
			up := withCosts(r, r.billing, r.suppliers)
			up.setReceipt(receipt)
			up.setScript(tc.script)
			account := r.funded(1e12)
			r.do("POST", "/v1/messages", account.key, strings.NewReader(stream("acme/alpha")))
			id := r.lastCall().ID
			if r.count("SELECT count(*) FROM billing.reservations WHERE request_id=$1 AND state='settled' AND profit IS NULL", id) != 1 {
				t.Fatal("an estimated cost was booked as a known profit")
			}
			if r.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id=$1 AND cost_flag=$2", id, tc.flag) != 1 {
				t.Fatalf("the supplier's row is not flagged %s", tc.flag)
			}
		})
	}
}

// The fallback after a failed first attempt is costed with the rate the request reserved, though a new one was
// published between the two attempts.
func TestFallbackAttemptKeepsTheReservedRate(t *testing.T) {
	r := newRig(t, func(o *rigOptions) { o.suppliers = true })
	_, endpoint := r.seedSupplierCost()
	payInCNY(t, r)
	first, err := setExchange(t.Context(), r, billing.ExchangeInput{
		Base: "CNY", Quote: "USD", Numerator: "10", Denominator: "71", Kind: "initial", Reason: "test reservation freeze",
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.suppliers.Acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	e1, e2 := r.endpoints["alpha-1"], r.endpoints["alpha-2"]
	e1.Exchange, e2.Exchange = first.Basis(), first.Basis()
	preset := routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}
	r.install(preset, e1, e2)
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(receipt)
	smp, _ := sample(t, protocol.Messages)
	// alpha-1 is the cheaper entry, so it is tried first.
	up.eps[uuid.UUID(e1.ID)].script = []scripted{failure(routing.ScopeEndpoint, false)}
	up.eps[uuid.UUID(e2.ID)].script = []scripted{costedScript(smp, true)}
	var acquired atomic.Int32
	updated := make(chan error, 1)
	up.beforeAcquire = func(suppliers.EndpointID) {
		if acquired.Add(1) == 2 {
			updated <- republishRate(t.Context(), r, preset, e1, e2)
		}
	}
	account := r.funded(1e12)
	r.do("POST", "/v1/messages", account.key, strings.NewReader(stream("acme/alpha")))
	requireRepublished(t, updated)
	id := r.lastCall().ID
	if n := r.count("SELECT count(*) FROM billing.exchange_rates WHERE base='CNY' AND quote='USD'"); n != 2 {
		t.Fatalf("%d rate versions: the rate never changed", n)
	}
	var rate uuid.UUID
	var state string
	if err := r.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT exchange_rate_id, conversion_state FROM suppliers.attempt_costs WHERE request_id=$1 AND attempt_seq=2", id).Scan(&rate, &state)
	}); err != nil {
		t.Fatal(err)
	}
	if rate != first.ID || state != "converted" {
		t.Fatalf("the fallback attempt used rate %s (%s), want the reserved %s", rate, state, first.ID)
	}
}
