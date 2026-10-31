package gateway

import (
	"cmp"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

const (
	apiHost     = "api.test"
	consoleHost = "console.test"
	// A request asks for at most maxOutput tokens of an endpoint that charges outputUnit per token, and sends about
	// inputBytes bytes at inputUnit each: the units are billionths of a credit.
	inputUnit, outputUnit, maxOutput = 1000, 2000, 1000
)

var (
	platformScope = pg.NewScope(true, nil, nil)
	siteID, tier  = uuid.NewV7(), uuid.NewV7()
	userID        = uuid.NewV7()
)

// rig is the handler on real accounts, billing and catalog tables, with a routing snapshot of two models and a Run
// that only releases the reservation, unless a test gives it an executor (exec).
type rig struct {
	t        *testing.T
	database pgtest.Database
	db       *pg.DB
	accounts *accounts.Service
	catalog  *catalog.Catalog
	router   *routing.Router
	billing  *billing.Billing
	limits   *Backpressure
	refused  func() map[string]int64 // the 429s counted so far, by reason
	memory   *protocol.Memory
	handler  *Handler
	server   *httptest.Server

	cfg       Config
	price     string                           // every model's sale schedule, unless a test lists another
	alpha     catalog.ModelID                  // acme/alpha, served by two endpoints
	beta      catalog.ModelID                  // acme/beta, served by one
	bare      catalog.ModelID                  // anthropic/claude-test, listed with its maker's name too, and no endpoint
	claude    catalog.ModelID                  // anthropic/claude-live, listed with its maker's name too, served by one
	routes    []routing.Endpoint               // endpoints in the installed snapshot
	endpoints map[string]routing.Endpoint      // by name: alpha-1 and alpha-2 serve alpha, beta-1 serves beta, claude-1 serves claude
	sold      map[catalog.ModelID]billing.Sold // the sale prices the OpenRouter list shows, by model
	models    *ModelList                       // the list in OpenRouter's shape, published as a build would publish it
	suppliers *suppliers.Service               // the real module, in a rig that has its tables

	mu         sync.Mutex
	admits     []*Call
	hold       chan struct{} // when set, Run waits for it to close before it settles; see holdCalls
	settled    int
	exec       *Executor       // when set, Run is its Run
	clientGone <-chan struct{} // closed when the server sees the client of the latest call that reached Run go
}

// publish builds the model list in OpenRouter's shape the way a route build does — from the catalog snapshot, the sale
// prices and the models the routing snapshot serves — and stores it where the handler reads it. The rig calls it wherever
// it changes a route, a model or a price, as a build would.
func (r *rig) publish() {
	r.t.Helper()
	snap, err := r.catalog.Current()
	if err != nil {
		r.t.Fatal(err)
	}
	l, err := newOpenRouterListing(snap, r.prices(), r.routes)
	if err != nil {
		r.t.Fatal(err)
	}
	r.models.mu.Lock()
	r.models.last = l
	r.models.mu.Unlock()
}

type rigOptions struct {
	config    func(*Config)
	endpoints func(name string, e *routing.Endpoint)
	routing   routing.Config
	prefer    string // the endpoint the preset puts first; the cheapest comes first when empty
	memory    int64
	conns     int                // the most connections the database pool opens; the driver's own limit when zero
	body      time.Duration      // how long Public gives a request to send its body; a minute when zero
	search    websearch.Searcher // the web plugin's backend, nil when the node has none
	price     string             // the model's sale schedule, the cheap one when empty
	suppliers bool               // the suppliers tables too, for a real suppliers service to price attempts
	lifetime  time.Duration      // a reservation's deadline after admission, for a test that lets the cleanup take a call over; an hour when zero
	proxies   []netip.Prefix     // the reverse proxies the handler believes, none when empty
	cache     *kv.Client         // the shared cache the precheck counts in, none when nil
}

// rigs limits how many rigs run at once. Each is a database and a server with requests in flight, and the tests wait
// on the clock (a second for a key lookup, the admission budget), so a machine with every test at once is slower than
// the clock allows and they fail for that alone.
var rigs = make(chan struct{}, 3)

func newRig(t *testing.T, opts ...func(*rigOptions)) *rig {
	t.Helper()
	rigs <- struct{}{}
	t.Cleanup(func() { <-rigs })
	o := rigOptions{memory: 1 << 30}
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	r := &rig{
		t: t, cfg: DefaultConfig(), endpoints: map[string]routing.Endpoint{}, sold: map[catalog.ModelID]billing.Sold{},
		models: NewModelList(), price: cmpOr(o.price, cheap),
	}
	// The tests run many rigs at once on a machine that is busy with other things, where a second is not enough for a
	// transaction or a key lookup; the tests of the budgets set their own.
	r.cfg.AdmissionBudget, r.cfg.KeyLookup = 30*time.Second, 30*time.Second
	// Nothing refreshes the catalog or the routing snapshot while a test runs, and on a busy machine a test outlives the
	// 30 seconds they are good for; the test of a stale snapshot sets its own.
	if o.routing.MaxStale == 0 {
		o.routing.MaxStale = time.Hour
	}
	if o.config != nil {
		o.config(&r.cfg)
	}
	// One source, as cmd/axiom wires it: the routing snapshot and billing reserve by the gateway's segment settings,
	// so a rig that segments in one of them segments in both.
	o.routing.Segments = r.cfg.Segments()
	mods := []pg.Module{accounts.Module(), billing.Migrations(), catalog.Migrations, observability.Module()}
	if o.suppliers {
		mods = append(mods, suppliers.Module())
	}
	r.database = pgtest.New(t, mods...)
	var err error
	dsn := r.database.App
	if o.conns > 0 {
		dsn += "&pool_max_conns=" + itoa(int64(o.conns))
	}
	if r.db, err = pg.Open(t.Context(), dsn, pg.WithCompletionReserve(r.cfg.PoolReserve)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.db.Close)
	meter, refused := countingMeter(t.Context(), t)
	r.refused = refused
	if r.limits, err = New(r.cfg, meter.Meter("test")); err != nil {
		t.Fatal(err)
	}
	r.warmConnections(12)
	r.seedSite()
	r.seedCatalog()
	r.seedRouting(o)
	sup := r.settlementSuppliers(o)
	if r.billing, err = billing.Start(t.Context(), r.db, platformScope, billing.Config{
		HolderTTL: time.Hour, ReapGrace: time.Hour, Lifetime: cmp.Or(o.lifetime, time.Hour), // nothing renews the holder while a test runs
		Segments: r.cfg.Segments(),
	}, sup); err != nil {
		t.Fatal(err)
	}
	r.memory = protocol.NewMemory(o.memory)
	if r.handler, err = NewHandler(Deps{
		Accounts: r.accounts, Catalog: r.catalog, Router: r.router, Billing: r.billing, Limits: r.limits, Memory: r.memory,
		Run: r.run, Logger: slog.New(slog.NewTextHandler(logWriter{t}, nil)), Models: r.models, Proxies: o.proxies, KV: o.cache,
		Outstanding: NewOutstanding(), Search: o.search,
	}); err != nil {
		t.Fatal(err)
	}
	r.server = httptest.NewServer(httpserver.Public(r.handler, slog.New(slog.NewTextHandler(logWriter{t}, nil)), cmp.Or(o.body, time.Minute)))
	t.Cleanup(r.server.Close)
	t.Cleanup(func() { r.handler.Outstanding.Drain(context.WithoutCancel(t.Context()), time.Minute) })
	return r
}

// settlementSuppliers is the supplier's side of a settlement. A rig without the suppliers tables settles it nowhere:
// nothing it runs reaches an upstream. One with them writes through the module's own functions, so a settlement's
// attempt rows are real (costs_test.go).
func (r *rig) settlementSuppliers(o rigOptions) billing.Suppliers {
	if !o.suppliers {
		return noSuppliers{}
	}
	var err error
	if r.suppliers, err = r.newSuppliers(); err != nil {
		r.t.Fatal(err)
	}
	return suppliers.CallWrites{}
}

// warmConnections opens n connections to the database at once, so the requests of a test do not wait for new ones to
// be made: with many databases being made at the same time on one machine that takes seconds.
func (r *rig) warmConnections(n int) {
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
				_, err := tx.Exec(r.t.Context(), "SELECT pg_sleep(0.2)")
				return err
			})
			if err != nil {
				r.t.Error(err)
			}
		})
	}
	wg.Wait()
}

