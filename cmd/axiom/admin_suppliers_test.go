package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

const publicOrigin = "https://8.8.8.8"

// supplierFixture is the admin with an operator who holds the admin role, to work on suppliers through the API. The
// outbound client is the production one: an address it refuses is refused here too, and the site is an address that is
// public, which nothing connects to in these tests.
type supplierFixture struct {
	adminFixture
	s session
}

func newSupplierFixture(t *testing.T) supplierFixture {
	t.Helper()
	f := newAdminFixture(t)
	return supplierFixture{f, f.owner()}
}

const createBody = `{"name":"Acme","kind":"newapi","origin":"` + publicOrigin + `","upstream_account":"ops","settlement_currency":"USD",` +
	`"login":{"access_token":"pat-secret"},"reason":"second source"}`

// connect connects the supplier of createBody and returns it.
func (f supplierFixture) connect() adminapi.Supplier {
	f.t.Helper()
	rec := f.change(f.s, http.MethodPost, "/suppliers", createBody, newKey())
	expectAdmin(f.t, "createSupplier", rec, http.StatusCreated, "")
	var made adminapi.Supplier
	decode(f.t, rec, &made)
	return made
}

// group makes a group of the supplier the way the sync does and returns its ID.
func (f supplierFixture) group(supplier, ref string, present bool) string {
	f.t.Helper()
	id := uuid.NewV7().String()
	f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, present)
		VALUES ('%s', '%s', '%s', 'Group %s', 1.5, %v)`, id, supplier, ref, ref, present))
	return id
}

// TestCreateSupplier: a supplier is connected `connecting`, with its origin as stored, and is listed and read back; the
// login is nowhere in what comes back, in the audit entry or in the database as written.
func TestCreateSupplier(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	made := f.connect()
	want := adminapi.Supplier{
		Id: made.Id, Name: "Acme", Kind: "newapi", Origin: publicOrigin, UpstreamAccount: "ops", NativeUnit: "quota",
		SettlementCurrency: "USD", Status: "connecting",
	}
	if diff := cmp.Diff(want, made); diff != "" {
		t.Errorf("the supplier (-want +got):\n%s", diff)
	}
	rec := f.do(http.MethodGet, "/suppliers/"+made.Id, "", as(f.s))
	expectAdmin(t, "getSupplier", rec, http.StatusOK, "")
	var detail adminapi.SupplierDetail
	decode(t, rec, &detail)
	if diff := cmp.Diff(want, detail.Supplier); diff != "" || len(detail.Groups) != 0 || len(detail.Credentials) != 0 || len(detail.UnboundKeys) != 0 {
		t.Errorf("getSupplier (-want +got):\n%s", diff)
	}
	rec = f.do(http.MethodGet, "/suppliers", "", as(f.s))
	expectAdmin(t, "listSuppliers", rec, http.StatusOK, "")
	var list adminapi.SupplierList
	decode(t, rec, &list)
	if diff := cmp.Diff([]adminapi.Supplier{want}, list.Data); diff != "" || list.NextCursor != nil {
		t.Errorf("listSuppliers (-want +got):\n%s", diff)
	}
	f.expectAudit("supplier.connect", map[string]string{"reason": "second source", "target_id": made.Id})
	if leaked := f.text("SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%pat-secret%' OR detail::text LIKE '%ops%'"); leaked != "0" {
		t.Error("the audit log holds the login or the account")
	}
	if strings.Contains(rec.Body.String(), "pat-secret") {
		t.Error("the login came back")
	}
}

// TestCreateSupplierKeys: the same request again, after a lost response, connects nothing more and answers the same;
// the key of another request is refused; the same origin and account under a new key is a conflict.
func TestCreateSupplierKeys(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/suppliers", createBody, key)
	expectAdmin(t, "createSupplier", rec, http.StatusCreated, "")
	var first, again adminapi.Supplier
	decode(t, rec, &first)
	rec = f.change(f.s, http.MethodPost, "/suppliers", createBody, key)
	expectAdmin(t, "createSupplier again", rec, http.StatusCreated, "")
	decode(t, rec, &again)
	if diff := cmp.Diff(first, again); diff != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-want +got) replayed=%q:\n%s", rec.Header().Get("Idempotency-Replayed"), diff)
	}
	// The login is not in what a key is compared by, so a retry with the same login members and another secret is the same request.
	rec = f.change(f.s, http.MethodPost, "/suppliers", strings.Replace(createBody, "pat-secret", "another-secret", 1), key)
	if expectAdmin(t, "createSupplier with another secret", rec, http.StatusCreated, ""); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("the retry with another secret was not a replay")
	}
	other := strings.Replace(createBody, `"Acme"`, `"Other"`, 1)
	expectAdmin(t, "the key for another request", f.change(f.s, http.MethodPost, "/suppliers", other, key), http.StatusConflict, codeIdempotencyReuse)
	expectAdmin(t, "the same origin and account", f.change(f.s, http.MethodPost, "/suppliers", createBody, newKey()), http.StatusConflict, codeConflict)
	if n := f.text("SELECT count(*)::text FROM suppliers.suppliers"); n != "1" || f.auditCount("supplier.connect") != "1" {
		t.Errorf("%s suppliers, %s audit entries, want 1 of each", n, f.auditCount("supplier.connect"))
	}
}

// TestCreateSupplierRefusals: an origin the outbound client refuses, a kind that cannot be connected, and a login of
// members the kind does not take are refused, each as the contract says, and nothing is made.
func TestCreateSupplierRefusals(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	for name, c := range map[string]struct {
		change func(string) string
		status int
		code   errorCode
		field  string
	}{
		"a private address":           {func(b string) string { return strings.Replace(b, publicOrigin, "https://10.0.0.1", 1) }, 422, codeUnprocessable, ""},
		"a loopback address":          {func(b string) string { return strings.Replace(b, publicOrigin, "https://127.0.0.1", 1) }, 422, codeUnprocessable, ""},
		"a kind that does not exist":  {func(b string) string { return strings.Replace(b, `"newapi"`, `"ghost"`, 1) }, 422, codeUnprocessable, ""},
		"a member the kind lacks":     {func(b string) string { return strings.Replace(b, `"pat-secret"`, `"x","password":"pw"`, 1) }, 400, codeInvalidRequest, "/login/password"},
		"a login without its token":   {func(b string) string { return strings.Replace(b, `{"access_token":"pat-secret"}`, `{}`, 1) }, 400, codeInvalidRequest, "/login/access_token"},
		"a currency that is none":     {func(b string) string { return strings.Replace(b, `"USD"`, `"usd"`, 1) }, 400, codeInvalidRequest, ""},
		"an origin with a path":       {func(b string) string { return strings.Replace(b, publicOrigin, publicOrigin+"/x", 1) }, 400, codeInvalidRequest, ""},
		"an origin that is not https": {func(b string) string { return strings.Replace(b, publicOrigin, "http://8.8.8.8", 1) }, 400, codeInvalidRequest, ""},
	} {
		rec := f.change(f.s, http.MethodPost, "/suppliers", c.change(createBody), newKey())
		expectAdmin(t, name, rec, c.status, c.code)
		if fields := errorDetailOf(t, rec).Fields; c.field != "" && (fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != c.field) {
			t.Errorf("%s: fields %v, want %s", name, fields, c.field)
		}
		if strings.Contains(rec.Body.String(), "pat-secret") || strings.Contains(rec.Body.String(), `"pw"`) {
			t.Errorf("%s: the answer repeats the login: %s", name, rec.Body)
		}
	}
	if n := f.text("SELECT count(*)::text FROM suppliers.suppliers"); n != "0" {
		t.Errorf("%s suppliers made by refused requests", n)
	}
}

// TestSupplierLifecycle: a supplier in use is paused and resumed, its unit value confirmed, its login replaced and
// removed, each answered with the supplier and audited once; a change the status does not allow is a conflict.
func TestSupplierLifecycle(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	made := f.connect()
	path := "/suppliers/" + made.Id
	put := func(what, suffix, body string, status int) adminapi.Supplier {
		rec := f.change(f.s, http.MethodPut, path+suffix, body, newKey())
		expectAdmin(t, what, rec, status, "")
		var s adminapi.Supplier
		decode(t, rec, &s)
		return s
	}
	expectAdmin(t, "pausing a supplier still connecting", f.change(f.s, http.MethodPut, path+"/status", `{"status":"paused","reason":"r"}`, newKey()),
		http.StatusConflict, codeConflict)
	f.run("UPDATE suppliers.suppliers SET status = 'active'")
	for _, status := range []string{"paused", "active"} {
		if s := put("setSupplierStatus "+status, "/status", `{"status":"`+status+`","reason":"r"}`, http.StatusOK); string(s.Status) != status {
			t.Errorf("the status after %s: %s", status, s.Status)
		}
	}
	if s := put("confirmSupplierUnitValue", "/unit-value", `{"value":"7.3","reason":"r"}`, http.StatusOK); s.UnitValue == nil || *s.UnitValue != "7.3" {
		t.Errorf("the unit value: %v", s.UnitValue)
	}
	expectAdmin(t, "a value above 1000", f.change(f.s, http.MethodPut, path+"/unit-value", `{"value":"1001","reason":"r"}`, newKey()),
		http.StatusUnprocessableEntity, codeUnprocessable)
	if s := put("replaceSupplierLogin", "/login", `{"login":{"access_token":"new-secret"},"reason":"rotated"}`, http.StatusOK); s.Status != "connecting" {
		t.Errorf("the status after a new login: %s", s.Status)
	}
	rec := f.change(f.s, http.MethodPost, path+"/removal", `{"reason":"done with it"}`, newKey())
	expectAdmin(t, "removeSupplier", rec, http.StatusOK, "")
	expectAdmin(t, "pausing a removed supplier", f.change(f.s, http.MethodPut, path+"/status", `{"status":"paused","reason":"r"}`, newKey()),
		http.StatusConflict, codeConflict)
	for _, action := range []string{"supplier.pause", "supplier.resume", "supplier.unit_value", "supplier.replace_login", "supplier.remove"} {
		if f.auditCount(action) != "1" {
			t.Errorf("%s audit entries of %s, want 1", f.auditCount(action), action)
		}
	}
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%new-secret%' OR detail::text LIKE '%pat-secret%'"); got != "0" {
		t.Error("the audit log holds a login")
	}
}

// TestSupplierNotFound: an ID that is none, one that is not written the way the API writes it, and one the platform
// lacks, are the same 404 in a path.
func TestSupplierNotFound(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	for _, id := range []string{uuid.NewV7().String(), "not-an-id", strings.ToUpper(uuid.NewV7().String())} {
		expectAdmin(t, "getSupplier "+id, f.do(http.MethodGet, "/suppliers/"+id, "", as(f.s)), http.StatusNotFound, codeNotFound)
		expectAdmin(t, "removeSupplier "+id, f.change(f.s, http.MethodPost, "/suppliers/"+id+"/removal", `{"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	}
}

