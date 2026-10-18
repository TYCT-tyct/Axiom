package main

import (
	"context"
	"encoding/json/v2" //nolint:depguard // decodes the node's own responses and logs; jsonx guards input from outside
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
)

// A node is started as a deployment starts it, `serve` over a database that `axiom site` and `axiom user` set up, and
// is called over HTTP; the supplier is the only fake, a loopback server that replays the recorded samples of
// internal/protocol/testdata. What is checked is a call from the HTTP request to the ledger and the logs.

const (
	// spec declares everything an endpoint needs of a model, and the tools Claude Code and Codex send.
	servedSpec = `{"items":["input","output","cache_read","cache_write"],"tiers":["standard"],` +
		`"context_length":200000,"max_output_tokens":8192,"streams":true,"abilities":["tools"]}`
	// referencePrice is a dollar a million tokens of input and five of output, as the catalog publishes it.
	servedReference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375"}}`
	// servedRule sells every model at one and a half times its reference price.
	servedRule = `{"kind":"reference_multiplier","multiplier":"1.5","currency_conversion":"face"}`
	// The sale prices of the recorded samples: 25 input and 15 output tokens of the Messages one, 37 and 11 of the
	// Responses one, at 4.5 and 22.5 millionths of a dollar a token, in billionths.
	messagesCharge  = 25*4_500 + 15*22_500
	responsesCharge = 37*4_500 + 11*22_500
	supplierToken   = "sk-supplier-secret"
)

// fakeSupplier is a new-api relay: it answers /v1/messages and /v1/responses with the recorded upstream streams, with
// the request id header a new-api relay sends, and remembers what it was sent.
type fakeSupplier struct {
	server *httptest.Server
	mu     sync.Mutex
	zero   bool // answers Messages with a stream that reports no output and no end
	seen   []supplied
}

type supplied struct{ path, auth, body string }

