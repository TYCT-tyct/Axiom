package gateway

import (
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// countBody is a Messages request of text only, so its count is the body's length in thirds.
const countBody = `{"model":"acme/alpha","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`

// TestCountInput: the estimate rounds the text up to whole thirds of a byte and adds an image and a file at their flat
// figures, and counts nothing else.
func TestCountInput(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		req  protocol.Request
		want int64
	}{
		"nothing":                  {protocol.Request{}, 0},
		"one byte":                 {protocol.Request{TextBytes: 1}, 1},
		"three bytes":              {protocol.Request{TextBytes: 3}, 1},
		"four bytes":               {protocol.Request{TextBytes: 4}, 2},
		"images and files":         {protocol.Request{Images: 2, Documents: 1}, 2*1600 + 3000},
		"all of them":              {protocol.Request{TextBytes: 300, Images: 1, Documents: 1}, 100 + 1600 + 3000},
		"output limit is no input": {protocol.Request{MaxOutput: 1 << 40, TextBytes: 3}, 1},
	} {
		if got := countInput(c.req); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

// TestCountFromTheBody: the count of a real body is what Inspect reads of it, through the whole handler, and counting
// reserves, charges and records nothing.
func TestCountFromTheBody(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(5e9)
	for _, prefix := range []string{"/v1", "/api/v1"} {
		resp, body := r.do(http.MethodPost, prefix+"/messages/count_tokens", a.key, strings.NewReader(countBody))
		var out struct {
			InputTokens int64 `json:"input_tokens"`
		}
		if err := jsonx.Unmarshal([]byte(body), &out); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s (%v)", prefix, resp.StatusCode, body, err)
		}
		if want := int64(len(countBody)+2) / 3; out.InputTokens != want {
			t.Errorf("%s: %d tokens, want %d for %d bytes", prefix, out.InputTokens, want, len(countBody))
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 || r.count("SELECT balance FROM billing.balances WHERE account_id = $1", a.id) != 5e9 {
		t.Error("counting reserved, ran or charged something")
	}
}

// TestCountRefusals: a body that is not a request, a model that does not exist, and a model the key may not use are
// refused as a call would be; the last is told the model does not exist, so what the key may use is not revealed.
func TestCountRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(5e9)
	r.sql("UPDATE accounts.api_keys SET models = '{acme/beta}'")
	for name, c := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"not json":          {`{`, 400, "invalid_request"},
		"no model":          {`{"messages":[]}`, 400, "invalid_request"},
		"unknown model":     {`{"model":"acme/none","messages":[]}`, 404, "model_not_found"},
		"model not allowed": {countBody, 404, "model_not_found"},
		"allowed model":     {strings.Replace(countBody, "alpha", "beta", 1), 200, ""},
	} {
		resp, body := r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(c.body))
		code := ""
		if c.status != http.StatusOK {
			code, _ = errorOf(t, body)
			if !strings.Contains(body, `"type":"error"`) {
				t.Errorf("%s: not Messages' error shell: %s", name, body)
			}
		}
		if resp.StatusCode != c.status || code != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, c.status, c.code)
		}
	}
}

type endpoint struct{ method, path, body string }

// auxEndpoints are the six endpoints by name.
var auxEndpoints = map[string]endpoint{
	"count":        {http.MethodPost, "/v1/messages/count_tokens", countBody},
	"key":          {http.MethodGet, "/api/v1/key", ""},
	"credits":      {http.MethodGet, "/api/v1/credits", ""},
	"generation":   {http.MethodGet, "/api/v1/generation?id=0198a8f0-0000-7000-8000-000000000000", ""},
	"content":      {http.MethodGet, "/api/v1/generation/content?id=0198a8f0-0000-7000-8000-000000000000", ""},
	"activity":     {http.MethodGet, "/api/v1/activity", ""},
	"subscription": {http.MethodGet, "/v1/dashboard/billing/subscription", ""},
	"usage":        {http.MethodGet, "/v1/dashboard/billing/usage?start_date=2026-10-01&end_date=2026-10-31", ""},
}

