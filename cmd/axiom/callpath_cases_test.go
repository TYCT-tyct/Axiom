package main

import (
	"cmp"
	"encoding/json/v2" //nolint:depguard // decodes the node's own responses; jsonx guards input from outside
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The cases of TestANodeServesModelCalls that change what the supplier says or what the client asks for, beside the
// plain Messages and Responses ones: Chat, the priority tier and web searches with a limit.

const (
	// searchMessagesBlocks are the blocks of a Messages reply in which the model searched the web once, before it
	// answered, as Anthropic streams them; the recorded text block follows them as the third.
	searchMessagesBlocks = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"axiom\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.com","title":"Example","encrypted_content":"x","page_age":null}]}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

`
	// searchResponsesItem is the item of a Responses reply for a web search, which the model made before it answered.
	searchResponsesItem = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"},"sequence_number":2}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"axiom"}},"sequence_number":3}

`
	// contextLength is the window of the served models: each sampling a search may add rereads all of it.
	contextLength = 200_000
)

// edit applies the replacements to the recorded stream, which each must find: a sample that changed is no sample to
// derive from.
func edit(t *testing.T, stream string, replacements ...string) string {
	t.Helper()
	for i := 0; i+1 < len(replacements); i += 2 {
		if !strings.Contains(stream, replacements[i]) {
			t.Fatalf("the recorded stream has no %q to replace", replacements[i])
		}
		stream = strings.ReplaceAll(stream, replacements[i], replacements[i+1])
	}
	return stream
}

// answerTiered makes the path reply with its recorded stream as a relay sends it when the call was served at the tier,
// which Messages reports in the usage of its first event and Responses on every response object.
func (f *fakeSupplier) answerTiered(t *testing.T, path, tier string) {
	t.Helper()
	if path == "/v1/responses" {
		f.answer(path, edit(t, f.streams[path], `"object":"response",`, `"object":"response","service_tier":"`+tier+`",`))
		return
	}
	f.answer(path, edit(t, f.streams[path], `"usage": {"input_tokens"`, `"usage": {"service_tier": "`+tier+`", "input_tokens"`))
}

// answerSearching makes the path reply with its recorded stream as a relay sends it when the model searched the web
// once before it answered. Messages reports the search in its usage as well as in the blocks; Responses only in the
// items, so a search of it is counted from the events.
func (f *fakeSupplier) answerSearching(t *testing.T, path string) {
	t.Helper()
	if path == "/v1/responses" {
		s := edit(t, f.streams[path], `"output_index":0`, `"output_index":1`, `"output":[{"id":"msg_`,
			`"output":[{"id":"ws_1","type":"web_search_call","status":"completed"},{"id":"msg_`)
		f.answer(path, strings.Replace(s, "event: response.output_item.added", searchResponsesItem+"event: response.output_item.added", 1))
		return
	}
	s := edit(t, f.streams[path], `"index": 0`, `"index": 2`, `"usage": {"output_tokens": 15}`,
		`"usage": {"output_tokens": 15, "server_tool_use": {"web_search_requests": 1}}`)
	f.answer(path, strings.Replace(s, "event: content_block_start", searchMessagesBlocks+"event: content_block_start", 1))
}

// lastRef is the request id the relay gave the supplier's latest call.
func (w *callWorld) lastRef() string { return "relay-" + strconv.Itoa(len(w.supplier.sent())) }

// served fails the test unless the call was answered.
func (w *callWorld) served(r reply, what string) {
	w.t.Helper()
	if r.status != http.StatusOK {
		w.t.Fatalf("%s: status %d: %s", what, r.status, r.body)
	}
}

