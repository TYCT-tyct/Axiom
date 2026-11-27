package newapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// The legacy systems' samples were recorded by TestLiveNewAPI0132 and TestLiveOneAPI0610 (suffixes -0132 and
// -oneapi0610).

const fakeAccessToken = "system-token-not-a-real-one-0123"

// openLegacy opens a manager with a legacy login for user 2 of the system whose status sample is sfx's.
func openLegacy(t *testing.T, c adapter.Client, sfx string, now func() time.Time) adapter.Manager {
	t.Helper()
	if f, ok := c.(*fakeClient); ok {
		f.routes["GET /api/status"] = sample(t, "01-detect-status-"+sfx)
	}
	raw, err := EncodeLegacyLogin(LegacyLogin{AccessToken: secret.New(fakeAccessToken), UserID: "2"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := (&Adapter{now: now}).Open(t.Context(), c, loginStore{raw})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fixedNow() time.Time { return now }

// TestLegacyDetectTellsTheSystem: new-api before 1.0 has no login encryption key but a setup member in its status;
// one-api has neither.
func TestLegacyDetectTellsTheSystem(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ sfx, generation, version string }{{"0132", "0.13", "v0.13.2"}, {"oneapi0610", "one-api", "v0.6.10"}} {
		f := newFake()
		f.routes["GET /api/status"], f.routes["GET /api/user/login/encryption-key"] = sample(t, "01-detect-status-"+c.sfx), sample(t, "02-detect-encryption-key-"+c.sfx)
		got, err := New().Detect(t.Context(), f)
		if err != nil || got != (adapter.Detection{Generation: c.generation, Version: c.version}) {
			t.Errorf("%s: Detect = %+v, %v; want %s %s", c.sfx, got, err, c.generation, c.version)
		}
	}
}

// TestLegacyAccount: a legacy login is sent as the system access token without a scheme and the user id, and reads
// the wallet; one-api has no subscriptions to ask about.
func TestLegacyAccount(t *testing.T) {
	t.Parallel()
	for _, sfx := range []string{"0132", "oneapi0610"} {
		f := newFake()
		f.routes["GET /api/user/self"] = sample(t, "24-self-"+sfx)
		if sfx == "0132" {
			f.routes["GET /api/subscription/self"] = sample(t, "45-subscription-self-0132")
		}
		got, err := openLegacy(t, f, sfx, fixedNow).Account(t.Context())
		want := adapter.Account{Balance: "5000000", Funding: adapter.FundingWallet, UnitHint: "quota_per_unit=500000"}
		if err != nil || got != want {
			t.Errorf("%s: Account = %+v, %v; want %+v", sfx, got, err, want)
		}
		for _, r := range f.sent {
			if r.Header.Get("Authorization") != fakeAccessToken || r.Header.Get("New-Api-User") != "2" {
				t.Errorf("%s: %s sent with %v, want the token and the user id", sfx, r.Path, r.Header)
			}
		}
	}
}

// TestLegacyLoginRefused: a token that is no good, answered 200 with success false, and a user id that is not the
// token's are a refused login, named without the supplier's words.
func TestLegacyLoginRefused(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"53-err-self-bad-token-0132", "53-err-self-wrong-user-0132", "53-err-self-bad-token-oneapi0610"} {
		f := newFake()
		f.routes["GET /api/user/self"] = sample(t, name)
		_, err := openLegacy(t, f, name[strings.LastIndex(name, "-")+1:], fixedNow).Account(t.Context())
		if !errors.Is(err, adapter.ErrAuthRejected) || strings.ContainsAny(err.Error(), "无效") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "match") {
			t.Errorf("%s: %v, want a rejected login without the supplier's words", name, err)
		}
	}
}

