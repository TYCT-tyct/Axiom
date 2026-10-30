package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
)

const merchantSecret = "fake-gateway-key-0123456789"

func merchantJSON(site, name string, enabled bool, rev int64, feeFixed string, withSecrets bool) string {
	siteField := ""
	if site != "" {
		siteField = fmt.Sprintf(`"site_id": %q,`, site)
	}
	revField := ""
	if rev > 0 {
		revField = fmt.Sprintf(`"rev": %d,`, rev)
	}
	secrets := ""
	if withSecrets {
		secrets = fmt.Sprintf(`"secrets": {"key": %q},`, merchantSecret)
	}
	return fmt.Sprintf(`{
		%s"merchant": {
			%s"kind": "easypay", "name": %q, "enabled": %t, "currency": "USD", "ttl_seconds": 1800,
			"refund_enabled": false, "user_refund": false, "sort": 1,
			"settings": {"gateway": "https://pay.example.com", "pid": "1001", "mode": "redirect"},
			%s"methods": [{"method": "alipay", "upstream": "alipay", "fee_ppm": 30000,
				"fee_fixed": {"amount": %q, "currency": "USD"}, "enabled": true}]
		}, "reason": "configured the gateway for the site"
	}`, siteField, revField, name, enabled, secrets, feeFixed)
}

func TestAdminMerchantLifecycle(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	created := createMerchant(t, f, s, site)
	updateMerchant(t, f, s, created)
	disableMerchant(t, f, s, created)
	expectAdmin(t, "unknown merchant", f.do(http.MethodGet, "/payments/merchants/mch_00000000000000000000000000", "", as(s)), http.StatusNotFound, codeNotFound)
}

// createMerchant creates one, proves the key replays as one change, and reads it back without secrets.
func createMerchant(t *testing.T, f adminFixture, s session, site string) adminapi.Merchant {
	t.Helper()
	key := newKey()
	rec := f.change(s, http.MethodPost, "/payments/merchants", merchantJSON(site, "Gateway", false, 0, "0", true), key)
	expectAdmin(t, "create merchant", rec, http.StatusCreated, "")
	var created adminapi.Merchant
	decode(t, rec, &created)
	if created.Id == nil || created.Rev == nil || *created.Rev != 1 || created.Enabled == nil || *created.Enabled {
		t.Fatalf("created merchant %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), merchantSecret) || strings.Contains(rec.Body.String(), "secrets") {
		t.Fatalf("the response repeats a secret: %s", rec.Body)
	}

	// The same key and request are one change: the second gets the first answer and creates nothing.
	replay := f.change(s, http.MethodPost, "/payments/merchants", merchantJSON(site, "Gateway", false, 0, "0", true), key)
	expectAdmin(t, "replay merchant", replay, http.StatusCreated, "")
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("merchant creation did not replay")
	}
	var replayed adminapi.Merchant
	decode(t, replay, &replayed)
	if replayed.Id == nil || *replayed.Id != *created.Id {
		t.Fatalf("replay made another merchant: %s", replay.Body)
	}

	// A different request with that key is refused rather than done.
	expectAdmin(t, "reuse key", f.change(s, http.MethodPost, "/payments/merchants", merchantJSON(site, "Other", false, 0, "0", true), key), http.StatusConflict, codeIdempotencyReuse)

	checkMerchantList(t, f, s, site)
	checkMerchantSealed(t, f)
	return created
}

