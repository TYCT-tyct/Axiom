package vendors_test

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/vendors"
)

// statusClient answers every request with one status and body, and keeps what it was sent.
type statusClient struct {
	status int
	body   []byte
	sent   []adapter.Request
}

func (c *statusClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	c.sent = append(c.sent, r)
	return adapter.Response{Status: c.status, Body: c.body}, nil
}

func openRouter(t *testing.T) adapter.Aggregator {
	t.Helper()
	for _, a := range vendors.New() {
		if a.Kind() == "openrouter" {
			return a.(adapter.Aggregator)
		}
	}
	t.Fatal("no openrouter row")
	return nil
}

// TestOpenRouterCeiling: the ceiling is each item's highest price over every provider and every price override of
// the model, the long-context tier among them, with either cache write price bounding both writes; a price field the
// table does not know, a list with no provider, a variant's name and a 404 leave the name unpriced, and a 5xx says
// nothing. The samples are OpenRouter's answers as recorded.
func TestOpenRouterCeiling(t *testing.T) {
	t.Parallel()
	samples := os.DirFS("testdata")
	sample := func(name string) []byte {
		b, err := fs.ReadFile(samples, "openrouter-endpoints-"+name+".json")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range []struct {
		name, model string
		status      int
		body        []byte
		want        adapter.CeilingOutcome
		rates       map[catalog.Item]string
		path        string
	}{
		{
			name: "providers and the long-context override", model: "anthropic/claude-sonnet-4.5", status: 200, body: sample("sonnet"),
			want: adapter.CeilingRead, path: "/api/v1/models/anthropic/claude-sonnet-4.5/endpoints",
			rates: map[catalog.Item]string{
				catalog.ItemInput: "0.000006", catalog.ItemOutput: "0.0000225", catalog.ItemCacheRead: "0.0000006",
				catalog.ItemCacheWrite: "0.000012", catalog.ItemCacheWrite1h: "0.000012", catalog.ItemWebSearch: "0.01",
			},
		},
		{
			name: "the dearest of many providers", model: "meta-llama/llama-3.3-70b-instruct", status: 200, body: sample("llama"),
			want:  adapter.CeilingRead,
			rates: map[catalog.Item]string{catalog.ItemInput: "0.00000072", catalog.ItemOutput: "0.000002253", catalog.ItemCacheRead: "0.00000071"},
		},
		{
			// A cache write of 0.0000000833333333333333 has more decimals than a price holds, and is rounded up.
			name: "audio, images and more decimals than a price holds", model: "google/gemini-2.5-flash", status: 200, body: sample("gemini"),
			want: adapter.CeilingRead,
			rates: map[catalog.Item]string{
				catalog.ItemInput: "0.00000054", catalog.ItemOutput: "0.0000045", catalog.ItemAudioIn: "0.0000018",
				catalog.ItemImageIn: "0.00000054", catalog.ItemCacheRead: "0.00000018", catalog.ItemCacheWrite: "0.00000015",
				catalog.ItemReasoning: "0.0000045",
			},
		},
		{
			name: "a price the schedule cannot express", model: "x/y", status: 200,
			body: []byte(`{"data":{"endpoints":[{"pricing":{"prompt":"0.000001","completion":"0.000002","video_second":"0.1"}}]}}`),
			want: adapter.CeilingUnexpressible,
		},
		{
			name: "an unknown field priced at zero", model: "x/y", status: 200,
			body:  []byte(`{"data":{"endpoints":[{"pricing":{"prompt":"0.000001","completion":"0.000002","video_second":"0","discount":0.5}}]}}`),
			want:  adapter.CeilingRead,
			rates: map[catalog.Item]string{catalog.ItemInput: "0.000001", catalog.ItemOutput: "0.000002"},
		},
		{name: "no provider", model: "x/y", status: 200, body: []byte(`{"data":{"endpoints":[]}}`), want: adapter.CeilingNone},
		{name: "not found", model: "x/y", status: 404, want: adapter.CeilingNone},
		{name: "a variant", model: "x/y:free", status: 200, body: sample("llama"), want: adapter.CeilingNone},
		{name: "a server error", model: "x/y", status: 503, want: adapter.CeilingInconclusive},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			client := &statusClient{status: c.status, body: c.body}
			got := openRouter(t).ReadCeiling(t.Context(), client, secret.New("sk-or-test"), nil, c.model)
			if got.Outcome != c.want {
				t.Fatalf("outcome %d, want %d", got.Outcome, c.want)
			}
			if c.path != "" && (len(client.sent) != 1 || client.sent[0].Path != c.path || client.sent[0].Header.Get("Authorization") != "Bearer sk-or-test") {
				t.Errorf("sent %+v", client.sent)
			}
			if c.rates != nil {
				expectRates(t, got.Schedule, c.rates)
			}
		})
	}
}