// reserved is what the call's reservation held, which admission quoted for the dearest way the call could go.
func (w *callWorld) reserved(r reply) int64 {
	w.t.Helper()
	n, err := strconv.ParseInt(w.query("SELECT amount::text FROM billing.reservations WHERE request_id::text = $1", r.header.Get("X-Generation-Id")), 10, 64)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// chatReply is what a Chat client reads of an answer, whole or streamed.
type chatReply struct {
	text, finish, model string
	usage               *chatUsage
}

type chatUsage struct {
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Cost       float64 `json:"cost"`
	Currency   string  `json:"cost_currency"`
}

type chatBody struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// chatReplyOf reads a Chat answer: the one JSON object, or the events of a stream, which must end in [DONE].
func (w *callWorld) chatReplyOf(r reply, streamed bool) chatReply {
	w.t.Helper()
	if !streamed {
		return w.chatCompletion(r)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		w.t.Errorf("the streamed Chat answer is %q, want an event stream", ct)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(r.body)), "data: [DONE]") {
		w.t.Errorf("the streamed Chat answer does not end in [DONE]: %s", r.body)
	}
	var got chatReply
	for line := range strings.Lines(string(r.body)) {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var b chatBody
		if err := json.Unmarshal([]byte(data), &b); err != nil {
			w.t.Fatalf("a Chat chunk is no JSON: %v: %s", err, data)
		}
		got.model = cmp.Or(b.Model, got.model)
		got.usage = cmp.Or(b.Usage, got.usage)
		for _, c := range b.Choices {
			got.text += c.Delta.Content
			got.finish = cmp.Or(deref(c.FinishReason), got.finish)
		}
	}
	return got
}