func newFakeSupplier(t *testing.T) *fakeSupplier {
	t.Helper()
	read := func(dir string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "protocol", "testdata", dir, "upstream.sse"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	f := &fakeSupplier{}
	messages, responses := read("messages/text"), read("responses/text")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, supplied{r.URL.Path, r.Header.Get("Authorization"), string(body)})
		n, zero := len(f.seen), f.zero
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Oneapi-Request-Id", "relay-"+strconv.Itoa(n))
		switch {
		case r.URL.Path == "/v1/responses":
			_, _ = io.WriteString(w, responses)
		case zero:
			_, _ = io.WriteString(w, zeroOutput)
		case r.URL.Path == "/v1/messages":
			_, _ = io.WriteString(w, messages)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// zeroOutput reports the input, sends a word, reports the output as zero and breaks off with no stop reason: a call
// whose output was reported zero and whose end was never reported, which the waiver rules make free.
const zeroOutput = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}

`

func (f *fakeSupplier) sent() []supplied {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]supplied(nil), f.seen...)
}

// callWorld is the database of one node: a site, two accounts with a key each (one funded, one not), the models of the
// samples, and a supplier that serves them, all but the sale price, which a test adds when it wants the node to route.
type callWorld struct {
	commandLine
	supplier       *fakeSupplier
	site           uuid.UUID
	richKey, noKey string
	models         map[string]uuid.UUID // by slug
}

func newCallWorld(t *testing.T) *callWorld {
	t.Helper()
	c := newCommandLine(t)
	if _, err := c.site(siteArgs...); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"rich@example.com", "broke@example.com"} {
		if _, err := c.user(ownerPassword+"\n", "create", "--email", email); err != nil {
			t.Fatal(err)
		}
	}
	w := &callWorld{commandLine: c, supplier: newFakeSupplier(t), models: map[string]uuid.UUID{}}
	// The node reaches the supplier, which listens on loopback, through a client that is allowed to.
	original := outbound
	outbound = func(tel *telemetry.Telemetry) (*egress.Client, error) {
		return egress.NewForTesting(egresstest.Loopback(t), egress.WithTracerProvider(tel.Traces)), nil
	}
	t.Cleanup(func() { outbound = original })
	w.site = uuid.MustParse(c.text("SELECT id::text FROM accounts.sites"))
	w.seedCatalog()
	w.seedSupplier()
	w.richKey = w.fundedKey("rich@example.com", 10_000_000_000)
	w.noKey = w.fundedKey("broke@example.com", 0)
	return w
}

// seedCatalog lists the two models of the samples, and a third that nothing serves, each with a spec and a reference
// price.
func (w *callWorld) seedCatalog() {
	w.t.Helper()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	for slug, name := range map[string]string{
		"anthropic/claude-opus-5-5": "claude-opus-5-5", "openai/gpt-6-astra": "gpt-6-astra", "acme/unserved": "unserved",
	} {
		w.models[slug] = w.listModel(cat, slug, name)
	}
}

func (w *callWorld) listModel(cat *catalog.Catalog, slug, name string) uuid.UUID {
	w.t.Helper()
	ctx := w.t.Context()
	op := func() catalog.Op {
		return catalog.Op{Actor: audit.System("callpath.test"), Key: uuid.NewV7(), Reason: "test"}
	}
	author, _, _ := strings.Cut(slug, "/")
	if _, err := cat.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: author, Name: author}); err != nil {
		w.t.Fatal(err)
	}
	made, err := cat.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: jsonx.Raw(servedSpec),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	id, err := uuid.Parse(made.Subject)
	if err != nil {
		w.t.Fatal(err)
	}
	model := catalog.ModelID(id)
	for _, public := range []string{slug, name} {
		if _, err := cat.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: model, Name: public, Visibility: catalog.VisibilityPublic}); err != nil {
			w.t.Fatal(err)
		}
	}
	if _, err := cat.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: model, Source: catalog.SourceManual, Schedule: jsonx.Raw(servedReference),
		Evidence: jsonx.Raw(`{"url":"https://example.com/pricing"}`),
	}); err != nil {
		w.t.Fatal(err)
	}
	if _, err := cat.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: model, Rev: 0}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// seedSupplier writes a supplier on the fake relay with an active credential, and one endpoint for each of the two
// models: Messages for the Claude one, Responses for the GPT one, each costed from a schedule and the supplier's
// confirmed unit value.
func (w *callWorld) seedSupplier() {
	w.t.Helper()
	ctx := w.t.Context()
	// The fake supplier speaks plain HTTP on loopback, which the origin column only admits as https and a hostname; the
	// one constraint it cannot meet is dropped from this database, and nothing else about the supplier is bent.
	if err := pgtest.Run(ctx, w.database.Superuser, "ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_origin_check"); err != nil {
		w.t.Fatal(err)
	}
	supplier, group, credential := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	sealed := masterKeys(w.t).Seal(secret.New(supplierToken), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO suppliers.suppliers (id, owner_scope, name, kind, origin, upstream_account, native_unit,
			settlement_currency, mgmt_egress_id, status)
			VALUES ($1, 'platform', 'Relay', 'newapi', $2, 'account', 'quota', 'USD', 'direct', 'active')`, []any{supplier, w.supplier.server.URL}},
		{`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, selected, present, codename)
			VALUES ($1, $2, 'default', 'default', 1, true, true, 'maple')`, []any{group, supplier}},
		{`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
			wrapped_dek, key_version, status, max_concurrency) VALUES ($1, $2, $3, 'key-1', $4, $5, $6, $7, 'active', 4)`,
			[]any{credential, supplier, group, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion)}},
		{`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 0.001, 'test', 'seed')`, []any{supplier}},
	}
	for _, e := range []struct{ slug, upstream, format string }{
		{"anthropic/claude-opus-5-5", "up-claude", "messages"}, {"openai/gpt-6-astra", "up-gpt", "responses"},
	} {
		endpoint, schedule := uuid.NewV7(), uuid.NewV7()
		stmts = append(stmts, []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
				VALUES ($1, $2, $3, $4, $5, ARRAY[$6])`, []any{endpoint, supplier, group, w.models[e.slug], e.upstream, e.format}},
			{`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
				VALUES ($1, $2, $3, 'quota', $4::jsonb, 'published', 'v1')`,
				[]any{schedule, supplier, endpoint, `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00004"}}`}},
			{`UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = $1 WHERE id = $2`, []any{schedule, endpoint}},
		}...)
	}
	if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		for _, s := range stmts {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
}

