package newapi

import (
	"errors"
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
	offer, err := readOffer(t, open(t, f, now))
	if err != nil {
		t.Fatal(err)
	}
	if len(offer) != 2 || offer[0].Ref != "default" || offer[1].Ref != "vip" {
		t.Fatalf("groups %+v, want default then vip", offer)
	}
	def := offer[0]
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

// TestHiddenPricesLeaveTheCostUnknown: a site that turned its pricing module off refuses the price list to a logged-in
// user, which is not a refused login: the groups and models are read and every model's cost is unknown. A 403 with a
// code of a refused login on the same read is still one.
func TestHiddenPricesLeaveTheCostUnknown(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		answer  adapter.Response
		refused bool
	}{
		{"only the root may read it", sample(t, "43-pricing-off-pat-rc51"), false},
		{"a release before that check", sample(t, "43-pricing-off-anon-rc51"), false},
		{"an expired token", answer(401, `{"code":"ACCESS_TOKEN_EXPIRED","success":false}`), true},
		{"a refused token", answer(403, `{"code":"AUTH_UNAUTHORIZED","success":false}`), true},
		{"a page from a firewall", answer(403, `<html>denied</html>`), true},
	} {
		f := offerClient(t, "", "")
		f.routes["GET /api/pricing"] = c.answer
		f.routes["GET /api/user/models?group=default"] = sample(t, "42-user-models")
		f.routes["GET /api/user/models?group=vip"] = answer(200, `{"success":true,"data":["mock-gpt"]}`)
		offer, err := readOffer(t, open(t, f, now))
		switch {
		case c.refused && !errors.Is(err, adapter.ErrAuthRejected):
			t.Errorf("%s: %v, want a refused login", c.name, err)
		case !c.refused && (err != nil || len(offer) != 2 || len(offer[0].Models) != 2):
			t.Errorf("%s: %+v, %v; want both groups and their models", c.name, offer, err)
		case !c.refused:
			checkNoCost(t, c.name, offer)
		}
	}
}

func checkNoCost(t *testing.T, name string, offer []adapter.Group) {
	t.Helper()
	for _, g := range offer {
		for _, m := range g.Models {
			if m.Cost != nil || m.Derivation != nil || m.Published {
				t.Errorf("%s: %s/%s has a cost %v, want none while the prices are hidden", name, g.Ref, m.UpstreamName, m.Cost)
			}
		}
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
	offer, err := readOffer(t, open(t, f, now))
	if err != nil {
		t.Fatal(err)
	}
	models := offer[0].Models
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
	offer, err := readOffer(t, open(t, f, now))
	if err != nil || len(offer) != 1 {
		t.Fatalf("Offer: %v, %d groups (the auto group has no ratio and is left out)", err, len(offer))
	}
	byName := map[string]adapter.Model{}
	for _, m := range offer[0].Models {
		byName[m.UpstreamName] = m
	}
	// The three the supplier prices are published, so no confirmed base stands in for them; only the unpriced one waits
	// for a base.
	for name, published := range map[string]bool{"percall": true, "expr": true, "huge": true, "unpriced": false} {
		if m := byName[name]; m.Cost != nil || m.Published != published {
			t.Errorf("%s: cost %v, published %t; want no cost, published %t", name, m.Cost, m.Published, published)
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

// TestFormatsOfDirectFormats: new-api's endpoint types name the direct formats, which do not convert, so a model that
// names only one of them must not be recorded as a chat model.
func TestFormatsOfDirectFormats(t *testing.T) {
	for _, tc := range []struct {
		endpoints []string
		want      []protocol.Format
	}{
		{[]string{"embeddings"}, []protocol.Format{protocol.Embeddings}},
		{[]string{"jina-rerank"}, []protocol.Format{protocol.Rerank}},
		{[]string{"rerank"}, []protocol.Format{protocol.Rerank}},
		{[]string{"openai", "embeddings"}, []protocol.Format{protocol.Chat, protocol.Embeddings}},
		{[]string{"embeddings", "openai"}, []protocol.Format{protocol.Embeddings, protocol.Chat}},
	} {
		if diff := cmp.Diff(tc.want, formatsOf(tc.endpoints)); diff != "" {
			t.Errorf("formats of %v (-want +got):\n%s", tc.endpoints, diff)
		}
	}
}

// TestOfferHandsOverTheFactors: each price of a model's plan comes with what new-api multiplies into it, in its order
// (model ratio, group ratio, then the ratio to the input price, its default when the pricing leaves it out), and the
// factors multiply out to the price the plan stores, rounded up once to a unit price. A ratio of more decimals than a
// price holds is rounded in the price only, so the factors still say exactly what new-api uses.
func TestOfferHandsOverTheFactors(t *testing.T) {
	t.Parallel()
	f := offerClient(t,
		`{"success":true,"data":{"vip":{"ratio":1.37,"desc":"v"}}}`,
		`{"success":true,"data":[{"model_name":"m","quota_type":0,"model_ratio":0.075,"completion_ratio":4,"cache_ratio":0.5,"create_cache_ratio":1.25},
			{"model_name":"plain","quota_type":0,"model_ratio":2},
			{"model_name":"fine","quota_type":0,"model_ratio":0.000000123456789,"completion_ratio":"3.3333"}]}`)
	f.routes["GET /api/user/models?group=vip"] = answer(200, `{"success":true,"data":["fine","m","plain"]}`)
	offer, err := readOffer(t, open(t, f, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range offer[0].Models {
		if m.Cost == nil {
			t.Fatalf("%s has no cost", m.UpstreamName)
		}
		if err := m.Derivation.Check(*m.Cost); err != nil {
			t.Errorf("%s: %v", m.UpstreamName, err)
		}
	}
	f2 := func(kind adapter.FactorKind, v string) adapter.Factor { return adapter.Factor{Kind: kind, Value: v} }
	model, group := f2(adapter.FactorModelRatio, "0.075"), f2(adapter.FactorGroupRatio, "1.37")
	for _, tc := range []struct {
		name, item string
		want       []adapter.Factor
	}{
		{"m", "input", []adapter.Factor{model, group}},
		{"m", "output", []adapter.Factor{model, group, f2(adapter.FactorCompletionRatio, "4")}},
		{"m", "cache_read", []adapter.Factor{model, group, f2(adapter.FactorCacheRatio, "0.5")}},
		{"m", "cache_write_1h", []adapter.Factor{model, group, f2(adapter.FactorCacheWriteRatio, "2")}},
		{"plain", "cache_write", []adapter.Factor{f2(adapter.FactorModelRatio, "2"), group, f2(adapter.FactorCacheWriteRatio, "1.25")}},
		{"fine", "output", []adapter.Factor{f2(adapter.FactorModelRatio, "0.000000123456789"), group, f2(adapter.FactorCompletionRatio, "3.3333")}},
	} {
		if diff := cmp.Diff(tc.want, factorsOf(offer[0].Models, tc.name, tc.item)); diff != "" {
			t.Errorf("%s %s (-want +got):\n%s", tc.name, tc.item, diff)
		}
	}
}

// factorsOf is the factors of one item of one model of a group.
func factorsOf(models []adapter.Model, name, item string) []adapter.Factor {
	for _, m := range models {
		for _, r := range m.Derivation {
			if m.UpstreamName == name && r.Item == item {
				return r.Factors
			}
		}
	}
	return nil
}
