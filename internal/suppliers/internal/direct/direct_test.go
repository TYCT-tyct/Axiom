package direct_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/direct"
)

// The providers of these tests are made here, not taken from the providers' packages, so that taking one of those out
// leaves this package building (just adapters-removable). They are each kind there is: one with Responses, one of
// Anthropic's family, one under a root of its own, one with an Anthropic-compatible API for some of its models, and one
// whose family is the supplier's.
var (
	openaiAPI = direct.New(direct.Provider{Type: entryType("openai", adapter.FamilyOpenAI, adapter.Field{
		Name: "organization", Header: "OpenAI-Organization",
	}), Responses: true})
	anthropicAPI = direct.New(direct.Provider{Type: entryType("anthropic", adapter.FamilyAnthropic)})
	googleAPI    = direct.New(direct.Provider{Type: entryType("google", adapter.FamilyOpenAI), Root: "/v1beta/openai"})
	bothAPI      = direct.New(direct.Provider{
		Type: entryType("both", adapter.FamilyOpenAI, adapter.FamilyAnthropic), Root: "/compatible-mode/v1",
		Anthropic: &direct.Anthropic{Root: "/apps/anthropic", Models: []string{"served"}},
	})
	compatibleAPI = direct.New(direct.Provider{Type: adapter.Type{
		Kind: "compatible", Name: "compatible", Group: adapter.GroupOfficial, Lists: true,
		Families: []adapter.Family{adapter.FamilyOpenAI, adapter.FamilyAnthropic},
	}})
	providers = []*direct.Adapter{openaiAPI, anthropicAPI, googleAPI, bothAPI, compatibleAPI}
)

// entryType is a type at one address of its own, speaking the families (the first its own) with the fields.
func entryType(kind string, families ...any) adapter.Type {
	t := adapter.Type{
		Kind: adapter.Kind(kind), Name: kind, Group: adapter.GroupOfficial, Lists: true,
		Entries: []adapter.PriceEntry{{ID: "global", Origin: "https://" + kind + ".example", Currency: "USD"}},
	}
	for _, f := range families {
		switch f := f.(type) {
		case adapter.Family:
			t.Families = append(t.Families, f)
		case adapter.Field:
			t.Fields = append(t.Fields, f)
		}
	}
	return t
}

// TestProviderTypesAreValid: each provider's description is one the registry would start with.
func TestProviderTypesAreValid(t *testing.T) {
	t.Parallel()
	for _, a := range providers {
		if err := a.Type().Check(); err != nil {
			t.Errorf("%s: %v", a.Kind(), err)
		}
	}
}

// TestProvidersKeepTheAdapterContract: every provider passes the suite every adapter does.
func TestProvidersKeepTheAdapterContract(t *testing.T) {
	t.Parallel()
	for _, a := range providers {
		adaptertest.Run(t, a)
	}
}

// fakeClient answers every request with one response or error, and keeps what it was sent.
type fakeClient struct {
	resp adapter.Response
	err  error
	sent []adapter.Request
}

func (c *fakeClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	c.sent = append(c.sent, r)
	return c.resp, c.err
}

// TestCheckKeyAsksTheModelList: each provider is asked for its model list where it serves it, with the key the way it
// takes it, in one request.
func TestCheckKeyAsksTheModelList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a              *direct.Adapter
		family         adapter.Family
		path, header   string
		value, version string
	}{
		{openaiAPI, adapter.FamilyOpenAI, "/v1/models", "Authorization", "Bearer sk-test-key", ""},
		{anthropicAPI, adapter.FamilyAnthropic, "/v1/models?limit=1000", "X-Api-Key", "sk-test-key", "2023-06-01"},
		{googleAPI, adapter.FamilyOpenAI, "/v1beta/openai/models", "Authorization", "Bearer sk-test-key", ""},
		{compatibleAPI, adapter.FamilyAnthropic, "/v1/models?limit=1000", "X-Api-Key", "sk-test-key", "2023-06-01"},
	} {
		c := &fakeClient{resp: adapter.Response{Status: http.StatusOK, Body: []byte(`{"data":[]}`)}}
		tc.a.CheckKey(t.Context(), c, tc.family, secret.New("sk-test-key"), nil)
		if len(c.sent) != 1 {
			t.Fatalf("%s: %d requests, want one", tc.a.Kind(), len(c.sent))
		}
		r := c.sent[0]
		if r.Method != http.MethodGet || r.Path != tc.path || r.Header.Get(tc.header) != tc.value || r.Header.Get("Anthropic-Version") != tc.version {
			t.Errorf("%s: sent %s %s %v, want GET %s with %s and version %q", tc.a.Kind(), r.Method, r.Path, r.Header, tc.path, tc.header, tc.version)
		}
	}
}