// fundedKey gives the user's account a balance and a call key, and returns the key's text.
func (w *callWorld) fundedKey(email string, credit int64) string {
	w.t.Helper()
	ctx := w.t.Context()
	account := uuid.MustParse(w.text("SELECT account_id::text FROM accounts.users WHERE email = '" + email + "'"))
	user := uuid.MustParse(w.text("SELECT id::text FROM accounts.users WHERE email = '" + email + "'"))
	svc, err := accounts.NewService(w.db, accounts.Config{Platform: pg.PlatformScope(), Hashes: macKeys(w.t), Keyring: masterKeys(w.t)})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := svc.Load(ctx, slog.New(slog.DiscardHandler)); err != nil {
		w.t.Fatal(err)
	}
	if credit > 0 {
		if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
			return billing.AddCredit(ctx, tx, billing.Credit{
				ID: uuid.NewV7(), Site: w.site, Account: account, Amount: credit,
				Actor: audit.System("callpath.test"), Reason: "a funded account for the call path test",
			})
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	unlimited := int64(1) << 50
	var created accounts.CreatedKey
	if err := w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
		created, err = svc.CreateKey(ctx, tx, audit.System("callpath.test"), accounts.KeyRequest{
			Site: accounts.SiteID(w.site), Account: accounts.AccountID(account), CreatedBy: accounts.UserID(user),
			KeySettings: accounts.KeySettings{Name: "Test", SpendLimit: &unlimited},
		})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return created.Text.Reveal()
}

// price makes the node able to sell: the site's one rule, at one and a half times the reference price.
func (w *callWorld) price() {
	w.t.Helper()
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(w.t.Context(), `INSERT INTO billing.pricing_rules
			(id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
			VALUES ($1, $2, true, 'all', '', 'all', '', $3::jsonb, '{}'::jsonb)`, uuid.NewV7(), w.site, servedRule)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// runningNode is a running `serve`.
type runningNode struct {
	addresses
	stop func() error
}

// start runs a node of the program over the world's database, whose suppliers it reaches on loopback.
func (w *callWorld) start() runningNode {
	w.t.Helper()
	environ := append(slices.Clone(w.environ), "AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=10s", "AXIOM_MASTER_KEY_FILE="+keyFile(w.t, 'm'), "AXIOM_KEY_HASH_FILE="+keyFile(w.t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE="+keyFile(w.t, 'i'))
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(w.t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, environ, slog.New(slog.NewJSONHandler(nodeLog{w.t, logs}, nil))) }()
	n := runningNode{addresses: servingAddrs(w.t, logs, done)}
	var once sync.Once
	var result error
	n.stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(15 * time.Second):
				w.t.Error("the node did not shut down")
			}
		})
		return result
	}
	w.t.Cleanup(func() { _ = n.stop() })
	return n
}

// ready reports whether the node says it is ready.
func (n runningNode) ready(t *testing.T) bool {
	t.Helper()
	resp, err := get(t, "http://"+n.Ops+"/readyz")
	return err == nil && resp.StatusCode == http.StatusOK
}

// reply is a model call's answer.
type reply struct {
	status int
	header http.Header
	body   []byte
}

