package payments

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
)

func TestPaymentSettingsBoundsAndIsolation(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	scope := siteScope(w.siteA)
	valid := SiteSettings{Open: true, Min: new(int64(9007199254740993)), Max: new(int64(9223372036854775807))}
	if err := w.db.Tx(ctx, scope, func(tx pg.Tx) error { return SetSiteSettings(ctx, tx, admin, w.siteA, valid, "enable") }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		settings SiteSettings
		field    string
	}{
		{SiteSettings{Min: new(int64(0))}, "/credits_min"},
		{SiteSettings{Max: new(int64(-1))}, "/credits_max"},
		{SiteSettings{Min: new(int64(2)), Max: new(int64(1))}, "/credits_max"},
	} {
		err := w.db.Tx(ctx, scope, func(tx pg.Tx) error { return SetSiteSettings(ctx, tx, admin, w.siteA, tc.settings, "invalid") })
		field, ok := errors.AsType[*FieldError](err)
		if !ok || field.Field != tc.field {
			t.Fatalf("invalid setting: %v, want %s", err, tc.field)
		}
	}

	if err := w.db.Tx(ctx, siteScope(w.siteB), func(tx pg.Tx) error { return SetSiteSettings(ctx, tx, admin, w.siteA, SiteSettings{}, "cross site") }); err == nil {
		t.Fatal("cross-site change")
	}
	if got := w.count(scope, `SELECT count(*) FROM platform.audit_log WHERE action='payment_settings.update'
 AND detail->'after' ?& ARRAY['open','credits_min','credits_max']
 AND NOT (detail->'after' ?| ARRAY['Open','Min','Max'])`); got != 1 {
		t.Fatal("payment settings audit does not use the public field names")
	}
	if err := w.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		s, err := ReadSiteSettings(ctx, tx, w.siteA)
		if err == nil && (!s.Open || s.Min == nil || *s.Min != *valid.Min) {
			t.Fatalf("changed: %+v", s)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMerchantConfigQuery(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"accepted", `{"code":1,"pid":"1001"}`, 200, true},
		{"rejected", `{"code":-1,"msg":"echoed-private-value"}`, 200, false},
		{"bad JSON", `<html>echoed-private-value</html>`, 200, false},
		{"no code", `{}`, 200, false},
		{"HTTP error", `{"code":1}`, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := merchantQueryServer(t, tc.body, tc.status)
			defer server.Close()
			ch := easyPayOpen(t, easyPayConfig(t, server.URL, server.Certificate())).(*easyPayChannel)
			err := ch.testConfig(t.Context())
			if (err == nil) != tc.ok {
				t.Fatalf("query %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "echoed-private-value") {
				t.Fatal("echoed gateway body")
			}
		})
	}
}

func merchantQueryServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" || r.URL.Query().Get("act") != "query" || r.URL.Query().Get("pid") != "1001" || r.URL.Query().Get("key") != theKey {
			t.Error("not an authenticated merchant query")
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
}

func TestMerchantConfigTestUsesSavedSettings(t *testing.T) {
	t.Parallel()
	w := newTestWorld(t, EasyPay{})
	server := merchantQueryServer(t, `{"code":1}`, 200)
	t.Cleanup(server.Close)
	cfg := easyPayConfig(t, server.URL, server.Certificate())
	w.p.client = cfg.Client
	in := w.input(w.siteA)
	in.Kind = "easypay"
	in.Settings = cfg.Settings
	merchant := w.mustSave(siteScope(w.siteA), in)
	status, err := testMerchant(t, w, siteScope(w.siteA), MerchantID(merchant.ID))
	if err != nil || status != "accepted" {
		t.Fatalf("test: %s %v", status, err)
	}
	if _, err := testMerchant(t, w, siteScope(w.siteB), MerchantID(merchant.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-site merchant: %v", err)
	}
	if got := w.count(siteScope(w.siteA), "SELECT count(*) FROM payments.orders"); got != 0 {
		t.Fatalf("test created %d orders", got)
	}
	fake := newWorld(t)
	saved := fake.mustSave(siteScope(fake.siteA), fake.input(fake.siteA))
	if status, err := testMerchant(t, fake, siteScope(fake.siteA), MerchantID(saved.ID)); err != nil || status != "unsupported" {
		t.Fatalf("unsupported probe: %s %v", status, err)
	}
}

func testMerchant(t *testing.T, w *world, scope pg.Scope, id MerchantID) (status string, err error) {
	t.Helper()
	err = w.db.Tx(t.Context(), scope, func(tx pg.Tx) (err error) { status, err = w.p.TestMerchant(t.Context(), tx, id); return err })
	return status, err
}

func TestMerchantConfigFailureDiagnostic(t *testing.T) {
	t.Parallel()
	w := newTestWorld(t, EasyPay{})
	server := merchantQueryServer(t, `{"code":-1,"msg":"echoed-private-value"}`, 200)
	t.Cleanup(server.Close)
	cfg := easyPayConfig(t, server.URL, server.Certificate())
	w.p.client = cfg.Client
	in := w.input(w.siteA)
	in.Kind, in.Settings = "easypay", cfg.Settings
	merchant := w.mustSave(siteScope(w.siteA), in)
	id := MerchantID(merchant.ID)
	status, err := testMerchant(t, w, siteScope(w.siteA), id)
	if err != nil || status != "failed" {
		t.Fatalf("test: %s %v", status, err)
	}
	logs := w.logs.String()
	for _, want := range []string{"merchant configuration test failed", id.TypeID(), "request_id=", "gateway_refused"} {
		if want == "" || !strings.Contains(logs, want) {
			t.Fatalf("missing merchant diagnostic %q: %s", want, logs)
		}
	}
	for _, private := range []string{theKey, "echoed-private-value"} {
		if strings.Contains(logs, private) {
			t.Fatal("merchant diagnostic contains a secret or gateway body")
		}
	}
}