func (w *callWorld) chatCompletion(r reply) chatReply {
	w.t.Helper()
	var b chatBody
	if err := json.Unmarshal(r.body, &b); err != nil || len(b.Choices) != 1 {
		w.t.Fatalf("the Chat answer is no completion: %v: %s", err, r.body)
	}
	return chatReply{text: b.Choices[0].Message.Content, finish: deref(b.Choices[0].FinishReason), model: b.Model, usage: b.Usage}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// chatCase is a Chat call: to the model, whole or streamed, expected to reach the supplier as a call to its path and to
// be answered with the text.
type chatCase struct {
	name, model, path string
	streamed          bool
	text              string
	booked            booking
}

// checkChat: /v1/chat/completions through the node, converted to the format the model's endpoint speaks: the supplier is
// sent that format under the endpoint's model name, and the client reads a Chat completion, whole or streamed, whose
// usage carries the amount the ledger charged.
func (w *callWorld) checkChat(n runningNode) {
	w.t.Helper()
	claude := booking{charge: messagesCharge, cost: messagesCost, upstream: "up-claude", input: 25, output: 15}
	gpt := booking{charge: responsesCharge, cost: responsesCost, upstream: "up-gpt", input: 37, output: 11}
	for _, tc := range []chatCase{
		{"to Messages", "claude-opus-5-5", "/v1/messages", false, "Hello!", claude},
		{"to Responses", "gpt-6-astra", "/v1/responses", false, "Hi there! How can I assist you today?", gpt},
		{"streamed to Messages", "claude-opus-5-5", "/v1/messages", true, "Hello!", claude},
		{"streamed to Responses", "gpt-6-astra", "/v1/responses", true, "Hi there! How can I assist you today?", gpt},
	} {
		w.checkChatCall(n, tc)
	}
}

func (w *callWorld) checkChatCall(n runningNode, tc chatCase) {
	t := w.t
	t.Helper()
	before := len(w.supplier.sent())
	body := `{"model":"` + tc.model + `","messages":[{"role":"developer","content":"You are a helpful assistant."},{"role":"user","content":"Hello!"}]`
	if tc.streamed {
		body += `,"stream":true`
	}
	r := w.call(n, "/v1/chat/completions", w.richKey, body+"}")
	w.served(r, "Chat "+tc.name)
	sent := w.supplier.sent()
	if len(sent) != before+1 || sent[before].path != tc.path || sent[before].auth != "Bearer "+supplierKey ||
		!strings.Contains(sent[before].body, `"`+tc.booked.upstream+`"`) {
		t.Errorf("Chat %s: the supplier was sent %+v, want one %s call under its own key and the endpoint's model name", tc.name, sent[before:], tc.path)
	}
	got := w.chatReplyOf(r, tc.streamed)
	if got.text != tc.text || got.finish != "stop" || got.model != tc.model {
		t.Errorf("Chat %s: the answer is %+v, want %q ending the turn, from the model asked for", tc.name, got, tc.text)
	}
	w.checkChatUsage(got.usage, tc)
	tc.booked.ref = w.lastRef()
	w.checkBooked(r, tc.booked)
}

// checkChatUsage: the usage a Chat client reads is the call's, with the amount the ledger charged as its cost.
func (w *callWorld) checkChatUsage(u *chatUsage, tc chatCase) {
	w.t.Helper()
	if u == nil || u.Prompt != tc.booked.input || u.Completion != tc.booked.output ||
		u.Cost != float64(tc.booked.charge)/1e9 || u.Currency != "USD" {
		w.t.Errorf("Chat %s: the usage is %+v, want %d and %d tokens and the %d billionths the ledger charged", tc.name, u, tc.booked.input, tc.booked.output, tc.booked.charge)
	}
}

// checkPriority: the upstream says it served the call at the priority tier. The call is charged at the priority sale
// price and costs what the supplier's priority tier says, both of which the supplier's row and the call log record as
// priority. A call that may be served at priority is reserved for it: admission quotes the dearest tier the request
// allows (twice the standard one here), and a request that allows the standard tier only is quoted at that.
func (w *callWorld) checkPriority(n runningNode) {
	t := w.t
	t.Helper()
	for _, tc := range []struct {
		name, path, call, standardOnly, agent string
		priorityCharge, priorityCost          int64
		inputPrice                            int64 // what a token of input is quoted at, standard tier, in billionths
		standard                              booking
	}{
		{
			"Messages", "/v1/messages", messagesCall, strings.Replace(messagesCall, `"max_tokens"`, `"service_tier":"standard_only","max_tokens"`, 1),
			"claude-cli/2.0.5 (external, cli)", 25*9_000 + 15*45_000, 25*20 + 15*80, 4_500,
			booking{charge: messagesCharge, cost: messagesCost, upstream: "up-claude", input: 25, output: 15, app: "claude-code"},
		},
		{
			"Responses", "/v1/responses", responsesCall, strings.Replace(responsesCall, `"input"`, `"service_tier":"default","input"`, 1),
			"codex_cli_rs/0.50.0", 37*9_000 + 11*45_000, 37*20 + 11*80, 5_625, // an OpenAI input may be billed as a cache write
			booking{charge: responsesCharge, cost: responsesCost, upstream: "up-gpt", input: 37, output: 11, app: "codex"},
		},
	} {
		priority := tc.standard
		priority.charge, priority.cost, priority.tier = tc.priorityCharge, tc.priorityCost, "priority"

		w.supplier.answerTiered(t, tc.path, "priority")
		r := w.call(n, tc.path, w.richKey, tc.call, "User-Agent", tc.agent)
		w.supplier.answer(tc.path, "")
		w.served(r, tc.name+" served at priority")
		priority.ref = w.lastRef()
		w.checkBooked(r, priority)

		restricted := w.call(n, tc.path, w.richKey, tc.standardOnly, "User-Agent", tc.agent)
		w.served(restricted, tc.name+" restricted to the standard tier")
		tc.standard.ref = w.lastRef()
		w.checkBooked(restricted, tc.standard)
		// A quote counts every byte of the request as an input token, the restricted one's tier member included.
		extra := int64(len(tc.standardOnly)-len(tc.call)) * tc.inputPrice
		if open, standard := w.reserved(r), w.reserved(restricted); open != 2*(standard-extra) {
			t.Errorf("%s: a call that may be served at priority was reserved %d, one that may not %d; want twice as much but for the %d the restricted request's extra bytes add, the tier's prices being twice",
				tc.name, open, standard, extra)
		}
	}
}

// checkBoundedSearch: a web search with a limit on its uses is reserved up to the limit, however few of them the model
// makes: each use may be one more sampling that rereads the whole context, and costs the search itself. What the model
// made is what is charged, on top of its tokens. A limit that no balance can cover is refused before the supplier hears
// of it, so the limit is what decides, not what the model would make.
func (w *callWorld) checkBoundedSearch(n runningNode) {
	w.t.Helper()
	for _, tc := range []searchCase{
		{"Messages max_uses", "/v1/messages", "claude-cli/2.0.5 (external, cli)", func(limit int) string {
			return fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"Hello"}],`+
				`"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":%d}]}`, limit)
		}, `"web_search_tool_result"`, 9_000, booking{
			charge: messagesCharge + searchCharge, cost: messagesCost + searchCost, upstream: "up-claude", input: 25, output: 15, searches: 1, app: "claude-code",
		}},
		{"Responses max_tool_calls", "/v1/responses", "codex_cli_rs/0.50.0", func(limit int) string {
			return fmt.Sprintf(`{"model":"gpt-6-astra","input":"Hello!","max_tool_calls":%d,"tools":[{"type":"web_search"}]}`, limit)
		}, `"web_search_call"`, 11_250, booking{ // an OpenAI input may be billed as a cache write
			charge: responsesCharge + searchCharge, cost: responsesCost + searchCost, upstream: "up-gpt", input: 37, output: 11, searches: 1, app: "codex",
			estimated: true, // Responses does not report the searches in its usage: they are counted from the events
		}},
	} {
		w.checkSearchLimits(n, tc)
	}
}

type searchCase struct {
	name, path, agent string
	call              func(limit int) string
	blocks            string // what the client reads of the search
	reread            int64  // what a token of input is quoted at, priority tier, in billionths
	booked            booking
}

// checkSearchLimits serves the case with limits of one and three, and asks for ten.
func (w *callWorld) checkSearchLimits(n runningNode, tc searchCase) {
	t := w.t
	t.Helper()
	w.supplier.answerSearching(t, tc.path)
	defer w.supplier.answer(tc.path, "")
	reserved := map[int]int64{}
	for _, limit := range []int{1, 3} {
		r := w.call(n, tc.path, w.richKey, tc.call(limit), "User-Agent", tc.agent)
		w.served(r, fmt.Sprintf("%s: %d", tc.name, limit))
		if !strings.Contains(string(r.body), tc.blocks) {
			t.Errorf("%s: the answer does not show the search (%s): %s", tc.name, tc.blocks, r.body)
		}
		tc.booked.ref = w.lastRef()
		w.checkBooked(r, tc.booked)
		reserved[limit] = w.reserved(r)
	}
	if got, want := reserved[3]-reserved[1], 2*(contextLength*tc.reread+searchCharge); got != want {
		t.Errorf("%s: three searches were reserved %d more than one, want %d: each further one may reread the window and costs its fee", tc.name, got, want)
	}

	transactions, called := w.query("SELECT count(*)::text FROM billing.transactions"), len(w.supplier.sent())
	r := w.call(n, tc.path, w.richKey, tc.call(10))
	if r.status != http.StatusPaymentRequired || !strings.Contains(string(r.body), "credits_insufficient") {
		t.Errorf("%s: a limit of ten searches got %d %s, want 402 credits_insufficient: the account cannot cover the limit", tc.name, r.status, r.body)
	}
	if got := w.query("SELECT count(*)::text FROM billing.transactions"); got != transactions || len(w.supplier.sent()) != called {
		t.Errorf("%s: a refused search moved the ledger (%s transactions, then %s) or reached the supplier", tc.name, transactions, got)
	}
	w.checkBalanced()
}

// checkDialect: what the catalog spec says of how the models are written reaches the supplier in the request it is
// sent. The Claude model's spec says it thinks with a budget, so a Chat call that asks for reasoning is not sent the
// adaptive form; the GPT model's spec says it returns encrypted reasoning, so a Responses call, which the gateway sends
// with store false, asks for it.
func (w *callWorld) checkDialect(n runningNode) {
	t := w.t
	t.Helper()
	before := len(w.supplier.sent())
	w.served(w.call(n, "/v1/chat/completions", w.richKey,
		`{"model":"claude-opus-5-5","max_tokens":10000,"reasoning":{"effort":"high"},"messages":[{"role":"user","content":"Hello!"}]}`), "Chat reasoning to Messages")
	w.served(w.call(n, "/v1/responses", w.richKey, responsesCall), "Responses")
	sent := w.supplier.sent()
	if len(sent) != before+2 {
		t.Fatalf("the supplier was sent %d calls, want 2", len(sent)-before)
	}
	if body := sent[before].body; !strings.Contains(body, `"thinking":{"type":"enabled","budget_tokens":8000}`) || strings.Contains(body, "adaptive") {
		t.Errorf("the Messages model, whose spec says it thinks with a budget, was sent %s", body)
	}
	if body := sent[before+1].body; !strings.Contains(body, `"store":false`) || !strings.Contains(body, `"include":["reasoning.encrypted_content"]`) {
		t.Errorf("the Responses model, whose spec says it returns encrypted reasoning, was sent %s, want store false and the include member", body)
	}
}