// call posts a model API call to the node, as a client of the main site's API domain.
func (w *callWorld) call(n runningNode, path, key, body string, headers ...string) reply {
	w.t.Helper()
	req, err := http.NewRequestWithContext(w.t.Context(), http.MethodPost, "http://"+n.Public+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	req.Host = "api.main.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test client
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, out}
}

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
// a model nothing serves.
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

	// Messages through /v1, with the tools Claude Code sends and a compressing client: the answer is not encoded.
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
	sent := w.supplier.sent()
	if len(sent) != 1 || sent[0].path != "/v1/messages" || sent[0].auth != "Bearer "+supplierToken ||
		!strings.Contains(sent[0].body, `"up-claude"`) || strings.Contains(sent[0].body, w.richKey) {
		t.Errorf("the supplier was sent %+v, want one Messages call under the supplier's own key and the endpoint's model name", sent)
	}

	// Responses through /api/v1, with a function tool, as Codex sends.
	r = w.call(n, "/api/v1/responses", w.richKey, responsesCall)
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

	// A reply that reports no output and never ends is free, and no waiver is counted.
	w.supplier.mu.Lock()
	w.supplier.zero = true
	w.supplier.mu.Unlock()
	r = w.call(n, "/v1/messages", w.richKey, strings.Replace(messagesCall, `"max_tokens"`, `"stream":true,"max_tokens"`, 1))
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
	w.checkBalanced()

	// An account with nothing to spend is refused before the supplier hears of it, and the ledger does not move.
	transactions, called := w.query("SELECT count(*)::text FROM billing.transactions"), len(w.supplier.sent())
	r = w.call(n, "/v1/messages", w.noKey, messagesCall)
	if r.status != http.StatusPaymentRequired || !strings.Contains(string(r.body), "credits_insufficient") {
		t.Errorf("an account without credit got %d %s, want 402 credits_insufficient", r.status, r.body)
	}
	if got := w.query("SELECT count(*)::text FROM billing.transactions"); got != transactions || len(w.supplier.sent()) != called {
		t.Errorf("a refused call moved the ledger (%s transactions, then %s) or reached the supplier", transactions, got)
	}
	w.checkBalanced()

	// A model that is listed and priced and has no endpoint.
	r = w.call(n, "/v1/messages", w.richKey, strings.Replace(messagesCall, "claude-opus-5-5", "unserved", 1))
	if r.status != http.StatusNotFound || !strings.Contains(string(r.body), "no_endpoint") {
		t.Errorf("a model nothing serves got %d %s, want 404 no_endpoint", r.status, r.body)
	}

	// A second node on the same database: its first sweep reaps holders that expired, not the first node's.
	second := w.start()
	if got := w.query("SELECT count(*)::text FROM billing.holders WHERE reaped_at IS NULL"); got != "2" {
		t.Errorf("%s holders alive, want one for each node: the second node's start reaped the first's", got)
	}
	if r = w.call(n, "/v1/responses", w.richKey, responsesCall); r.status != http.StatusOK {
		t.Errorf("the first node answers %d %s after the second started", r.status, r.body)
	}
	if err := second.stop(); err != nil {
		t.Errorf("the second node's shutdown: %v", err)
	}
	if err := n.stop(); err != nil {
		t.Errorf("the first node's shutdown: %v", err)
	}
	if got := w.query("SELECT count(*)::text FROM billing.reservations WHERE state = 'pending'"); got != "0" {
		t.Errorf("%s reservations pending after the calls and the shutdown", got)
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

// query is the first column of the query's one row, read in the platform scope.
func (w *callWorld) query(sql string, args ...any) (value string) {
	w.t.Helper()
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		err := tx.QueryRow(w.t.Context(), sql, args...).Scan(&value)
		if err != nil && strings.Contains(err.Error(), "no rows") {
			value = ""
			return nil
		}
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return value
}

func waitUntil(t *testing.T, limit time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("the condition never held")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// nodeLog sends a node's log to the test's, where it shows when the test fails, and to the channel serving is read from.
type nodeLog struct {
	t      *testing.T
	serves records
}

func (l nodeLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return l.serves.Write(p)
}
