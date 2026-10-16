package main

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"axiom/internal/platform/consoleapi"
)

// endpoint is a request to one operation of the console's API, with a body the contract accepts.
type endpoint struct {
	method, path, body string
}

const (
	someKey  = "/keys/key_00000000000000000000000000"
	someCall = "/calls/0198c0a8-0000-7000-8000-000000000001"
)

// sessionOperations are every operation that needs a session, which a request without one must be refused.
var sessionOperations = []endpoint{
	{http.MethodPost, "/auth/mfa", `{"code":"123456"}`},
	{http.MethodPost, "/auth/sign-out", ""},
	{http.MethodGet, "/session", ""},
	{http.MethodPost, "/auth/totp/setup", ""},
	{http.MethodPost, "/auth/totp/confirm", `{"secret":"JBSWY3DPEHPK3PXP","password":"p","code":"123456"}`},
	{http.MethodPost, "/auth/recovery-codes", `{"code":"123456"}`},
	{http.MethodGet, "/keys", ""},
	{http.MethodPost, "/keys", `{"name":"k"}`},
	{http.MethodGet, someKey, ""},
	{http.MethodPut, someKey, `{"name":"k","disabled":false}`},
	{http.MethodDelete, someKey, ""},
	{http.MethodGet, "/balance", ""},
	{http.MethodGet, "/calls", ""},
	{http.MethodGet, someCall, ""},
}

