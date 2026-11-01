package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// TestServerLoopReplaysAssistantText: the model's prose and reasoning before a tool call are part of the assistant
// turn. The next hop must see them, not just the call and its result.
func TestServerLoopReplaysAssistantText(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatToolCallWithText}), serve(beat{data: chatAnswer}))
		h.run()
		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		second := string(h.up.endpoint(1).calls[1].Body)
		for _, want := range []string{`"content":"I will search."`, `"reasoning_content":"Need current data."`, `"role":"tool"`} {
			if !strings.Contains(second, want) {
				t.Fatalf("the next hop lost %s: %s", want, second)
			}
		}
	})
}

const chatMixedCalls = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"openrouter_web_search","arguments":"{\"query\":\"axiom\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"client_tool","arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"up-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

// TestServerLoopRefusesMixedCalls: a hop that calls both the gateway's search and a client function cannot be replayed
// by the loop without either dropping the client's call or inventing its result. Refusing is explicit and costs no
// search.
func TestServerLoopRefusesMixedCalls(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com"}}}
		h := serverHarness(t, ctx, searcher, serve(beat{data: chatMixedCalls}))
		h.run()
		if h.w.status != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s, want 502", h.w.status, h.w.body())
		}
		if len(h.up.endpoint(1).calls) != 1 || len(searcher.queries) != 0 {
			t.Fatalf("calls = %d, searches = %d, want one hop and no search", len(h.up.endpoint(1).calls), len(searcher.queries))
		}
	})
}

const messagesToolWithThinking = `event: message_start
data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"up-1","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Need current data."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-thinking"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"openrouter_web_search","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"axiom\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

const messagesAnswer = `event: message_start
data: {"type":"message_start","message":{"id":"m2","type":"message","role":"assistant","model":"up-1","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Axiom is a gateway."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

// TestServerLoopReplaysMessagesThinking: Anthropic signs a thinking block; the loop must carry the block and its
// signature into the next hop, not replace the assistant turn with a bare tool call.
func TestServerLoopReplaysMessagesThinking(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		request := `{"model":"acme/alpha","stream":true,"max_tokens":1000,"messages":[{"role":"user","content":"what is axiom"}],` +
			`"tools":[{"type":"openrouter:web_search","parameters":{"engine":"exa","max_results":2}}]}`
		upstream := protocol.Messages
		h := newHarness(t, ctx, options{
			client: protocol.Messages, upstream: &upstream, request: []byte(request),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{1: {serve(beat{data: messagesToolWithThinking}), serve(beat{data: messagesAnswer})}},
		})
		h.run()
		if !h.call.serverLoop {
			t.Fatal("the harness call did not enable the loop")
		}
		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		if len(h.up.endpoint(1).calls) < 2 {
			t.Fatalf("the upstream got %d calls, body = %s", len(h.up.endpoint(1).calls), h.w.body())
		}
		second := string(h.up.endpoint(1).calls[1].Body)
		for _, want := range []string{`"type":"thinking"`, `"thinking":"Need current data."`, `"signature":"signed-thinking"`, `"type":"tool_result"`} {
			if !strings.Contains(second, want) {
				t.Fatalf("the next Messages hop lost %s: %s", want, second)
			}
		}
	})
}

// TestServerLoopBrokenContinuationDoesNotFallBack covers the other failure exit: a stream that ends without a usable
// end event after a search. It is the same 502 and the same no-fallback rule as an empty 200.
func TestServerLoopBrokenContinuationDoesNotFallBack(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		upstream := protocol.Chat
		h := newHarness(t, ctx, options{
			client: protocol.Chat, upstream: &upstream, entries: 2, request: []byte(searchTool),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n", hangup: true})}, 2: {serve(beat{data: chatAnswer})}},
		})
		h.run()
		if h.w.status != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s, want 502", h.w.status, h.w.body())
		}
		if n := len(h.up.endpoint(2).calls); n != 0 {
			t.Fatalf("the fallback entry got %d calls after a broken continuation", n)
		}
	})
}

// TestServerLoopReleasesTheOldSlotBeforeTheNextHop: an endpoint with one credential and capacity one must let its own
// finished request go before the next hop asks for that same slot.
func TestServerLoopReleasesTheOldSlotBeforeTheNextHop(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		searcher := &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}}
		upstream := protocol.Chat
		h := newHarness(t, ctx, options{
			client: protocol.Chat, upstream: &upstream, request: []byte(searchTool),
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			search: searcher, server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
			script: map[int][]scripted{1: {serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer})}},
		})
		h.up.beforeAcquire = func(id suppliers.EndpointID) {
			e := h.up.endpoint(1)
			e.noSlot = e.acquired > 0 && e.done == 0
		}
		h.run()
		if h.w.status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
		}
		if h.up.endpoint(1).acquired != 2 || h.up.endpoint(1).done != 2 {
			t.Fatalf("acquired/done = %d/%d, want 2/2", h.up.endpoint(1).acquired, h.up.endpoint(1).done)
		}
		if len(h.settlements) != 1 {
			t.Fatalf("settlements = %d, want 1", len(h.settlements))
		}
		hops := h.settlements[0].outcome.Attempts[0].Hops
		if len(hops) != 2 || len(hops[0].Segments) != 1 || len(hops[1].Segments) != 1 {
			t.Fatalf("settled hops = %+v, want one segment and receipt per supplier request", hops)
		}
	})
}