// run answers an admitted call the way a call that never reached the upstream ends: its reservation is released and
// its slots are given back. It records the call and, when hold is set, waits for it first.
func (r *rig) run(w http.ResponseWriter, req *http.Request, c *Call) {
	r.mu.Lock()
	r.admits = append(r.admits, c)
	r.clientGone = req.Context().Done()
	hold, exec := r.hold, r.exec
	r.mu.Unlock()
	if exec != nil {
		exec.Run(w, req, c)
		return
	}
	if hold != nil {
		<-hold
	}
	ctx, cancel := SettleContext(req.Context(), 30*time.Second)
	defer cancel()
	err := submitCompletion(ctx, r.limits.Funds, accountKey(c.caller))
	if err == nil {
		err = r.billing.SettleOrphan(ctx, c.Admitted, c.ID)
	}
	if err != nil {
		r.t.Errorf("release the reservation: %v", err)
	}
	c.Done()
	r.mu.Lock()
	r.settled++
	r.mu.Unlock()
	entries := make([]string, len(c.Plan.Entries))
	for i, e := range c.Plan.Entries {
		entries[i] = uuid.UUID(e.Endpoint).String()
	}
	out, err := jsonx.MarshalCanonical(map[string]any{"entries": entries, "id": c.ID.String()})
	if err != nil {
		r.t.Error(err)
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(out); err != nil {
		r.t.Error(err)
	}
}

// holdCalls makes Run wait before it settles, until the returned function is called (or the test ends, which must not
// leave the server waiting for a request that never finishes).
func (r *rig) holdCalls() (release func()) {
	hold := make(chan struct{})
	r.mu.Lock()
	r.hold = hold
	r.mu.Unlock()
	release = sync.OnceFunc(func() { close(hold) })
	r.t.Cleanup(release)
	return release
}

// logWriter sends the handler's log to the test's, where it shows when a test fails.
type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func (r *rig) calls() []*Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Call(nil), r.admits...)
}

