package main

import (
	"encoding/json/v2" //nolint:depguard // decodes the node's own responses; jsonx guards input from outside
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pgtest"
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
// call and a Responses call that are charged and recorded, Chat calls converted to both, calls the upstream served at
// the priority tier, web searches reserved up to their limit, one whose reply is free, one refused for credit and one for
// a model nothing serves, and a second node that leaves the first one's holder alone.
func TestANodeServesModelCalls(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	n := w.start()

	// Models and endpoints exist but no rule sells them: every call would answer 404, so the node says it is not ready.
	// The rule arrives, and the next rebuild of the snapshot makes it ready.
	if n.ready(t) {
		t.Fatal("the node is ready though no endpoint has a sale price")
	}
	w.price()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })

	// Both reconciliations run on the node's queue at startup, without waiting for their next interval.
	waitUntil(t, 15*time.Second, func() bool {
		return w.query(`SELECT count(DISTINCT kind)::text FROM river.river_job
			WHERE kind IN ('billing.reconcile', 'suppliers.reconcile') AND state = 'completed'`) == "2"
	})

	w.checkMessages(n)
	w.checkResponses(n)
	w.checkChat(n)
	w.checkPriority(n)
	w.checkBoundedSearch(n)
	w.checkToolWithoutLimit(n)
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
	r := w.call(n, "/v1/messages", w.richKey, messagesCall, "Accept-Encoding", "gzip", "User-Agent", "claude-cli/2.0.5 (external, cli)")
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
	w.checkBooked(r, booking{charge: messagesCharge, cost: messagesCost, ref: "relay-1", upstream: "up-claude", input: 25, output: 15, app: "claude-code"})
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
	r := w.call(n, "/api/v1/responses", w.richKey, responsesCall, "User-Agent", "codex_cli_rs/0.50.0")
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
	w.checkBooked(r, booking{charge: responsesCharge, cost: responsesCost, ref: "relay-2", upstream: "up-gpt", input: 37, output: 11, app: "codex"})
}

// checkToolWithoutLimit: a web search with no limit on its uses, on a model that has no default maximum, is refused
// as an invalid request before the supplier hears of it, and the ledger does not move; the same search with a limit is
// served.
func (w *callWorld) checkToolWithoutLimit(n runningNode) {
	t := w.t
	t.Helper()
	transactions, called := w.query("SELECT count(*)::text FROM billing.transactions"), len(w.supplier.sent())
	search := `{"model":"gpt-6-astra","input":"Hello!","tools":[{"type":"web_search"}]}`
	r := w.call(n, "/v1/responses", w.richKey, search)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "invalid_request") || !strings.Contains(string(r.body), "max_tool_calls") {
		t.Errorf("a search with no limit got %d %s, want 400 invalid_request naming max_tool_calls", r.status, r.body)
	}
	if got := w.query("SELECT count(*)::text FROM billing.transactions"); got != transactions || len(w.supplier.sent()) != called {
		t.Errorf("a refused search moved the ledger (%s transactions, then %s) or reached the supplier", transactions, got)
	}
	r = w.call(n, "/v1/responses", w.richKey, strings.Replace(search, `"tools"`, `"max_tool_calls":2,"tools"`, 1))
	if r.status != http.StatusOK {
		t.Errorf("a search with a limit got %d %s, want it served", r.status, r.body)
	}
	w.checkBalanced()
}

// checkFreeReply: a reply that reports no output and never ends is free, and no waiver is counted.
func (w *callWorld) checkFreeReply(n runningNode) {
	t := w.t
	t.Helper()
	w.supplier.answer("/v1/messages", zeroOutput)
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
	var logged string
	waitUntil(t, 5*time.Second, func() bool {
		logged = w.query("SELECT free_of_charge::text || ' ' || price_nano::text || ' ' || finish_reason FROM observability.call_log WHERE id::text = $1", id)
		return logged != ""
	})
	if logged != "true 0 error" { // the stream broke off, which the relay reports as an error finish
		t.Errorf("the call log holds %q for the free call, want it free, charged nothing and ended in error", logged)
	}
	w.checkBalanced()
	w.supplier.answer("/v1/messages", "")
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
	r := w.call(n, "/v1/responses", w.richKey, responsesCall)
	if r.status != http.StatusOK {
		t.Fatalf("the first node answers %d %s after the second started", r.status, r.body)
	}
	w.checkSettled(r, responsesCharge)
	if err := second.stop(); err != nil {
		t.Errorf("the second node's shutdown: %v", err)
	}
}

