//go:build contract

package newapi

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// liveRun is the state the steps of TestLiveNewAPI hand on to each other: the adapter's managers for the two tokens,
// the scan that starts at the connection, the made keys and the relayed calls. The steps pass t.Context() to the adapter
// where they call it, and the helpers of live make their own requests.
type liveRun struct {
	*live
	a       *Adapter
	c       *client.Client
	m       adapter.Manager
	narrow  adapter.Manager
	scan    adapter.ScanState
	made    [2]adapter.KeyRef
	keyText [2]string
	relays  []*relay
	logged  map[string]logRecord
}

func TestLiveNewAPI(t *testing.T) {
	l := startLive(t)
	a := &Adapter{now: l.clock.Now}
	r := &liveRun{live: l, a: a, c: l.adapterClient(a)}

	l.record("01-detect-status-rc51", http.MethodGet, "/api/status", "", "")
	l.record("02-detect-encryption-key-rc51", http.MethodGet, "/api/user/login/encryption-key", "", "")
	t.Run("detect", r.detect)
	r.openManagers(t)
	t.Run("account", r.account)
	t.Run("offer", r.offer)
	t.Run("keys", r.keys)
	if r.keyText[0] == "" {
		t.Fatal("no key to relay with")
	}
	r.relayCalls()
	t.Run("charges", r.charges)
	t.Run("a token without the usage scope", r.narrowToken)
	r.scenarios(t)
	l.writeSamples()
}

func (r *liveRun) detect(t *testing.T) {
	d, err := r.a.Detect(t.Context(), r.c)
	if err != nil || d.Generation != "1.0" || d.Version != "v1.0.0-rc.51" || d.Captcha {
		t.Fatalf("Detect = %+v, %v", d, err)
	}
}

func (r *liveRun) openManagers(t *testing.T) {
	var err error
	if r.m, err = r.open(r.a, r.c, r.pat); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.narrow, err = r.open(r.a, r.c, r.narrowPAT); err != nil {
		t.Fatalf("Open with the narrow token: %v", err)
	}
	// The scan starts now: nothing before the connection is ours.
	if _, r.scan, err = r.m.Charges(t.Context(), nil); err != nil {
		t.Fatalf("Charges, first scan: %v", err)
	}
}

func (r *liveRun) account(t *testing.T) {
	r.record("24-self-pat-rc51", http.MethodGet, "/api/user/self", r.pat, "<ALICE_PAT>")
	r.record("45-subscription-self-rc51", http.MethodGet, "/api/subscription/self", r.pat, "<ALICE_PAT>")
	acc, err := r.m.Account(t.Context())
	want := adapter.Account{Balance: "5000000", Funding: adapter.FundingWallet, UnitHint: "quota_per_unit=500000"}
	if err != nil || acc != want {
		t.Fatalf("Account = %+v, %v; want %+v", acc, err, want)
	}
}

