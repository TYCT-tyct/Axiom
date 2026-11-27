//go:build contract

package sub2api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// liveRun is the state the steps of TestLiveSub2API hand on to each other: the adapter's manager, the scan that starts at
// the connection, the made keys by group and the relayed calls. The steps pass t.Context() to the adapter where they call
// it, and the helpers of live make their own requests.
type liveRun struct {
	*live
	a      *Adapter
	c      *client.Client
	m      adapter.Manager
	scan   adapter.ScanState
	keys   map[string]string // the text of each group's key
	ids    map[string]adapter.KeyRef
	relays []*relay
	logged map[string]usageRecord
}

func TestLiveSub2API(t *testing.T) {
	l := startLive(t)
	a := NewWithClock(l.clock.Now)
	r := &liveRun{live: l, a: a, c: l.adapterClient(a), keys: map[string]string{}, ids: map[string]adapter.KeyRef{}}

	l.record("settings_public_0225", "/api/v1/settings/public", "")
	t.Run("detect", r.detect)
	r.open(t)
	t.Run("account", r.account)
	t.Run("offer before there are keys", r.offerBeforeKeys)
	t.Run("keys", r.makeKeys)
	if len(r.keys) != len(l.groups) {
		t.Fatal("no keys to relay with")
	}
	t.Run("offer with keys", r.offerWithKeys)
	r.relayCalls(t)
	t.Run("charges", r.checkCharges)
	t.Run("model plaza", r.plaza)
	l.writeSamples()
}

func (r *liveRun) detect(t *testing.T) {
	d, err := r.a.Detect(t.Context(), r.c)
	if err != nil || d.Generation != "v1" || d.Version != "0.2.25" || d.Captcha {
		t.Fatalf("Detect = %+v, %v", d, err)
	}
}

