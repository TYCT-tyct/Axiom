package routing_test

import (
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/routing"
)

func audienceLayers(tb testing.TB) []billing.AudiencePrice {
	tb.Helper()
	return []billing.AudiencePrice{
		{Target: billing.Selector{Kind: "all"}, Sold: billing.Sold{Version: uuid.UUID(id(501)), Schedule: schedule(tb, "0.000001", "0.000002", "")}},
		{Audience: billing.Audience{Tier: accounts.TierID(id(601))}, Target: billing.Selector{Kind: "all"}, Sold: billing.Sold{Version: uuid.UUID(id(502)), Schedule: schedule(tb, "0.000002", "0.000004", "")}},
		{Audience: billing.Audience{Account: accounts.AccountID(acct)}, Target: billing.Selector{Kind: "all"}, Sold: billing.Sold{Version: uuid.UUID(id(503)), Schedule: schedule(tb, "0.000003", "0.000006", "")}},
	}
}

type audienceRouteCase struct {
	name    string
	account routing.AccountID
	tier    accounts.TierID
	blocked bool
	target  string
	want    int
}

func TestAudienceRoutesUseThePreviewPrice(t *testing.T) {
	for _, tc := range []audienceRouteCase{
		{"default", routing.AccountID(id(600)), accounts.TierID(id(602)), false, "all", 0},
		{"tier", routing.AccountID(id(600)), accounts.TierID(id(601)), false, "all", 1},
		{"account", acct, accounts.TierID(id(601)), false, "all", 2},
		{"changed tier", routing.AccountID(id(600)), accounts.TierID(id(602)), false, "all", 0},
		{"target precedes audience", acct, accounts.TierID(id(601)), false, "model", 0},
		{"blocked override", acct, accounts.TierID(id(601)), true, "all", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkAudienceRoute(t, tc)
		})
	}
}

func assertRoutePrice(t *testing.T, got routing.PriceRef, preview billing.Sold) {
	t.Helper()
	usage, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 135, catalog.ItemOutput: 29}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	charge, _, err := got.Schedule.Cost(usage)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := preview.Schedule.Cost(usage)
	if err != nil || charge != expected {
		t.Fatal("route price differs from preview", err)
	}
}

func checkAudienceRoute(t *testing.T, tc audienceRouteCase) {
	t.Helper()
	layers := audienceLayers(t)
	layers[0].Target.Kind = tc.target
	if tc.blocked {
		layers[2].Sold = billing.Sold{}
	}
	e := endpoint(t, 1, func(e *routing.Endpoint) { e.AudiencePrices = billing.CompilePrices(layers) })
	h := newHarness(t, routing.Config{}, openPreset(), e)
	in := input()
	in.Admission.Account, in.Admission.Tier = tc.account, tc.tier
	p, _, err := h.r.Plan(in)
	if tc.blocked {
		if err == nil {
			t.Fatal("blocked account used another audience's price")
		}
		return
	}
	if err != nil || len(p.Entries) == 0 {
		t.Fatal(err)
	}
	preview := billing.Sold{Audiences: billing.CompilePrices(layers)}.For(billing.Audience{Account: accounts.AccountID(tc.account), Tier: tc.tier})
	got := p.Entries[0].Price
	if got.Version != layers[tc.want].Sold.Version || got.Version != preview.Version {
		t.Fatalf("route version %s, preview %s", got.Version, preview.Version)
	}
	assertRoutePrice(t, got, preview)
	in.Override.MaxPrice = limitOn(t, catalog.ItemInput, "0.0000015")
	_, _, err = h.r.Plan(in)
	if (err != nil) != (tc.want != 0) {
		t.Fatal("price cap used the wrong audience", err)
	}
}