func (r *liveRun) offer(t *testing.T) {
	r.record("40-self-groups-rc51", http.MethodGet, "/api/user/self/groups", r.pat, "<ALICE_PAT>")
	r.record("42-user-models-rc51", http.MethodGet, "/api/user/models?group=default", r.pat, "<ALICE_PAT>")
	r.record("43-pricing-anon-rc51", http.MethodGet, "/api/pricing", "", "")
	page, err := r.m.Offer(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != 2 {
		t.Fatalf("Offer = %+v, %v", page, err)
	}
	for _, g := range page.Items {
		checkOfferedGroup(t, g)
	}
}

func checkOfferedGroup(t *testing.T, g adapter.Group) {
	want := map[string]adapter.Amount{"default": "1", "premium": "1.5"}[g.Ref]
	if want == "" || g.Ratio != want || len(g.Models) != 2 || g.Models[0].UpstreamName != adaptertest.UpstreamAnthropicModel {
		t.Errorf("group %+v, want ratio %q and the two upstream models", g, want)
	}
	// gpt-6-astra: model ratio 2.5, completion ratio 4, and the group's ratio: (100 + 50 × 4) × 2.5 × ratio quota.
	wantCost := new(big.Rat).Mul(big.NewRat(750, 1), liveRatio(t, g.Ratio))
	if got := liveCost(t, g.Models[1], 100, 50); got.Cmp(wantCost) != 0 {
		t.Errorf("group %s prices 100 in and 50 out of %s at %v quota, want %v", g.Ref, g.Models[1].UpstreamName, got, wantCost)
	}
	t.Logf("group %s: %s formats %v, %s formats %v", g.Ref, g.Models[0].UpstreamName, g.Models[0].Formats, g.Models[1].UpstreamName, g.Models[1].Formats)
}

func (r *liveRun) keys(t *testing.T) {
	ctx := t.Context()
	maker := r.m.(adapter.KeyMaker)
	for i, g := range []string{"default", "premium"} {
		ref, err := maker.CreateKey(ctx, g, "axiom-live-"+g)
		if err != nil {
			t.Fatalf("CreateKey(%s): %v", g, err)
		}
		r.made[i] = ref
	}
	text := make([]secret.Secret, len(r.made))
	for i, ref := range r.made {
		var err error
		if text[i], err = r.m.KeySecret(ctx, ref); err != nil || !strings.HasPrefix(text[i].Reveal(), "sk-") {
			t.Fatalf("KeySecret(%s) = %v", ref, err)
		}
		r.keyText[i] = text[i].Reveal()
		bare, placeholder := strings.TrimPrefix(r.keyText[i], "sk-"), fmt.Sprintf("<ALICE_KEY%d_BARE>", i+1)
		r.scrub.Secret(r.keyText[i], fmt.Sprintf("<ALICE_KEY%d>", i+1))
		r.scrub.Secret(maskKey(bare), maskKey(placeholder)) // the listing shows a key masked, and FindKey matches by the mask
		r.scrub.Secret(bare, placeholder)
	}
	r.checkKeyListing(t)
	r.record("28-reveal-key-rc51", http.MethodPost, "/api/token/"+string(r.made[0])+"/key", r.pat, "<ALICE_PAT>")
	r.checkFindKey(t, text[0])
}

func (r *liveRun) checkKeyListing(t *testing.T) {
	page, err := r.m.Keys(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != 2 {
		t.Fatalf("Keys = %+v, %v", page, err)
	}
	for _, k := range page.Items {
		if k.Status != adapter.KeyActive || k.Policy == nil || k.Policy.Models != nil || k.Policy.ExpiresAt != nil || k.Name != "axiom-live-"+k.GroupRef {
			t.Errorf("key %+v, want an active key of the group it was named for, with no limit", k)
		}
	}
	r.record("57-list-keys-rc51", http.MethodGet, "/api/token/?p=1&page_size=100", r.pat, "<ALICE_PAT>")
	r.record("57-search-key-rc51", http.MethodGet, "/api/token/search?keyword=axiom-live-default&page_size=100", r.pat, "<ALICE_PAT>")
}

func (r *liveRun) checkFindKey(t *testing.T, first secret.Secret) {
	finder := r.m.(adapter.KeyFinder)
	found, ok, err := finder.FindKey(t.Context(), first)
	if err != nil || !ok || found.Ref != r.made[0] || found.GroupRef != "default" {
		t.Fatalf("FindKey = %+v, %t, %v; want the first key", found, ok, err)
	}
	if _, ok, err := finder.FindKey(t.Context(), secret.New("sk-"+strings.Repeat("x", 48))); err != nil || ok {
		t.Fatalf("FindKey of a text the system does not hold = %t, %v", ok, err)
	}
}

// relayCalls makes one call of each format and stream mode with the made keys and reads what the system logged for them.
func (r *liveRun) relayCalls() {
	r.relays = []*relay{
		{key: r.keyText[0], path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, wantQuota: 750},
		{key: r.keyText[0], path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, stream: true, wantQuota: 750},
		{key: r.keyText[0], path: "/v1/responses", model: adaptertest.UpstreamOpenAIModel, wantQuota: 750},
		{key: r.keyText[0], path: "/v1/responses", model: adaptertest.UpstreamOpenAIModel, stream: true, wantQuota: 750},
		{key: r.keyText[0], path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, wantQuota: 1750},
		{key: r.keyText[0], path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, stream: true, wantQuota: 1750},
		{key: r.keyText[1], path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, wantQuota: 1125},
	}
	for _, c := range r.relays {
		r.send(r.a, c)
	}
	r.logged = r.logs(len(r.relays))
	r.record("32-log-self-rc51", http.MethodGet, "/api/log/self?type=2&page_size=100&p=1", r.pat, "<ALICE_PAT>")
}

func (r *liveRun) charges(t *testing.T) {
	ctx := t.Context()
	r.clock.Advance(2 * time.Minute) // a pass reads up to half a minute ago, and not before a minute has passed
	var charges []adapter.Charge
	for done := false; !done; {
		page, next, err := r.m.Charges(ctx, r.scan)
		if err != nil {
			t.Fatalf("Charges: %v", err)
		}
		charges, r.scan, done = append(charges, page.Charges...), next, page.Done
	}
	byRef := map[adapter.ChargeRef]adapter.Charge{}
	for _, c := range charges {
		byRef[c.Ref] = c
	}
	if len(charges) != len(r.relays) || len(byRef) != len(r.relays) {
		t.Fatalf("Charges read %d records (%d distinct), want %d", len(charges), len(byRef), len(r.relays))
	}
	for _, c := range r.relays {
		got, ok := byRef[c.ref]
		r.checkCharge(t, c, got, ok)
	}
	acc, err := r.m.Account(ctx)
	if err != nil || acc.Balance != adapter.Amount(strconv.FormatInt(5000000-(4*750+2*1750+1125), 10)) {
		t.Errorf("Account after the calls = %+v, %v; want the quota less the charges", acc, err)
	}
}

func (r *liveRun) checkCharge(t *testing.T, c *relay, got adapter.Charge, ok bool) {
	rec := r.logged[string(c.ref)]
	switch {
	case !ok:
		t.Errorf("%s (stream %t): no charge with the request id %q the relay answered with", c.path, c.stream, c.ref)
	case got.Amount != adapter.Amount(strconv.FormatInt(rec.Quota, 10)) || rec.Quota != c.wantQuota:
		t.Errorf("%s (stream %t): charge %s, the log says %d, want %d", c.path, c.stream, got.Amount, rec.Quota, c.wantQuota)
	case got.Model != c.model || got.Funding != adapter.FundingWallet || got.Key != adapter.KeyRef(strconv.FormatInt(rec.TokenID, 10)):
		t.Errorf("%s (stream %t): charge %+v, want model %s paid from the wallet by key %d", c.path, c.stream, got, c.model, rec.TokenID)
	case got.Usage.Count(catalog.ItemInput) != adaptertest.UpstreamInputTokens || got.Usage.Count(catalog.ItemOutput) != adaptertest.UpstreamOutputTokens || !got.Reported.Has(catalog.ItemInput):
		t.Errorf("%s (stream %t): usage %+v reported %v, want 100 in and 50 out with the input reported", c.path, c.stream, got.Usage, got.Reported.List())
	}
}

func (r *liveRun) narrowToken(t *testing.T) {
	ctx := t.Context()
	_, state, err := r.narrow.Charges(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(2 * time.Minute)
	_, _, err = r.narrow.Charges(ctx, state)
	var denied adapter.ScopeDenied
	if !errors.Is(err, adapter.ErrAuthRejected) || !errors.As(err, &denied) || denied.Scope != "usage:read" {
		t.Fatalf("Charges with the narrow token = %v; want a rejection naming usage:read", err)
	}
	r.record("83-pat-narrow-missing-scope-rc51", http.MethodGet, "/api/log/self", r.narrowPAT, "<ALICE_PAT_NARROW>")
}
