package sub2api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
)

// TestAdapterKeepsTheContract: the rules every adapter follows (adaptertest) hold for sub2api.
func TestAdapterKeepsTheContract(t *testing.T) { adaptertest.Run(t, New()) }

// panel is the table of recorded answers of a healthy supplier.
func panel(t testing.TB) map[string]adapter.Response {
	return map[string]adapter.Response{
		"GET /api/v1/auth/me":                   fixture(t, "me"),
		"GET /api/v1/subscriptions/active":      fixture(t, "subscriptions_active"),
		"GET /api/v1/groups/available":          fixture(t, "groups_available"),
		"GET /api/v1/groups/rates":              fixture(t, "groups_rates"),
		"GET /api/v1/keys?page=1&page_size=100": fixture(t, "keys"),
		"GET /api/v1/keys/2":                    fixture(t, "key_2"),
		"GET /v1/sub2api/billing":               fixture(t, "billing"),
		"GET /api/v1/settings/public":           fixture(t, "settings_public"),
	}
}

func TestAccountReadsBalanceAndFunding(t *testing.T) {
	t.Parallel()
	table := panel(t)
	srv := routes(table)
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	acc, err := m.Account(t.Context())
	if err != nil || acc.Balance != "199.944425" || acc.Funding != adapter.FundingWallet {
		t.Fatalf("Account = %+v, %v; want the recorded balance as exact text, from the wallet", acc, err)
	}
	table["GET /api/v1/subscriptions/active"] = adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":[{"id":1}]}`)}
	if acc, err = m.Account(t.Context()); err != nil || acc.Funding != adapter.FundingOther {
		t.Errorf("with an active subscription: %+v, %v; want funding other", acc, err)
	}
}

func TestKeysAndKeySecret(t *testing.T) {
	t.Parallel()
	srv := routes(panel(t))
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	keys, err := m.Keys(t.Context())
	want := []adapter.KeyState{{Ref: "2", GroupRef: "2", Status: adapter.KeyActive}, {Ref: "1", GroupRef: "1", Status: adapter.KeyActive}}
	if err != nil || !slices.Equal(keys, want) {
		t.Fatalf("Keys = %+v, %v; want %+v", keys, err, want)
	}
	s, err := m.KeySecret(t.Context(), "2")
	if err != nil || s.Reveal() != "placeholder-key-one" {
		t.Errorf("KeySecret(2) = %v", err)
	}
	sent := len(srv.requests)
	for _, bad := range []adapter.KeyRef{"", "2/../1", "2?x=1", "abc", "1234567890123456789"} {
		if _, err := m.KeySecret(t.Context(), bad); err == nil {
			t.Errorf("KeySecret(%q) succeeded", bad)
		}
	}
	if len(srv.requests) != sent {
		t.Errorf("a key id that is not a number was put in a path: %v", srv.paths()[sent:])
	}
	if _, err := m.KeySecret(t.Context(), "3"); err == nil {
		t.Error("a key answer for another key's id was taken")
	}
}

// TestOfferReadsGroupsModelsAndMultipliers: models come from a key of the group, the multiplier from the key's own
// billing view, and a group whose key cannot list (empty wallet) is marked unread instead of empty.
func TestOfferReadsGroupsModelsAndMultipliers(t *testing.T) {
	t.Parallel()
	m := manager0(t, offerServer(t), login(t, epoch.Add(24*time.Hour)))
	offer, err := m.Offer(t.Context())
	if err != nil || len(offer.Groups) != 2 {
		t.Fatalf("Offer = %+v, %v", offer, err)
	}
	named, empty := offer.Groups[0], offer.Groups[1]
	if named.Name != "claude-mock" {
		named, empty = empty, named
	}
	if named.ModelsUnread || len(named.Models) != 14 || named.Ratio != "1.5" || !slices.Equal(named.Models[0].Formats, []protocol.Format{protocol.Messages}) {
		t.Errorf("claude-mock: %d models, ratio %s, unread %v; want 14 models at 1.5 for Messages", len(named.Models), named.Ratio, named.ModelsUnread)
	}
	if !empty.ModelsUnread || len(empty.Models) != 0 || empty.Ratio != "1" {
		t.Errorf("default group: %+v; want models unread at the group's own multiplier", empty)
	}
	if named.Models[0].Cost != nil {
		t.Error("a price was invented for a model the supplier shows none for")
	}
}

