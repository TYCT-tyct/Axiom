package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"

	"axiom/internal/platform/adminapi"
)

func TestAdminSystemSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/sites/" + f.siteID() + "/system-settings"
	body := `{"settings":{"name":"Changed site","registration":"closed"},"reason":"pause registrations"}`
	key := newKey()
	for i := range 2 {
		rec := f.change(s, http.MethodPut, path, body, key)
		expectAdmin(t, "save", rec, 200, "")
		if i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal("not replayed")
		}
	}
	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "Changed site") || !strings.Contains(rec.Body.String(), "closed") {
		t.Fatal(rec.Body.String())
	}
	if f.auditCount("site_system.update") != "1" {
		t.Fatal("replay duplicated audit")
	}
	expectAdmin(t, "bad registration", f.change(s, http.MethodPut, path, strings.Replace(body, "closed", "open", 1), newKey()), 400, "")

	// Changing only the name keeps the registration mode, including open, which this operation cannot set itself.
	f.run(`UPDATE accounts.sites SET registration = 'open' WHERE is_main`)
	rec = f.change(s, http.MethodPut, path, `{"settings":{"name":"Still open"},"reason":"rename"}`, newKey())
	expectAdmin(t, "name only", rec, 200, "")
	checkSystemSettings(t, rec, "Still open", "open")
}

// checkSystemSettings reads a response and checks the name, the registration mode it kept and the name-only capability.
func checkSystemSettings(t *testing.T, rec *httptest.ResponseRecorder, name, registration string) {
	t.Helper()
	var system adminapi.SystemSettings
	decode(t, rec, &system)
	if system.Name == nil || *system.Name != name || system.Registration == nil || string(*system.Registration) != registration {
		t.Fatalf("system settings %s", rec.Body)
	}
	if system.NameEditable == nil || !*system.NameEditable {
		t.Fatalf("a node that accepts name-only saves does not say so: %s", rec.Body)
	}
}

// TestAdminSystemSettingsNameOnlyKeepsConcurrentRegistration: another administrator closes registration after this form
// was filled. A name-only save must not put the mode read with that form back; the site row lock reads and writes the
// current mode in one turn.
func TestAdminSystemSettingsNameOnlyKeepsConcurrentRegistration(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/sites/" + f.siteID() + "/system-settings"
	f.run(`UPDATE accounts.sites SET registration = 'closed' WHERE is_main`)
	rec := f.change(s, http.MethodPut, path, `{"settings":{"name":"Closed meanwhile"},"reason":"rename"}`, newKey())
	expectAdmin(t, "name after a concurrent close", rec, 200, "")
	checkSystemSettings(t, rec, "Closed meanwhile", "closed")
}

func TestAdminPaymentSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	payments := "/sites/" + f.siteID() + "/system-settings/payments"
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"settings":{"open":true,"credits_min":{"amount":"9007199254740993","currency":"USD"},"credits_max":{"amount":"9223372036854775807","currency":"USD"}},"reason":"r"}`, 200},
		{`{"settings":{"open":true,"credits_min":{"amount":"0","currency":"USD"}},"reason":"r"}`, 400},
		{`{"settings":{"open":true,"credits_max":{"amount":"1","currency":"CNY"}},"reason":"r"}`, 400},
		{`{"settings":{"open":false},"reason":"r"}`, 200},
	} {
		expectAdmin(t, "payment settings", f.change(s, http.MethodPut, payments, tc.body, newKey()), tc.code, "")
	}
	checkExactPaymentAudit(t, f, s)
	rec := f.do(http.MethodGet, payments, "", as(s))
	expectAdmin(t, "payment read", rec, 200, "")
	var p adminapi.PaymentSettings
	decode(t, rec, &p)
	if p.Open == nil || *p.Open || p.CreditsMin != nil || p.CreditsMax != nil {
		t.Fatalf("limits not cleared: %+v", p)
	}
}

func TestAdminMailSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/settings/mail"
	body := `{"settings":{"enabled":true,"host":"8.8.8.8","port":587,"username":"test-user","from":"no-reply@example.com","password":"smtp-test-value"},"reason":"configure"}`
	rec := f.change(s, http.MethodPut, path, body, newKey())
	expectAdmin(t, "save mail", rec, 200, "")
	if strings.Contains(rec.Body.String(), "smtp-test-value") {
		t.Fatal("password in response")
	}
	var got adminapi.MailSettings
	decode(t, rec, &got)
	if got.HasPassword == nil || !*got.HasPassword || got.RestartRequired == nil || !*got.RestartRequired {
		t.Fatalf("missing metadata: %+v", got)
	}
	body = strings.Replace(body, `,"password":"smtp-test-value"`, "", 1)
	expectAdmin(t, "retain password", f.change(s, http.MethodPut, path, body, newKey()), 200, "")
	expectAdmin(t, "destination change", f.change(s, http.MethodPut, path, strings.Replace(body, "8.8.8.8", "8.8.4.4", 1), newKey()), 400, "")
	rec = f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read mail", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "8.8.8.8") || strings.Contains(rec.Body.String(), "smtp-test-value") {
		t.Fatal(rec.Body.String())
	}
	if strings.Contains(f.text("SELECT detail::text FROM platform.audit_log WHERE action='smtp_config.update' LIMIT 1"), "smtp-test-value") {
		t.Fatal("secret in audit")
	}
	expectAdmin(t, "disable", f.change(s, http.MethodPut, path, `{"settings":{"enabled":false},"reason":"disable"}`, newKey()), 200, "")
	checkDisabledMailProbe(t, f, s, path)
}

func checkDisabledMailProbe(t *testing.T, f adminFixture, s session, path string) {
	t.Helper()
	key := newKey()
	for i := range 2 {
		rec := f.change(s, http.MethodPost, path+"/tests", `{"to":"recipient@example.com","reason":"probe"}`, key)
		expectAdmin(t, "test disabled", rec, 200, "")
		if !strings.Contains(rec.Body.String(), "disabled") || i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal(rec.Body.String())
		}
	}
	if f.auditCount("smtp_config.test") != "1" {
		t.Fatal("probe replay duplicated audit")
	}
}

func checkExactPaymentAudit(t *testing.T, f adminFixture, s session) {
	t.Helper()
	auditResponse := f.do(http.MethodGet, "/audit?action=payment_settings.update", "", as(s))
	expectAdmin(t, "exact audit detail", auditResponse, 200, "")
	if !strings.Contains(auditResponse.Body.String(), "9007199254740993") {
		t.Fatal("audit rounded amount")
	}
}

func TestSMTPFingerprintExcludesPassword(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	body := `{"settings":{"enabled":true,"host":"8.8.8.8","port":587,"username":"test","from":"no-reply@example.com","password":"first-test-value"},"reason":"configure"}`
	first := newKey()
	expectAdmin(t, "first password", f.change(s, http.MethodPut, "/settings/mail", body, first), 200, "")
	second := strings.Replace(body, "first-test-value", "different-test-value", 1)
	expectAdmin(t, "second password", f.change(s, http.MethodPut, "/settings/mail", second, newKey()), 200, "")
	if got := f.text("SELECT count(DISTINCT fingerprint)::text FROM platform.idempotency WHERE operation='setMailSettings'"); got != "1" {
		t.Fatalf("password changed persisted fingerprint: %s", got)
	}
	replay := f.change(s, http.MethodPut, "/settings/mail", second, first)
	expectAdmin(t, "secret-independent replay", replay, 200, "")
	if replay.Header().Get("Idempotency-Replayed") != "true" || f.auditCount("smtp_config.update") != "2" {
		t.Fatal("replayed credential save changed state")
	}
	withoutPassword := strings.Replace(body, `,"password":"first-test-value"`, "", 1)
	expectAdmin(t, "password presence marker", f.change(s, http.MethodPut, "/settings/mail", withoutPassword, newKey()), 200, "")
	if got := f.text("SELECT count(DISTINCT fingerprint)::text FROM platform.idempotency WHERE operation='setMailSettings'"); got != "2" {
		t.Fatal("fingerprint lost the password presence marker")
	}
}

func TestSMTPDestinationBoundary(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	for _, host := range []string{"127.0.0.1", "10.0.0.5", "169.254.169.254", "::1"} {
		body := fmt.Sprintf(`{"settings":{"enabled":true,"host":%q,"port":6379,"from":"no-reply@example.com"},"reason":"check"}`, host)
		expectAdmin(t, "private SMTP destination", f.change(s, http.MethodPut, "/settings/mail", body, newKey()), 400, "")
	}
	if f.auditCount("smtp_config.update") != "0" {
		t.Fatal("denied SMTP configuration was stored")
	}
	// A configuration saved by an older node must still be checked at the actual dial.
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := mail.SaveConfig(t.Context(), tx, masterKeys(t), audit.System("platform.test"), mail.Settings{Enabled: true, Host: "127.0.0.1", Port: 6379, From: "no-reply@example.com"}, "old node")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.routes = httpserver.Public(f.routes, slog.New(slog.DiscardHandler), time.Second)
	rec := f.change(s, http.MethodPost, "/settings/mail/tests", `{"to":"private-recipient@example.com","reason":"check saved config"}`, newKey())
	expectAdmin(t, "check old unsafe config", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "failed") {
		t.Fatal(rec.Body.String())
	}
	logs := f.logs.String()
	requestID := rec.Header().Get("X-Request-ID")
	if requestID == "" || !strings.Contains(logs, "destination denied") || !strings.Contains(logs, requestID) || strings.Contains(logs, "private-recipient@example.com") {
		t.Fatal("missing or unsafe SMTP diagnostic")
	}
}

//nolint:cyclop,gocognit // the steps are the admin flow and each assertion is one step
func TestAdminSearchSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/settings/search"
	engine := `{"name":"searxng","kind":"searxng","url":"https://8.8.8.8","key":"search-test-value","enabled":true,"default":true,` +
		`"cost_per_search":"0.005","cost_per_extra_result":"0.002","markup":"0.2"}`
	body := `{"engines":[` + engine + `],"reason":"configure"}`
	rec := f.change(s, http.MethodPut, path, body, newKey())
	expectAdmin(t, "save search", rec, 200, "")
	if strings.Contains(rec.Body.String(), "search-test-value") {
		t.Fatal("key in response")
	}
	var got adminapi.SearchSettings
	decode(t, rec, &got)
	if got.Engines == nil || len(*got.Engines) != 1 {
		t.Fatalf("saved engines = %+v", got.Engines)
	}
	engines := *got.Engines
	if !engines[0].HasKey || !engines[0].Default || !engines[0].Enabled ||
		engines[0].PricePerSearch != "0.006" || engines[0].PricePerExtraResult != "0.0024" {
		t.Fatalf("saved engine = %+v", engines[0])
	}
	if got.RestartRequired == nil || !*got.RestartRequired || got.Source == nil || *got.Source != adminapi.SearchSettingsSourceDatabase {
		t.Fatalf("missing metadata: %+v", got)
	}
	// The key stays when the backend does, and is dropped when the destination changes rather than sent to a new host.
	noKey := strings.Replace(body, `,"key":"search-test-value"`, "", 1)
	rec = f.change(s, http.MethodPut, path, noKey, newKey())
	expectAdmin(t, "retain key", rec, 200, "")
	decode(t, rec, &got)
	engines = *got.Engines
	if !engines[0].HasKey {
		t.Fatal("the saved key was not retained")
	}
	rec = f.change(s, http.MethodPut, path, strings.Replace(noKey, "8.8.8.8", "8.8.4.4", 1), newKey())
	expectAdmin(t, "destination change", rec, 200, "")
	decode(t, rec, &got)
	engines = *got.Engines
	if engines[0].HasKey {
		t.Fatal("the old key was reused for a new destination")
	}
	rec = f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read search", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "8.8.4.4") || strings.Contains(rec.Body.String(), "search-test-value") {
		t.Fatal(rec.Body.String())
	}
	if strings.Contains(f.text("SELECT detail::text FROM platform.audit_log WHERE action='search_engines.update' LIMIT 1"), "search-test-value") {
		t.Fatal("secret in audit")
	}
	expectAdmin(t, "disable", f.change(s, http.MethodPut, path, `{"engines":[],"reason":"disable"}`, newKey()), 200, "")
	rec = f.change(s, http.MethodPost, path+"/tests",
		`{"engine":{"name":"searxng","kind":"searxng","enabled":true,"default":true,"cost_per_search":"0","cost_per_extra_result":"0","markup":"0"},"reason":"probe"}`, newKey())
	expectAdmin(t, "test disabled", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatal(rec.Body.String())
	}
	// A page built before engines sends only the reason and tests the saved backend; with the list cleared that is
	// the documented "disabled" answer, not a schema failure.
	rec = f.change(s, http.MethodPost, path+"/tests", `{"reason":"probe old page"}`, newKey())
	expectAdmin(t, "test old page", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatal(rec.Body.String())
	}
	// The old single-backend write shape still saves, and the old read fields describe the default engine.
	rec = f.change(s, http.MethodPut, path, `{"settings":{"url":"https://8.8.8.8"},"reason":"old page"}`, newKey())
	expectAdmin(t, "save old page", rec, 200, "")
	var old adminapi.SearchSettings
	decode(t, rec, &old)
	if old.Url == nil || *old.Url != "https://8.8.8.8" || old.HasKey == nil || *old.HasKey {
		t.Fatalf("old page view = %+v", old)
	}
}

// TestAdminSearchLegacyGetThenSaveKeepsKey is the old page's first round trip: GET returns the legacy config with a
// zero UUID, the page submits it back without a key, and the save must create a new engine rather than look up a row
// that cannot exist and drop the credential.
func TestAdminSearchLegacyGetThenSaveKeepsKey(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	sealed := masterKeys(t).Seal(secret.New(`{"url":"https://8.8.8.8","key":"legacy-key"}`), secret.Location{
		Table: "platform.search_config", RowID: "00000000-0000-0000-0000-000000000000", Purpose: "config",
	})
	f.run(fmt.Sprintf(`INSERT INTO platform.search_config (id,ciphertext,nonce,wrapped_dek,key_version)
