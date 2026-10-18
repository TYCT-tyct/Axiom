package main

import (
	"encoding/json/v2" //nolint:depguard // decodes the node's own responses; jsonx guards input from outside
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
)

const (
	// messagesCall is what Claude Code sends: tools, and a reply wanted whole here.
	messagesCall = `{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"Hello"}],` +
		`"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{}}}]}`
	// responsesCall is what Codex sends.
	responsesCall = `{"model":"gpt-6-astra","instructions":"You are a helpful assistant.","input":"Hello!",` +
		`"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{}}}]}`
)

// TestANodeServesModelCalls: a node started on a database that holds a site, a supplier and models answers the model
// API on its public listener and books the call, from the HTTP request to the ledger, the supplier's cost row and the
// call log. One node serves the whole story, since it is the story: not ready until something can be sold, then a Messages
// call and a Responses call that are charged and recorded, one whose reply is free, one refused for credit and one for
// a model nothing serves, and a second node that leaves the first one's holder alone.
func TestANodeServesModelCalls(t *testing.T) {
	w := newCallWorld(t)
	n := w.start()

	// Models and endpoints exist but no rule sells them: every call would answer 404, so the node says it is not ready.
	// The rule arrives, and the next rebuild of the snapshot makes it ready.
	if n.ready(t) {
		t.Fatal("the node is ready though no endpoint has a sale price")
	}
	w.price()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })

	w.checkMessages(n)
	w.checkResponses(n)
	w.checkFreeReply(n)
	w.checkRefusedForCredit(n)
	w.checkUnservedModel(n)
	w.checkSecondNode(n)

	if err := n.stop(); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	if got := w.query("SELECT count(*)::text FROM billing.reservations WHERE state = 'pending'"); got != "0" {
		t.Errorf("%s reservations pending after the calls and the shutdown", got)
	}
}

