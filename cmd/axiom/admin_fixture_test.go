package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers"
)

const (
	adminHost   = "admin.main.example"
	adminOrigin = "https://" + adminHost
	adminBase   = "/api/admin/v1"
)

// adminFixture is the admin's API on a database that the command line made a site on, as a deployment is started:
// requests go through adminRoutes with the real accounts module behind it, and operators are made the way
// `axiom user` makes them.
type adminFixture struct {
	commandLine
	svc       *accounts.Service
	routes    http.Handler
	clock     *testClock
	logs      *bytes.Buffer
	upstreams *suppliers.Service
}

// newAdminFixture is the main site with its admin domain and one operator, the owner, who holds the admin role.
func newAdminFixture(t *testing.T) adminFixture {
	t.Helper()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	f := startAdminNode(c, &testClock{now: time.Now()})
	if _, err := f.user("", "grant-platform", "--email", ownerEmail, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	return f
}

// startAdminNode starts a node of the program on the command line's database that serves the admin listener.
func startAdminNode(c commandLine, clock *testClock) adminFixture {
	c.t.Helper()
	exit, err := egress.New(egress.Direct)
	if err != nil {
		c.t.Fatal(err)
	}
	return startAdminNodeVia(c, clock, exit)
}

// startAdminNodeVia is startAdminNode with the outbound client the node's suppliers go through.
func startAdminNodeVia(c commandLine, clock *testClock, exit *egress.Client) adminFixture {
	c.t.Helper()
	svc, err := accounts.NewService(c.db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: macKeys(c.t), Keyring: masterKeys(c.t), Now: clock.Now, OnAccountOpened: openBalance,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := svc.Load(c.t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		c.t.Fatal(err)
	}
	contract, err := adminContract()
	if err != nil {
		c.t.Fatal(err)
	}
	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	cat, err := catalog.Open(c.db, pg.PlatformScope(), catalog.Config{Logger: logger})
	if err != nil {
		c.t.Fatal(err)
	}
	supplierAdmin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: masterKeys(c.t), Egress: exit})
	if err != nil {
		c.t.Fatal(err)
	}
	upstreams, err := suppliers.New(suppliers.Config{
		DB: c.db, Scope: pg.PlatformScope(), Keys: masterKeys(c.t), Egress: exit, Logger: logger, Clock: clock.Now,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	routes := adminRoutes(newAdminAPI("test", svc, cat, supplierAdmin, upstreams, c.db, logger), &guard{contract: contract, accounts: svc, logger: logger, admin: true}, testFrontend(c.t), logger)
	return adminFixture{commandLine: c, svc: svc, routes: routes, clock: clock, logs: logs, upstreams: upstreams}
}

// restarted is a new node on the same database, as the reload loop would make the old one after 30 seconds.
func (f adminFixture) restarted() adminFixture { return startAdminNode(f.commandLine, f.clock) }

// addUser makes a user of the main site with the shared password, and grants them the platform role when it is named.
func (f adminFixture) addUser(email, role string) {
	f.t.Helper()
	if _, err := f.user(ownerPassword+"\n", "create", "--email", email); err != nil {
		f.t.Fatal(err)
	}
	if role == "" {
		return
	}
	if _, err := f.user("", "grant-platform", "--email", email, "--role", role); err != nil {
		f.t.Fatal(err)
	}
}

// run executes statements as the database's superuser, who is bound by no row-level security.
func (f adminFixture) run(statements ...string) {
	f.t.Helper()
	if err := pgtest.Run(f.t.Context(), f.database.Superuser, statements...); err != nil {
		f.t.Fatal(err)
	}
}

// do makes a request to the admin as a page of its origin would: a body is JSON, and a request that changes data
// carries the admin's Origin.
func (f adminFixture) do(method, path, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.doContext(f.t.Context(), method, path, body, mods...)
}

func (f adminFixture) doContext(ctx context.Context, method, path, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequestWithContext(ctx, method, "https://"+adminHost+adminBase+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:4000"
	if method != http.MethodGet {
		r.Header.Set("Origin", adminOrigin)
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mods {
		m(r)
	}
	rec := httptest.NewRecorder()
	f.routes.ServeHTTP(rec, r)
	return rec
}

// sessionOf is the session a sign-in or a verification response set.
func (f adminFixture) sessionOf(rec *httptest.ResponseRecorder) session {
	f.t.Helper()
	var grant adminapi.SessionGrant
	decode(f.t, rec, &grant)
	for _, c := range rec.Result().Cookies() {
		if strings.HasSuffix(c.Name, "axiom_session") && c.MaxAge >= 0 && c.Value != "" {
			return session{c, grant.CsrfToken}
		}
	}
	f.t.Fatalf("no session cookie in %v", rec.Header())
	return session{}
}

// signIn signs a user in on the admin domain with the shared password; the session proves only the password.
func (f adminFixture) signIn(email string) session {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+email+`","password":"`+ownerPassword+`"}`)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}
	return f.sessionOf(rec)
}

// verified signs the user in and turns on two-step verification, which ends the session with one that has passed it:
// what every operation of the admin needs.
func (f adminFixture) verified(email string) session {
	f.t.Helper()
	s := f.signIn(email)
	rec := f.do(http.MethodPost, "/auth/totp/setup", "", as(s))
	expectAdmin(f.t, "beginTotpSetup", rec, http.StatusOK, "")
	var setup adminapi.TotpSetup
	decode(f.t, rec, &setup)
	confirm := `{"secret":"` + setup.Secret + `","password":"` + ownerPassword + `","code":"` + totpCode(f.t, setup.Secret, f.clock.Now()) + `"}`
	rec = f.do(http.MethodPost, "/auth/totp/confirm", confirm, as(s))
	expectAdmin(f.t, "confirmTotpSetup", rec, http.StatusOK, "")
	var changed adminapi.FactorChange
	decode(f.t, rec, &changed)
	return session{rec.Result().Cookies()[0], changed.CsrfToken}
}

// owner is the owner, who holds the admin role, with a session that passed two-step verification.
func (f adminFixture) owner() session { return f.verified(ownerEmail) }

// idempotent sends the Idempotency-Key of an operation that changes data.
func idempotent(key string) mod { return header("Idempotency-Key", key) }

// newKey is a fresh Idempotency-Key.
func newKey() string { return uuid.NewV7().String() }

// errorDetailOf reads the response as the contract's error body.
func errorDetailOf(t testing.TB, rec *httptest.ResponseRecorder) errorDetail {
	t.Helper()
	var body errorBody
	decode(t, rec, &body)
	return body.Error
}

// expectAdmin checks the status and, for a failure, the code of a response.
func expectAdmin(t testing.TB, what string, rec *httptest.ResponseRecorder, status int, code errorCode) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("%s: %d %s, want %d", what, rec.Code, rec.Body, status)
		return
	}
	if code != "" {
		if got := errorDetailOf(t, rec).Code; got != code {
			t.Errorf("%s: code %q, want %q", what, got, code)
		}
	}
}