// booking is what a settled call is expected to have left: the ledger's charge, the relay's request id on the supplier's
// cost row with the endpoint that was called, and the usage the call log records.
type booking struct {
	attempts      int   // defaults to one; the last attempt delivered the recorded usage
	charge, cost  int64 // what the user was charged and what the call cost the supplier, in billionths
	ref, upstream string
	input, output int
	searches      int    // the web searches the call used
	app           string // the client application the call log names
	tier          string // the service tier it was served at; standard when empty
	estimated     bool   // some of the usage was not reported by the upstream, and the supplier's cost with it
}

// checkSettled: the call's reservation is settled for the charge.
func (w *callWorld) checkSettled(r reply, charge int64) {
	w.t.Helper()
	id := r.header.Get("X-Generation-Id")
	if got := w.query("SELECT state || ' ' || charged FROM billing.reservations WHERE request_id::text = $1", id); got != "settled "+strconv.FormatInt(charge, 10) {
		w.t.Errorf("the reservation is %q, want settled for %d", got, charge)
	}
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
	w.checkSettled(r, want.charge)
	w.checkBalanced()
	if want.attempts == 0 {
		want.attempts = 1
	}
	if got := w.query("SELECT count(*)::text FROM suppliers.attempt_costs WHERE request_id::text = $1", id); got != strconv.Itoa(want.attempts) {
		w.t.Errorf("the supplier has %s attempt rows, want %d", got, want.attempts)
	}
	if want.tier == "" {
		want.tier = "standard"
	}
	flag := "complete" // an estimated call is not: the supplier's row says its cost rests on usage nobody reported
	if want.estimated {
		flag = "unpriced"
	}
	if got := w.query(`SELECT count(*)::text || ' ' || coalesce(max(c.charge_ref), '') || ' ' || coalesce(max(e.upstream_name), '') || ' ' ||
		coalesce(max(c.usage->>'tier'), '') || ' ' || coalesce(max(c.cost_flag), '')
		FROM suppliers.attempt_costs c JOIN suppliers.endpoints e ON e.id = c.endpoint_id WHERE c.request_id::text = $1 AND c.attempt_seq = $2`, id, want.attempts); got != "1 "+want.ref+" "+want.upstream+" "+want.tier+" "+flag {
		w.t.Errorf("the supplier's rows are %q, want one with charge ref %q on the endpoint %q, served at %s, its cost %s", got, want.ref, want.upstream, want.tier, flag)
	}
	if got := w.query("SELECT estimated::text FROM billing.reservations WHERE request_id::text = $1", id); got != strconv.FormatBool(want.estimated) {
		w.t.Errorf("the reservation's estimated is %s, want %v", got, want.estimated)
	}
	var logged string
	waitUntil(w.t, 5*time.Second, func() bool {
		logged = w.query(`SELECT coalesce(count(*)::text || ' ' || max(price_nano)::text || ' ' || max(cost_nano)::text || ' ' || bool_or(free_of_charge)::text || ' ' ||
			max(service_tier) || ' ' || max(usage->>'input') || ' ' || max(usage->>'output') || ' ' || coalesce(max(usage->>'web_search'), '0') || ' ' ||
			max(client_app) || ' ' || max(source_class), '') FROM observability.call_log WHERE id::text = $1`, id)
		return logged != ""
	})
	if wanted := fmt.Sprintf("1 %d %d false %s %d %d %d %s relay", want.charge, want.cost, want.tier, want.input, want.output, want.searches, want.app); logged != wanted {
		w.t.Errorf("the call log holds %q, want %q: one record with the ledger's charge, the supplier's cost, the service tier, the usage the relay reported, the client application and the source class", logged, wanted)
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
	t.Parallel()
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

// TestANodeExitsWhenItsHolderIsReaped: once another node has reaped this one's holder, nothing it writes is fenced in
// any more, so its next heartbeat ends the node (with an error that says why) instead of leaving it to answer every
// call with a failure. The process manager starts it again with a holder of its own.
func TestANodeExitsWhenItsHolderIsReaped(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	n := w.start()
	if err := pgtest.Run(t.Context(), w.database.Superuser, "UPDATE billing.holders SET reaped_at = clock_timestamp() WHERE reaped_at IS NULL"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-n.finished:
		if err := n.ended(); err == nil || !strings.Contains(err.Error(), "holder is gone") {
			t.Errorf("the node ended with %v, want it to say its holder is gone", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the node kept running with a reaped holder")
	}
}
