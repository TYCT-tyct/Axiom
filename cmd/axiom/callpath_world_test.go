package main

import (
	"context"
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
	"axiom/internal/platform/kv/kvtest"
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
	servedSpec = `{"items":["input","output","cache_read","cache_write","web_search"],"tiers":["standard","priority"],` +
		`"context_length":200000,"max_output_tokens":8192,"streams":true,"abilities":["tools","web_search","reasoning"],` +
		`"thinking":"manual","encrypted_reasoning":true,"mid_system":true,"untrusted_system_after_tools":false,` +
		`"default_sampling_only":false,"forced_tool_choice":true,"cache_breakpoints":false}`
	// servedReference is three dollars a million tokens of input and fifteen of output, and twice that at the priority
	// tier, which prices every item the spec reports since a tier inherits nothing from the standard one.
	servedReference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375","web_search":"0.01"},` +
		`"service_tiers":{"priority":{"rates":{"input":"0.000006","output":"0.00003","cache_read":"0.0000006","cache_write":"0.0000075","web_search":"0.01"}}}}`
	// servedRule sells every model at one and a half times its reference price.
	servedRule = `{"kind":"reference_multiplier","multiplier":"1.5","currency_conversion":"face"}`
	// The sale prices of the recorded samples: 25 input and 15 output tokens of the Messages one, 37 and 11 of the
	// Responses one, at 4.5 and 22.5 millionths of a dollar a token, in billionths; at the priority tier, twice that.
	messagesCharge  = 25*4_500 + 15*22_500
	responsesCharge = 37*4_500 + 11*22_500
	// What the same tokens cost the supplier, from servedCost: ten and forty billionths of a dollar a token of input and
	// output, twice that at the priority tier.
	messagesCost  = 25*10 + 15*40
	responsesCost = 37*10 + 11*40
	// A web search is sold at one and a half times its reference price of a cent, and costs the supplier five millionths
	// of a dollar.
	searchCharge = 15_000_000
	searchCost   = 5_000
	supplierKey  = "sk-supplier-secret"
	// servedCost is what the supplier charges, in its native quota: a model's tokens and searches, at twice the price at
	// the priority tier. The supplier's unit value is 0.001, which makes a standard token ten and forty billionths.
	servedCost = `{"v":1,"currency":"XXX","rates":{"input":"0.00001","cache_read":"0.00001","cache_write":"0.00001","output":"0.00004","web_search":"0.005"},` +
		`"service_tiers":{"priority":{"rates":{"input":"0.00002","cache_read":"0.00002","cache_write":"0.00002","output":"0.00008","web_search":"0.01"}}}}`
)

// fakeSupplier is a new-api relay: it answers /v1/messages and /v1/responses with the recorded upstream streams, with
// the request id header a new-api relay sends, and remembers what it was sent.
type fakeSupplier struct {
	server      *httptest.Server
	streams     map[string]string // the recorded upstream stream each path answers with
	mu          sync.Mutex
	replies     map[string]string // what a path answers with instead, until answer clears it
	seen        []supplied
	retryAfter  string // optional Retry-After on failed replies
	failureBody string // optional classified rejection body
	failures    []int  // HTTP statuses consumed before the recorded successful replies
}

type supplied struct{ path, auth, body string }