// auditCount is how many audit entries have the action.
func (f adminFixture) auditCount(action string) string {
	f.t.Helper()
	return f.text("SELECT count(*)::text FROM platform.audit_log WHERE action = '" + action + "'")
}

// siteID is the main site's ID as the API writes it.
func (f adminFixture) siteID() string {
	f.t.Helper()
	site, _, err := f.svc.SiteForHost(adminHost)
	if err != nil {
		f.t.Fatal(err)
	}
	return site.ID.TypeID()
}

// accountOf is the ID, as the API writes it, of the personal account of the user with the email.
func (f adminFixture) accountOf(email string) string {
	f.t.Helper()
	id, err := uuid.Parse(f.text("SELECT account_id::text FROM accounts.users WHERE email = '" + email + "'"))
	if err != nil {
		f.t.Fatal(err)
	}
	return accounts.AccountID(id).TypeID()
}

// userOf is the ID, as the API writes it, of the user with the email.
func (f adminFixture) userOf(email string) string {
	f.t.Helper()
	id, err := uuid.Parse(f.text("SELECT id::text FROM accounts.users WHERE email = '" + email + "'"))
	if err != nil {
		f.t.Fatal(err)
	}
	return accounts.UserID(id).TypeID()
}

// change sends a request that changes data with the session and a key and returns the response.
func (f adminFixture) change(s session, method, path, body, key string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(method, path, body, as(s), idempotent(key))
}

// expectAudit checks the one audit entry of the action: each key is an expression over its columns, each value what it
// must read.
func (f adminFixture) expectAudit(action string, want map[string]string) {
	f.t.Helper()
	if n := f.auditCount(action); n != "1" {
		f.t.Errorf("%s audit entries of %s, want 1", n, action)
		return
	}
	for expression, value := range want {
		if got := f.text("SELECT " + expression + " FROM platform.audit_log WHERE action = '" + action + "'"); got != value {
			f.t.Errorf("the audit entry of %s: %s is %q, want %q", action, expression, got, value)
		}
	}
}
