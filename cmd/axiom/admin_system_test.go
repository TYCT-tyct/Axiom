package main

import (
	"net/http"
	"strings"
	"testing"

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
	body := `{"settings":{"enabled":true,"host":"smtp.example.com","port":587,"username":"test-user","from":"no-reply@example.com","password":"smtp-test-value"},"reason":"configure"}`
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
	expectAdmin(t, "destination change", f.change(s, http.MethodPut, path, strings.Replace(body, "smtp.example.com", "elsewhere.example.com", 1), newKey()), 400, "")
	rec = f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read mail", rec, 200, "")
	if !strings.Contains(rec.Body.String(), "smtp.example.com") || strings.Contains(rec.Body.String(), "smtp-test-value") {
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
