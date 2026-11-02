package protocol_test

import (
	"regexp"
	"strings"
	"testing"

	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// TestCachedReplayZeroesUsage: a relay told the upstream bytes are a cache replay reports every usage count as zero,
// in the formats the cache stores, while keeping the answer itself.
//
//nolint:gocognit // three formats and their stream/aggregate replays are one table
func TestCachedReplayZeroesUsage(t *testing.T) {
	t.Parallel()
	nonzero := regexp.MustCompile(`"(input_tokens|output_tokens|prompt_tokens|completion_tokens|total_tokens)":\s*[1-9]`)
	chat := strings.Join([]string{
		`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`data: [DONE]`, "", "",
	}, "\n\n")
	messages := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`, "", "",
	}, "\n\n")
	for _, tc := range []struct {
		name string
		f    protocol.Format
		body string
	}{
		{"chat", protocol.Chat, chat},
		{"messages", protocol.Messages, messages},
		{"responses", protocol.Responses, string(sample(t, "responses/text/upstream.sse"))},
	} {
		for _, aggregate := range []bool{false, true} {
			name := tc.name
			if aggregate {
				name += "/aggregate"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := protocol.NewRelay(tc.f, tc.f, strings.NewReader(tc.body), protocol.UsageContext{}, protocol.RelayOptions{
					ID: "gen", Model: "m", Memory: protocol.NewMemory(1 << 20), Cached: true, Aggregate: aggregate,
				})
				var out []byte
				for {
					st, err := r.Next()
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, st.Out...)
					if st.Kind == protocol.StepUpstreamDone {
						out = append(out, r.Finish(protocol.Final{})...)
						break
					}
				}
				if len(out) == 0 || !strings.Contains(string(out), "Hi") {
					t.Fatalf("the answer was not relayed:\n%s", out)
				}
				if m := nonzero.FindString(string(out)); m != "" {
					t.Errorf("a cache replay reports %s:\n%s", m, out)
				}
			})
		}
	}
}

// TestACachedReplayUsesThisCallsTimestampAndCurrency: a hit is a new call, not the generation it was stored from. Chat
// and Responses name this call's start, in both streams and aggregates, and every format reports the site's currency
// with its zero cost.
//
//nolint:gocognit // the table runs every format through both stream and aggregate
func TestACachedReplayUsesThisCallsTimestampAndCurrency(t *testing.T) {
	t.Parallel()
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	chat := strings.Join([]string{
		`data: {"id":"c","object":"chat.completion.chunk","created":1741290958,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: {"id":"c","object":"chat.completion.chunk","created":1741290958,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`data: [DONE]`, "", "",
	}, "\n\n")
	for _, tc := range []struct {
		name string
		f    protocol.Format
		body string
		time string
	}{
		{"chat", protocol.Chat, chat, `"created":1760000000`},
		{"responses", protocol.Responses, string(sample(t, "responses/text/upstream.sse")), `"created_at":1760000000`},
		{"messages", protocol.Messages, string(sample(t, "messages/text/upstream.sse")), ""},
	} {
		for _, aggregate := range []bool{false, true} {
			name := tc.name
			if aggregate {
				name += "/aggregate"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := protocol.NewRelay(tc.f, tc.f, strings.NewReader(tc.body), protocol.UsageContext{}, protocol.RelayOptions{
					ID: "gen", Model: "m", Created: 1760000000, Memory: protocol.NewMemory(1 << 20), Cached: true, Aggregate: aggregate,
				})
				var out []byte
				for {
					st, err := r.Next()
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, st.Out...)
					if st.Kind == protocol.StepUpstreamDone {
						out = append(out, r.Finish(protocol.Final{Currency: usd})...)
						break
					}
				}
				if tc.time != "" && !strings.Contains(string(out), tc.time) {
					t.Errorf("a cached %s replay does not name this call's time:\n%s", tc.name, out)
				}
				if strings.Contains(string(out), "1741290958") {
					t.Errorf("a cached %s replay kept the stored generation's time:\n%s", tc.name, out)
				}
				if !strings.Contains(string(out), `"cost_currency":"USD"`) {
					t.Errorf("a cached %s replay has no currency:\n%s", tc.name, out)
				}
			})
		}
	}
}