// TestAuxJudgeTheKeyAsACallDoes: every auxiliary endpoint asks the database whether the key may be used, so a key
// disabled, expired, deleted or limited to other addresses is 401 whatever the cache holds. A management key may read
// `/key` and `/credits`, as OpenRouter's may, and nothing else: it cannot make a call, and the endpoints that report
// what a call did are for call keys.
//
//nolint:gocognit // the endpoint table and its assertions are the test
func TestAuxJudgeTheKeyAsACallDoes(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]string{
		"disabled":                   "SET status = 'disabled'",
		"expired":                    "SET expires_at = now() - interval '1 minute'",
		"deleted":                    "SET deleted_at = now()",
		"a management key":           "SET kind = 'management', expires_at = now() + interval '1 day'",
		"limited to other addresses": "SET ip_allow = '{198.51.100.0/24}'",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.newAccount(1e9)
			r.sql("UPDATE accounts.api_keys " + change)
			for endpoint, e := range auxEndpoints {
				resp, body := r.do(e.method, e.path, a.key, strings.NewReader(e.body))
				managementRead := name == "a management key" && (endpoint == "key" || endpoint == "credits")
				if managementRead {
					if resp.StatusCode != http.StatusOK {
						t.Errorf("%s: %d %s", endpoint, resp.StatusCode, body)
					} else if endpoint == "key" && data(t, body)["is_management_key"] != true {
						t.Errorf("%s does not say it is a management key: %s", endpoint, body)
					}
					continue
				}
				if code, _ := errorOf(t, body); resp.StatusCode != http.StatusUnauthorized || code != "authentication" {
					t.Errorf("%s: %d %s", endpoint, resp.StatusCode, body)
				}
			}
		})
	}
}

// TestAuxNeedAKeyOfTheAPIHost: no key, a key that is not one and a key sent to the console host are 401 at every
// endpoint.
func TestAuxNeedAKeyOfTheAPIHost(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for endpoint, e := range auxEndpoints {
		for who, key := range map[string]string{"no key": "", "a wrong key": "not-a-key"} {
			if resp, _ := r.do(e.method, e.path, key, strings.NewReader(e.body)); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with %s: %d", endpoint, who, resp.StatusCode)
			}
		}
		req := httptest.NewRequestWithContext(t.Context(), e.method, "http://"+consoleHost+e.path, strings.NewReader(e.body))
		req.Header.Set("Authorization", "Bearer "+a.key)
		if rec := r.serve(req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s on the console host: %d", endpoint, rec.Code)
		}
	}
}

// data is the "data" member of a response, decoded.
func data(t *testing.T, body string) map[string]any {
	t.Helper()
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := jsonx.Unmarshal([]byte(body), &out); err != nil || out.Data == nil {
		t.Fatalf("not a data object: %s (%v)", body, err)
	}
	return out.Data
}

func near(got any, want float64) bool {
	f, ok := got.(float64)
	return ok && math.Abs(f-want) < 1e-9
}

// spendAccount gives the account a spend of 2 credits in two workspaces, a deleted one included, and the same 2 under
// the key: the key's counter must not be added to its workspace's.
func (r *rig) spendAccount(a account) {
	r.t.Helper()
	extra := uuid.NewV7()
	r.sql("INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default, deleted_at) VALUES ('" + extra.String() +
		"', '" + siteID.String() + "', '" + a.id.String() + "', 'Old', 'active', false, now())")
	r.spendRow(a, a.workspace, 0, 1.5e9, 0)
	r.spendRow(a, extra, 3, 0.5e9, 0)
	r.spendRow(a, uuid.UUID(a.keyID), 0, 2e9, 0)
}