// TestLegacyOffer: new-api before 1.0 lists the models of all the account's groups together, so a priced model stays
// only in the groups its price names, and one without a price in every group without a cost; its auto group, whose
// ratio it writes as 自动, is left out as 1.0's is. one-api's one group comes at a hidden ratio of 1, without costs: it
// has no price list to ask.
func TestLegacyOffer(t *testing.T) {
	t.Parallel()
	const none = -1 // no cost
	models := sample(t, "42-user-models-0132")
	models.Body = []byte(strings.Replace(string(models.Body), `"claude-opus-5-5"`, `"claude-opus-5-5","unpriced"`, 1))
	groups := sample(t, "40-self-groups-0132")
	groups.Body = []byte(strings.Replace(string(groups.Body), `"premium":{`, `"auto":{"desc":"Auto","ratio":"自动"},"premium":{`, 1))
	cases := []struct {
		name, sfx string
		routes    map[string]adapter.Response
		want      map[string]map[string]int64 // group → model → the quota of 100 tokens in and 50 out
		ratios    map[string]adapter.Amount
		hidden    bool
	}{
		{
			name: "new-api 0.13", sfx: "0132",
			routes: map[string]adapter.Response{"GET /api/user/self/groups": groups, "GET /api/user/models": models, "GET /api/pricing": sample(t, "43-pricing-0132")},
			want: map[string]map[string]int64{
				"default": {"gpt-6-astra": 750, "unpriced": none},
				"premium": {"claude-opus-5-5": 2625, "gpt-6-astra": 1125, "unpriced": none},
			},
			ratios: map[string]adapter.Amount{"default": "1", "premium": "1.5"},
		},
		{
			name: "one-api", sfx: "oneapi0610",
			routes: map[string]adapter.Response{"GET /api/user/self": sample(t, "24-self-oneapi0610"), "GET /api/user/available_models": sample(t, "42-available-models-oneapi0610")},
			want:   map[string]map[string]int64{"default": {"gpt-6-astra": none}},
			ratios: map[string]adapter.Amount{"default": "1"}, hidden: true,
		},
	}
	for _, c := range cases {
		f := newFake()
		maps.Copy(f.routes, c.routes)
		offer, err := readOffer(t, openLegacy(t, f, c.sfx, fixedNow))
		if err != nil || len(offer) != len(c.want) {
			t.Fatalf("%s: Offer = %+v, %v", c.name, offer, err)
		}
		for _, g := range offer {
			checkLegacyGroup(t, c.name, g, c.ratios[g.Ref], c.hidden, c.want[g.Ref])
		}
	}
}

func checkLegacyGroup(t *testing.T, name string, g adapter.Group, ratio adapter.Amount, hidden bool, want map[string]int64) {
	t.Helper()
	var facts struct {
		RatioShown *bool `json:"ratio_shown"`
	}
	if g.Ratio != ratio || jsonx.Unmarshal(g.Facts, &facts) != nil || (facts.RatioShown != nil) != hidden || len(g.Models) != len(want) {
		t.Errorf("%s: group %s at %s with facts %s and %d models, want %s, ratio hidden %t, models %v", name, g.Ref, g.Ratio, g.Facts, len(g.Models), ratio, hidden, want)
		return
	}
	for _, m := range g.Models {
		quota, ok := want[m.UpstreamName]
		switch {
		case !ok:
			t.Errorf("%s: group %s serves %s", name, g.Ref, m.UpstreamName)
		case quota < 0 && m.Cost != nil:
			t.Errorf("%s: group %s prices %s, which has no price", name, g.Ref, m.UpstreamName)
		case quota >= 0 && cost(t, m, 100, 50).Cmp(big.NewRat(quota, 1)) != 0:
			t.Errorf("%s: group %s prices %s at %v, want %d", name, g.Ref, m.UpstreamName, cost(t, m, 100, 50), quota)
		}
	}
}

// TestLegacyKeys: the keys are listed in their groups (one-api's in the account's one), a key is made and its id
// known, its text is read, and a text is found in the listing.
func TestLegacyKeys(t *testing.T) {
	t.Parallel()
	reveal2 := sample(t, "28-reveal-key-0132")
	reveal2.Body = []byte(strings.Replace(string(reveal2.Body), "KEY1", "KEY2", 1))
	cases := []struct {
		sfx    string
		routes map[string]adapter.Response
		keys   []adapter.KeyState
		read   adapter.KeyRef
		made   adapter.KeyRef
	}{
		{
			sfx: "0132",
			routes: map[string]adapter.Response{
				"GET /api/token/?p=1&page_size=100": sample(t, "57-list-keys-0132"),
				"POST /api/token/1/key":             sample(t, "28-reveal-key-0132"),
				"POST /api/token/2/key":             reveal2, // the second key has the first's mask
				"POST /api/token/":                  answer(200, `{"success":true,"message":""}`),
				"GET /api/token/search?keyword=axiom-live-default&page_size=100": sample(t, "57-search-key-0132"),
			},
			keys: []adapter.KeyState{
				{Ref: "2", GroupRef: "premium", Name: "axiom-live-premium", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}},
				{Ref: "1", GroupRef: "default", Name: "axiom-live-default", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}},
			},
			read: "1", made: "1",
		},
		{
			sfx: "oneapi0610",
			routes: map[string]adapter.Response{
				"GET /api/user/self": sample(t, "24-self-oneapi0610"), "GET /api/token/?p=0": sample(t, "57-list-keys-oneapi0610"),
				"GET /api/token/2": sample(t, "28-read-key-oneapi0610"), "POST /api/token/": sample(t, "58-create-key-oneapi0610"),
			},
			keys: []adapter.KeyState{
				{Ref: "2", GroupRef: "default", Name: "axiom-live-default", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}},
				{Ref: "1", GroupRef: "default", Name: "default", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{}},
			},
			read: "2", made: "3",
		},
	}
	for _, c := range cases {
		f := newFake()
		maps.Copy(f.routes, c.routes)
		m := openLegacy(t, f, c.sfx, fixedNow)
		if keys, err := readKeys(t, m); err != nil || len(keys) != len(c.keys) || !sameKey(keys[0], c.keys[0]) || !sameKey(keys[1], c.keys[1]) {
			t.Errorf("%s: Keys = %+v, %v; want %+v", c.sfx, keys, err, c.keys)
		}
		checkLegacyKeyText(t, m, c.sfx, c.read, c.made)
	}
}