// offerServer answers as a supplier whose default group's key has an empty wallet (it cannot list models and has no
// billing view) and whose other key is billed at 1.5.
func offerServer(t testing.TB) *server {
	table := panel(t)
	return newServer(func(r adapter.Request) (adapter.Response, error) {
		key := r.Header.Get("Authorization")
		switch {
		case r.Path == "/v1/models" && key == "Bearer placeholder-key-two":
			return adapter.Response{Status: 403, Body: []byte(`{"code":"INSUFFICIENT_BALANCE","message":"x"}`)}, nil
		case r.Path == "/v1/models":
			return fixture(t, "models"), nil
		case r.Path == "/v1/sub2api/billing" && key == "Bearer placeholder-key-two":
			return adapter.Response{Status: 404, Body: []byte(`{"code":404}`)}, nil
		case r.Path == "/v1/sub2api/billing":
			resp := fixture(t, "billing")
			resp.Body = []byte(strings.Replace(string(resp.Body), `"resolved_rate_multiplier": 1`, `"resolved_rate_multiplier": 1.5`, 1))
			return resp, nil
		}
		return table[r.Method+" "+r.Path], nil
	})
}

func TestChargesScanARecordedPage(t *testing.T) {
	t.Parallel()
	const tail = "&page=1&page_size=200&sort_by=created_at&sort_order=desc&start_date=2026-10-15&timezone=UTC"
	pages := map[string]adapter.Response{
		"/api/v1/usage?api_key_id=2&end_date=2026-10-15" + tail: fixture(t, "usage_page1"),
		"/api/v1/usage?api_key_id=1&end_date=2026-10-15" + tail: fixture(t, "usage_page2"),
	}
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if strings.HasPrefix(r.Path, "/api/v1/keys?") {
			return fixture(t, "keys"), nil
		}
		resp, ok := pages[r.Path]
		if !ok {
			return adapter.Response{}, fmt.Errorf("unexpected %s", r.Path)
		}
		return resp, nil
	})
	clk := &clock{t: time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)}
	all := scanPass(t, managerAt(t, srv, login(t, clk.t.Add(24*time.Hour)), clk), clk)
	if len(all) != 2 {
		t.Fatalf("%d charges, want the 2 recorded", len(all))
	}
	c := all[0]
	if c.Ref != "client:00000000-0000-4000-8000-000000000001" || c.Key != "2" || c.Amount != "0.0092625" || c.Funding != adapter.FundingWallet || c.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("first charge = %+v", c)
	}
	checkUsage(t, c)
	if c.Reported.Has(catalog.ItemReasoning) {
		t.Error("an item the record does not state was reported")
	}
	if strings.Contains(fmt.Sprintf("%+v", all), "placeholder-key") {
		t.Error("a key's text reached a charge")
	}
}

// checkUsage: 1000 input, 300 output, 3000 cache read, 150 five-minute and 50 one-hour cache write, all reported.
func checkUsage(t *testing.T, c adapter.Charge) {
	t.Helper()
	for item, want := range map[catalog.Item]int64{catalog.ItemInput: 1000, catalog.ItemOutput: 300, catalog.ItemCacheRead: 3000, catalog.ItemCacheWrite: 150, catalog.ItemCacheWrite1h: 50} {
		if !c.Reported.Has(item) || usageOf(t, c, item) != want {
			t.Errorf("%s: reported %v, count %d, want %d", item, c.Reported.Has(item), usageOf(t, c, item), want)
		}
	}
}