// TestCreditsShowTheOwnAccount: credits are the balance plus what was charged, usage is what was charged in all the
// account's workspaces, so credits less usage is the balance, whatever is reserved; of the key's account and no other; an
// account with no balance row has nothing.
func TestCreditsShowTheOwnAccount(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := r.newAccount(7e9), r.newAccount(100e9)
	r.sql("UPDATE billing.balances SET reserved = 2000000000 WHERE account_id = '" + a.id.String() + "'")
	r.spendAccount(a)
	for _, prefix := range []string{"/v1", "/api/v1"} {
		resp, body := r.do(http.MethodGet, prefix+"/credits", a.key, nil)
		if d := data(t, body); resp.StatusCode != http.StatusOK || !near(d["total_credits"], 9) || !near(d["total_usage"], 2) {
			t.Errorf("%s/credits: %d %s", prefix, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s/credits is cacheable: %v", prefix, resp.Header)
		}
	}
	if _, body := r.do(http.MethodGet, "/api/v1/credits", b.key, nil); !near(data(t, body)["total_credits"], 100) || !near(data(t, body)["total_usage"], 0) {
		t.Errorf("the other account sees %s", body)
	}
	r.sql("DELETE FROM billing.balances WHERE account_id = '" + a.id.String() + "'")
	if _, body := r.do(http.MethodGet, "/api/v1/credits", a.key, nil); !near(data(t, body)["total_credits"], 0) {
		t.Errorf("an account with no balance row: %s", body)
	}
}

// TestCreditsDoNotWrap: a balance at the largest amount and a spend on top of it add up past what the smallest unit holds;
// the credits shown stay positive and the balance is still the credits less the usage.
func TestCreditsDoNotWrap(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(math.MaxInt64)
	r.spendRow(a, a.workspace, 0, 1e9, 0)
	_, body := r.do(http.MethodGet, "/api/v1/credits", a.key, nil)
	if d := data(t, body); !near(d["total_usage"], 1) || !(d["total_credits"].(float64) > 9.2e9) {
		t.Errorf("/credits: %s", body)
	}
	_, body = r.do(http.MethodGet, "/v1/dashboard/billing/subscription", a.key, nil)
	var sub map[string]any
	if err := jsonx.Unmarshal([]byte(body), &sub); err != nil || !(sub["hard_limit_usd"].(float64) > 9.2e9) {
		t.Errorf("/subscription: %s (%v)", body, err)
	}
}

// TestCountRateIsItsOwn: counting tokens and calling the model each have their own allowance in the window, so one
// running out leaves the other's alone, in both directions.
func TestCountRateIsItsOwn(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.KeyRate, c.CountRate = 2, 2 } })
	counted, called := r.newAccount(1e9), r.newAccount(1e9)
	status := func(a account, count bool) int {
		if count {
			resp, _ := r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
			return resp.StatusCode
		}
		resp, _ := r.message(a, "acme/beta")
		return resp.StatusCode
	}
	for range 2 {
		if got := status(counted, true); got != http.StatusOK {
			t.Fatalf("count within its allowance: %d", got)
		}
	}
	if got := status(counted, true); got != http.StatusTooManyRequests {
		t.Errorf("a third count: %d, want 429", got)
	}
	if got := status(counted, false); got != http.StatusOK {
		t.Errorf("a call after counts used up the counting allowance: %d", got)
	}
	for range 2 {
		status(called, false)
	}
	if got := status(called, false); got != http.StatusTooManyRequests {
		t.Errorf("a third call: %d, want 429", got)
	}
	if got := status(called, true); got != http.StatusOK {
		t.Errorf("a count after calls used up the calling allowance: %d", got)
	}
	wantRefused(t, r.refused(), map[string]int64{"count_rate": 1, "key_rate": 1})
}

// TestLegacyBalance: the OpenAI dashboard endpoints new-api reads give the same figures: the hard limit is the credits,
// the usage is in hundredths, and the dates asked do not narrow it, so the hard limit less the usage is the balance.
func TestLegacyBalance(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(7e9)
	r.spendAccount(a)
	for _, prefix := range []string{"/v1", "/api/v1"} {
		var sub, usage map[string]any
		_, body := r.do(http.MethodGet, prefix+"/dashboard/billing/subscription", a.key, nil)
		if err := jsonx.Unmarshal([]byte(body), &sub); err != nil || sub["object"] != "billing_subscription" ||
			!near(sub["hard_limit_usd"], 9) || !near(sub["system_hard_limit_usd"], 9) {
			t.Errorf("%s/subscription: %s (%v)", prefix, body, err)
		}
		_, body = r.do(http.MethodGet, prefix+"/dashboard/billing/usage?start_date=2026-10-01&end_date=2026-11-01", a.key, nil)
		if err := jsonx.Unmarshal([]byte(body), &usage); err != nil || usage["object"] != "list" || !near(usage["total_usage"], 200) {
			t.Errorf("%s/usage: %s (%v)", prefix, body, err)
		}
	}
}

