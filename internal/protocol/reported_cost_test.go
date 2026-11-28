package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// TestReportedCost: usage.cost reaches billing only from a reply that ended normally, streamed or whole, read exactly
// from its text; an explicit 0 is a figure of 0, and a missing, malformed or negative one is no figure at all
// (docs/domains/suppliers-direct.md「OpenRouter 的成本」).
func TestReportedCost(t *testing.T) {
	t.Parallel()
	usage := func(extra string) string { return `{"prompt_tokens":9,"completion_tokens":2` + extra + `}` }
	whole := func(extra string) string {
		return `{"id":"gen-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant",` +
			`"content":"Hi"},"finish_reason":"stop"}],"usage":` + usage(extra) + `}`
	}
	for _, c := range []struct {
		name, stream string
		aggregate    bool
		whole        string // a whole reply body, read by UsageOf instead of a stream
		want         *protocol.ReportedCost
	}{
		{
			name: "streamed", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0.000123,"is_byok":false`)) + chatDone,
			want: &protocol.ReportedCost{Amount: exact("0.000123")},
		},
		{
			name: "streamed to a client that asked for no stream", aggregate: true,
			stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0.95`)) + chatDone, want: &protocol.ReportedCost{Amount: exact("0.95")},
		},
		{name: "whole", whole: whole(`,"cost":1.5e-5`), want: &protocol.ReportedCost{Amount: exact("0.000015")}},
		{
			name: "explicit zero", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0`)) + chatDone,
			want: &protocol.ReportedCost{},
		},
		{
			name: "more decimals than kept", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0.0000000000000001`)) + chatDone,
			want: &protocol.ReportedCost{Amount: exact("0.000000000000001")},
		},
		{
			name: "byok", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0.05,"is_byok":true`)) + chatDone,
			want: &protocol.ReportedCost{Amount: exact("0.05"), BYOK: true},
		},
		{name: "no cost", stream: chatRole + chatHi + chatStop + chatUsage(usage(``)) + chatDone},
		{name: "null", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":null`)) + chatDone},
		{name: "negative", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":-1`)) + chatDone},
		{name: "a string", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":"0.5"`)) + chatDone},
		{name: "an exponent past any cost", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":1e999999`)) + chatDone},
		{name: "cut before the usage chunk", stream: chatRole + chatHi},
		// Chat's usage chunk is its last report, sent once the answer is over: only [DONE] is lost.
		{
			name: "cut after the usage chunk", stream: chatRole + chatHi + chatStop + chatUsage(usage(`,"cost":0.5`)),
			want: &protocol.ReportedCost{Amount: exact("0.5")},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var got protocol.Usage
			switch {
			case c.whole != "":
				got = protocol.UsageOf([]byte(c.whole))
			case c.aggregate:
				o := options
				o.Aggregate = true
				got = relayWith(t, protocol.Chat, protocol.Chat, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), o, settled).r.Usage()
			default:
				got = relay(t, protocol.Chat, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled).r.Usage()
			}
			if (got.Cost == nil) != (c.want == nil) || got.Cost != nil && (got.Cost.Amount.Cmp(c.want.Amount) != 0 || got.Cost.BYOK != c.want.BYOK) {
				t.Errorf("cost %+v, want %+v", got.Cost, c.want)
			}
		})
	}
}

// TestPrepareProvider: an endpoint's provider object, OpenRouter's max_price, goes on the wire in place of the
// client's, which is a routing parameter and never reaches the upstream.
func TestPrepareProvider(t *testing.T) {
	t.Parallel()
	maxPrice := []byte(`{"max_price":{"prompt":"3","completion":"15"}}`)
	for _, c := range []struct {
		name, body string
		provider   []byte
		want       string // the provider member sent, "" for none
	}{
		{"the endpoint's", `{"model":"a","messages":[{"role":"user","content":"hi"}]}`, maxPrice, string(maxPrice)},
		{
			"in place of the client's", `{"model":"a","provider":{"max_price":{"prompt":100}},"messages":[{"role":"user","content":"hi"}]}`,
			maxPrice, string(maxPrice),
		},
		{"none", `{"model":"a","provider":{"order":["x"]},"messages":[{"role":"user","content":"hi"}]}`, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := must(protocol.Inspect(protocol.Chat, []byte(c.body)))
			u, err := protocol.Prepare(&r, []byte(c.body), protocol.Target{Format: protocol.Chat, Model: "up", Streams: true, Provider: c.provider}, rewrites())
			if err != nil {
				t.Fatal(err)
			}
			var sent map[string]jsonx.Raw
			if err := jsonx.Unmarshal(u.Body, &sent); err != nil {
				t.Fatalf("%s: %v", u.Body, err)
			}
			if got := string(sent["provider"]); got != c.want {
				t.Errorf("provider %q, want %q in %s", got, c.want, u.Body)
			}
		})
	}
}

func exact(s string) platform.Exact { return must(platform.ParseExact(s)) }