// TestScanCoversEveryKeyAndPage: a full page asks for the next; a short page ends the key; a page whose oldest record
// is before the window ends it too; the pass ends after the last key, across midnight with both days asked for.
func TestScanCoversEveryKeyAndPage(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	item := func(id int, at time.Time) string {
		return fmt.Sprintf(`{"request_id":"client:r%d","api_key_id":7,"model":"m","created_at":%q,"actual_cost":0.5,"billing_type":0,"billing_mode":"token","input_tokens":1,"output_tokens":1}`, id, at.Format(time.RFC3339))
	}
	full := make([]string, usagePageSize)
	for i := range full {
		full[i] = item(i, time.Date(2026, 10, 15, 0, 30, 0, 0, time.UTC))
	}
	var asked []string
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if strings.HasPrefix(r.Path, "/api/v1/keys?") {
			return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[{"id":7,"key":"k","group_id":1,"status":"active"},{"id":8,"key":"k","group_id":1,"status":"active"}],"pages":1}}`)}, nil
		}
		asked = append(asked, r.Path)
		body := `[` + item(1, older) + `]`
		if strings.Contains(r.Path, "api_key_id=7") && strings.Contains(r.Path, "page=1&") {
			body = `[` + strings.Join(full, ",") + `]`
		}
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":` + body + `,"pages":9}}`)}, nil
	})
	clk := &clock{t: time.Date(2026, 10, 14, 23, 50, 0, 0, time.UTC)}
	m := managerAt(t, srv, login(t, clk.t.Add(24*time.Hour)), clk)
	state := begin(t, m)
	clk.advance(20 * time.Minute) // the pass covers 23:45 to 00:09:30, across midnight
	var pages int
	for done := false; !done; pages++ {
		page, next, err := m.Charges(t.Context(), state)
		if err != nil {
			t.Fatal(err)
		}
		state, done = next, page.Done
	}
	if pages != 3 { // key 7 page 1 (full), key 7 page 2 (its oldest record is before the window), key 8 page 1
		t.Errorf("%d pages read, want 3: %v", pages, asked)
	}
	for _, p := range asked {
		if !strings.Contains(p, "start_date=2026-10-14") || !strings.Contains(p, "end_date=2026-10-15") {
			t.Errorf("%s does not ask for both days of a window across midnight", p)
		}
	}
	if _, _, err := m.Charges(t.Context(), []byte(`{"v":2}`)); !errors.Is(err, adapter.ErrUnrecognised) {
		t.Errorf("a position of another version: %v", err)
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	srv := routes(panel(t))
	d, err := New().Detect(t.Context(), srv)
	if err != nil || d.Version != "0.2.16" || d.Captcha {
		t.Errorf("Detect = %+v, %v; want version 0.2.16 without a captcha", d, err)
	}
	captcha := panel(t)
	captcha["GET /api/v1/settings/public"] = adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"version":"0.2.16","turnstile_enabled":true}}`)}
	if d, err = New().Detect(t.Context(), routes(captcha)); err != nil || !d.Captcha {
		t.Errorf("Detect with a captcha = %+v, %v", d, err)
	}
	other := map[string]adapter.Response{"GET /api/v1/settings/public": {Status: 404, Body: []byte("not found")}}
	if _, err = New().Detect(t.Context(), routes(other)); !errors.Is(err, adapter.ErrNotRecognised) {
		t.Errorf("another system: %v", err)
	}
}

func TestChargeRefAndClassify(t *testing.T) {
	t.Parallel()
	q := New().Calls()
	if ref, ok := q.ChargeRef(http.Header{"X-Client-Request-Id": {"00000000-0000-4000-8000-000000000001"}}); !ok || ref != "client:00000000-0000-4000-8000-000000000001" {
		t.Errorf("ChargeRef = %q, %v", ref, ok)
	}
	if _, ok := q.ChargeRef(http.Header{"X-Client-Request-Id": {"a b\x00"}}); ok {
		t.Error("a value that is not an id was taken for one")
	}
	body := func(s string) []byte { return []byte(s) }
	for _, c := range []struct {
		name   string
		status int
		h      http.Header
		body   []byte
		want   adapter.Failure
	}{
		{"balance", 403, nil, body(`{"code":"INSUFFICIENT_BALANCE"}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeSupplier, Cause: adapter.SupplierBalanceLow{}}},
		{"invalid key", 401, nil, body(`{"code":"INVALID_API_KEY"}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
		{"key quota", 429, nil, body(`{"code":"API_KEY_QUOTA_EXHAUSTED"}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialQuotaExhausted{}}},
		{"usage limit", 429, nil, body(`{"code":"USAGE_LIMIT_EXCEEDED"}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialQuotaExhausted{}}},
		{"key rate limit", 429, http.Header{"Retry-After": {"7"}}, body(`{"code":"rate_limit_exceeded"}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.RateLimited{RetryAfter: 7 * time.Second}}},
		{"other 429", 429, nil, body(`{}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{}}},
		{"no capacity", 503, nil, body(`{}`), adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}},
		{"upstream 502", 502, nil, nil, adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: 502}}},
		{"context", 400, nil, body(`{"error":{"code":"context_length_exceeded"}}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.ContextTooLong{}}},
		{"bad request", 400, nil, body(`{"error":{"type":"invalid_request_error"}}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}},
		{"unknown", 418, nil, body(`{"code":"TEAPOT"}`), adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: 418}}},
	} {
		if got := q.Classify(c.status, c.h, c.body); got != c.want {
			t.Errorf("%s: Classify = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestFormatsServeOnlyWhatCanBeSold(t *testing.T) {
	t.Parallel()
	q := New().Calls()
	for facts, want := range map[string][]protocol.Format{
		`{"platform":"anthropic","status":"active","subscription_type":"standard"}`:                          {protocol.Messages},
		`{"platform":"openai","status":"active","subscription_type":"standard"}`:                             {protocol.Responses, protocol.Chat},
		`{"platform":"openai","status":"active","subscription_type":"subscription"}`:                         nil,
		`{"platform":"anthropic","status":"active","subscription_type":"standard","peak_rate_enabled":true}`: nil,
		`{"platform":"gemini","status":"active","subscription_type":"standard"}`:                             nil,
		`{"platform":"openai","status":"inactive","subscription_type":"standard"}`:                           nil,
	} {
		if got := q.Formats(adapter.GroupFacts(facts)); !slices.Equal(got, want) {
			t.Errorf("Formats(%s) = %v, want %v", facts, got, want)
		}
	}
}

func TestAmountsAreExactText(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"0.0092625": "0.0092625", "1e-7": "0.0000001", "199.944425": "199.944425", "5": "5", "0": "0", "-13.5": "-13.5", "1.50": "1.5"} {
		if got, err := number(in).amount(); err != nil || string(got) != want {
			t.Errorf("amount(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "0.1234567890123", "1e-20", ""} {
		if got, err := number(in).amount(); err == nil {
			t.Errorf("amount(%q) = %q; want a refusal, not a rounded figure", in, got)
		}
	}
}

// TestAbsentFieldsAreNotReported: a record that does not state an item leaves it out of Reported, which differs from a
// stated zero; only the stated ones may correct an estimate.
func TestAbsentFieldsAreNotReported(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if strings.HasPrefix(r.Path, "/api/v1/keys?") {
			return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[{"id":7,"key":"k","group_id":1,"status":"active"}],"pages":1}}`)}, nil
		}
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[
			{"request_id":"client:a","api_key_id":7,"model":"m","created_at":"2026-10-15T12:00:00Z","actual_cost":0.25,"billing_type":0,"billing_mode":"token","input_tokens":10,"output_tokens":0},
			{"request_id":"client:b","api_key_id":7,"model":"m","created_at":"2026-10-15T12:00:00Z","actual_cost":1,"billing_type":1,"billing_mode":"per_request"}],"pages":1}}`)}, nil
	})
	clk := &clock{t: epoch}
	got := scanPass(t, managerAt(t, srv, login(t, epoch.Add(24*time.Hour)), clk), clk)
	if len(got) != 2 {
		t.Fatalf("%d charges", len(got))
	}
	a, b := got[0], got[1]
	if !a.Reported.Has(catalog.ItemInput) || !a.Reported.Has(catalog.ItemOutput) || a.Reported.Has(catalog.ItemCacheRead) || a.Reported.Has(catalog.ItemCacheWrite) {
		t.Errorf("reported %v, want only input and output (output stated as zero)", a.Reported)
	}
	if b.Reported.Has(catalog.ItemInput) || b.Funding != adapter.FundingOther || len(b.Usage.Unknown()) != 1 {
		t.Errorf("a per-request subscription record: reported %v, funding %v, unknown %v", b.Reported, b.Funding, b.Usage.Unknown())
	}
}

// begin makes the first scan, which only marks the start, and returns its position.
func begin(t *testing.T, m adapter.Manager) adapter.ScanState {
	t.Helper()
	page, state, err := m.Charges(t.Context(), nil)
	if err != nil || !page.Done {
		t.Fatalf("the first scan: %+v, %v", page, err)
	}
	return state
}

// usageOf reads a count back through a one-line schedule that prices only that item at 1 per unit.
func usageOf(t *testing.T, c adapter.Charge, item catalog.Item) int64 {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(fmt.Sprintf(`{"currency":"XXX","rates":{%q:"1"},"v":1}`, item.String())))
	if err != nil {
		t.Fatal(err)
	}
	total, _, err := s.Cost(c.Usage)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := total.Rat().Float64()
	return int64(n)
}