// spendRow is a day of a subject's counters: days ago from today (UTC), spent and reserved in billionths.
func (r *rig) spendRow(a account, subject uuid.UUID, daysAgo int, spent, reserved int64) {
	r.t.Helper()
	r.sql("INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent, reserved) VALUES ('" + subject.String() +
		"', date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '" + itoa(int64(daysAgo)) + " days', '" +
		siteID.String() + "', '" + a.id.String() + "', " + itoa(spent) + ", " + itoa(reserved) + ")")
}

// TestKeyShowsItsOwnUsageAndLimit: usage is what was charged, all time and in this UTC day, week and month; the limit's
// remaining takes off what is reserved too, in the limit's own window; another key of the account and another account
// do not count.
func TestKeyShowsItsOwnUsageAndLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := r.newAccount(50e9), r.newAccount(50e9)
	r.sql("UPDATE accounts.api_keys SET spend_limit = 5000000000, limit_reset = 'daily', expires_at = '2099-01-02T03:04:05Z' WHERE id = '" +
		uuid.UUID(a.keyID).String() + "'")
	r.spendRow(a, uuid.UUID(a.keyID), 0, 1.5e9, 0.5e9)
	r.spendRow(a, uuid.UUID(a.keyID), 400, 2e9, 0)
	r.spendRow(a, a.workspace, 0, 40e9, 0) // the workspace's counter is not the key's
	r.spendRow(b, uuid.UUID(b.keyID), 0, 9e9, 0)
	resp, body := r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	d := data(t, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	for field, want := range map[string]float64{
		"usage": 3.5, "usage_daily": 1.5, "usage_weekly": 1.5, "usage_monthly": 1.5, "limit": 5, "limit_remaining": 3,
	} {
		if !near(d[field], want) {
			t.Errorf("%s: %v, want %v (%s)", field, d[field], want, body)
		}
	}
	if label, _ := d["label"].(string); !strings.HasPrefix(label, "sk-ax-v1-...") || len(label) != len("sk-ax-v1-...")+4 || !strings.HasSuffix(a.key, label[len(label)-4:]) {
		t.Errorf("label %q of %s", label, a.key)
	}
	for field, want := range map[string]any{"limit_reset": "daily", "expires_at": "2099-01-02T03:04:05Z", "name": "Test", "is_free_tier": false} {
		if d[field] != want {
			t.Errorf("%s: %v, want %v", field, d[field], want)
		}
	}
	if strings.Contains(body, a.key) {
		t.Error("the response holds the whole key")
	}
}

// TestKeyWithoutALimit: a key with no limit shows null for the limit, what is left and the reset, and a limit that never
// resets counts every day; a limit already passed leaves nothing, not a negative.
func TestKeyWithoutALimit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(50e9)
	r.sql("UPDATE accounts.api_keys SET spend_limit = NULL, limit_reset = NULL")
	_, body := r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	d := data(t, body)
	for _, field := range []string{"limit", "limit_remaining", "limit_reset", "expires_at"} {
		if v, present := d[field]; !present || v != nil {
			t.Errorf("%s: %v, want null (%s)", field, v, body)
		}
	}
	r.sql("UPDATE accounts.api_keys SET spend_limit = 1000000000")
	r.spendRow(a, uuid.UUID(a.keyID), 400, 1.5e9, 0.5e9) // a total limit counts the old day too
	_, body = r.do(http.MethodGet, "/api/v1/key", a.key, nil)
	if d := data(t, body); !near(d["limit"], 1) || !near(d["limit_remaining"], 0) || d["limit_reset"] != nil {
		t.Errorf("a total limit already spent: %s", body)
	}
}