// TestDetectSupplier: an address the outbound client refuses is a 422 and no probe is made; a session may ask ten times
// a minute and the eleventh is a 429 that says when to come back.
func TestDetectSupplier(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	post := func() *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/suppliers/detections", `{"origin":"https://10.0.0.1"}`, as(f.s))
	}
	for range detectionsPerMinute {
		expectAdmin(t, "detectSupplier", post(), http.StatusUnprocessableEntity, codeUnprocessable)
	}
	rec := post()
	expectAdmin(t, "the eleventh detection", rec, http.StatusTooManyRequests, codeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
}

// TestWindowLimiter: n requests of a key in a window, the next refused with the time to wait, and the window slides.
func TestWindowLimiter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	l := newWindowLimiter(2, time.Minute, func() time.Time { return now })
	a, b := uuid.NewV7(), uuid.NewV7()
	steps := []struct {
		key     uuid.UUID
		advance time.Duration
		retry   time.Duration // zero: let through
	}{
		{a, 0, 0},
		{a, 10 * time.Second, 0},
		{a, 10 * time.Second, 40 * time.Second},
		{b, 0, 0},
		{a, 40 * time.Second, 0},
		{a, 0, 10 * time.Second},
	}
	for i, s := range steps {
		now = now.Add(s.advance)
		err := l.allow(s.key)
		var many *accounts.TooManyAttempts
		switch {
		case s.retry == 0 && err != nil:
			t.Errorf("step %d: refused: %v", i, err)
		case s.retry != 0 && (!errors.As(err, &many) || many.RetryAfter != s.retry):
			t.Errorf("step %d: %v, want a refusal to retry after %v", i, err, s.retry)
		}
	}
}