VALUES ('00000000-0000-0000-0000-000000000000','\x%x','\x%x','\x%x',%d)`,
		sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion))
	rec := f.do(http.MethodGet, "/settings/search", "", as(s))
	expectAdmin(t, "legacy read", rec, 200, "")
	var view adminapi.SearchSettings
	decode(t, rec, &view)
	if view.Engines == nil || len(*view.Engines) != 1 || (*view.Engines)[0].Id != (adminapi.SearchEngine{}).Id ||
		view.Url == nil || *view.Url != "https://8.8.8.8" || view.HasKey == nil || !*view.HasKey {
		t.Fatalf("legacy view = %+v", view)
	}
	body := fmt.Sprintf(`{"engines":[{"id":"%s","name":"searxng","kind":"searxng","url":"https://8.8.8.8",`+
		`"enabled":true,"default":true,"cost_per_search":"0","cost_per_extra_result":"0","markup":"0"}],"reason":"first save"}`,
		(*view.Engines)[0].Id)
	rec = f.change(s, http.MethodPut, "/settings/search", body, newKey())
	expectAdmin(t, "first save", rec, 200, "")
	var saved adminapi.SearchSettings
	decode(t, rec, &saved)
	if saved.Engines == nil || len(*saved.Engines) != 1 || !(*saved.Engines)[0].HasKey {
		t.Fatalf("first save = %+v", saved)
	}
}

// TestAdminSearchTestRefusesToMoveAKey: a test request that names an existing engine but a new destination must not
// send the saved credential to that destination.
func TestAdminSearchTestRefusesToMoveAKey(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/settings/search"
	body := `{"engines":[{"name":"paid","kind":"firecrawl","url":"https://8.8.8.8","key":"old-key","enabled":true,"default":true,` +
		`"cost_per_search":"0.005","cost_per_extra_result":"0.002","markup":"0.2"}],"reason":"configure"}`
	rec := f.change(s, http.MethodPut, path, body, newKey())
	expectAdmin(t, "save", rec, 200, "")
	var saved adminapi.SearchSettings
	decode(t, rec, &saved)
	if saved.Engines == nil || len(*saved.Engines) != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	id := (*saved.Engines)[0].Id
	test := `{"engine":{"id":"` + id.String() + `","name":"paid","kind":"firecrawl","url":"https://8.8.4.4",` +
		`"enabled":true,"default":true,"cost_per_search":"0.005","cost_per_extra_result":"0.002","markup":"0.2"},"reason":"probe"}`
	rec = f.change(s, http.MethodPost, path+"/tests", test, newKey())
	expectAdmin(t, "moved destination", rec, 400, "")
	if strings.Contains(rec.Body.String(), "old-key") {
		t.Fatal("the response leaked the key")
	}
}