// callRow records a call in the call log of an account, as the writer would.
func (r *rig) callRow(a account, id uuid.UUID) {
	r.t.Helper()
	r.sql("INSERT INTO observability.call_log (id, at, site_id, account_id, workspace_id, key_id, ip, client_app, request_class, " +
		"used_tools, has_image, has_file, model, endpoint_id, attempt_count, usage, service_tier, price_nano, cost_nano, currency, " +
		"free_of_charge, latency_ms, first_token_ms, finish_reason, consent_version, training_allowed) VALUES ('" + id.String() +
		"', now(), '" + siteID.String() + "', '" + a.id.String() + "', '" + a.workspace.String() + "', '" + uuid.UUID(a.keyID).String() +
		"', '203.0.113.7', 'claude-code', 'chat', false, false, false, 'acme/alpha', '" + uuid.Nil().String() + "', 1, " +
		`'{"input":100,"cache_read":40,"output":20,"reasoning":5}', 'standard', 1500000000, 900000000, 'USD', false, ` +
		"2500, 300, 'stop', 'v1', false)")
}

// TestGenerationIsTheAccountsOwn: a call number shows the call's usage and price to its own account, and nothing of our
// cost, endpoint or route.
func TestGenerationIsTheAccountsOwn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	mine := uuid.NewV7()
	r.callRow(a, mine)
	for _, prefix := range []string{"/v1", "/api/v1"} {
		resp, body := r.do(http.MethodGet, prefix+"/generation?id="+mine.String(), a.key, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", prefix, resp.StatusCode, body)
		}
		checkGeneration(t, data(t, body), mine)
		for _, hidden := range []string{"cost_nano", "900000000", "endpoint", "route", "ledger"} {
			if strings.Contains(body, hidden) {
				t.Errorf("%s shows %q: %s", prefix, hidden, body)
			}
		}
	}
}

// checkGeneration compares what callRow recorded with what the endpoint shows.
func checkGeneration(t *testing.T, d map[string]any, id uuid.UUID) {
	t.Helper()
	for field, want := range map[string]any{
		"id": id.String(), "model": "acme/alpha", "currency": "USD", "finish_reason": "stop", "app": "claude-code",
		"streamed": false, "cancelled": false,
	} {
		if d[field] != want {
			t.Errorf("%s: %v, want %v", field, d[field], want)
		}
	}
	if _, ok := d["charge_details"]; ok {
		t.Error("legacy call has invented charge details")
	}
	if _, ok := d["api_type"]; ok { // the call record has no format, and the request class is not one
		t.Errorf("api_type is shown: %v", d["api_type"])
	}
	if _, ok := d["native_tokens_prompt"]; ok {
		t.Error("a call without a reported prompt total shows a native prompt")
	}
	for field, want := range map[string]float64{
		"total_cost": 1.5, "tokens_prompt": 140, "tokens_completion": 25, "tokens_reasoning": 5, "tokens_cached": 40,
		"native_tokens_completion": 25, "native_tokens_reasoning": 5, "native_tokens_cached": 40,
		"native_tokens_completion_images": 0, "latency": 300, "generation_time": 2500,
	} {
		if !near(d[field], want) {
			t.Errorf("%s: %v, want %v", field, d[field], want)
		}
	}
}