// TestCreateSupplierRetryAfterTheNetworkChanged: a connection that went through is answered again to its retry, though
// the site's address would be refused now; a new request for it is refused.
func TestCreateSupplierRetryAfterTheNetworkChanged(t *testing.T) {
	t.Parallel()
	c := newCommandLine(t)
	if _, err := c.site(siteArgs...); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now()}
	body := strings.Replace(createBody, publicOrigin, "https://127.0.0.1:8443", 1)
	loopback := startAdminNodeVia(c, clock, egress.NewForTesting(egresstest.Loopback(t)))
	loopback.addUser(ownerEmail, "admin")
	key := newKey()
	owner := loopback.owner()
	expectAdmin(t, "the connection", loopback.change(owner, http.MethodPost, "/suppliers", body, key), http.StatusCreated, "")

	restarted := loopback.restarted()
	f := supplierFixture{restarted, owner}
	rec := f.change(f.s, http.MethodPost, "/suppliers", body, key)
	if expectAdmin(t, "the retry", rec, http.StatusCreated, ""); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("the retry was not a replay")
	}
	expectAdmin(t, "a new request", f.change(f.s, http.MethodPost, "/suppliers", strings.Replace(body, "ops", "other", 1), newKey()),
		http.StatusUnprocessableEntity, codeUnprocessable)
}