func newFakeSupplier(t *testing.T) *fakeSupplier {
	t.Helper()
	samples, err := os.OpenRoot(filepath.Join("..", "..", "internal", "protocol", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSupplier{streams: map[string]string{}, replies: map[string]string{}}
	for path, dir := range map[string]string{"/v1/messages": "messages/text", "/v1/responses": "responses/text"} {
		b, err := samples.ReadFile(dir + "/upstream.sse")
		if err != nil {
			t.Fatal(err)
		}
		f.streams[path] = string(b)
	}
	if err := samples.Close(); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the fake supplier could not read a request: %v", err)
		}
		n, reply, status, retryAfter := f.record(supplied{r.URL.Path, r.Header.Get("Authorization"), string(body)})
		w.Header().Set("X-Oneapi-Request-Id", "relay-"+strconv.Itoa(n))
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", retryAfter)
			w.WriteHeader(status)
		} else {
			if reply == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if _, err := io.WriteString(w, reply); err != nil {
			t.Errorf("the fake supplier could not answer: %v", err)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// record remembers a call and says how many there have been, and what it is to be answered with: nothing for a path the
// supplier does not serve.
func (f *fakeSupplier) record(s supplied) (n int, reply string, status int, retryAfter string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, s)
	if len(f.failures) > 0 {
		status, f.failures = f.failures[0], f.failures[1:]
	}
	if reply = f.replies[s.path]; reply == "" {
		reply = f.streams[s.path]
	}
	if status != 0 {
		reply = f.failureBody
		if reply == "" {
			reply = `{"error":{"type":"new_api_error","message":"upstream refused"}}`
		}
	}
	return len(f.seen), reply, status, f.retryAfter
}

// answer makes the path reply with the stream from now on, or with its recorded one again when the stream is empty.
func (f *fakeSupplier) answer(path, stream string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[path] = stream
}

// zeroOutput reports the input, sends a word, reports the output as zero and breaks off without a message_stop: a call
// whose output was reported zero, which the waiver rules make free.
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

// callWorldSeed is what a copy of the world's database cannot carry in Go: the IDs and key texts written when it was
// built. They name rows the copy has too, since CREATE DATABASE copied them.
type callWorldSeed struct {
	site           uuid.UUID
	richKey, noKey string
	models         map[string]uuid.UUID // by slug
}

// callWorldFixture is the world's database with its site, accounts, catalog and supplier, which each test copies.
var callWorldFixture fixtureDatabase[callWorldSeed]

func newCallWorld(t *testing.T) *callWorld {
	t.Helper()
	c, seed := callWorldFixture.get(t, buildCallWorld)
	w := &callWorld{commandLine: c, supplier: newFakeSupplier(t), site: seed.site, richKey: seed.richKey, noKey: seed.noKey, models: seed.models}
	// The fixture's supplier names the fake relay it was built with; each test's world reaches its own. The write is
	// setup, so it runs as the superuser: the app role reads suppliers but may not change them.
	if err := pgtest.Run(t.Context(), c.database.Superuser, "UPDATE suppliers.suppliers SET origin = '"+w.supplier.server.URL+"'"); err != nil {
		t.Fatal(err)
	}
	return w
}

// buildCallWorld makes the world's site, accounts, catalog and supplier once, for newCallWorld to copy.
func buildCallWorld(t *testing.T) (pgtest.Database, callWorldSeed) {
	t.Helper()
	database := pgtest.New(t, modules()...)
	c := openCommandLine(t, database)
	defer c.db.Close()
	if _, err := c.site(siteArgs...); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"rich@example.com", "broke@example.com"} {
		if _, err := c.user(ownerPassword+"\n", "create", "--email", email); err != nil {
			t.Fatal(err)
		}
	}
	supplier := newFakeSupplier(t)
	w := &callWorld{commandLine: c, supplier: supplier, models: map[string]uuid.UUID{}}
	w.site = uuid.MustParse(c.text("SELECT id::text FROM accounts.sites"))
	w.seedCatalog()
	w.seedSupplier()
	w.richKey = w.fundedKey("rich@example.com", 10_000_000_000)
	w.noKey = w.fundedKey("broke@example.com", 0)
	supplier.server.Close()
	return database, callWorldSeed{site: w.site, richKey: w.richKey, noKey: w.noKey, models: w.models}
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
	sealed := masterKeys(w.t).Seal(secret.New(supplierKey), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	stmts := []statement{
		{`INSERT INTO suppliers.suppliers (id, owner_scope, name, kind, origin, upstream_account, native_unit,
			settlement_currency, mgmt_egress_id, status, balance_native)
			VALUES ($1, 'platform', 'Relay', 'newapi', $2, 'account', 'quota', 'USD', 'direct', 'active', 100)`, []any{supplier, w.supplier.server.URL}},
		{`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, selected, present, codename)
			VALUES ($1, $2, 'default', 'default', 1, true, true, 'maple')`, []any{group, supplier}},
		{
			`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
			wrapped_dek, key_version, status, max_concurrency) VALUES ($1, $2, $3, 'key-1', $4, $5, $6, $7, 'active', 4)`,
			[]any{credential, supplier, group, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion)},
		},
		{`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 0.001, 'test', 'seed')`, []any{supplier}},
	}
	for _, e := range []struct{ slug, upstream, format string }{
		{"anthropic/claude-opus-5-5", "up-claude", "messages"}, {"openai/gpt-6-astra", "up-gpt", "responses"},
	} {
		stmts = append(stmts, endpointStatements(supplier, group, w.models[e.slug], e.upstream, e.format)...)
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
			Name: "Test", SpendLimit: &unlimited,
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
	stop     func() error  // asks the node to stop, waits for it and says how it ended
	finished chan struct{} // closed when serve has returned, whoever made it
	ended    func() error  // how serve ended; only after finished
}

// start runs a node of the program over the world's database, whose suppliers it reaches on loopback.
func (w *callWorld) start() runningNode {
	w.t.Helper()
	environ := append(slices.Clone(w.environ), "AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=10s", "AXIOM_MASTER_KEY_FILE="+keyFile(w.t, 'm'), "AXIOM_KEY_HASH_FILE="+keyFile(w.t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE="+keyFile(w.t, 'i'))
	// The story has two nodes of one deployment (checkSecondNode), so this is one: N is 2, and the nodes share a Valkey
	// for rate counts and stickiness. serveWith bypasses the production guard for unpartitioned supplier ceilings.
	cache := kvtest.New(w.t)
	environ = append(environ, "AXIOM_NODE_ID="+uuid.New().String(), "AXIOM_CORE_NODES=2", "AXIOM_VALKEY_URL="+cache.URL, "AXIOM_VALKEY_PASSWORD_FILE="+cache.PasswordFile)
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(w.t.Context())
	done := make(chan error, 1)
	// The node reaches the supplier, which listens on loopback, through a client that is allowed to.
	loopback := func(tel *telemetry.Telemetry) (*egress.Client, error) { //nolint:unparam // the signature serveWith asks for; the production client can fail
		return egress.NewForTesting(egresstest.Loopback(w.t), egress.WithTracerProvider(tel.Traces)), nil
	}
	go func() {
		done <- serveWith(ctx, environ, slog.New(slog.NewJSONHandler(nodeLog{w.t, logs}, nil)), loopback, startRecorder, nil)
	}()
	n := runningNode{addresses: servingAddrs(w.t, logs, done), finished: make(chan struct{})}
	var result error
	go func() { result = <-done; close(n.finished) }()
	n.ended = func() error { return result }
	n.stop = func() error {
		cancel()
		select {
		case <-n.finished:
		case <-time.After(15 * time.Second):
			w.t.Error("the node did not shut down")
		}
		return result
	}
	w.t.Cleanup(func() {
		select {
		case <-n.finished: // it ended already, and whoever ended it has said how it should have
			return
		default:
		}
		if err := n.stop(); err != nil {
			w.t.Errorf("stopping a node: %v", err)
		}
	})
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

// statement is one SQL statement of a seed and its arguments.
type statement struct {
	sql  string
	args []any
}

// endpointStatements write an endpoint of a model on the supplier, speaking format and costed from servedCost.
func endpointStatements(supplier, group, model uuid.UUID, upstream, format string) []statement {
	endpoint, schedule := uuid.NewV7(), uuid.NewV7()
	return []statement{
		{`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
			VALUES ($1, $2, $3, $4, $5, ARRAY[$6])`, []any{endpoint, supplier, group, model, upstream, format}},
		{
			`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
			VALUES ($1, $2, $3, 'quota', $4::jsonb, 'published', 'v1')`,
			[]any{schedule, supplier, endpoint, servedCost},
		},
		{`UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = $1 WHERE id = $2`, []any{schedule, endpoint}},
	}
}
