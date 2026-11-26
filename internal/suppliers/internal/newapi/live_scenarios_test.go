//go:build contract

package newapi

import (
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
)

func liveRatio(t *testing.T, a adapter.Amount) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(string(a))
	if !ok {
		t.Fatalf("%q is not a number", a)
	}
	return r
}

// liveCost is what the model's schedule says in tokens in and out cost, in quota.
func liveCost(t *testing.T, m adapter.Model, in, out int64) *big.Rat {
	t.Helper()
	if m.Cost == nil {
		t.Fatalf("%s has no cost schedule", m.UpstreamName)
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: in, catalog.ItemOutput: out}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	total, missing, err := m.Cost.Cost(u)
	if err != nil || missing != 0 {
		t.Fatalf("Cost of %s: %v, missing %v", m.UpstreamName, err, missing)
	}
	return total.Rat()
}

// scenarios turns on, one at a time, the two settings of a supplier that its own administrator changes and the
// adapter has to live with: the pricing module off, and the log's range limited to a day. It records the system's answer
// to each and checks that the adapter goes on reading: the groups and models with their costs unknown, and every pass of
// the log within the limit.
func (r *liveRun) scenarios(t *testing.T) {
	t.Run("pricing module off", r.pricingModuleOff)
	t.Run("log range limited to a day", r.logRangeLimited)
}

func (r *liveRun) pricingModuleOff(t *testing.T) {
	r.setPricingModule(false)
	t.Cleanup(func() { r.setPricingModule(true) })
	anon := r.record("43-pricing-off-anon-rc51", http.MethodGet, "/api/pricing", "", "")
	pat := r.record("43-pricing-off-pat-rc51", http.MethodGet, "/api/pricing", r.pat, "<ALICE_PAT>")
	if anon.Status != http.StatusForbidden || pat.Status != http.StatusForbidden {
		t.Fatalf("/api/pricing with the module off answered %d to anyone and %d to the token, want 403 to both", anon.Status, pat.Status)
	}
	page, err := r.m.Offer(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != 2 {
		t.Fatalf("Offer with the pricing module off = %+v, %v; want both groups", page, err)
	}
	for _, g := range page.Items {
		for _, model := range g.Models {
			if model.Cost != nil {
				t.Errorf("%s/%s has the cost %v while the prices are hidden", g.Ref, model.UpstreamName, model.Cost)
			}
		}
	}
}

func (r *liveRun) logRangeLimited(t *testing.T) {
	r.limitLogRange(1)
	now := r.clock.Now().Unix()
	a := r.record("84-err-log-range-rc51", http.MethodGet,
		fmt.Sprintf("/api/log/self?type=2&page_size=100&p=1&start_timestamp=%d&end_timestamp=%d", now-3*86400, now), r.pat, "<ALICE_PAT>")
	var env struct {
		Success bool `json:"success"`
	}
	a.Decode(t, &env)
	if a.Status != http.StatusOK || env.Success {
		t.Fatalf("a range of three days with the limit at one answered %d %.200s, want 200 with success false", a.Status, a.Body)
	}
	// A supplier connected three days ago whose log was never read: new-api clamps the end of a range to now, so only a
	// start that old makes the range long. The first passes are the late one, from the connection to now, a day each.
	scan, err := adapter.NewWindowScan(r.clock.Now().Add(-72 * time.Hour)).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, scan, err = r.m.Charges(t.Context(), scan); err != nil {
			t.Fatalf("Charges over three days with the range limited to one: %v", err)
		}
	}
}
