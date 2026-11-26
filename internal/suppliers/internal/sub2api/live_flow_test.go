//go:build contract

package sub2api

import (
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
)

// relay is one call of a made key and what a charge for it must be, in dollars.
type relay struct {
	group, path, model string
	stream             bool
	want               string
	ref                adapter.ChargeRef
}

// send makes the call with key and keeps the request id the relay answered with, as the module's call code reads it.
func (l *live) send(a *Adapter, key string, r *relay) adaptertest.Answer {
	l.t.Helper()
	header := adaptertest.Bearer(key)
	body := map[string]any{"model": r.model, "stream": r.stream}
	switch {
	case strings.HasSuffix(r.path, "/messages"):
		header.Set("Anthropic-Version", "2023-06-01")
		body["max_tokens"] = 100
		body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
	case strings.HasSuffix(r.path, "/responses"):
		body["input"] = "hi"
	default:
		body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
	}
	ans := adaptertest.Send(l.t, http.MethodPost, l.base, r.path, header, body)
	ref, ok := a.Calls().ChargeRef(ans.Header)
	if !ok {
		l.t.Fatalf("%s: %d, no charge reference in the answer's headers: %.300s", r.path, ans.Status, ans.Body)
	}
	r.ref = ref
	return ans
}

// offered reads the offer and returns our groups by name.
func (l *live) offered(t *testing.T, m adapter.Manager) map[string]adapter.Group {
	t.Helper()
	page, err := m.Offer(t.Context(), nil)
	if err != nil || !page.Done {
		t.Fatalf("Offer = %+v, %v", page, err)
	}
	ours := map[string]adapter.Group{}
	for _, g := range page.Items {
		if known := l.groupByID(g.Ref); known != nil {
			ours[known.name] = g
		}
	}
	if len(ours) != len(l.groups) {
		t.Fatalf("the offer lists %d of our %d groups: %+v", len(ours), len(l.groups), page.Items)
	}
	return ours
}

func (l *live) groupByID(ref string) *liveGroup {
	for _, g := range l.groups {
		if strconv.FormatInt(g.id, 10) == ref {
			return g
		}
	}
	return nil
}

func amount(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat).SetInt64(-1)
	}
	return r
}

// checkCharges reads the scan to its end and compares every charge with the relay call that made it and with the system's
// own usage list.
func (r *liveRun) checkCharges(t *testing.T) {
	r.clock.Advance(2 * time.Minute) // a pass reads up to half a minute ago, and not before a minute has passed
	byRef := r.readCharges(t)
	spent := new(big.Rat)
	for _, c := range r.relays {
		got, ok := byRef[c.ref]
		rec := r.logged[strings.TrimPrefix(string(c.ref), "client:")]
		if rec.RequestID == "" {
			rec = r.logged[string(c.ref)] // the usage list names a request "client:" and the id the relay answered with
		}
		want := amount(c.want)
		spent.Add(spent, want)
		r.checkCharge(t, c, got, ok, rec, want)
	}
	acc, err := r.m.Account(t.Context())
	if want := new(big.Rat).Sub(big.NewRat(10, 1), spent); err != nil || amount(string(acc.Balance)).Cmp(want) != 0 {
		t.Errorf("Account after the calls = %+v, %v; want %v", acc, err, want.FloatString(6))
	}
}

func (r *liveRun) readCharges(t *testing.T) map[adapter.ChargeRef]adapter.Charge {
	byRef := map[adapter.ChargeRef]adapter.Charge{}
	count := 0
	for done := false; !done; {
		page, next, err := r.m.Charges(t.Context(), r.scan)
		if err != nil {
			t.Fatalf("Charges: %v", err)
		}
		for _, c := range page.Charges {
			byRef[c.Ref] = c
			count++
		}
		r.scan, done = next, page.Done
	}
	if count != len(r.relays) || len(byRef) != len(r.relays) {
		t.Fatalf("Charges read %d records (%d distinct), want %d", count, len(byRef), len(r.relays))
	}
	return byRef
}

