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

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// The legacy systems the adapter is checked against, pinned by digest (linux/amd64) like liveImage: new-api v0.13.2,
// the last release before 1.0, and one-api v0.6.10, its last release that is not a preview.
const (
	liveImage0132   = "calciumion/new-api@sha256:81da2c875f1de1460e6dd30324c5cb36b23ef55eaae00f317621fcb8a701ad43"
	liveImageOneAPI = "justsong/one-api@sha256:18324b85189be50c9f889f61d1723973dd088ef56f24670d6aab076ac0a044b8"
)

// liveRootToken is the access token one-api gives its root administrator on first boot (INITIAL_ROOT_ACCESS_TOKEN),
// of a throwaway container; one-api keeps it in a column of 32 characters.
const liveRootToken = "live-test-root-access-token-0001"

const liveQuota = 5000000

// legacyRun is a running legacy system set up through its own API like a supplier's account (an administrator, an
// ordinary user with quota and its system access token, channels to the fake upstream) and the adapter's manager of
// that user.
type legacyRun struct {
	*live
	sfx    string      // of the recorded samples
	user   http.Header // the user's system access token and id, as the adapter sends them
	userID string
	a      *Adapter
	c      *client.Client
	m      adapter.Manager
	scan   adapter.ScanState
}

func newLegacyRun(t *testing.T, sfx string, image string, env map[string]string) (*legacyRun, *adaptertest.Upstream, adaptertest.Reach) {
	t.Helper()
	reach := adaptertest.HostReach(false)
	up := adaptertest.StartUpstream(t, reach)
	l := &live{t: t, clock: &adaptertest.Clock{}, scrub: adaptertest.NewScrubber(), recorded: map[string]string{}}
	env["TZ"] = "UTC"
	env["SESSION_SECRET"] = strings.Repeat("live-test-session-", 2) // a throwaway container's, not a credential
	l.base = adaptertest.StartSystem(t, reach, up, adaptertest.System{
		Name: sfx, Image: image, Port: "3000", PortEnv: "PORT", ReadyPath: "/api/status", Env: env,
	})
	r := &legacyRun{live: l, sfx: sfx}
	r.a = &Adapter{now: l.clock.Now}
	r.c = l.adapterClient(r.a)
	return r, up, reach
}

