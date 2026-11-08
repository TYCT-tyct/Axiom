package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP, which authenticator apps implement, is HMAC-SHA1
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/payments"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/quality"
)

const (
	consoleOrigin = "https://" + consoleHost
	ownerEmail    = "owner@example.com"
	ownerPassword = "a first horse battery"
	basePath      = "/api/console/v1"
)

// testClock is the accounts module's clock in a test, which moves only when the test says.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *testClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

// consoleFixture is the console's API on a database that the command line made a site and an owner on, as a
// deployment is started: requests go through publicRoutes with the real accounts module behind it.
type consoleFixture struct {
	commandLine
	svc    *accounts.Service
	routes http.Handler
	clock  *testClock
	logs   *bytes.Buffer
	// pay is the payments module behind the top-up operations, with the one channel kind a test has: gateway.
	pay     *payments.Payments
	gateway *fakeGateway
	queue   *pg.Queue
}

func newConsoleFixture(t *testing.T) consoleFixture {
	t.Helper()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	return startNode(c, &testClock{now: time.Now()})
}

// startNode starts a node of the program on the command line's database: the accounts module, which has read the
// sites, domains and roles as they are now, and the console's routes in front of it, with the program's request
// deadline.
func startNode(c commandLine, clock *testClock) consoleFixture {
	return startNodeWith(c, clock, false)
}

// startNodeWithRegistration starts a node with the open-registration worker, which a test that registers by email
// needs; the queue is not started, so a test starts it when it is ready.
func startNodeWithRegistration(c commandLine, clock *testClock) consoleFixture {
	return startNodeWith(c, clock, true)
}

func startNodeWith(c commandLine, clock *testClock, withRegistration bool) consoleFixture {
	return startNodeWithModels(c, clock, withRegistration, nil)
}

// startNodeWithModels starts a node with the given public projection, or the empty list the program starts with. A
// test that checks which audience a page asked as passes a fake; every other test gets an empty real list.
func startNodeWithModels(c commandLine, clock *testClock, withRegistration bool, models modelProjection) consoleFixture {
	c.t.Helper()
	if models == nil {
		models = gateway.NewModelList()
	}
	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	config := accounts.Config{
		Platform: pg.PlatformScope(), Hashes: macKeys(c.t), Keyring: masterKeys(c.t), Now: clock.Now, OnAccountOpened: openBalance, KeyPreset: keyPreset,
	}
	var queue *pg.Queue
	if withRegistration {
		queue = c.db.NewQueue(logger, pg.PlatformScope(), nil)
		mailer, err := axmail.Register(c.t.Context(), c.db, queue, axmail.Config{
			Host: "localhost", Port: 1, From: mail.Address{Address: "no-reply@example.com"},
		}, nil, nil, logger)
		if err != nil {
			c.t.Fatal(err)
		}
		registration, err := accounts.RegisterRegistration(c.db, queue, mailer, pg.PlatformScope(), logger)
		if err != nil {
			c.t.Fatal(err)
		}
		config.Registration = registration
	}
	svc, err := accounts.NewService(c.db, config)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := svc.Load(c.t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		c.t.Fatal(err)
	}
	gateway := new(fakeGateway)
	pay, err := payments.Open(payments.Config{
		DB: c.db, Platform: pg.PlatformScope(), Keyring: masterKeys(c.t), Plugins: []payments.Plugin{gatewayPlugin{gateway}},
	})
	if err != nil {
		c.t.Fatal(err)
	}
	exit, err := egress.New(egress.Direct)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(exit.CloseIdleConnections)
	cat, err := catalog.Open(c.db, pg.PlatformScope(), catalog.Config{Logger: logger})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := cat.Refresh(c.t.Context()); err != nil {
		c.t.Fatal(err)
	}
	classes := quality.New(nil)
	if err := classes.SetSourceClasses(quality.DefaultSourceClasses()); err != nil {
		c.t.Fatal(err)
	}
	routes := publicRoutes(consoleAPI{charges: billing.NewChargeReader(logger, nil), exit: exit, version: "test", accounts: svc, payments: pay, catalog: cat, db: c.db, models: models, classes: classes, router: checkRouter(c.t)}, &guard{contract: loadedContract(c.t), accounts: svc, logger: logger}, testFrontend(c.t), logger)
	return consoleFixture{commandLine: c, svc: svc, routes: routes, clock: clock, logs: logs, pay: pay, gateway: gateway, queue: queue}
}

