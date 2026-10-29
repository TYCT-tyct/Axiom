package gateway

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

func TestPresetRequests(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	other := r.newAccount(1e9)
	err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := accounts.SavePreset(t.Context(), tx, audit.System("gateway.test"), accounts.SiteID(siteID), accounts.AccountID(a.id), accounts.Preset{Slug: "support", Name: "Support", Config: jsonx.Raw(`{"service_tier":"standard_only","model":"acme/alpha","max_tokens":12,"temperature":0.5,"provider":{"sort":"price"},"system":"Be kind"}`)}, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.accounts.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, model, members string }{
		{"direct", "@preset/support", ""},
		{"field", "acme/beta", `"preset":"@preset/support",`},
		{"combined", "acme/beta@preset/support", ""},
		{"escaped", "@pr\\u0065set/support", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkPresetRequest(t, r, a, tc.name, tc.model, tc.members)
		})
	}
	for _, tc := range []struct {
		key, body string
		status    int
	}{
		{other.key, `{"service_tier":"standard_only","model":"@preset/support"}`, 404},
		{a.key, `{"service_tier":"standard_only","model":"@preset/missing"}`, 404},
		{a.key, `{"service_tier":"standard_only","model":"@preset/support","preset":"@preset/other"}`, 400},
		{a.key, `{"service_tier":"standard_only","preset":"@preset/support","model":null}`, 400},
		{a.key, `{"service_tier":"standard_only","model":"acme/alpha","preset":"ignored"}`, 200},
		{a.key, `{"service_tier":"standard_only","preset":"@preset/support"}`, 200},
	} {
		resp, body := r.do(http.MethodPost, "/v1/messages", tc.key, strings.NewReader(tc.body))
		if resp.StatusCode != tc.status {
			t.Fatalf("%s: %d %s", tc.body, resp.StatusCode, body)
		}
	}
	checkPresetSession(t, r, a)
	r.sql("UPDATE accounts.api_keys SET models=ARRAY['acme/beta'] WHERE id='" + uuid.UUID(a.keyID).String() + "'")
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(`{"service_tier":"standard_only","model":"@preset/support"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("preset bypassed key models: %d %s", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}

func checkPresetRequest(t *testing.T, r *rig, a account, name, model, members string) {
	t.Helper()
	if name == "escaped" {
		model = "@preset/support"
	}
	body := strings.Replace(messageBody(model), `{"model"`, `{`+members+`"temperature":0,"model"`, 1)
	if name == "escaped" {
		body = strings.Replace(body, "@preset/", "@pr\u0065set/", 1)
	}
	resp, out := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, out)
	}
	c := r.calls()[len(r.calls())-1]
	want := "acme/beta"
	if name == "direct" || name == "escaped" {
		want = "acme/alpha"
	}
	if c.Request.Model != want || c.Request.MaxOutput != 1000 {
		t.Fatalf("summary: %+v", c.Request)
	} // request max_tokens overrides preset
	var merged map[string]jsonx.Raw
	if err := jsonx.Unmarshal(c.Body, &merged); err != nil {
		t.Fatal(err)
	}
	if string(merged["temperature"]) != "0" || merged["system"] == nil {
		t.Fatalf("merged body: %s", c.Body)
	}
}

func checkPresetSession(t *testing.T, r *rig, a account) {
	t.Helper()
	var key routing.SessionKey
	for i, text := range []string{"a", "b"} {
		body := `{"service_tier":"standard_only","model":"@preset/support","prompt_cache_key":"cache","messages":[{"role":"user","content":"` + strings.Repeat(text, 17000) + `"}]}`
		resp, out := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s", resp.StatusCode, out)
		}
		got := r.calls()[len(r.calls())-1].Session
		if i == 0 {
			key = got
		} else if got != key {
			t.Fatal("preset expansion lost the client's cache identity")
		}
	}
}

func TestSessionHeaderAndMessagesFallbackRoute(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	b := r.newAccount(1e9)
	var first routing.SessionKey
	for i, tc := range []struct {
		account                  account
		header, session, content string
	}{
		{a, "session", "", "first"}, {a, "session", "", "different"}, {a, "ignored", "session", "third"}, {b, "session", "", "first"},
	} {
		body := `{"service_tier":"standard_only","model":"acme/alpha","session_id":"` + tc.session + `","messages":[{"role":"user","content":"` + tc.content + `"}],"fallbacks":[{"model":"acme/beta"}]}`
		resp, out := r.do(http.MethodPost, "/v1/messages", tc.account.key, strings.NewReader(body), "X-Session-Id", tc.header)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s", resp.StatusCode, out)
		}
		call := r.calls()[len(r.calls())-1]
		if i == 0 {
			first = call.Session
		} else if (call.Session == first) != (i < 3) {
			t.Fatal("header precedence or account isolation lost")
		}
		found := false
		for _, entry := range call.Plan.Entries {
			found = found || entry.Model == r.beta
		}
		if !found {
			t.Fatal("Messages fallback absent from route")
		}
	}
	// The header is consulted even when a body session lies outside routing's old bounded prefix.
	body := `{"service_tier":"standard_only","model":"acme/alpha","messages":[{"role":"user","content":"` + strings.Repeat("x", 17000) + `"}],"session_id":"session"}`
	resp, out := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(body), "X-Session-Id", "ignored")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	if got := r.calls()[len(r.calls())-1].Session; got != first {
		t.Fatal("late body session did not override header")
	}
	r.idle(1 << 30)
}