// checkMerchantList reads the site's accounts back and checks they carry methods but no secrets.
func checkMerchantList(t *testing.T, f adminFixture, s session, site string) {
	t.Helper()
	rec := f.do(http.MethodGet, "/payments/merchants?site_id="+site, "", as(s))
	expectAdmin(t, "list merchants", rec, http.StatusOK, "")
	var list adminapi.MerchantList
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 1 || (*list.Data)[0].Methods == nil || len(*(*list.Data)[0].Methods) != 1 {
		t.Fatalf("merchant list %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), merchantSecret) {
		t.Fatal("the list repeats a secret")
	}
}

// checkMerchantSealed proves neither the stored configuration nor the audit detail holds the key.
func checkMerchantSealed(t *testing.T, f adminFixture) {
	t.Helper()
	if got := f.text(fmt.Sprintf(`SELECT count(*)::text FROM payments.merchant_configs WHERE encode(ciphertext,'escape') LIKE '%%%s%%'`, merchantSecret)); got != "0" {
		t.Fatalf("the secret is stored in clear: %s", got)
	}
	f.expectAudit("merchant.create", map[string]string{
		"detail->>'currency'":   "USD",
		"detail->>'enabled'":    "false",
		"detail->>'kind'":       "easypay",
		"detail->>'name'":       "Gateway",
		"detail->'methods'->>0": "alipay",
	})
	if got := f.text(fmt.Sprintf(`SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%%%s%%'`, merchantSecret)); got != "0" {
		t.Fatalf("the audit log holds the secret: %s", got)
	}
}

// updateMerchant changes it as of the revision it read, refuses a stale one and answers a bad amount by pointer.
func updateMerchant(t *testing.T, f adminFixture, s session, created adminapi.Merchant) {
	t.Helper()
	path := "/payments/merchants/" + *created.Id
	update := merchantJSON("", "Renamed", true, 1, "0", false)
	rec := f.change(s, http.MethodPut, path, update, newKey())
	expectAdmin(t, "update merchant", rec, http.StatusOK, "")
	var updated adminapi.Merchant
	decode(t, rec, &updated)
	if updated.Name == nil || *updated.Name != "Renamed" || updated.Rev == nil || *updated.Rev != 2 {
		t.Fatalf("updated merchant %s", rec.Body)
	}
	expectAdmin(t, "stale update", f.change(s, http.MethodPut, path, update, newKey()), http.StatusConflict, codeStaleRevision)

	// A malformed amount is refused by pointer, not by a bare 400.
	rec = f.change(s, http.MethodPut, path, merchantJSON("", "Renamed", false, 2, "1", false), newKey())
	expectAdmin(t, "amount not whole minor units", rec, http.StatusUnprocessableEntity, codeUnprocessable)
	if fields := errorDetailOf(t, rec).Fields; fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != "/merchant/methods/0/fee_fixed/amount" {
		t.Fatalf("field pointer %s", rec.Body)
	}
}

// disableMerchant keeps its configuration and seals and stops new orders choosing it.
func disableMerchant(t *testing.T, f adminFixture, s session, created adminapi.Merchant) {
	t.Helper()
	rec := f.change(s, http.MethodPost, "/payments/merchants/"+*created.Id+"/disable", `{"reason":"stop new orders"}`, newKey())
	expectAdmin(t, "disable merchant", rec, http.StatusOK, "")
	var disabled adminapi.Merchant
	decode(t, rec, &disabled)
	if disabled.Enabled == nil || *disabled.Enabled || disabled.Rev == nil || *disabled.Rev != 3 {
		t.Fatalf("disabled merchant %s", rec.Body)
	}
	if disabled.Settings == nil || disabled.Methods == nil || len(*disabled.Methods) != 1 {
		t.Fatalf("disable lost the configuration: %s", rec.Body)
	}
	f.expectAudit("merchant.disable", map[string]string{"reason": "stop new orders"})
}

func TestAdminPaymentSettingsTopUpBuffer(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	path := "/sites/" + site + "/system-settings/payments"

	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read payment settings", rec, http.StatusOK, "")
	var view adminapi.PaymentSettings
	decode(t, rec, &view)
	if view.TopUpBufferPpm == nil || *view.TopUpBufferPpm != 20_000 {
		t.Fatalf("default buffer %s", rec.Body)
	}

	body := `{"settings":{"open":true,"top_up_buffer_ppm":35000},"reason":"a wider buffer for this site"}`
	rec = f.change(s, http.MethodPut, path, body, newKey())
	expectAdmin(t, "set payment settings", rec, http.StatusOK, "")
	decode(t, rec, &view)
	if view.TopUpBufferPpm == nil || *view.TopUpBufferPpm != 35_000 {
		t.Fatalf("saved buffer %s", rec.Body)
	}
	f.expectAudit("billing.top_up_buffer.update", map[string]string{"detail::text": `{"after": 35000, "before": 20000}`})

	// An update without the buffer leaves it as it is.
	rec = f.change(s, http.MethodPut, path, `{"settings":{"open":false},"reason":"close collection"}`, newKey())
	expectAdmin(t, "set payment settings without buffer", rec, http.StatusOK, "")
	decode(t, rec, &view)
	if view.TopUpBufferPpm == nil || *view.TopUpBufferPpm != 35_000 {
		t.Fatalf("buffer after another save %s", rec.Body)
	}
}