// TestGenerationRefusals: another account's number, one that does not exist and one not yet written are the same 404;
// a malformed or missing number is 400; the method is GET.
func TestGenerationRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := r.newAccount(1e9), r.newAccount(1e9)
	theirs := uuid.NewV7()
	r.callRow(b, theirs)
	for name, id := range map[string]string{"another account's": theirs.String(), "unknown": uuid.NewV7().String()} {
		resp, body := r.do(http.MethodGet, "/api/v1/generation?id="+id, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "generation_not_found" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	for name, path := range map[string]string{"malformed": "/api/v1/generation?id=abc", "missing": "/api/v1/generation"} {
		resp, body := r.do(http.MethodGet, path, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, _ := r.do(http.MethodPost, "/api/v1/key", a.key, nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /key: %d", resp.StatusCode)
	}
}

// TestGenerationShowsHowTheCallRan: the generation endpoint reports whether the client streamed, whether it left
// before the end, the session, and the provider's native token counts, with the prompt total only when the upstream
// reported one.
func TestGenerationShowsHowTheCallRan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	id := uuid.NewV7()
	r.callRow(a, id)
	r.sql("UPDATE observability.call_log SET streamed = true, physical_input = 100, finish_reason = 'cancelled', " +
		"session_id = 'session-7', usage = '{\"input\":60,\"cache_read\":40,\"output\":20,\"reasoning\":5,\"image_out\":3}' " +
		"WHERE id = '" + id.String() + "'")
	resp, body := r.do(http.MethodGet, "/api/v1/generation?id="+id.String(), a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	d := data(t, body)
	if d["streamed"] != true || d["cancelled"] != true || d["session_id"] != "session-7" {
		t.Errorf("stream, cancellation or session: %v", d)
	}
	for field, want := range map[string]float64{
		"native_tokens_prompt": 100, "native_tokens_completion": 28, "native_tokens_reasoning": 5,
		"native_tokens_cached": 40, "native_tokens_completion_images": 3, "tokens_prompt": 100,
	} {
		if !near(d[field], want) {
			t.Errorf("%s: %v, want %v", field, d[field], want)
		}
	}
}

// TestGenerationReportsTheCacheDiscount: /api/v1/generation reports what cache reads saved against the input price,
// computed from the frozen sale schedule and the usage settlement stored, not read from the call log's snapshot. The
// call reports 143 input, 77 cached and 84 output tokens over its three samplings; the sale price is a millionth of a
// credit an input token and half that a cached one, so the reads saved 77 × 0.0000005 credits.
func TestGenerationReportsTheCacheDiscount(t *testing.T) {
	t.Parallel()
	r, receipt, _ := supplierEveryItemRig(t, supplierScheduleEvery)
	sample, _ := sample(t, protocol.Messages)
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(receipt)
	up.setScript(serve(beat{data: threeSegments(sample)}))
	records, err := observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		Dir: t.TempDir(), Capacity: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r.exec.Records = records
	a := r.funded(1e12)
	r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))
	if err := r.exec.Records.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	resp, body := r.do(http.MethodGet, "/api/v1/generation?id="+r.lastCall().ID.String(), a.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	d := data(t, body)
	if !near(d["cache_discount"], float64(everyCacheRead)*(1e-6-0.5e-6)) {
		t.Errorf("cache_discount = %v, want %v (%d cached tokens at the sale price)", d["cache_discount"],
			float64(everyCacheRead)*(1e-6-0.5e-6), everyCacheRead)
	}
	if !near(d["total_cost"], float64(everyInput)*1e-6+float64(everyCacheRead)*0.5e-6+float64(everyOutput)*2e-6) {
		t.Errorf("total_cost = %v, want the settled charge", d["total_cost"])
	}
}

// TestGenerationContentIsNotRetained: an existing call of the account has no content to show and says so; another
// account's call is indistinguishable from one that does not exist.
func TestGenerationContentIsNotRetained(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := r.newAccount(1e9), r.newAccount(1e9)
	mine, theirs := uuid.NewV7(), uuid.NewV7()
	r.callRow(a, mine)
	r.callRow(b, theirs)
	resp, body := r.do(http.MethodGet, "/api/v1/generation/content?id="+mine.String(), a.key, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "content_not_available") {
		t.Errorf("own call: %d %s", resp.StatusCode, body)
	}
	resp, body = r.do(http.MethodGet, "/api/v1/generation/content?id="+theirs.String(), a.key, nil)
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "generation_not_found" {
		t.Errorf("another account's call: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/api/v1/generation/content", "/api/v1/generation/content?id=abc"} {
		resp, body = r.do(http.MethodGet, path, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}
