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
	servedSpec = `{"items":["input","output","cache_read","cache_write","web_search"],"tiers":["standard"],` +
		`"context_length":200000,"max_output_tokens":8192,"streams":true,"abilities":["tools","web_search"]}`
	// referencePrice is a dollar a million tokens of input and five of output, as the catalog publishes it.
	servedReference = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003","cache_write":"0.00000375","web_search":"0.01"}}`
	// servedRule sells every model at one and a half times its reference price.
	servedRule = `{"kind":"reference_multiplier","multiplier":"1.5","currency_conversion":"face"}`
	// The sale prices of the recorded samples: 25 input and 15 output tokens of the Messages one, 37 and 11 of the
	// Responses one, at 4.5 and 22.5 millionths of a dollar a token, in billionths.
	messagesCharge  = 25*4_500 + 15*22_500
	responsesCharge = 37*4_500 + 11*22_500
	supplierKey     = "sk-supplier-secret"
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
	samples, err := os.OpenRoot(filepath.Join("..", "..", "internal", "protocol", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	var streams [2]string // what the supplier answers Messages and Responses with: the samples' recorded upstream streams
	for i, dir := range []string{"messages/text", "responses/text"} {
		b, err := samples.ReadFile(dir + "/upstream.sse")
		if err != nil {
			t.Fatal(err)
		}
		streams[i] = string(b)
	}
	if err := samples.Close(); err != nil {
		t.Fatal(err)
	}
	f := &fakeSupplier{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the fake supplier could not read a request: %v", err)
		}
		n, zero := f.record(supplied{r.URL.Path, r.Header.Get("Authorization"), string(body)})
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Oneapi-Request-Id", "relay-"+strconv.Itoa(n))
		var reply string
		switch {
		case r.URL.Path == "/v1/responses":
			reply = streams[1]
		case r.URL.Path != "/v1/messages":
			http.NotFound(w, r)
			return
		case zero:
			reply = zeroOutput
		default:
			reply = streams[0]
		}
		if _, err := io.WriteString(w, reply); err != nil {
			t.Errorf("the fake supplier could not answer: %v", err)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// record remembers a call and says how many there have been, and whether Messages is to be answered with no output.
func (f *fakeSupplier) record(s supplied) (n int, zero bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, s)
	return len(f.seen), f.zero
}

func (f *fakeSupplier) setZero(zero bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zero = zero
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
	sealed := masterKeys(w.t).Seal(secret.New(supplierKey), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	stmts := []struct {
		sql  string
		args []any
	}{
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
		endpoint, schedule := uuid.NewV7(), uuid.NewV7()
		stmts = append(stmts, []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
				VALUES ($1, $2, $3, $4, $5, ARRAY[$6])`, []any{endpoint, supplier, group, w.models[e.slug], e.upstream, e.format}},
			{
				`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
				VALUES ($1, $2, $3, 'quota', $4::jsonb, 'published', 'v1')`,
				[]any{schedule, supplier, endpoint, `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00004"}}`},
			},
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
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(w.t.Context())
	done := make(chan error, 1)
	// The node reaches the supplier, which listens on loopback, through a client that is allowed to.
	loopback := func(tel *telemetry.Telemetry) (*egress.Client, error) { //nolint:unparam // the signature serveVia asks for; the production client can fail
		return egress.NewForTesting(egresstest.Loopback(w.t), egress.WithTracerProvider(tel.Traces)), nil
	}
	go func() {
		done <- serveVia(ctx, environ, slog.New(slog.NewJSONHandler(nodeLog{w.t, logs}, nil)), loopback)
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
