package newapi

import (
	"math/big"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// offerClient answers the three reads of Offer from the recorded groups, pricing and model list.
func offerClient(t *testing.T, groups, pricing string) *fakeClient {
	t.Helper()
	f := newFake()
	f.routes["GET /api/user/self/groups"] = sample(t, "40-self-groups")
	f.routes["GET /api/pricing"] = sample(t, "43-pricing-anon")
	if groups != "" {
		f.routes["GET /api/user/self/groups"] = answer(200, groups)
	}
	if pricing != "" {
		f.routes["GET /api/pricing"] = answer(200, pricing)
	}
	return f
}

func cost(t *testing.T, m adapter.Model, input, output int64) *big.Rat {
	t.Helper()
	if m.Cost == nil {
		t.Fatalf("%s has no cost schedule", m.UpstreamName)
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: input, catalog.ItemOutput: output}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	total, missing, err := m.Cost.Cost(u)
	if err != nil || missing != 0 {
		t.Fatalf("Cost: %v, missing %v", err, missing)
	}
	return total.Rat()
}

// TestOfferPricesTheRecordedModelsLikeTheRecordedCharge: the recorded pricing and groups give a cost schedule whose
// estimate for the recorded request (100 in, 50 out) is the 22.5 quota that new-api rounded to the 23 it charged, so
// the schedule and the real charge agree to within the rounding of one quota.
func TestOfferPricesTheRecordedModelsLikeTheRecordedCharge(t *testing.T) {
	t.Parallel()
	f := offerClient(t, "", "")
	f.routes["GET /api/user/models?group=default"] = sample(t, "42-user-models")
	f.routes["GET /api/user/models?group=vip"] = answer(200, `{"success":true,"data":["mock-gpt"]}`)
	offer, err := open(t, f, now).Offer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(offer.Groups) != 2 || offer.Groups[0].Ref != "default" || offer.Groups[1].Ref != "vip" {
		t.Fatalf("groups %+v, want default then vip", offer.Groups)
	}
	def := offer.Groups[0]
	if def.Ratio != "1" || len(def.Models) != 2 || def.Models[0].UpstreamName != "gpt-4o-mini" {
		t.Fatalf("default group %+v", def)
	}
	if got := cost(t, def.Models[0], 100, 50); got.Cmp(big.NewRat(45, 2)) != 0 {
		t.Errorf("estimate for 100 in and 50 out is %v quota, want 22.5", got)
	}
	if diff := cmp.Diff([]protocol.Format{protocol.Chat}, def.Models[0].Formats); diff != "" {
		t.Errorf("formats (-want +got):\n%s", diff)
	}
	if got := cost(t, def.Models[1], 1, 0); got.Cmp(big.NewRat(75, 2)) != 0 { // mock-gpt: model_ratio 37.5
		t.Errorf("mock-gpt costs %v per input token, want 37.5", got)
	}
	if diff := cmp.Diff(`{"desc":"Default group","endpoint_types":["openai"]}`, string(def.Facts)); diff != "" {
		t.Errorf("facts (-want +got):\n%s", diff)
	}
}

// TestOfferMultipliesTheGroupRatioAndDerivesTheCachePrices: the group ratio scales every price, and the cache prices
// follow new-api's ratios with its defaults (a read at the input price, a write at 1.25 of it, an hour-long write at
// 1.6 of that).
func TestOfferMultipliesTheGroupRatioAndDerivesTheCachePrices(t *testing.T) {
	t.Parallel()
	f := offerClient(t,
		`{"success":true,"data":{"vip":{"ratio":1.5,"desc":"v"}}}`,
		`{"success":true,"data":[{"model_name":"m","quota_type":0,"model_ratio":0.075,"completion_ratio":4,"cache_ratio":0.5,"create_cache_ratio":1.25},
			{"model_name":"plain","quota_type":0,"model_ratio":2}]}`)
	f.routes["GET /api/user/models?group=vip"] = answer(200, `{"success":true,"data":["plain","m"]}`)
	offer, err := open(t, f, now).Offer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	models := offer.Groups[0].Models
	// m: input 0.075 × 1.5 = 0.1125; output ×4; cache read ×0.5; cache write ×1.25; one hour ×1.6.
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemOutput: 10, catalog.ItemCacheRead: 10, catalog.ItemCacheWrite: 10, catalog.ItemCacheWrite1h: 10}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	total, missing, err := models[0].Cost.Cost(u)
	if err != nil || missing != 0 {
		t.Fatalf("Cost: %v %v", err, missing)
	}
	// 0.1125 × (10 + 40 + 5 + 12.5 + 20) = 0.1125 × 87.5
	if got := total.Rat(); got.Cmp(big.NewRat(1125*875, 100000)) != 0 {
		t.Errorf("m costs %v, want %v", got, big.NewRat(1125*875, 100000))
	}
	// plain: no completion or cache ratios, so output and cache read are at the input price and a write at 1.25.
	if got := cost(t, models[1], 10, 10); got.Cmp(big.NewRat(2*15*2, 1)) != 0 { // 2 × 1.5 × 20
		t.Errorf("plain costs %v for 10 in and 10 out, want 60", got)
	}
}