// checkMessages: Messages through /v1, with the tools Claude Code sends and a compressing client: the answer is the
// recorded sample's, not encoded, booked, and the supplier was sent the endpoint's model name under its own key.
func (w *callWorld) checkMessages(n runningNode) {
	t := w.t
	t.Helper()
	r := w.call(n, "/v1/messages", w.richKey, messagesCall, "Accept-Encoding", "gzip")
	if r.status != http.StatusOK {
		t.Fatalf("Messages: status %d: %s", r.status, r.body)
	}
	if enc := r.header.Get("Content-Encoding"); enc != "" {
		t.Errorf("the answer is encoded %q: a compressor on the model API would hold streams back", enc)
	}
	var message struct {
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(r.body, &message); err != nil {
		t.Fatalf("the Messages answer is no JSON: %v: %s", err, r.body)
	}
	if message.Role != "assistant" || message.StopReason != "end_turn" || len(message.Content) != 1 || message.Content[0].Text != "Hello!" {
		t.Errorf("the Messages answer is %+v, want the recorded sample's Hello! ending the turn", message)
	}
	w.checkBooked(r, booking{charge: messagesCharge, ref: "relay-1", upstream: "up-claude", input: 25, output: 15})
	w.checkSent()
}

// checkSent: the supplier was sent the one Messages call, under its own key and with the endpoint's model name, and
// nothing of the client's key.
func (w *callWorld) checkSent() {
	w.t.Helper()
	sent := w.supplier.sent()
	if len(sent) != 1 || sent[0].path != "/v1/messages" || sent[0].auth != "Bearer "+supplierKey ||
		!strings.Contains(sent[0].body, `"up-claude"`) || strings.Contains(sent[0].body, w.richKey) {
		w.t.Errorf("the supplier was sent %+v, want one Messages call under the supplier's own key and the endpoint's model name", sent)
	}
}

// checkResponses: Responses through /api/v1, with a function tool, as Codex sends.
func (w *callWorld) checkResponses(n runningNode) {
	t := w.t
	t.Helper()
	r := w.call(n, "/api/v1/responses", w.richKey, responsesCall)
	if r.status != http.StatusOK {
		t.Fatalf("Responses: status %d: %s", r.status, r.body)
	}
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(r.body, &response); err != nil {
		t.Fatalf("the Responses answer is no JSON: %v: %s", err, r.body)
	}
	if response.Status != "completed" || len(response.Output) != 1 || len(response.Output[0].Content) != 1 ||
		response.Output[0].Content[0].Text != "Hi there! How can I assist you today?" {
		t.Errorf("the Responses answer is %+v, want the recorded sample's text, completed", response)
	}
	w.checkBooked(r, booking{charge: responsesCharge, ref: "relay-2", upstream: "up-gpt", input: 37, output: 11})
}

// checkFreeReply: a reply that reports no output and never ends is free, and no waiver is counted.
func (w *callWorld) checkFreeReply(n runningNode) {
	t := w.t
	t.Helper()
	w.supplier.setZero(true)
	r := w.call(n, "/v1/messages", w.richKey, strings.Replace(messagesCall, `"max_tokens"`, `"stream":true,"max_tokens"`, 1))
	if r.status != http.StatusOK {
		t.Fatalf("the empty reply: status %d: %s", r.status, r.body)
	}
	id := r.header.Get("X-Generation-Id")
	waitUntil(t, 5*time.Second, func() bool {
		return w.query("SELECT state FROM billing.reservations WHERE request_id::text = $1", id) == "settled"
	})
	if got := w.query("SELECT charged::text || ' ' || waived::text FROM billing.reservations WHERE request_id::text = $1", id); got != "0 true" {
		t.Errorf("the call was charged and waived as %q, want nothing charged and waived: its output was reported zero", got)
	}
	if got := w.query("SELECT count(*)::text FROM billing.waivers"); got != "0" {
		t.Errorf("billing.waivers holds %s rows, want none: no cap counts a waiver", got)
	}
	var logged string
	waitUntil(t, 5*time.Second, func() bool {
		logged = w.query("SELECT free_of_charge::text || ' ' || price_nano::text || ' ' || finish_reason FROM observability.call_log WHERE id::text = $1", id)
		return logged != ""
	})
	t.Logf("the free call is logged as %q", logged)
	if !strings.HasPrefix(logged, "true 0 ") {
		t.Errorf("the call log holds %q for the free call, want it free and charged nothing", logged)
	}
	w.checkBalanced()
}

// checkRefusedForCredit: an account with nothing to spend is refused before the supplier hears of it, and the ledger
// does not move.
func (w *callWorld) checkRefusedForCredit(n runningNode) {
	t := w.t
	t.Helper()
	transactions, called := w.query("SELECT count(*)::text FROM billing.transactions"), len(w.supplier.sent())
	r := w.call(n, "/v1/messages", w.noKey, messagesCall)
	if r.status != http.StatusPaymentRequired || !strings.Contains(string(r.body), "credits_insufficient") {
		t.Errorf("an account without credit got %d %s, want 402 credits_insufficient", r.status, r.body)
	}
	if got := w.query("SELECT count(*)::text FROM billing.transactions"); got != transactions || len(w.supplier.sent()) != called {
		t.Errorf("a refused call moved the ledger (%s transactions, then %s) or reached the supplier", transactions, got)
	}
	w.checkBalanced()
}

// checkUnservedModel: a model that is listed and priced and has no endpoint.
func (w *callWorld) checkUnservedModel(n runningNode) {
	t := w.t
	t.Helper()
	r := w.call(n, "/v1/messages", w.richKey, strings.Replace(messagesCall, "claude-opus-5-5", "unserved", 1))
	if r.status != http.StatusNotFound || !strings.Contains(string(r.body), "no_endpoint") {
		t.Errorf("a model nothing serves got %d %s, want 404 no_endpoint", r.status, r.body)
	}
}

// checkSecondNode: a second node on the same database. Its first sweep reaps holders that expired, not the first
// node's, which keeps answering.
func (w *callWorld) checkSecondNode(n runningNode) {
	t := w.t
	t.Helper()
	second := w.start()
	if got := w.query("SELECT count(*)::text FROM billing.holders WHERE reaped_at IS NULL"); got != "2" {
		t.Errorf("%s holders alive, want one for each node: the second node's start reaped the first's", got)
	}
	if r := w.call(n, "/v1/responses", w.richKey, responsesCall); r.status != http.StatusOK {
		t.Errorf("the first node answers %d %s after the second started", r.status, r.body)
	}
	if err := second.stop(); err != nil {
		t.Errorf("the second node's shutdown: %v", err)
	}
}

// booking is what a settled call is expected to have left: the ledger's charge, the relay's request id on the supplier's
// cost row with the endpoint that was called, and the usage the call log records.
type booking struct {
	charge        int64
	ref, upstream string
	input, output int
}

// checkBooked: the call has an id, its reservation is settled for the charge, the books balance, the supplier's side
// has one cost row with the relay's request id and the endpoint, and the call log has the call with the same usage and
// amounts.
func (w *callWorld) checkBooked(r reply, want booking) {
	w.t.Helper()
	id := r.header.Get("X-Generation-Id")
	if id == "" {
		w.t.Fatal("the answer has no X-Generation-Id")
	}
	if got := w.query("SELECT state || ' ' || charged FROM billing.reservations WHERE request_id::text = $1", id); got != "settled "+strconv.FormatInt(want.charge, 10) {
		w.t.Errorf("the reservation is %q, want settled for %d", got, want.charge)
	}
	w.checkBalanced()
	if got := w.query(`SELECT count(*)::text || ' ' || coalesce(max(c.charge_ref), '') || ' ' || coalesce(max(e.upstream_name), '')
		FROM suppliers.attempt_costs c JOIN suppliers.endpoints e ON e.id = c.endpoint_id WHERE c.request_id::text = $1`, id); got != "1 "+want.ref+" "+want.upstream {
		w.t.Errorf("the supplier's rows are %q, want one with charge ref %q on the endpoint %q", got, want.ref, want.upstream)
	}
	var logged string
	waitUntil(w.t, 5*time.Second, func() bool {
		logged = w.query(`SELECT coalesce(count(*)::text || ' ' || max(price_nano)::text || ' ' || bool_or(free_of_charge)::text || ' ' ||
			max(usage->>'input') || ' ' || max(usage->>'output'), '') FROM observability.call_log WHERE id::text = $1`, id)
		return logged != ""
	})
	if wanted := "1 " + strconv.FormatInt(want.charge, 10) + " false " + strconv.Itoa(want.input) + " " + strconv.Itoa(want.output); logged != wanted {
		w.t.Errorf("the call log holds %q, want %q: one record with the ledger's charge and the usage the relay reported", logged, wanted)
	}
}

// checkBalanced: every ledger transaction sums to zero, no balance differs from its book, and no reservation holds
// money it is not owed.
func (w *callWorld) checkBalanced() {
	w.t.Helper()
	if got := w.query("SELECT count(*)::text FROM (SELECT 1 FROM billing.entries GROUP BY transaction_id HAVING sum(amount) <> 0) t"); got != "0" {
		w.t.Errorf("%s ledger transactions do not balance", got)
	}
	if got := w.query(`SELECT count(*)::text FROM billing.balances b WHERE b.balance <>
		-coalesce((SELECT sum(e.amount) FROM billing.entries e WHERE e.account_id = b.account_id AND e.book = 'user_balance'), 0)`); got != "0" {
		w.t.Errorf("%s balances differ from the ledger", got)
	}
	if got := w.query(`SELECT count(*)::text FROM billing.balances b WHERE b.reserved <>
		coalesce((SELECT sum(r.amount) FROM billing.reservations r WHERE r.account_id = b.account_id AND r.state = 'pending'), 0)`); got != "0" {
		w.t.Errorf("%s balances reserve more or less than their pending reservations", got)
	}
}

// TestModelCallsAreMountedBesideTheConsole: /v1 and /api/v1 answer from the gateway, not from the console's /api/ and
// not through its compressor, which gzips the large JSON answers it is given; everything else still reaches the
// console. A large answer is what tells the two apart: the compressor leaves small ones alone.
func TestModelCallsAreMountedBesideTheConsole(t *testing.T) {
	big := []byte(`{"text":"` + strings.Repeat("x", 4096) + `"}`)
	answering := func(who string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Answered-By", who)
			if _, err := w.Write(big); err != nil {
				t.Errorf("%s could not answer: %v", who, err)
			}
		})
	}
	console := http.NewServeMux()
	console.Handle("/api/", httpserver.Compress(answering("console")))
	routes := withModelCalls(console, answering("gateway"))
	for path, want := range map[string]string{
		"/v1/messages": "gateway", "/api/v1/responses": "gateway", "/api/console/v1/version": "console",
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		encoded := rec.Header().Get("Content-Encoding") != ""
		if got := rec.Header().Get("X-Answered-By"); got != want || encoded != (want == "console") {
			t.Errorf("%s was answered by %q, encoded %v; want %q, encoded %v", path, got, encoded, want, want == "console")
		}
	}
}