// checkLegacyKeyText reads the first key's text by its ref, finds it by its text, and makes a key whose ref is made.
func checkLegacyKeyText(t *testing.T, m adapter.Manager, sfx string, read, made adapter.KeyRef) {
	t.Helper()
	if text, err := m.KeySecret(t.Context(), read); err != nil || text.Reveal() != "sk-<ALICE_KEY1_BARE>" {
		t.Errorf("%s: KeySecret = %v; want the first key's text", sfx, err)
	}
	found, ok, err := m.(adapter.KeyFinder).FindKey(t.Context(), secret.New("sk-<ALICE_KEY1_BARE>"))
	if err != nil || !ok || found.Ref != read || found.GroupRef != "default" {
		t.Errorf("%s: FindKey = %+v, %t, %v; want key %s", sfx, found, ok, err, read)
	}
	if ref, err := m.(adapter.KeyMaker).CreateKey(t.Context(), "default", "axiom-live-default"); err != nil || ref != made {
		t.Errorf("%s: CreateKey = %q, %v; want %s", sfx, ref, err, made)
	}
}

// prefixClient answers a request by the first route its "METHOD path" starts with, for the log reads whose query
// holds the times of the pass.
type prefixClient struct{ routes map[string]adapter.Response }

func (c prefixClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	for prefix, resp := range c.routes {
		if strings.HasPrefix(r.Method+" "+r.Path, prefix) {
			return resp, nil
		}
	}
	return adapter.Response{}, fmt.Errorf("prefix client: no answer for %s %s", r.Method, r.Path)
}

// TestLegacyCharges: new-api 0.13's log reads as 1.0's; one-api's is read from page 0 of ten, without a total, and a
// record in it, which has no request id, stops the pass instead of being matched to a call by a guess. A pass with no
// records ends.
func TestLegacyCharges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, sfx, route string
		log              adapter.Response
		want             map[adapter.ChargeRef]adapter.Charge
		err              string
	}{
		{
			name: "new-api 0.13", sfx: "0132", route: "GET /api/log/self?type=2&page_size=100&p=1&", log: sample(t, "32-log-self-0132"),
			want: map[adapter.ChargeRef]adapter.Charge{
				"202611270312076924855168268d9d63lc7wf9m": {Key: "2", Amount: "2625", Model: "claude-opus-5-5"},
				"202611270312076329240638268d9d6Ir9o3JwO": {Key: "1", Amount: "750", Model: "gpt-6-astra"},
			},
		},
		{name: "one-api", sfx: "oneapi0610", route: "GET /api/log/self?type=2&p=0&", log: sample(t, "32-log-self-oneapi0610"), err: "request id"},
		{name: "one-api, nothing logged", sfx: "oneapi0610", route: "GET /api/log/self?type=2&p=0&", log: answer(200, `{"data":[],"message":"","success":true}`), want: map[adapter.ChargeRef]adapter.Charge{}},
	}
	for _, c := range cases {
		page, err := legacyPass(t, c.sfx, c.route, c.log)
		if c.err != "" {
			if !errors.Is(err, adapter.ErrUnrecognised) || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: Charges = %v, want the pass stopped at a record without a %s", c.name, err, c.err)
			}
			continue
		}
		if err != nil || !page.Done || len(page.Charges) != len(c.want) {
			t.Fatalf("%s: Charges = %+v, %v", c.name, page, err)
		}
		checkLegacyCharges(t, c.name, page.Charges, c.want)
	}
}

func checkLegacyCharges(t *testing.T, name string, charges []adapter.Charge, want map[adapter.ChargeRef]adapter.Charge) {
	t.Helper()
	for _, got := range charges {
		w := want[got.Ref]
		if got.Key != w.Key || got.Amount != w.Amount || got.Model != w.Model || got.Funding != adapter.FundingWallet {
			t.Errorf("%s: charge %+v, want %+v", name, got, w)
		}
	}
}

// legacyPass starts the scan of sfx's system and reads its first pass, whose log page is answered with log.
func legacyPass(t *testing.T, sfx, route string, log adapter.Response) (adapter.ChargePage, error) {
	t.Helper()
	clk := &testClock{t: now}
	m := openLegacy(t, prefixClient{map[string]adapter.Response{"GET /api/status": sample(t, "01-detect-status-"+sfx), route: log}}, sfx, clk.now)
	_, state, err := m.Charges(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clk.set(now.Add(2 * time.Minute))
	page, _, err := m.Charges(t.Context(), state)
	return page, err
}