// sql runs the statements as one, as the superuser; the site's deferred foreign key needs its rows to arrive together.
func (r *rig) sql(statements ...string) {
	r.t.Helper()
	if err := pgtest.Run(r.t.Context(), r.database.Superuser, strings.Join(statements, ";\n")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) seedSite() {
	account, workspace := uuid.NewV7(), uuid.NewV7()
	r.sql(
		"INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id) VALUES ('"+siteID.String()+"', 'Main', 'active', 'sk-ax-v1-', 'invite', '"+tier.String()+"')",
		"INSERT INTO accounts.tiers VALUES ('"+tier.String()+"', '"+siteID.String()+"', 'Standard')",
		"INSERT INTO platform.tenants VALUES ('"+siteID.String()+"', '"+siteID.String()+"'), ('"+siteID.String()+"', '"+account.String()+"')", // the audit log names tenants that are registered
		"INSERT INTO accounts.accounts VALUES ('"+account.String()+"', '"+siteID.String()+"', 'personal', 'Owner', 'active', '"+tier.String()+"')",
		"INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES ('"+workspace.String()+"', '"+siteID.String()+"', '"+account.String()+"', 'Default', 'active', true)",
		"INSERT INTO accounts.site_domains VALUES ('"+apiHost+"', '"+siteID.String()+"', 'api', 'https://"+apiHost+"', NULL), ('"+consoleHost+"', '"+siteID.String()+"', 'console', 'https://"+consoleHost+"', NULL)",
		"INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code) VALUES ('"+userID.String()+"', '"+siteID.String()+"', '"+account.String()+"', 'o@example.com', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA', 1, 'active', 'invite')",
	)
	path := filepath.Join(r.t.TempDir(), "hash-keys")
	line := "1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("1", 32))) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		r.t.Fatal(err)
	}
	keys, err := secret.LoadMAC(path)
	if err != nil {
		r.t.Fatal(err)
	}
	if r.accounts, err = accounts.NewService(r.db, accounts.Config{Platform: platformScope, Hashes: keys, LookupSlot: r.limits.KeyLookupSlot}); err != nil {
		r.t.Fatal(err)
	}
	if err := r.accounts.Load(r.t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		r.t.Fatal(err)
	}
}