// expectRates checks that the schedule prices each item of rates at its price, and no request, which no provider
// prices.
func expectRates(t *testing.T, s catalog.Schedule, rates map[catalog.Item]string) {
	t.Helper()
	for it, want := range rates {
		if p, priced := s.BaseRate(it); !priced || p.Cmp(must(platform.ParseUnitPrice(want))) != 0 {
			t.Errorf("%s: %v (priced %v), want %s", it, p, priced, want)
		}
	}
	if _, priced := s.BaseRate(catalog.ItemRequest); priced {
		t.Error("a request price no provider has")
	}
}

// TestOpenRouterGeneration: a generation's record gives what it cost and whether it was a BYOK call; a 404 is a record
// not written yet, and a cost that is not a number is an error, never a cost of zero.
func TestOpenRouterGeneration(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   adapter.Generation
		err    bool
	}{
		{"charged", 200, `{"data":{"id":"gen-1","total_cost":0.0015,"is_byok":false}}`, adapter.Generation{Found: true, Cost: must(platform.ParseExact("0.0015"))}, false},
		{"zero", 200, `{"data":{"total_cost":0}}`, adapter.Generation{Found: true}, false},
		{"byok", 200, `{"data":{"total_cost":0.05,"is_byok":true}}`, adapter.Generation{Found: true, Cost: must(platform.ParseExact("0.05")), BYOK: true}, false},
		{"not yet", 404, `{"error":{"code":404}}`, adapter.Generation{}, false},
		{"no cost", 200, `{"data":{"is_byok":false}}`, adapter.Generation{}, true},
		{"a server error", 502, ``, adapter.Generation{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			client := &statusClient{status: c.status, body: []byte(c.body)}
			got, err := openRouter(t).ReadGeneration(t.Context(), client, secret.New("sk-or-test"), nil, "gen-1 2")
			if (err != nil) != c.err || got.Found != c.want.Found || got.BYOK != c.want.BYOK || got.Cost.Cmp(c.want.Cost) != 0 {
				t.Errorf("%+v (%v), want %+v", got, err, c.want)
			}
			if client.sent[0].Path != "/api/v1/generation?id=gen-1+2" {
				t.Errorf("asked %s", client.sent[0].Path)
			}
		})
	}
}

// TestOnlyOpenRouterAggregates: OpenRouter's calls are reserved whole whatever is measured, and carry the id its
// generation record is read by; no other row aggregates.
func TestOnlyOpenRouterAggregates(t *testing.T) {
	t.Parallel()
	for _, a := range vendors.New() {
		aggregates := a.(adapter.Aggregator).Aggregates()
		p := a.Calls().Policy()
		ref, kept := a.Calls().ChargeRef(http.Header{"X-Generation-Id": {"gen-9"}})
		if aggregates != (a.Kind() == "openrouter") || aggregates == p.Cancels || aggregates != p.CancelsFixed || kept != aggregates || aggregates && ref != "gen-9" {
			t.Errorf("%s: aggregates %v, policy %+v, ref %q %v", a.Kind(), aggregates, p, ref, kept)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