// TestCheckKeyJudgesOnlyWhatSaysSomethingOfTheKey: only a refusal of the key fails it; what may be a region, a limit or
// the provider's own trouble leaves it unjudged; a pass carries the names a call sends, once each.
func TestCheckKeyJudgesOnlyWhatSaysSomethingOfTheKey(t *testing.T) {
	t.Parallel()
	ok := func(body string) adapter.Response { return adapter.Response{Status: http.StatusOK, Body: []byte(body)} }
	for _, tc := range []struct {
		name string
		resp adapter.Response
		err  error
		want adapter.KeyCheck
	}{
		{
			"a list", ok(`{"data":[{"id":"gpt-b"},{"id":"gpt-a"},{"id":"gpt-a"},{"id":""},{"id":"has space"}]}`),
			nil,
			adapter.KeyCheck{Outcome: adapter.KeyPassed, Status: 200, Models: []string{"gpt-a", "gpt-b"}},
		},
		{
			"Google's names", ok(`{"object":"list","data":[{"id":"models/gemini-x"}]}`),
			nil,
			adapter.KeyCheck{Outcome: adapter.KeyPassed, Status: 200, Models: []string{"gemini-x"}},
		},
		{"an empty list", ok(`{"data":[]}`), nil, adapter.KeyCheck{Outcome: adapter.KeyPassed, Status: 200, Models: []string{}}},
		{"not a list", ok(`{"models":[]}`), nil, adapter.KeyCheck{Cause: adapter.CauseUnknown, Status: 200}},
		{"refused", adapter.Response{Status: 401}, nil, adapter.KeyCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRejected, Status: 401}},
		{
			"Google's refusal",
			adapter.Response{Status: 400, Body: []byte(`[{"error":{"details":[{"reason":"API_KEY_INVALID"}]}}]`)},
			nil,
			adapter.KeyCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRejected, Status: 400},
		},
		{"another 400", adapter.Response{Status: 400}, nil, adapter.KeyCheck{Cause: adapter.CauseUnknown, Status: 400}},
		{"a region block", adapter.Response{Status: 403}, nil, adapter.KeyCheck{Cause: adapter.CauseNotPermitted, Status: 403}},
		{"a 5xx", adapter.Response{Status: 503}, nil, adapter.KeyCheck{Cause: adapter.CauseUpstreamError, Status: 503}},
		{"a rate limit", adapter.Response{}, adapter.Blocked{}, adapter.KeyCheck{Cause: adapter.CauseRateLimited, Status: 429}},
		{"a timeout", adapter.Response{}, fmt.Errorf("send: %w", context.DeadlineExceeded), adapter.KeyCheck{Cause: adapter.CauseTimeout}},
		{"no answer", adapter.Response{}, errors.New("connection refused"), adapter.KeyCheck{Cause: adapter.CauseTransport}},
	} {
		got := openaiAPI.CheckKey(t.Context(), &fakeClient{resp: tc.resp, err: tc.err}, adapter.FamilyOpenAI, secret.New("sk-test-key"), nil)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.name, diff)
		}
	}
}

