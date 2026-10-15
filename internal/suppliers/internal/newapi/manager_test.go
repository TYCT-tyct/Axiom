package newapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
)

var now = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

func TestAdapterFollowsTheContract(t *testing.T) {
	t.Parallel()
	adaptertest.Run(t, New())
}

// TestDetectTellsTheSystemAndItsGeneration: a new-api shows itself by its request id header and status answer; the
// 1.0 line is told by the login encryption-key route, which 0.13 lacks.
func TestDetectTellsTheSystemAndItsGeneration(t *testing.T) {
	t.Parallel()
	status, key := sample(t, "01-detect-status"), sample(t, "02-detect-encryption-key")
	cases := []struct {
		name    string
		status  adapter.Response
		key     adapter.Response
		want    adapter.Detection
		wantErr error
	}{
		{"1.0", status, key, adapter.Detection{Generation: "1.0", Version: "v1.0.0-rc.44"}, nil},
		{"0.13 has no such route", status, answer(404, `{"success":false}`), adapter.Detection{Generation: "0.13", Version: "v1.0.0-rc.44"}, nil},
		{"not new-api: no request id", adapter.Response{Status: 200, Body: []byte(`{"success":true,"data":{}}`)}, key, adapter.Detection{}, adapter.ErrNotRecognised},
		{"not new-api: a web page", adapter.Response{Status: 200, Body: []byte("<html></html>")}, key, adapter.Detection{}, adapter.ErrNotRecognised},
		{
			"a captcha at login", answer(200, `{"success":true,"data":{"version":"v1","turnstile_check":true}}`), key,
			adapter.Detection{Generation: "1.0", Version: "v1", Captcha: true},
			nil,
		},
	}
	for _, c := range cases {
		f := newFake()
		f.routes["GET /api/status"], f.routes["GET /api/user/login/encryption-key"] = c.status, c.key
		got, err := New().Detect(t.Context(), f)
		if !errors.Is(err, c.wantErr) || cmp.Diff(c.want, got) != "" {
			t.Errorf("%s: %+v, %v; want %+v, %v", c.name, got, err, c.want, c.wantErr)
		}
	}
}

func TestAccountReadsTheWalletAndSaysWhenItIsNotTheFunding(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.routes["GET /api/user/self"], f.routes["GET /api/status"] = sample(t, "24-self-pat"), sample(t, "01-detect-status")
	f.routes["GET /api/subscription/self"] = sample(t, "45-subscription-self")
	got, err := open(t, f, now).Account(t.Context())
	want := adapter.Account{Balance: "5000000", Funding: adapter.FundingWallet, UnitHint: "quota_per_unit=500000"}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("Account: %+v, %v; want %+v", got, err, want)
	}
	f.routes["GET /api/subscription/self"] = answer(200, `{"success":true,"data":{"subscriptions":[{"id":1}]}}`)
	if got, err := open(t, f, now).Account(t.Context()); err != nil || got.Funding != adapter.FundingOther {
		t.Errorf("an active subscription: %+v, %v, want funding other than the wallet", got, err)
	}
	for _, request := range f.sent {
		if request.Header.Get("Authorization") != "Bearer "+fakePAT {
			t.Errorf("%s sent without the token", request.Path)
		}
	}
}

// TestAuthFailuresNameACodeAndNothingElse: a refused token is reported by the code from a fixed list, with the missing
// permission when that is the reason, and never with the supplier's own words.
func TestAuthFailuresNameACodeAndNothingElse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		resp adapter.Response
		want string
	}{
		{"an invalid token", sample(t, "53-err-self-bad-pat"), "AUTH_UNAUTHORIZED"},
		{"a missing permission", sample(t, "83-pat-narrow-missing-scope"), "ACCESS_TOKEN_SCOPE_DENIED usage:read"},
		{"an unlisted code", answer(401, `{"code":"PLANTED-CODE","message":"planted text","success":false}`), "unknown"},
		{"not JSON", answer(403, "<html>planted</html>"), "unknown"},
	}
	for _, c := range cases {
		f := newFake()
		f.routes["GET /api/user/self"] = c.resp
		_, err := open(t, f, now).Account(t.Context())
		if !errors.Is(err, adapter.ErrAuthRejected) || !strings.HasSuffix(err.Error(), c.want) || strings.Contains(err.Error(), "planted") ||
			strings.Contains(err.Error(), "Unauthorized, invalid") {
			t.Errorf("%s: %v, want a rejected login ending %q", c.name, err, c.want)
		}
	}
	f := newFake()
	f.routes["GET /api/user/self"] = sample(t, "55-err-handler-success-false")
	if _, err := open(t, f, now).Account(t.Context()); !errors.Is(err, adapter.ErrUnrecognised) || strings.Contains(err.Error(), "record not found") {
		t.Errorf("a handler failure (200, success false): %v, want an unrecognised answer without its text", err)
	}
}