// cookieLogin signs user in to the dashboard and returns the headers of its session: the cookie, and the user id that
// new-api asks for beside it.
func (r *legacyRun) cookieLogin(user, password string) (http.Header, string) {
	r.t.Helper()
	a := r.callWith(http.MethodPost, "/api/user/login", nil, map[string]any{"username": user, "password": password})
	var out struct {
		Data struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	a.Decode(r.t, &out)
	cookie, _, _ := strings.Cut(a.Header.Get("Set-Cookie"), ";")
	if cookie == "" || out.Data.ID == 0 {
		r.t.Fatalf("login of %s: no session: %d %.300s", user, a.Status, a.Body)
	}
	id := strconv.Itoa(out.Data.ID)
	return http.Header{"Cookie": {cookie}, "New-Api-User": {id}}, id
}

// registerUser makes the ordinary user, gives it quota the way grant does, and generates its system access token, the
// login a person gives Axiom for such a system.
func (r *legacyRun) registerUser(grant func(id int)) {
	r.t.Helper()
	r.callWith(http.MethodPost, "/api/user/register", nil, map[string]any{"username": liveUser, "password": livePassword})
	session, id := r.cookieLogin(liveUser, livePassword)
	n, err := strconv.Atoi(id)
	if err != nil {
		r.t.Fatal(err)
	}
	grant(n)
	var made struct {
		Data string `json:"data"`
	}
	r.callWith(http.MethodGet, "/api/user/token", session, nil).Decode(r.t, &made)
	if made.Data == "" {
		r.t.Fatal("no system access token")
	}
	r.scrub.Secret(made.Data, "<ALICE_ACCESS_TOKEN>")
	r.user, r.userID = http.Header{"Authorization": {made.Data}, "New-Api-User": {id}}, id
}

func (r *legacyRun) setOption(admin http.Header, key, value string) {
	r.t.Helper()
	r.callWith(http.MethodPut, "/api/option/", admin, map[string]any{"key": key, "value": value})
}

func (r *legacyRun) record(name, method, path string) {
	r.t.Helper()
	r.recordWith(name+"-"+r.sfx, method, path, r.user, "<ALICE_ACCESS_TOKEN>")
}

func (r *legacyRun) openWith(token, userID string) (adapter.Manager, error) {
	r.t.Helper()
	raw, err := EncodeLegacyLogin(LegacyLogin{AccessToken: secret.New(token), UserID: userID})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.a.Open(r.t.Context(), r.c, liveLogin{raw})
}

// detectAndOpen checks what Detect tells, opens the manager with the user's login and starts the scan at the
// connection, after checking that a login that is not the user's is refused at the first read.
func (r *legacyRun) detectAndOpen(t *testing.T, generation string) {
	r.recordWith("01-detect-status-"+r.sfx, http.MethodGet, "/api/status", nil, "")
	r.recordWith("02-detect-encryption-key-"+r.sfx, http.MethodGet, "/api/user/login/encryption-key", nil, "")
	d, err := r.a.Detect(t.Context(), r.c)
	if err != nil || d.Generation != generation || d.Captcha {
		t.Fatalf("Detect = %+v, %v; want generation %s", d, err, generation)
	}
	t.Logf("detected %+v", d)
	bad := strings.Repeat("x", 32)
	r.recordWith("53-err-self-bad-token-"+r.sfx, http.MethodGet, "/api/user/self", http.Header{"Authorization": {bad}, "New-Api-User": {r.userID}}, "<NOT_A_TOKEN>")
	m, err := r.openWith(bad, r.userID)
	if err == nil {
		_, err = m.Account(t.Context())
	}
	if !errors.Is(err, adapter.ErrAuthRejected) {
		t.Fatalf("Account with a token that is no good = %v, want a rejected login", err)
	}
	if r.m, err = r.openWith(r.user.Get("Authorization"), r.userID); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, r.scan, err = r.m.Charges(t.Context(), nil); err != nil {
		t.Fatalf("Charges, first scan: %v", err)
	}
}

func (r *legacyRun) account(t *testing.T, spent int64) {
	acc, err := r.m.Account(t.Context())
	want := adapter.Account{Balance: adapter.Amount(strconv.FormatInt(liveQuota-spent, 10)), Funding: adapter.FundingWallet, UnitHint: "quota_per_unit=500000"}
	if err != nil || acc != want {
		t.Fatalf("Account = %+v, %v; want %+v", acc, err, want)
	}
}

// makeKeys makes a key in each group, reads its text back and finds the first by its text; the listing has each in
// its group.
func (r *legacyRun) makeKeys(t *testing.T, groups ...string) ([]adapter.KeyRef, []string) {
	refs, texts := r.createKeys(t, groups)
	r.checkListing(t, len(groups))
	found, ok, err := r.m.(adapter.KeyFinder).FindKey(t.Context(), secret.New(texts[0]))
	if err != nil || !ok || found.Ref != refs[0] || found.GroupRef != groups[0] {
		t.Fatalf("FindKey = %+v, %t, %v; want the first key", found, ok, err)
	}
	return refs, texts
}

func (r *legacyRun) createKeys(t *testing.T, groups []string) ([]adapter.KeyRef, []string) {
	ctx := t.Context()
	refs, texts := make([]adapter.KeyRef, len(groups)), make([]string, len(groups))
	for i, g := range groups {
		ref, err := r.m.(adapter.KeyMaker).CreateKey(ctx, g, "axiom-live-"+g)
		if err != nil {
			t.Fatalf("CreateKey(%s): %v", g, err)
		}
		text, err := r.m.KeySecret(ctx, ref)
		if err != nil || !strings.HasPrefix(text.Reveal(), "sk-") {
			t.Fatalf("KeySecret(%s) = %v", ref, err)
		}
		refs[i], texts[i] = ref, text.Reveal()
		bare := strings.TrimPrefix(texts[i], "sk-")
		r.scrub.Secret(texts[i], fmt.Sprintf("<ALICE_KEY%d>", i+1))
		r.scrub.Secret(bare, fmt.Sprintf("<ALICE_KEY%d_BARE>", i+1))
		r.scrub.Secret(maskKey(bare), maskKey(fmt.Sprintf("<ALICE_KEY%d_BARE>", i+1)))
	}
	return refs, texts
}

func (r *legacyRun) checkListing(t *testing.T, want int) {
	page, err := r.m.Keys(t.Context(), nil)
	if err != nil || !page.Done {
		t.Fatalf("Keys = %+v, %v", page, err)
	}
	made := 0
	for _, k := range page.Items {
		if !strings.HasPrefix(k.Name, "axiom-live-") { // one-api gives a user it registers a key of its own
			continue
		}
		made++
		if k.Status != adapter.KeyActive || k.Policy == nil || k.Policy.Models != nil || k.Policy.ExpiresAt != nil || k.Name != "axiom-live-"+k.GroupRef {
			t.Errorf("key %+v, want an active key of the group it was named for, with no limit", k)
		}
	}
	if made != want {
		t.Errorf("Keys lists %d of the %d keys made: %+v", made, want, page.Items)
	}
}

// sendRelays makes the calls and keeps the request id each was answered with.
func (r *legacyRun) sendRelays(calls []*relay) {
	for _, c := range calls {
		r.send(r.a, c)
	}
}

// waitLog reads the system's own log until it holds n consumption records (it writes them after the answer).
func waitLog[T any](t *testing.T, n int, read func() []T) []T {
	t.Helper()
	var got []T
	for range 60 {
		if got = read(); len(got) >= n {
			return got
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the log holds %d consumption records, want %d", len(got), n)
	return nil
}

// scanAll reads the scan from where it stands to its end, a little later than the calls so that the pass covers them.
func (r *legacyRun) scanAll(t *testing.T) ([]adapter.Charge, error) {
	r.clock.Advance(2 * time.Minute)
	var charges []adapter.Charge
	for done := false; !done; {
		page, next, err := r.m.Charges(t.Context(), r.scan)
		if err != nil {
			return charges, err
		}
		charges, r.scan, done = append(charges, page.Charges...), next, page.Done
	}
	return charges, nil
}

// TestLiveNewAPI0132 checks the adapter against new-api v0.13.2 with a legacy login: its model list is the union of
// the account's groups, so the Anthropic channel is put in one group only to show each group keeps only its own.
func TestLiveNewAPI0132(t *testing.T) {
	r, up, reach := newLegacyRun(t, "0132", liveImage0132, map[string]string{})
	r.callWith(http.MethodPost, "/api/setup", nil, map[string]any{
		"username": liveRoot, "password": livePassword, "confirmPassword": livePassword, "SelfUseModeEnabled": false, "DemoSiteEnabled": false,
	})
	root, _ := r.cookieLogin(liveRoot, livePassword)
	r.setOption(root, "performance_setting.monitor_enabled", "false")
	r.setOption(root, "GroupRatio", `{"default":1,"premium":1.5}`)
	r.setOption(root, "UserUsableGroups", `{"default":"Default group","premium":"Premium group"}`)
	r.setOption(root, "ModelRatio", fmt.Sprintf(`{%q:2.5,%q:5}`, adaptertest.UpstreamOpenAIModel, adaptertest.UpstreamAnthropicModel))
	r.setOption(root, "CompletionRatio", fmt.Sprintf(`{%q:4,%q:5}`, adaptertest.UpstreamOpenAIModel, adaptertest.UpstreamAnthropicModel))
	for _, ch := range []struct {
		typ          int
		model, group string
	}{{1, adaptertest.UpstreamOpenAIModel, "default,premium"}, {14, adaptertest.UpstreamAnthropicModel, "premium"}} {
		r.callWith(http.MethodPost, "/api/channel/", root, map[string]any{"mode": "single", "channel": map[string]any{
			"type": ch.typ, "name": "upstream-" + ch.model, "key": adaptertest.UpstreamKey, "base_url": reach.UpstreamURL(up),
			"models": ch.model, "group": ch.group, "status": 1,
		}})
	}
	r.registerUser(func(id int) {
		r.callWith(http.MethodPost, "/api/user/manage", root, map[string]any{"id": id, "action": "add_quota", "mode": "add", "value": liveQuota})
	})

	t.Run("detect and open", func(t *testing.T) {
		r.detectAndOpen(t, "0.13")
		wrong := http.Header{"Authorization": {r.user.Get("Authorization")}, "New-Api-User": {"999"}}
		r.recordWith("53-err-self-wrong-user-0132", http.MethodGet, "/api/user/self", wrong, "<ALICE_ACCESS_TOKEN>")
	})
	if r.m == nil {
		t.FailNow()
	}
	r.record("24-self", http.MethodGet, "/api/user/self")
	r.record("45-subscription-self", http.MethodGet, "/api/subscription/self")
	t.Run("account", func(t *testing.T) { r.account(t, 0) })
	r.record("40-self-groups", http.MethodGet, "/api/user/self/groups")
	r.record("42-user-models", http.MethodGet, "/api/user/models")
	r.record("43-pricing", http.MethodGet, "/api/pricing")
	t.Run("offer", r.offer0132)
	var refs []adapter.KeyRef
	var texts []string
	t.Run("keys", func(t *testing.T) { refs, texts = r.makeKeys(t, "default", "premium") })
	if len(texts) != 2 {
		t.FailNow()
	}
	r.record("28-reveal-key", http.MethodPost, "/api/token/"+string(refs[0])+"/key")
	r.record("57-list-keys", http.MethodGet, "/api/token/?p=1&page_size=100")
	r.record("57-search-key", http.MethodGet, "/api/token/search?keyword=axiom-live-default&page_size=100")
	calls := []*relay{
		{key: texts[0], path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, wantQuota: 750},
		{key: texts[1], path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, wantQuota: 2625},
	}
	r.sendRelays(calls)
	logged := waitLog(t, len(calls), func() []logRecord {
		var page struct {
			Data struct {
				Items []logRecord `json:"items"`
			} `json:"data"`
		}
		r.callWith(http.MethodGet, "/api/log/self?type=2&page_size=100&p=1", r.user, nil).Decode(t, &page)
		return page.Data.Items
	})
	r.record("32-log-self", http.MethodGet, "/api/log/self?type=2&page_size=100&p=1")
	t.Run("charges", func(t *testing.T) { r.charges0132(t, calls, logged) })
	t.Run("account after the calls", func(t *testing.T) { r.account(t, 750+2625) })
	r.writeSamples()
}

func (r *legacyRun) offer0132(t *testing.T) {
	page, err := r.m.Offer(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != 2 {
		t.Fatalf("Offer = %+v, %v", page, err)
	}
	want := map[string]struct {
		ratio adapter.Amount
		costs map[string]int64 // 100 tokens in and 50 out, in quota
	}{
		"default": {"1", map[string]int64{adaptertest.UpstreamOpenAIModel: 750}},
		"premium": {"1.5", map[string]int64{adaptertest.UpstreamOpenAIModel: 1125, adaptertest.UpstreamAnthropicModel: 2625}},
	}
	for _, g := range page.Items {
		w := want[g.Ref]
		if g.Ratio != w.ratio || len(g.Models) != len(w.costs) {
			t.Errorf("group %+v, want ratio %s and the models %v", g, w.ratio, w.costs)
			continue
		}
		for _, m := range g.Models {
			if got := liveCost(t, m, 100, 50); got.Cmp(big.NewRat(w.costs[m.UpstreamName], 1)) != 0 {
				t.Errorf("group %s prices %s at %v quota, want %d", g.Ref, m.UpstreamName, got, w.costs[m.UpstreamName])
			}
		}
	}
}

func (r *legacyRun) charges0132(t *testing.T, calls []*relay, logged []logRecord) {
	charges, err := r.scanAll(t)
	if err != nil {
		t.Fatalf("Charges: %v", err)
	}
	byRef := map[adapter.ChargeRef]adapter.Charge{}
	for _, c := range charges {
		byRef[c.Ref] = c
	}
	byID := map[string]logRecord{}
	for _, rec := range logged {
		byID[rec.RequestID] = rec
	}
	if len(byRef) != len(calls) {
		t.Fatalf("Charges read %d records, want %d", len(charges), len(calls))
	}
	for _, c := range calls {
		got, rec := byRef[c.ref], byID[string(c.ref)]
		if got.Amount != adapter.Amount(strconv.FormatInt(c.wantQuota, 10)) || rec.Quota != c.wantQuota || got.Model != c.model ||
			got.Key != adapter.KeyRef(strconv.FormatInt(rec.TokenID, 10)) || got.Funding != adapter.FundingWallet {
			t.Errorf("%s: charge %+v, the log says %+v, want %d quota", c.path, got, rec, c.wantQuota)
		}
	}
}

// TestLiveOneAPI0610 checks the adapter against one-api v0.6.10 with a legacy login. The account's group has the ratio
// 1.5, which one-api does not show an ordinary user: the adapter reports the group at 1, marked hidden, with no costs,
// and the system's own log shows the ratio was charged. Its log records no request id, so the charge scan stops at the
// first record instead of guessing which call it was for.
func TestLiveOneAPI0610(t *testing.T) {
	r, up, reach := newLegacyRun(t, "oneapi0610", liveImageOneAPI, map[string]string{"INITIAL_ROOT_ACCESS_TOKEN": liveRootToken})
	r.scrub.Secret(liveRootToken, "<ROOT_ACCESS_TOKEN>")
	root := http.Header{"Authorization": {liveRootToken}}
	r.setOption(root, "GroupRatio", `{"default":1.5}`)
	r.setOption(root, "ModelRatio", fmt.Sprintf(`{%q:2.5}`, adaptertest.UpstreamOpenAIModel))
	r.setOption(root, "CompletionRatio", fmt.Sprintf(`{%q:4}`, adaptertest.UpstreamOpenAIModel))
	r.callWith(http.MethodPost, "/api/channel/", root, map[string]any{
		"type": 1, "name": "upstream", "key": adaptertest.UpstreamKey, "base_url": reach.UpstreamURL(up),
		"models": adaptertest.UpstreamOpenAIModel, "group": "default", "status": 1,
	})
	r.registerUser(func(id int) {
		r.callWith(http.MethodPost, "/api/topup", root, map[string]any{"user_id": id, "quota": liveQuota, "remark": "live test"})
	})

	t.Run("detect and open", func(t *testing.T) { r.detectAndOpen(t, "one-api") })
	if r.m == nil {
		t.FailNow()
	}
	r.record("24-self", http.MethodGet, "/api/user/self")
	t.Run("account", func(t *testing.T) { r.account(t, 0) })
	r.record("42-available-models", http.MethodGet, "/api/user/available_models")
	r.record("43-pricing", http.MethodGet, "/api/pricing")
	t.Run("offer", r.offerOneAPI)
	var refs []adapter.KeyRef
	var texts []string
	t.Run("keys", func(t *testing.T) { refs, texts = r.makeKeys(t, "default") })
	if len(texts) != 1 {
		t.FailNow()
	}
	r.record("28-read-key", http.MethodGet, "/api/token/"+string(refs[0]))
	r.scrubOtherKeys()
	r.record("57-list-keys", http.MethodGet, "/api/token/?p=0")
	r.recordCreate()
	calls := []*relay{{key: texts[0], path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, wantQuota: 1125}}
	r.sendRelays(calls)
	type oneAPIRecord struct {
		Quota     int64  `json:"quota"`
		TokenName string `json:"token_name"`
	}
	logged := waitLog(t, len(calls), func() []oneAPIRecord {
		var page struct {
			Data []oneAPIRecord `json:"data"`
		}
		r.callWith(http.MethodGet, "/api/log/self?type=2&p=0", r.user, nil).Decode(t, &page)
		return page.Data
	})
	r.record("32-log-self", http.MethodGet, "/api/log/self?type=2&p=0")
	t.Run("charges", func(t *testing.T) {
		if logged[0].Quota != calls[0].wantQuota || logged[0].TokenName != "axiom-live-default" {
			t.Errorf("the log says %+v, want %d quota (the hidden ratio 1.5) by the made key", logged[0], calls[0].wantQuota)
		}
		_, err := r.scanAll(t)
		if !errors.Is(err, adapter.ErrUnrecognised) || !strings.Contains(err.Error(), "request id") {
			t.Fatalf("Charges = %v, want the scan stopped at a record without a request id", err)
		}
	})
	t.Run("account after the call", func(t *testing.T) { r.account(t, calls[0].wantQuota) })
	r.writeSamples()
}

func (r *legacyRun) offerOneAPI(t *testing.T) {
	page, err := r.m.Offer(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != 1 {
		t.Fatalf("Offer = %+v, %v", page, err)
	}
	g := page.Items[0]
	var facts struct {
		RatioShown *bool `json:"ratio_shown"`
	}
	if g.Ref != "default" || g.Ratio != "1" || jsonx.Unmarshal(g.Facts, &facts) != nil || facts.RatioShown == nil || *facts.RatioShown ||
		len(g.Models) != 1 || g.Models[0].UpstreamName != adaptertest.UpstreamOpenAIModel || g.Models[0].Cost != nil {
		t.Errorf("group %+v facts %s, want default at a hidden ratio of 1 with the model and no cost", g, g.Facts)
	}
}

// scrubOtherKeys takes out of the samples the text of the keys the test did not make (the one one-api made for the
// user when it registered), which one-api's listing shows whole.
func (r *legacyRun) scrubOtherKeys() {
	r.t.Helper()
	var list struct {
		Data []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"data"`
	}
	r.callWith(http.MethodGet, "/api/token/?p=0", r.user, nil).Decode(r.t, &list)
	for _, k := range list.Data {
		if !strings.HasPrefix(k.Name, "axiom-live-") {
			r.scrub.Secret(k.Key, "<KEY_"+strings.ToUpper(k.Name)+"_BARE>")
		}
	}
}

// recordCreate records one-api's answer to making a key, which holds the key made, with its text scrubbed.
func (r *legacyRun) recordCreate() {
	r.t.Helper()
	a := adaptertest.Send(r.t, http.MethodPost, r.base, "/api/token/", r.user,
		map[string]any{"name": "axiom-live-recorded", "unlimited_quota": true, "expired_time": -1})
	var made struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	a.Decode(r.t, &made)
	r.scrub.Secret(made.Data.Key, "<RECORDED_KEY_BARE>")
	name := "58-create-key-" + r.sfx
	r.recorded[name] = r.sample(name, http.MethodPost, "/api/token/", "<ALICE_ACCESS_TOKEN>", a)
}