// account is an account of the site with a balance, a default workspace and one key.
type account struct {
	id, workspace uuid.UUID
	key           string
	keyID         accounts.KeyID
}

func (r *rig) newAccount(balance int64) account {
	r.t.Helper()
	a := account{id: uuid.NewV7(), workspace: uuid.NewV7()}
	r.sql(
		"INSERT INTO platform.tenants VALUES ('"+siteID.String()+"', '"+a.id.String()+"')",
		"INSERT INTO accounts.accounts VALUES ('"+a.id.String()+"', '"+siteID.String()+"', 'personal', 'A', 'active', '"+tier.String()+"')",
		"INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES ('"+a.workspace.String()+"', '"+siteID.String()+"', '"+a.id.String()+"', 'Default', 'active', true)",
	)
	scope := pg.NewScope(false, nil, []uuid.UUID{a.id})
	if err := r.db.Tx(r.t.Context(), scope, func(tx pg.Tx) error {
		return billing.OpenBalance(r.t.Context(), tx, siteID, a.id, 0)
	}); err != nil {
		r.t.Fatal(err)
	}
	r.sql("UPDATE billing.balances SET balance = " + itoa(balance) + " WHERE account_id = '" + a.id.String() + "'")
	a.key, a.keyID = r.newKey(a)
	// Recognise the key once, so the requests find it in the cache: a cold key is looked up in the database within a
	// second (accounts' tests cover that), which a busy machine under the race detector does not always keep to.
	if _, err := r.accounts.AuthenticateKey(r.t.Context(), accounts.SiteID(siteID), a.key); err != nil {
		r.t.Fatal(err)
	}
	return a
}

// reply is what a client got back: the status and headers, with the body read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

// do sends a request to the API host, as a client with the key (as a Bearer token when it is not empty).
func (r *rig) do(method, path, key string, body io.Reader, headers ...string) (reply, string) {
	r.t.Helper()
	req, err := http.NewRequestWithContext(r.t.Context(), method, r.server.URL+path, body)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Host = apiHost
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header}, string(out)
}

// message posts a Messages request for the model.
func (r *rig) message(a account, model string) (reply, string) {
	r.t.Helper()
	return r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(messageBody(model)))
}

func messageBody(model string) string {
	return `{"model":"` + model + `","max_tokens":` + itoa(maxOutput) + `,"service_tier":"standard_only","messages":[{"role":"user","content":"hello"}]}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// newKey makes another key of the account, one the gateway has not seen.
func (r *rig) newKey(a account) (string, accounts.KeyID) {
	r.t.Helper()
	var created accounts.CreatedKey
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) (err error) {
		created, err = r.accounts.CreateKey(r.t.Context(), tx, audit.System("gateway.test"), accounts.KeyRequest{
			Site: accounts.SiteID(siteID), Account: accounts.AccountID(a.id), Workspace: accounts.WorkspaceID(a.workspace),
			CreatedBy: accounts.UserID(userID), Name: "Test",
		})
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return created.Text.Reveal(), created.ID
}

// noSuppliers stands in for suppliers' side of settlement in a rig that has no suppliers tables: admission never reaches
// an upstream there, so nothing is ever recorded.
type noSuppliers struct{}

func (noSuppliers) RecordAttempts(context.Context, pg.Tx, []suppliers.AttemptRecord) error {
	return nil
}

func (noSuppliers) RecordLostRequest(context.Context, pg.Tx, uuid.UUID, uuid.UUID, uuid.UUID, []uuid.UUID) error {
	return nil
}

func (noSuppliers) ResolveLostRequest(context.Context, pg.Tx, uuid.UUID) error { return nil }
func (noSuppliers) EndRequest(context.Context, pg.Tx, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}

func (noSuppliers) EndRequests(context.Context, pg.Tx, []suppliers.EndedRequest) error { return nil }
