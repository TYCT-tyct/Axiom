package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"

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
	payments := path + "/payments"
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
	rec = f.do(http.MethodGet, payments, "", as(s))
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