// TestSessionOperationsNeedASession: every operation that names a session refuses a request without one, or with one
// that was never issued, as unauthenticated, and says nothing about what it would have done.
func TestSessionOperationsNeedASession(t *testing.T) {
	f := newConsoleFixture(t)
	for _, op := range sessionOperations {
		expect(t, op.method+" "+op.path+" without a session", f.do(op.method, op.path, op.body), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
		forged := session{cookie: &http.Cookie{Name: "__Host-axiom_session", Value: strings.Repeat("A", 43)}, csrf: "x"} // #nosec G124 -- a cookie only a test sends
		expect(t, op.method+" "+op.path+" with a forged session", f.do(op.method, op.path, op.body, as(forged)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	}
}

// changing are the operations that change data with a session, each with a way to see whether it did.
var changing = []struct {
	endpoint
	changed func(f consoleFixture, key string) bool
}{
	{endpoint{http.MethodPost, "/keys", `{"name":"made"}`}, func(f consoleFixture, _ string) bool {
		return f.text("SELECT count(*)::text FROM accounts.api_keys WHERE name = 'made'") != "0"
	}},
	{endpoint{http.MethodPut, "", `{"name":"renamed","disabled":true}`}, func(f consoleFixture, _ string) bool {
		return f.text("SELECT count(*)::text FROM accounts.api_keys WHERE name = 'renamed'") != "0"
	}},
	{endpoint{http.MethodDelete, "", ""}, func(f consoleFixture, _ string) bool {
		return f.text("SELECT count(*)::text FROM accounts.api_keys WHERE deleted_at IS NOT NULL") != "0"
	}},
	{endpoint{http.MethodPost, "/auth/sign-out", ""}, func(f consoleFixture, _ string) bool {
		return f.text("SELECT count(*)::text FROM accounts.sessions WHERE revoked_at IS NOT NULL") != "0"
	}},
}

// TestChangesNeedTheSitesOriginAsJSONWithTheToken: a request that changes data and comes without the site's Origin,
// from another or from "null", as something other than JSON, or without or with another CSRF token, is refused as
// origin_rejected and changes nothing; the same request done right works; a read needs none of these.
func TestChangesNeedTheSitesOriginAsJSONWithTheToken(t *testing.T) {
	refusals := map[string]mod{
		"no origin":          func(r *http.Request) { r.Header.Del("Origin") },
		"another origin":     header("Origin", "https://evil.example"),
		"the origin of http": header("Origin", "http://"+consoleHost),
		"a null origin":      header("Origin", "null"),
		"a form":             header("Content-Type", "application/x-www-form-urlencoded"),
		"no content type":    func(r *http.Request) { r.Header.Del("Content-Type") },
		"no token":           func(r *http.Request) { r.Header.Del("X-Csrf-Token") },
		"another token":      header("X-Csrf-Token", "not-the-token"),
	}
	f := newConsoleFixture(t)
	s := f.owner()
	key := f.createKey(s, `{"name":"kept"}`).Key.Id
	for _, c := range changing {
		path := c.path
		if path == "" {
			path = "/keys/" + key
		}
		for name, refusal := range refusals {
			expect(t, c.method+" "+path+" with "+name, f.do(c.method, path, c.body, as(s), refusal), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
			if c.changed(f, key) {
				t.Errorf("%s %s with %s changed something", c.method, path, name)
			}
		}
	}
}

// TestChangesWorkWhenDoneRight pairs the test above: the same requests with everything in order change what they say.
func TestChangesWorkWhenDoneRight(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	key := f.createKey(s, `{"name":"kept"}`).Key.Id
	for _, c := range changing {
		path := c.path
		if path == "" {
			path = "/keys/" + key
		}
		if rec := f.do(c.method, path, c.body, as(s)); rec.Code >= 300 {
			t.Errorf("%s %s: %d %s", c.method, path, rec.Code, rec.Body)
		}
		if !c.changed(f, key) {
			t.Errorf("%s %s changed nothing", c.method, path)
		}
	}
}

// TestReadsNeedNoOrigin: a GET works whatever Origin it carries, which a page of another site cannot read anyway
// (the API sends no CORS headers), but a link or an image tag may fire.
func TestReadsNeedNoOrigin(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	rec := f.do(http.MethodGet, "/session", "", as(s), header("Origin", "https://evil.example"))
	expect(t, "a read from another origin", rec, http.StatusOK, "")
	for name, h := range rec.Header() {
		if strings.HasPrefix(name, "Access-Control-") {
			t.Errorf("the API sent the CORS header %s: %v", name, h)
		}
	}
}

// TestSignInAndRegisterNeedTheOrigin: the operations without a session are protected the same way, by the Origin and
// the content type alone.
func TestSignInAndRegisterNeedTheOrigin(t *testing.T) {
	f := newConsoleFixture(t)
	body := `{"email":"` + ownerEmail + `","password":"` + ownerPassword + `"}`
	for name, refusal := range map[string]mod{
		"another origin": header("Origin", "https://evil.example"),
		"no origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"a form":         header("Content-Type", "text/plain"),
	} {
		expect(t, "sign-in with "+name, f.do(http.MethodPost, "/auth/sign-in", body, refusal), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
		expect(t, "register with "+name, f.do(http.MethodPost, "/auth/register",
			`{"invite_code":"x","email":"a@b.c","password":"another horse battery"}`, refusal), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
	}
}

// TestPermissionsAreCheckedEveryRequest: taking a permission from the role takes effect on the next request, for
// exactly the operations that need it.
func TestPermissionsAreCheckedEveryRequest(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	f.run(`ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY`,
		`UPDATE accounts.roles SET permissions = '{api_keys.view}' WHERE name = 'owner'`,
		`ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY`)
	f = f.restarted()
	for _, c := range []struct {
		endpoint
		status int
	}{
		{endpoint{http.MethodGet, "/keys", ""}, http.StatusOK},
		{endpoint{http.MethodGet, "/session", ""}, http.StatusOK},
		{endpoint{http.MethodPost, "/keys", `{"name":"k"}`}, http.StatusForbidden},
		{endpoint{http.MethodPut, someKey, `{"name":"k","disabled":false}`}, http.StatusForbidden},
		{endpoint{http.MethodDelete, someKey, ""}, http.StatusForbidden},
		{endpoint{http.MethodGet, "/balance", ""}, http.StatusForbidden},
		{endpoint{http.MethodGet, "/calls", ""}, http.StatusForbidden},
		{endpoint{http.MethodGet, someCall, ""}, http.StatusForbidden},
	} {
		rec := f.do(c.method, c.path, c.body, as(s))
		want := consoleapi.ErrorCode("")
		if c.status == http.StatusForbidden {
			want = consoleapi.ErrorCodeForbidden
		}
		expect(t, c.method+" "+c.path+" without the permission", rec, c.status, want)
	}
	var info consoleapi.Session
	decode(t, f.do(http.MethodGet, "/session", "", as(s)), &info)
	if len(info.Permissions) != 1 || info.Permissions[0] != "api_keys.view" {
		t.Errorf("the session's permissions: %v", info.Permissions)
	}
}

// TestSessionsEnd: a session past its end, or idle for longer than the site allows, is no session.
func TestSessionsEnd(t *testing.T) {
	for name, statement := range map[string]string{
		"expired": `UPDATE accounts.sessions SET expires_at = now() - interval '1 minute'`,
		"idle":    `UPDATE accounts.sessions SET last_seen_at = now() - interval '30 days'`,
		"revoked": `UPDATE accounts.sessions SET revoked_at = now()`,
	} {
		f := newConsoleFixture(t)
		s := f.owner()
		expect(t, "before", f.do(http.MethodGet, "/session", "", as(s)), http.StatusOK, "")
		f.run(statement)
		expect(t, name+" session", f.do(http.MethodGet, "/session", "", as(s)), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	}
}

// TestHostsOfNoConsole: a request is a console's only at a domain registered for the console of an active site. At any
// other host, an admin's or an API's domain among them, every operation but the version is a 404.
func TestHostsOfNoConsole(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	for _, host := range []string{"unknown.example", "admin.main.example", "api.main.example", "127.0.0.1:8080"} {
		for _, op := range append([]endpoint{{http.MethodGet, "/site", ""}, {http.MethodPost, "/auth/sign-in", `{"email":"abc","password":"p"}`}}, sessionOperations...) {
			expect(t, op.method+" "+op.path+" at "+host, f.do(op.method, op.path, op.body, as(s), atHost(host)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
		}
		expect(t, "the version at "+host, f.do(http.MethodGet, "/version", "", atHost(host)), http.StatusOK, "")
	}
	var site consoleapi.Site
	decode(t, f.do(http.MethodGet, "/site", ""), &site)
	if site.Name != "Main" || site.KeyPrefix != "sk-ax-v1-" || site.Registration != consoleapi.SiteRegistration("invite") || site.LogoUrl != nil {
		t.Errorf("the site: %+v", site)
	}
	f.run(`UPDATE accounts.site_domains SET disabled_at = now() WHERE domain = '` + consoleHost + `'`)
	f = f.restarted()
	expect(t, "a disabled domain", f.do(http.MethodGet, "/site", ""), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
}

// TestClientIP: only the connection's address is believed unless it is a trusted proxy, and then the header is read
// from its right past the proxies; a header that cannot be read is not believed.
func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.9/32")}
	cases := []struct {
		name, remote string
		forwarded    []string
		want         string
	}{
		{"no proxies", "203.0.113.5:1", []string{"198.51.100.1"}, "203.0.113.5"},
		{"a proxy that forwards", "10.1.1.1:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"the client's own claims are left of the proxy's", "10.1.1.1:1", []string{"6.6.6.6, 198.51.100.1"}, "198.51.100.1"},
		{"a chain of proxies", "10.1.1.1:1", []string{"198.51.100.1, 10.2.2.2", "192.0.2.9"}, "198.51.100.1"},
		{"all proxies", "10.1.1.1:1", []string{"10.2.2.2"}, "10.1.1.1"},
		{"no header", "10.1.1.1:1", nil, "10.1.1.1"},
		{"an unreadable header", "10.1.1.1:1", []string{"198.51.100.1, garbage"}, "10.1.1.1"},
		{"v6 mapped v4", "[::ffff:10.1.1.1]:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"an address that is no address", "pipe", []string{"198.51.100.1"}, ""},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{"X-Forwarded-For": c.forwarded}}
		trusted := proxies
		if c.name == "no proxies" {
			trusted = nil
		}
		got := ""
		if ip := clientIP(r, trusted); ip.IsValid() {
			got = ip.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