// TestWireIsTheProvidersOwn: a call goes to the path the provider serves the format at, Messages under its
// Anthropic-compatible root, with the key the way the format takes it; a setting the type sends in a header goes with
// it, and one it does not is not sent.
func TestWireIsTheProvidersOwn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a            *direct.Adapter
		format       protocol.Format
		path, header string
		value        string
		settings     adapter.Settings
		headers      int
	}{
		{openaiAPI, protocol.Responses, "/v1/responses", "Authorization", "Bearer k", nil, 1},
		{openaiAPI, protocol.Chat, "/v1/chat/completions", "Openai-Organization", "org-a", adapter.Settings{"organization": "org-a"}, 2},
		{googleAPI, protocol.Chat, "/v1beta/openai/chat/completions", "Authorization", "Bearer k", adapter.Settings{"project": "p"}, 1},
		{googleAPI, protocol.Embeddings, "/v1beta/openai/embeddings", "Authorization", "Bearer k", nil, 1},
		{bothAPI, protocol.Chat, "/compatible-mode/v1/chat/completions", "Authorization", "Bearer k", nil, 1},
		{bothAPI, protocol.Messages, "/apps/anthropic/v1/messages", "X-Api-Key", "k", nil, 1},
		{anthropicAPI, protocol.Messages, "/v1/messages", "X-Api-Key", "k", nil, 1},
		{compatibleAPI, protocol.Messages, "/v1/messages", "X-Api-Key", "k", nil, 1},
		{compatibleAPI, protocol.Chat, "/v1/chat/completions", "Authorization", "Bearer k", nil, 1},
	} {
		w, ok := tc.a.Calls().(adapter.Wire)
		if !ok {
			t.Fatalf("%s: the call side has no wire", tc.a.Kind())
		}
		defaults := map[protocol.Format]string{
			protocol.Chat: "/v1/chat/completions", protocol.Responses: "/v1/responses",
			protocol.Embeddings: "/v1/embeddings", protocol.Messages: "/v1/messages",
		}
		h := http.Header{}
		w.Authorize(h, tc.format, "k", tc.settings)
		if got := w.Path(tc.format, defaults[tc.format]); got != tc.path || h.Get(tc.header) != tc.value || len(h) != tc.headers {
			t.Errorf("%s %v: %s with %v, want %s with %s", tc.a.Kind(), tc.format, got, h, tc.path, tc.header)
		}
	}
}

// TestClassifyRestsOnlyTheKeyItsProviderRefused: each pasted key is an account of its own, so a refusal or a used-up
// allowance is the key's; a 403 may be a region and must not mark a working key.
func TestClassifyRestsOnlyTheKeyItsProviderRefused(t *testing.T) {
	t.Parallel()
	q := openaiAPI.Calls()
	for _, tc := range []struct {
		status int
		body   string
		scope  adapter.Scope
		cause  adapter.Cause
	}{
		{401, `{"error":{"code":"invalid_api_key"}}`, adapter.ScopeCredential, adapter.CredentialRejected{}},
		{400, `[{"error":{"status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}]`, adapter.ScopeCredential, adapter.CredentialRejected{}},
		{429, `{"error":{"code":"insufficient_quota"}}`, adapter.ScopeCredential, adapter.CredentialQuotaExhausted{}},
		{402, `{"error":{"message":"Insufficient Balance","type":"unknown_error"}}`, adapter.ScopeCredential, adapter.CredentialQuotaExhausted{}},
		{429, `{"error":{"code":"rate_limit_exceeded"}}`, adapter.ScopeEndpoint, adapter.RateLimited{}},
		{403, `{"error":{"code":"unsupported_country_region_territory"}}`, adapter.ScopeEndpoint, adapter.NotPermitted{}},
		{400, `{"error":{"code":"context_length_exceeded"}}`, adapter.ScopeEndpoint, adapter.ContextTooLong{}},
		{400, `{"type":"error","error":{"type":"invalid_request_error"}}`, adapter.ScopeRequest, adapter.BadRequest{}},
		{404, `{"type":"error","error":{"type":"not_found_error"}}`, adapter.ScopeEndpoint, adapter.ModelNotFound{}},
		{529, `{"type":"error","error":{"type":"overloaded_error"}}`, adapter.ScopeEndpoint, adapter.Overloaded{}},
		{500, `{}`, adapter.ScopeEndpoint, adapter.UpstreamError{Status: 500}},
	} {
		f := q.Classify(tc.status, http.Header{}, []byte(tc.body))
		if f.Scope != tc.scope || f.Cause != tc.cause {
			t.Errorf("%d %s: %v %v, want %v %v", tc.status, tc.body, f.Scope, f.Cause, tc.scope, tc.cause)
		}
	}
	if _, ok := q.ChargeRef(http.Header{"X-Request-Id": {"req_1"}}); ok {
		t.Error("a request id is kept, which no charge record will ever match")
	}
}