// restarted is a new node on the same database, as the reload loop would make the old one after 30 seconds: it sees
// the roles and domains as they are now.
func (f consoleFixture) restarted() consoleFixture { return startNode(f.commandLine, f.clock) }

// session is what a page holds after signing in: the cookie it cannot read and the CSRF token it can.
type session struct {
	cookie *http.Cookie
	csrf   string
}

// mod changes a request before it is made.
type mod func(*http.Request)

// as makes the request from the session.
func as(s session) mod {
	return func(r *http.Request) {
		r.AddCookie(s.cookie)
		r.Header.Set("X-Csrf-Token", s.csrf)
	}
}

func header(name, value string) mod { return func(r *http.Request) { r.Header.Set(name, value) } }

func atHost(host string) mod { return func(r *http.Request) { r.Host = host } }

// do makes a request to the console as a page of its origin would: a body is JSON, and a request that changes data
// carries the console's Origin.
func (f consoleFixture) do(method, path, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.doWith(f.t.Context(), method, path, body, mods...)
}

// doWith is do on ctx, for a test that sets the request's deadline.
func (f consoleFixture) doWith(ctx context.Context, method, path, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequestWithContext(ctx, method, "https://"+consoleHost+basePath+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:4000"
	if method != http.MethodGet {
		r.Header.Set("Origin", consoleOrigin)
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(r)
	}
	rec := httptest.NewRecorder()
	f.routes.ServeHTTP(rec, r)
	return rec
}

// signIn signs the owner in, or a user of the given email and password, and returns the session.
func (f consoleFixture) signIn(email, password string, mods ...mod) session {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+email+`","password":"`+password+`"}`, mods...)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}
	return f.sessionOf(rec)
}

// sessionOf is the session a sign-in or verification response set.
func (f consoleFixture) sessionOf(rec *httptest.ResponseRecorder) session {
	f.t.Helper()
	var grant consoleapi.SessionGrant
	decode(f.t, rec, &grant)
	for _, c := range rec.Result().Cookies() {
		if strings.HasSuffix(c.Name, "axiom_session") && c.MaxAge >= 0 && c.Value != "" {
			return session{c, grant.CsrfToken}
		}
	}
	f.t.Fatalf("no session cookie in %v", rec.Header())
	return session{}
}

// owner is the owner signed in.
func (f consoleFixture) owner() session { return f.signIn(ownerEmail, ownerPassword) }

// decodeBytes reads JSON into v.
func decodeBytes(body []byte, v any) error { return jsonx.Unmarshal(body, v) }

// decode reads the response's JSON body into v.
func decode(t testing.TB, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := jsonx.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("response %d %q is not the expected JSON: %v", rec.Code, rec.Body, err)
	}
}

// errorOf reads the response as the contract's error body.
func errorOf(t testing.TB, rec *httptest.ResponseRecorder) consoleapi.ErrorDetail {
	t.Helper()
	var body consoleapi.Error
	decode(t, rec, &body)
	return body.Error
}

// expect checks the status and, for a failure, the code of a response.
func expect(t testing.TB, what string, rec *httptest.ResponseRecorder, status int, code consoleapi.ErrorCode) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("%s: %d %s, want %d", what, rec.Code, rec.Body, status)
		return
	}
	if code != "" {
		if got := errorOf(t, rec).Code; got != code {
			t.Errorf("%s: code %q, want %q", what, got, code)
		}
	}
}

// totpCode is the code an authenticator app shows for the base32 secret at the time (RFC 6238).
func totpCode(t testing.TB, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha1.New, key)
	mac.Write(binary.BigEndian.AppendUint64(nil, uint64(at.Unix()/30))) // #nosec G115 -- a time since 1970 is positive
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1_000_000)
}
