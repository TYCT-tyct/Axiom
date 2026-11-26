package sub2api

import (
	"math/big"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// plazaOffer is the offer of offerServer with the model plaza answering plaza, by the group named claude-mock's models.
func plazaOffer(t *testing.T, plaza string) map[string]adapter.Model {
	t.Helper()
	srv := offerServer(t)
	answer := srv.answer
	srv.answer = func(r adapter.Request) (adapter.Response, error) {
		if r.Path == "/api/v1/model-plaza" {
			return fixture(t, plaza), nil
		}
		return answer(r)
	}
	offer, err := readOffer(t, manager0(t, srv, login(t, epoch.Add(24*time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]adapter.Model{}
	for _, g := range offer {
		if g.Name == "claude-mock" {
			for _, m := range g.Models {
				models[m.UpstreamName] = m
			}
		}
	}
	return models
}

// TestPublishedPricesAreWhatACallIsCharged: with the model plaza on, a model's cost is the published price times the
// multiplier its group is billed at, so an estimate is what sub2api charges: (Σ tokens × price) × multiplier
// (service/billing_service.go computeTokenBreakdown). The price line explains it as the base price times the group's
// ratio.
func TestPublishedPricesAreWhatACallIsCharged(t *testing.T) {
	t.Parallel()
	flat := plazaOffer(t, "model_plaza")["claude-fable-5"]
	if flat.Cost == nil {
		t.Fatal("a model the plaza prices per token has no cost")
	}
	tokens := map[catalog.Item]int64{
		catalog.ItemInput: 1000, catalog.ItemOutput: 200, catalog.ItemCacheRead: 5000, catalog.ItemCacheWrite: 300, catalog.ItemCacheWrite1h: 100,
	}
	published := map[catalog.Item]string{
		catalog.ItemInput: "3e-06", catalog.ItemOutput: "1.5e-05", catalog.ItemCacheRead: "3e-07", catalog.ItemCacheWrite: "3.75e-06", catalog.ItemCacheWrite1h: "6e-06",
	}
	charged := new(big.Rat)
	for item, n := range tokens {
		price, _ := new(big.Rat).SetString(published[item])
		charged.Add(charged, price.Mul(price, big.NewRat(n, 1)))
	}
	charged.Mul(charged, big.NewRat(3, 2)) // the key's billing view: 1.5
	u, err := catalog.NewUsage(tokens, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	total, missing, err := flat.Cost.Cost(u)
	if err != nil || missing != 0 || total.Rat().Cmp(charged) != 0 {
		t.Errorf("the estimate is %s dollars (missing %v, %v), sub2api charges %s", total.Rat().FloatString(12), missing, err, charged.FloatString(12))
	}
	d := flat.Derivation
	if err := d.Check(*flat.Cost); err != nil || len(d) == 0 || len(d[0].Factors) != 2 || d[0].Factors[0].Kind != adapter.FactorBasePrice ||
		d[0].Factors[1] != (adapter.Factor{Kind: adapter.FactorGroupRatio, Value: "1.5"}) {
		t.Errorf("derivation %+v (%v), want the base price times the group's ratio", d, err)
	}
}

// TestUnpublishedPricesLeaveTheCostUnknown: a model the plaza prices by context length, and every model while the plaza
// is off (its default), have no cost.
func TestUnpublishedPricesLeaveTheCostUnknown(t *testing.T) {
	t.Parallel()
	if tiered := plazaOffer(t, "model_plaza")["claude-fable-5-1"]; tiered.Cost != nil {
		t.Errorf("a model priced by context length has the cost %v", tiered.Cost)
	}
	for name, m := range plazaOffer(t, "model_plaza_off") {
		if m.Cost != nil {
			t.Errorf("with the plaza off %s has the cost %v", name, m.Cost)
		}
	}
}

// TestPlazaPricesThatAreNotPerTokenAreNotCosts: a price that depends on more than the tokens, or that a schedule cannot
// state, gives no cost; images priced apart keep the cost and leave images out; a price with more digits than a unit
// price holds is rounded up, never down.
func TestPlazaPricesThatAreNotPerTokenAreNotCosts(t *testing.T) {
	t.Parallel()
	const flat = `"billing_mode":"token","input_price":3e-06,"output_price":1.5e-05,"cache_read_price":3e-07,"intervals":[]`
	for _, c := range []struct {
		name, model, pricing, extra string
		priced                      bool
	}{
		{"per token", "m", flat, "", true},
		{"by the request", "m", `"billing_mode":"per_request","per_request_price":0.01,"intervals":[]`, "", false},
		{"by time of day", "m", flat, `,"time_pricing":{"timezone":"UTC","periods":[{"start_time":"01:00","end_time":"02:00","multiplier":2}]}`, false},
		{"by reasoning effort", "m", flat + `,"reasoning_effort_multipliers":{"high":2}`, "", false},
		{"DeepSeek's peak hours", "DeepSeek-v4-flash", flat, "", false},
		{"image output apart", "m", flat + `,"image_output_price":4e-05`, "", false},
		{"no output price", "m", `"billing_mode":"token","input_price":3e-06,"intervals":[]`, "", false},
		{"a quoted price", "m", `"billing_mode":"token","input_price":"3e-06","output_price":1.5e-05,"intervals":[]`, "", false},
	} {
		pm := plazaModel{}
		if err := jsonx.Unmarshal([]byte(`{"name":"`+c.model+`","pricing":{`+c.pricing+`}`+c.extra+`}`), &pm); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, ok := pm.base(); ok != c.priced {
			t.Errorf("%s: priced %v, want %v", c.name, ok, c.priced)
		}
	}
	images := plazaModel{Name: "m", Pricing: &plazaPricing{Mode: "token", Input: "3e-06", Output: "1.5e-05", ImageInput: "6e-06"}}
	if b, ok := images.base(); !ok || b.without != protocol.Images {
		t.Errorf("images priced apart: %+v, %v; want a cost that leaves images out", b, ok)
	}
	long := plazaModel{Name: "m", Pricing: &plazaPricing{Mode: "token", Input: "3.33333333333e-06", Output: "1e-05"}}
	b, ok := long.base()
	if !ok {
		t.Fatal("a price with 17 decimals is not priced")
	}
	if p, _ := b.schedule.BaseRate(catalog.ItemInput); p.String() != "0.000003333333334" {
		t.Errorf("3.33333333333e-06 is priced %s, want it rounded up to 0.000003333333334", p)
	}
}
