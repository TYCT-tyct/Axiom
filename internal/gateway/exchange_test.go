package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func exchangeCurrency(t *testing.T, code string) platform.Currency {
	t.Helper()
	c, err := platform.ParseCurrency(code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *routeFixture) publish() {
	f.t.Helper()
	snap, list, count, err := f.deps.Build(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	f.deps.Publish(snap, list, count)
}

func wantVersion(t *testing.T, entries []routing.Entry, version int64, who string) {
	t.Helper()
	for _, e := range entries {
		if e.Exchange.Version != version {
			t.Fatalf("%s got rate version %d want %d", who, e.Exchange.Version, version)
		}
	}
}

func TestRouteFreezesExchangeAcrossRefreshes(t *testing.T) {
	f := newRouteFixture(t)
	a, b := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	a.Currency, b.Currency = exchangeCurrency(t, "CNY"), exchangeCurrency(t, "JPY")
	f.build([]suppliers.Routable{a, b})
	rates := []platform.ExchangeRate{
		{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1},
		{ID: uuid.NewV7(), Base: "JPY", Quote: "USD", Numerator: 1, Denominator: 150, Version: 1},
	}
	f.deps.ExchangeRates = func(context.Context) ([]platform.ExchangeRate, error) { return rates, nil }
	f.publish()
	old, wait, err := f.router.Plan(call(f.served))
	if err != nil || wait != nil || len(old.Entries) != 2 {
		t.Fatalf("plan: %+v %v %v", old, wait, err)
	}
	// Update before the first send, then between retries, then throughout a long request.
	for version := int64(2); version <= 5; version++ {
		for i := range rates {
			rates[i].ID = uuid.NewV7()
			rates[i].Version = version
			rates[i].Numerator = version
		}
		f.publish()
		fresh, _, err := f.router.Plan(call(f.served))
		if err != nil {
			t.Fatal(err)
		}
		wantVersion(t, fresh.Entries, version, "new request")
		wantVersion(t, old.Entries, 1, "in-flight request")
	}
}

func TestExchangeRefreshFailureDoesNotExtendFreshness(t *testing.T) {
	f := newRouteFixture(t)
	now := time.Now()
	router, err := routing.New(routing.Config{Now: func() time.Time { return now }, MaxStale: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.router = router
	f.build([]suppliers.Routable{routable(f.served, protocol.Messages)})
	before := f.models.published()
	f.deps.ExchangeRates = func(context.Context) ([]platform.ExchangeRate, error) {
		return nil, errors.New("configuration unavailable")
	}
	for range 3 {
		if snap, list, _, err := f.deps.Build(t.Context()); err == nil || snap != nil || list != nil {
			t.Fatal("failed refresh published a snapshot")
		}
	}
	if f.models.published() != before {
		t.Error("a failed build replaced the model list, which would quote prices the standing snapshot does not charge")
	}
	now = now.Add(time.Hour)
	if router.Fresh() {
		t.Fatal("failed refresh kept stale rates fresh")
	}
	if _, _, err := router.Plan(call(f.served)); !errors.Is(err, routing.ErrStale) {
		t.Fatalf("plan with stale rates: %v", err)
	}
}

func (f *routeFixture) setCNYRate(ctx context.Context, version int64) error {
	kind := "adjustment"
	if version == 0 {
		kind = "initial"
	}
	return f.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := billing.SetExchangeRate(ctx, tx, audit.System("gateway.test"), billing.ExchangeInput{
			Base: "CNY", Quote: "USD", Numerator: "10", Denominator: "71", Kind: kind,
			ExpectedVersion: version, Reason: "commit during a routing build",
		})
		return err
	})
}

func (f *routeFixture) currentRates(ctx context.Context) (rates []platform.ExchangeRate, err error) {
	err = f.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		rows, err := billing.ExchangeRates(ctx, tx)
		for _, r := range rows {
			rates = append(rates, r.Basis())
		}
		return err
	})
	return rates, err
}

func TestRouteBuildKeepsTheRatesReadBeforeAConcurrentCommit(t *testing.T) {
	f := newRouteFixture(t)
	e := routable(f.served, protocol.Messages)
	e.Currency = exchangeCurrency(t, "CNY")
	f.build([]suppliers.Routable{e})
	if err := f.setCNYRate(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	f.deps.ExchangeRates = f.currentRates
	changed := false
	f.deps.Prices = func(ctx context.Context) (map[catalog.ModelID]billing.Sold, error) {
		if changed {
			return f.prices, nil
		}
		changed = true
		return f.prices, f.setCNYRate(ctx, 1)
	}
	for _, version := range []int64{1, 2} {
		f.publish()
		plan, _, err := f.router.Plan(call(f.served))
		if err != nil {
			t.Fatal(err)
		}
		wantVersion(t, plan.Entries, version, "build")
	}
}