func (r *liveRun) open(t *testing.T) {
	var err error
	if r.m, err = r.a.Open(t.Context(), r.c, r.store(liveUserMail)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The scan starts now: nothing before the connection is ours.
	if _, r.scan, err = r.m.Charges(t.Context(), nil); err != nil {
		t.Fatalf("Charges, first scan: %v", err)
	}
}

func (r *liveRun) account(t *testing.T) {
	r.record("me_0225", "/api/v1/auth/me", r.session)
	r.record("subscriptions_active_0225", "/api/v1/subscriptions/active", r.session)
	acc, err := r.m.Account(t.Context())
	if want := (adapter.Account{Balance: "10", Funding: adapter.FundingWallet, UnitHint: "USD"}); err != nil || acc != want {
		t.Fatalf("Account = %+v, %v; want %+v", acc, err, want)
	}
}

func (r *liveRun) offerBeforeKeys(t *testing.T) {
	r.record("groups_rates_0225", "/api/v1/groups/rates", r.session)
	for name, g := range r.offered(t, r.m) {
		// A group for Claude Code only is not sold, so it needs no key of ours.
		if g.Ratio != adapter.Amount(strconv.FormatFloat(r.group(name).rate, 'f', -1, 64)) || !g.ModelsUnread || g.NeedsKey == r.group(name).claudeCodeOnly {
			t.Errorf("group %s = %+v, want its multiplier, and its models unread because no key of ours is in it", name, g)
		}
	}
}

func (r *liveRun) makeKeys(t *testing.T) {
	maker := r.m.(adapter.KeyMaker)
	for n, g := range r.groups {
		ref, err := maker.CreateKey(t.Context(), strconv.FormatInt(g.id, 10), "axiom-live-"+g.name)
		if err != nil {
			t.Fatalf("CreateKey(%s): %v", g.name, err)
		}
		text, err := r.m.KeySecret(t.Context(), ref)
		if err != nil || !strings.HasPrefix(text.Reveal(), "sk-") {
			t.Fatalf("KeySecret(%s) = %v", ref, err)
		}
		// A word, not a digit: a placeholder with digits reads as a key to the secret scanner.
		r.scrub.Secret(text.Reveal(), "placeholder-key-"+[]string{"one", "two", "three", "four"}[n])
		r.keys[g.name], r.ids[g.name] = text.Reveal(), ref
	}
	r.checkKeyListing(t)
	r.record("keys_0225", "/api/v1/keys?page=1&page_size=100", r.session)
	r.record("key_0225", "/api/v1/keys/"+string(r.ids["claude-1"]), r.session)
	r.checkFindKey(t)
}

func (r *liveRun) checkKeyListing(t *testing.T) {
	page, err := r.m.Keys(t.Context(), nil)
	if err != nil || !page.Done || len(page.Items) != len(r.groups) {
		t.Fatalf("Keys = %+v, %v", page, err)
	}
	for _, k := range page.Items {
		if g := r.groupByID(k.GroupRef); g == nil || k.Status != adapter.KeyActive || k.Policy == nil || k.Policy.ExpiresAt != nil || k.Name != "axiom-live-"+g.name {
			t.Errorf("key %+v, want an active key of the group it was named for, with no expiry", k)
		}
	}
}

func (r *liveRun) checkFindKey(t *testing.T) {
	finder := r.m.(adapter.KeyFinder)
	found, ok, err := finder.FindKey(t.Context(), secret.New(r.keys["gpt-1"]))
	if err != nil || !ok || found.Ref != r.ids["gpt-1"] || found.GroupRef != strconv.FormatInt(r.group("gpt-1").id, 10) {
		t.Fatalf("FindKey = %+v, %t, %v; want the key of gpt-1", found, ok, err)
	}
	if _, ok, err := finder.FindKey(t.Context(), secret.New("sk-"+strings.Repeat("x", 64))); err != nil || ok {
		t.Fatalf("FindKey of a text the system does not hold = %t, %v", ok, err)
	}
}

func (r *liveRun) offerWithKeys(t *testing.T) {
	r.record("groups_available_0225", "/api/v1/groups/available", r.session)
	r.record("models_anthropic_0225", "/v1/models", r.keys["claude-1"])
	r.record("models_openai_0225", "/v1/models", r.keys["gpt-1"])
	r.record("billing_0225", "/v1/sub2api/billing", r.keys["claude-15"])
	for name, g := range r.offered(t, r.m) {
		if g.ModelsUnread || g.NeedsKey || len(g.Models) == 0 {
			t.Errorf("group %s = %+v, want the models its key lists", name, g)
			continue
		}
		// The group for Claude Code only is marked and serves nothing; the others serve their platform's formats.
		only := r.group(name).claudeCodeOnly
		if marked := strings.Contains(string(g.Facts), `"claude_code_only":true`); marked != only || (len(r.a.Calls().Formats(g.Facts)) == 0) != only {
			t.Errorf("group %s: facts %s, formats %v; want it sold unless it takes Claude Code only", name, g.Facts, r.a.Calls().Formats(g.Facts))
		}
	}
}

// relayCalls makes one call of each format and stream mode with the made keys, tries the group for Claude Code only with
// a plain client and reads what the system logged.
func (r *liveRun) relayCalls(t *testing.T) {
	r.relays = []*relay{
		{group: "claude-1", path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, want: "0.00175"},
		{group: "claude-1", path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, stream: true, want: "0.00175"},
		{group: "claude-15", path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel, want: "0.002625"},
		{group: "gpt-1", path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, want: "0.00075"},
		{group: "gpt-1", path: "/v1/chat/completions", model: adaptertest.UpstreamOpenAIModel, stream: true, want: "0.00075"},
		{group: "gpt-1", path: "/v1/responses", model: adaptertest.UpstreamOpenAIModel, want: "0.00075"},
		{group: "gpt-1", path: "/v1/responses", model: adaptertest.UpstreamOpenAIModel, stream: true, want: "0.00075"},
	}
	for _, c := range r.relays {
		if ans := r.send(r.a, r.keys[c.group], c); ans.Status != http.StatusOK {
			t.Fatalf("%s of %s (stream %t): %d %.300s", c.path, c.group, c.stream, ans.Status, ans.Body)
		}
	}
	// A group that takes Claude Code only refuses any other client, before the upstream is asked.
	refused := &relay{group: "claude-code", path: "/v1/messages", model: adaptertest.UpstreamAnthropicModel}
	refusal := r.send(r.a, r.keys["claude-code"], refused)
	r.keep("claude_code_only_refusal_0225", http.MethodPost, refused.path, refusal)
	t.Logf("a plain client in the group for Claude Code only: %d %s, classified %+v", refusal.Status, refusal.Body, r.a.Calls().Classify(refusal.Status, refusal.Header, refusal.Body))
	r.logged = r.usage(len(r.relays))
	r.record("usage_0225", "/api/v1/usage?page=1&page_size=200&timezone=UTC&sort_by=created_at&sort_order=desc", r.session)
}
