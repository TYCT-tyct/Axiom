//go:build journey

package main

import (
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

// The browser journey (web/journey, #321) runs against a node this test starts: a deployment's `serve` over a database
// that `axiom site` and `axiom user` set up, with the fakes of the call path test as its only outside world. The
// journey's tests are the clients, so this is not a test: it serves until its standard input closes, and does nothing
// when the journey did not start it.

// journeyStateVar names the file the node writes its addresses to once it serves.
const journeyStateVar = "AXIOM_JOURNEY_STATE"

// journeyState is what the journey needs to know about the node, as JSON.
type journeyState struct {
	Console    string `json:"console"`     // the console's origin; the site's console domain is this host
	API        string `json:"api"`         // the origin model calls go to; the site's API domain is this host
	PayHost    string `json:"pay_host"`    // a second console domain of the site, https, which top-ups require
	InviteCode string `json:"invite_code"` // the code of the user the site started with
	Settle     string `json:"settle"`      // POST here makes the payment gateway report every order it knows as paid
	Calls      string `json:"calls"`       // GET here lists the paths the supplier was called on
}

func TestJourneyServer(t *testing.T) {
	path, ok := os.LookupEnv(journeyStateVar)
	if !ok {
		t.Skip("started by web/journey as the node")
	}
	gateway := newJourneyGateway(t)
	c := newCommandLine(t)
	// The site's domains are the addresses the browser uses: localhost is the one host the cookie and Origin rules take
	// over plain http (internal/accounts.origin), and the API host is the same port by IP, as the two must differ.
	addr := freePort(t)
	port := addr[strings.LastIndex(addr, ":")+1:]
	console, api := "localhost:"+port, addr
	if _, err := c.site("init", "--name", "Main", "--currency", "USD", "--console", console, "--api", api); err != nil {
		t.Fatal(err)
	}
	if _, err := c.user(ownerPassword+"\n", "create", "--email", ownerEmail); err != nil {
		t.Fatal(err)
	}
	// Top-ups come back to an https console (returnURL), which a plain-http localhost one is not. The browser keeps
	// the localhost domain; the journey places the order as a TLS-terminating proxy would pass it, under this one.
	const payHost = "pay.journey.example"
	if err := pgtest.Run(t.Context(), c.database.Superuser, `INSERT INTO accounts.site_domains (domain, site_id, surface, origin)
		SELECT '`+payHost+`', id, 'console', 'https://`+payHost+`' FROM accounts.sites`); err != nil {
		t.Fatal(err)
	}
	w := &callWorld{commandLine: c, supplier: newFakeSupplier(t), models: map[string]uuid.UUID{}, trusted: gateway.trusted()}
	w.site = uuid.MustParse(c.text("SELECT id::text FROM accounts.sites"))
	w.seedCatalog()
	w.seedSupplier()
	w.price()
	gateway.open(w)

	n := w.start("AXIOM_PUBLIC_ADDR=" + addr)
	// Ready once something can be sold, and the startup reconciliations are done, as TestANodeServesModelCalls waits.
	waitUntil(t, 30*time.Second, func() bool { return n.ready(t) })
	waitUntil(t, 30*time.Second, func() bool {
		return w.query(`SELECT count(DISTINCT kind)::text FROM river.river_job
			WHERE kind IN ('billing.reconcile', 'suppliers.reconcile') AND state = 'completed'`) == "2"
	})
	state, err := jsonx.MarshalCanonical(journeyState{
		Console: "http://" + console, PayHost: payHost, API: "http://" + api, InviteCode: w.query("SELECT invite_code FROM accounts.users"),
		Settle: gateway.control.URL + "/settle", Calls: gateway.control.URL + "/calls",
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway.supplier = w.supplier
	// Written last and in one step, so the journey never reads half of it.
	if err := os.WriteFile(path+".tmp", state, 0o600); err != nil || os.Rename(path+".tmp", path) != nil {
		t.Fatal("writing the state:", err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin) //nolint:errcheck // the read ends when the journey closes the pipe or dies
	if err := n.stop(); err != nil {
		t.Error(err)
	}
}

// journeyGateway is an EasyPay gateway (docs/domains/payments.md) as far as the journey needs it: it takes an order,
// answers the node's query about it, and reports it paid when the journey says so through its control address.
type journeyGateway struct {
	t        *testing.T
	server   *httptest.Server // TLS, as the channel requires
	control  *httptest.Server // plain http, for the journey's own requests
	supplier *fakeSupplier
	mu       sync.Mutex
	orders   map[string]*journeyOrder
}

type journeyOrder struct {
	money string
	paid  bool
}

func newJourneyGateway(t *testing.T) *journeyGateway {
	t.Helper()
	g := &journeyGateway{t: t, orders: map[string]*journeyOrder{}}
	g.server = httptest.NewTLSServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /settle", g.settle)
	mux.HandleFunc("GET /calls", g.calls)
	g.control = httptest.NewServer(mux)
	t.Cleanup(g.control.Close)
	return g
}

func (g *journeyGateway) trusted() []*x509.Certificate {
	return []*x509.Certificate{g.server.Certificate()}
}

func (g *journeyGateway) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.Form.Get("out_trade_no")
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/mapi.php" {
		g.orders[id] = &journeyOrder{money: r.Form.Get("money")}
		_, _ = fmt.Fprintf(w, `{"code":"1","trade_no":"T-%s","payurl":"https://pay.example.com/checkout"}`, id) //nolint:errcheck // a test server
		return
	}
	o, found := g.orders[id]
	if !found {
		_, _ = io.WriteString(w, `{"code":0,"msg":"no such order"}`) //nolint:errcheck // a test server
		return
	}
	status := "0"
	if o.paid {
		status = "1"
	}
	_, _ = fmt.Fprintf(w, `{"code":"1","data":{"status":%q,"money":%q,"trade_no":"T-%s","out_trade_no":%q}}`, status, o.money, id, id) //nolint:errcheck // a test server
}

func (g *journeyGateway) settle(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, o := range g.orders {
		o.paid = true
	}
	_, _ = fmt.Fprint(w, len(g.orders)) //nolint:errcheck // a test server
}

func (g *journeyGateway) calls(w http.ResponseWriter, _ *http.Request) {
	var paths []string
	for _, s := range g.supplier.sent() {
		paths = append(paths, s.path)
	}
	_, _ = io.WriteString(w, strings.Join(paths, "\n")) //nolint:errcheck // a test server
}

// open lets the site take top-ups: one EasyPay merchant account in the site's own currency, taking alipay, so the
// order's price needs no exchange rate.
func (g *journeyGateway) open(w *callWorld) {
	g.t.Helper()
	ctx := g.t.Context()
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		g.t.Fatal(err)
	}
	client := egress.NewForTesting(egresstest.Loopback(g.t, g.server.Certificate()))
	g.t.Cleanup(client.CloseIdleConnections)
	pay, err := payments.Open(payments.Config{
		DB: w.db, Platform: pg.PlatformScope(), Keyring: masterKeys(g.t), Plugins: paymentPlugins, Client: client,
	})
	if err != nil {
		g.t.Fatal(err)
	}
	settings := `{"gateway":"` + g.server.URL + `","pid":"1001","mode":"api","cids":{"alipay":"2"},"query":true}`
	if err = w.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := pay.SaveMerchant(ctx, tx, audit.System("journey"), payments.MerchantInput{
			Site: w.site, Kind: "easypay", Name: "Gateway", Enabled: true, Currency: usd, TTLSeconds: 1800,
			Settings: []byte(settings), Secrets: map[string]secret.Secret{"key": secret.New("journey-gateway-key")},
			Methods: []payments.MethodInput{{Method: "alipay", Upstream: "alipay", Enabled: true}},
		})
		return err
	}); err != nil {
		g.t.Fatal(err)
	}
	if err = pgtest.Run(ctx, w.database.Superuser, "INSERT INTO payments.settings (site_id, open) VALUES ('"+w.site.String()+"', true)"); err != nil {
		g.t.Fatal(err)
	}
}