// TestOfferLeavesWhatAScheduleCannotSayUnknown: a price per call, an expression, a missing model price and a ratio of
// images that differs from text are not guessed at: the cost is unknown or the capability is not claimed.
func TestOfferLeavesWhatAScheduleeCannotSayUnknown(t *testing.T) {
	t.Parallel()
	f := offerClient(t, `{"success":true,"data":{"g":{"ratio":1,"desc":""},"auto":{"ratio":"auto","desc":""}}}`,
		`{"success":true,"data":[
			{"model_name":"percall","quota_type":1,"model_price":0.02},
			{"model_name":"expr","quota_type":0,"billing_mode":"tiered_expr","billing_expr":"p*2","model_ratio":1},
			{"model_name":"img","quota_type":0,"model_ratio":1,"image_ratio":2},
			{"model_name":"img1","quota_type":0,"model_ratio":1,"image_ratio":1},
			{"model_name":"huge","quota_type":0,"model_ratio":5000},
			{"model_name":"claude","quota_type":0,"model_ratio":1,"supported_endpoint_types":["anthropic","openai"]}]}`)
	f.routes["GET /api/user/models?group=g"] = answer(200, `{"success":true,"data":["percall","expr","img","img1","huge","claude","unpriced"]}`)
	offer, err := open(t, f, now).Offer(t.Context())
	if err != nil || len(offer.Groups) != 1 {
		t.Fatalf("Offer: %v, %d groups (the auto group has no ratio and is left out)", err, len(offer.Groups))
	}
	byName := map[string]adapter.Model{}
	for _, m := range offer.Groups[0].Models {
		byName[m.UpstreamName] = m
	}
	for _, name := range []string{"percall", "expr", "huge", "unpriced"} {
		if byName[name].Cost != nil {
			t.Errorf("%s has a cost schedule", name)
		}
	}
	for name, without := range map[string]protocol.Capabilities{"img": protocol.Images, "img1": 0, "claude": 0} {
		if m := byName[name]; m.Cost == nil || m.Without != without {
			t.Errorf("%s: cost %v without %v, want a cost and %v", name, m.Cost, m.Without, without)
		}
	}
	if diff := cmp.Diff([]protocol.Format{protocol.Messages, protocol.Chat}, byName["claude"].Formats); diff != "" {
		t.Errorf("claude formats (-want +got):\n%s", diff)
	}
}

// TestUnitPriceRoundsUp: a price is never lower than the ratio says, and has at most 15 decimals.
func TestUnitPriceRoundsUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   *big.Rat
		want string
		ok   bool
	}{
		{big.NewRat(0, 1), "0", true},
		{big.NewRat(3, 40), "0.075", true},
		{big.NewRat(1, 3), "0.333333333333334", true},
		{big.NewRat(1, 10000000000000000), "0.000000000000001", true},
		{big.NewRat(1000, 1), "1000", true},
		{new(big.Rat).Add(big.NewRat(1000, 1), big.NewRat(1, 1000000000000000)), "", false},
		{big.NewRat(-1, 2), "", false},
	}
	for _, c := range cases {
		got, ok := unitPrice(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("unitPrice(%v) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