func TestKeysAreReadWithTheirStatus(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.routes["GET /api/token/?p=1&page_size=100"] = sample(t, "57-list-keys2")
	got, err := open(t, f, now).Keys(t.Context())
	want := []adapter.KeyState{{Ref: "2", Status: adapter.KeyActive}, {Ref: "1", Status: adapter.KeyActive}}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("Keys: %+v, %v; want %+v", got, err, want)
	}
	f.routes["GET /api/token/?p=1&page_size=100"] = answer(200, `{"success":true,"data":{"items":[
		{"id":5,"status":4,"group":"vip"},{"id":6,"status":3,"group":""},{"id":7,"status":2,"group":""}]}}`)
	got, err = open(t, f, now).Keys(t.Context())
	want = []adapter.KeyState{{Ref: "5", GroupRef: "vip", Status: adapter.KeyQuotaExhausted}, {Ref: "6", Status: adapter.KeyExpired}, {Ref: "7", Status: adapter.KeyDisabled}}
	if err != nil || cmp.Diff(want, got) != "" {
		t.Errorf("Keys by status: %+v, %v; want %+v", got, err, want)
	}
	f.routes["GET /api/token/?p=1&page_size=100"] = answer(200, `{"success":true,"data":{"items":[{"id":5,"status":9}]}}`)
	if _, err := open(t, f, now).Keys(t.Context()); !errors.Is(err, adapter.ErrUnrecognised) {
		t.Errorf("a status new-api has not had: %v", err)
	}
}

// TestKeysCountedInTheCriticalBucketAreRevealedByIDOnly: a key's text comes from new-api by its id, in the critical
// bucket as well as the global one, and an id that is not a plain number is never put in a path.
func TestKeysCountedInTheCriticalBucketAreRevealedByIDOnly(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.routes["POST /api/token/1/key"] = sample(t, "28-reveal-key")
	m := open(t, f, now)
	got, err := m.KeySecret(t.Context(), "1")
	if err != nil || got.Reveal() != "sk-<ALICE_KEY1_BARE>" {
		t.Errorf("KeySecret: %v", err)
	}
	if want := []string{"global", "critical"}; cmp.Diff(want, f.sent[0].Buckets) != "" {
		t.Errorf("buckets %v, want %v", f.sent[0].Buckets, want)
	}
	for _, ref := range []adapter.KeyRef{"1/../2", "", "0", "-1", "1?x=y", "01"} {
		if _, err := m.KeySecret(t.Context(), ref); err == nil {
			t.Errorf("key id %q was read", ref)
		}
	}
	if len(f.sent) != 1 {
		t.Errorf("%d requests, want one: bad ids must not be sent", len(f.sent))
	}
	f.routes["POST /api/token/2/key"] = sample(t, "66-reveal-rate-limit-429")
	f.errs["POST /api/token/2/key"] = adapter.Blocked{Until: now.Add(time.Hour)}
	if _, err := m.KeySecret(t.Context(), "2"); !errors.Is(err, adapter.ErrBucketsFull) {
		t.Errorf("a blocked reveal: %v, want the caller to defer", err)
	}
}

// TestClassifyReadsRecordedFailures: each recorded failure of new-api is the failure the design's table says, and one
// that only looks like it (the channel's upstream answering the same status) is not taken for it.
func TestClassifyReadsRecordedFailures(t *testing.T) {
	t.Parallel()
	html := adapter.Response{Status: 403, Body: []byte("<!DOCTYPE html><html>Just a moment</html>")}
	cases := []struct {
		name string
		resp adapter.Response
		want adapter.Failure
	}{
		{"an invalid or overdrawn key", sample(t, "50-err-invalid-key"), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
		{"the same for a key used up", sample(t, "59-err-key-exhausted"), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
		{"no balance", sample(t, "64-err-insufficient-wallet"), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeSupplier, Cause: adapter.SupplierBalanceLow{}}},
		{"no channel for the model", sample(t, "51-err-no-model-access"), adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}},
		{"a rate limit", sample(t, "66-reveal-rate-limit-429"), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: 1200 * time.Second}}},
		{"a web page", html, adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: 403, SuspectedEgressBlock: true}}},
		{
			"a 401 that is the channel's own", answer(401, `{"error":{"code":"","message":"bad provider key","type":"upstream_error"}}`),
			adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: 401}},
		},
		{"a 502", answer(502, `{"error":{"code":"x","type":"upstream_error"}}`), adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: 502}}},
		{"a 400", answer(400, `{"error":{"code":"invalid_request","type":"invalid_request_error"}}`), adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}},
	}
	for _, c := range cases {
		got := New().Calls().Classify(c.resp.Status, c.resp.Header, c.resp.Body)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}

// TestChargeRefIsNewAPIsRequestID and the formats a group serves.
func TestChargeRefAndFormats(t *testing.T) {
	t.Parallel()
	q := New().Calls()
	ref, ok := q.ChargeRef(sample(t, "32-log-by-request-id").Header)
	if !ok || ref != "sample-request-id" {
		t.Errorf("ChargeRef = %q, %v", ref, ok)
	}
	for _, bad := range []string{"", "has space", "ctrl\x01", strings.Repeat("a", 129)} {
		if _, ok := q.ChargeRef(http.Header{requestIDHeader: {bad}}); ok {
			t.Errorf("request id %q taken", bad)
		}
	}
	formats := q.Formats(adapter.GroupFacts(`{"desc":"x","endpoint_types":["anthropic","openai","openai","unknown"]}`))
	if diff := cmp.Diff([]protocol.Format{protocol.Messages, protocol.Chat}, formats); diff != "" {
		t.Errorf("formats (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]protocol.Format{protocol.Chat}, q.Formats(nil)); diff != "" {
		t.Errorf("formats without facts (-want +got):\n%s", diff)
	}
}