func (r *liveRun) checkCharge(t *testing.T, c *relay, got adapter.Charge, ok bool, rec usageRecord, want *big.Rat) {
	switch {
	case !ok:
		t.Errorf("%s of %s (stream %t): no charge with the request id %q the relay answered with", c.path, c.group, c.stream, c.ref)
	case amount(string(got.Amount)).Cmp(want) != 0 || amount(string(rec.Cost)).Cmp(want) != 0:
		t.Errorf("%s of %s (stream %t): charge %s, the usage list says %s, want %s", c.path, c.group, c.stream, got.Amount, rec.Cost, c.want)
	case got.Model != c.model || got.Funding != adapter.FundingWallet || got.Key != r.ids[c.group]:
		t.Errorf("%s of %s (stream %t): charge %+v, want model %s paid from the wallet by key %s", c.path, c.group, c.stream, got, c.model, r.ids[c.group])
	case got.Usage.Count(catalog.ItemInput) != adaptertest.UpstreamInputTokens || got.Usage.Count(catalog.ItemOutput) != adaptertest.UpstreamOutputTokens ||
		!got.Reported.Has(catalog.ItemInput) || !got.Reported.Has(catalog.ItemOutput):
		t.Errorf("%s of %s (stream %t): usage %+v reported %v, want 100 in and 50 out, both reported", c.path, c.group, c.stream, got.Usage, got.Reported.List())
	}
}

// plaza checks the model plaza in both of its states and records them: the one setting of a supplier whose
// administrator changes what an ordinary user sees of prices, and the adapter prices the models from it.
func (r *liveRun) plaza(t *testing.T) {
	on := r.record("model_plaza_0225", "/api/v1/model-plaza", r.session)
	r.setModelPlaza(false)
	off := r.record("model_plaza_disabled_0225", "/api/v1/model-plaza", r.session)
	r.setModelPlaza(true)
	if on.Status != http.StatusOK || off.Status != http.StatusNotFound {
		t.Fatalf("the model plaza answered %d on and %d off, want 200 and 404", on.Status, off.Status)
	}
	checkPlazaPrices(t, on)
	r.checkOfferCosts(t)
}

func checkPlazaPrices(t *testing.T, on adaptertest.Answer) {
	var plaza struct {
		Data struct {
			Groups []struct {
				Name   string `json:"name"`
				Models []struct {
					Name    string `json:"name"`
					Pricing struct {
						Input number `json:"input_price"`
					} `json:"pricing"`
				} `json:"models"`
			} `json:"groups"`
		} `json:"data"`
	}
	on.Decode(t, &plaza)
	prices := map[string]number{}
	for _, g := range plaza.Data.Groups {
		for _, model := range g.Models {
			prices[g.Name+"/"+model.Name] = model.Pricing.Input
		}
	}
	if prices["claude-1/"+adaptertest.UpstreamAnthropicModel] != "0.000005" || prices["gpt-1/"+adaptertest.UpstreamOpenAIModel] != "0.0000025" {
		t.Errorf("the model plaza prices %v, want the channel's per-token input prices", prices)
	}
}

// checkOfferCosts compares what the offer prices a call of the fake upstream's usage at with what sub2api charged for it
// (the relays' want, read back from its usage log by the charges subtest): the published price times the group's
// multiplier.
func (r *liveRun) checkOfferCosts(t *testing.T) {
	offer := r.offered(t, r.m)
	for _, c := range r.relays {
		model := offer[c.group].Models[slices.IndexFunc(offer[c.group].Models, func(m adapter.Model) bool { return m.UpstreamName == c.model })]
		u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: adaptertest.UpstreamInputTokens, catalog.ItemOutput: adaptertest.UpstreamOutputTokens}, catalog.ServiceTierStandard, nil)
		if err != nil || model.Cost == nil {
			t.Fatalf("%s/%s: cost %v, %v; want the plaza's price", c.group, c.model, model.Cost, err)
		}
		total, missing, err := model.Cost.Cost(u)
		want, _ := new(big.Rat).SetString(c.want)
		if err != nil || missing != 0 || total.Rat().Cmp(want) != 0 {
			t.Errorf("%s/%s: the offer prices the call at %s (missing %v, %v), sub2api charged %s", c.group, c.model, total.Rat().FloatString(10), missing, err, c.want)
		}
	}
}
