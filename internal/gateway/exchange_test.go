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

func TestRouteFreezesExchangeAcrossRefreshes(t *testing.T) {
	f := newRouteFixture(t)
	cny, _ := platform.ParseCurrency("CNY")
	jpy, _ := platform.ParseCurrency("JPY")
	a, b := routable(f.served, protocol.Messages), routable(f.served, protocol.Messages)
	a.Currency, b.Currency = cny, jpy
	f.build([]suppliers.Routable{a, b})
	rates := []platform.ExchangeRate{
		{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1},
		{ID: uuid.NewV7(), Base: "JPY", Quote: "USD", Numerator: 1, Denominator: 150, Version: 1},
	}
	f.deps.ExchangeRates = func(context.Context) ([]platform.ExchangeRate, error) { return rates, nil }
	publish := func() {
		snap, count, err := f.deps.Build(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		f.deps.Publish(snap, count)
	}
	publish()
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
		publish()
		fresh, _, err := f.router.Plan(call(f.served))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range fresh.Entries {
			if e.Exchange.Version != version {
				t.Fatal("new request got old rate")
			}
		}
		for _, e := range old.Entries {
			if e.Exchange.Version != 1 {
				t.Fatal("in-flight request changed rate")
			}
		}
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
	f.deps.ExchangeRates = func(context.Context) ([]platform.ExchangeRate, error) {
		return nil, errors.New("configuration unavailable")
	}
	for range 3 {
		if snap, _, err := f.deps.Build(t.Context()); err == nil || snap != nil {
			t.Fatal("failed refresh published a snapshot")
		}
	}
	now = now.Add(time.Hour)
	if router.Fresh() {
		t.Fatal("failed refresh kept stale rates fresh")
	}
	if _, _, err := router.Plan(call(f.served)); !errors.Is(err, routing.ErrStale) {
		t.Fatalf("plan with stale rates: %v", err)
	}
}

func TestRouteBuildKeepsTheRatesReadBeforeAConcurrentCommit(t *testing.T) {
	f := newRouteFixture(t)
	e := routable(f.served, protocol.Messages)
	e.Currency, _ = platform.ParseCurrency("CNY")
	f.build([]suppliers.Routable{e})
	set := func(version int64) error {
		return f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			kind := "adjustment"
			if version == 0 {
				kind = "initial"
			}
			_, err := billing.SetExchangeRate(t.Context(), tx, audit.System("gateway.test"), billing.ExchangeInput{
				Base: "CNY", Quote: "USD", Numerator: "10", Denominator: "71", Kind: kind,
				ExpectedVersion: version, Reason: "commit during a routing build",
			})
			return err
		})
	}
	if err := set(0); err != nil {
		t.Fatal(err)
	}
	f.deps.ExchangeRates = func(ctx context.Context) (rates []platform.ExchangeRate, err error) {
		err = f.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			rows, err := billing.ExchangeRates(ctx, tx)
			for _, r := range rows {
				rates = append(rates, r.Basis())
			}
			return err
		})
		return
	}
	changed := false
	f.deps.Prices = func(context.Context) (map[catalog.ModelID]billing.Sold, error) {
		if !changed {
			changed = true
			if err := set(1); err != nil {
				return nil, err
			}
		}
		return f.prices, nil
	}
	for _, version := range []int64{1, 2} {
		snap, count, err := f.deps.Build(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		f.deps.Publish(snap, count)
		plan, _, err := f.router.Plan(call(f.served))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Entries[0].Exchange.Version != version {
			t.Fatalf("build used version %d want %d", plan.Entries[0].Exchange.Version, version)
		}
	}
}
