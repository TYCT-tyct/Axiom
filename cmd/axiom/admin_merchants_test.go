package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/typeid"
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

// epusdtMerchantJSON is a merchant account whose Epusdt preset probes the gateway on every enabled save.
func epusdtMerchantJSON(site, name string, enabled bool, rev int64, gateway string, withSecrets bool) string {
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
			"settings": {"preset": "epusdt", "gateway": %q, "pid": "1001", "mode": "redirect"},
			%s"methods": [{"method": "usdt.tron", "upstream": "usdt.tron", "fee_ppm": 0,
				"fee_fixed": {"amount": "0", "currency": "USD"}, "enabled": true}]
		}, "reason": "switched the account on"
	}`, siteField, revField, name, enabled, gateway, secrets)
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
		"reason":                "configured the gateway for the site",
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
	f.expectAudit("merchant.update", map[string]string{
		"reason":                "configured the gateway for the site",
		"detail->>'name'":       "Renamed",
		"detail->>'enabled'":    "true",
		"detail->'methods'->>0": "alipay",
	})
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

// TestAdminMerchantReplaySurvivesLaterChanges: a lost answer is replayed from its claim even after the merchant has
// changed since. Preparation and probing run only when the change still has to happen, so a stale revision of the
// original request cannot turn its replay into a 409.
func TestAdminMerchantReplaySurvivesLaterChanges(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	created := createMerchant(t, f, s, f.siteID())
	path := "/payments/merchants/" + *created.Id
	update := merchantJSON("", "Renamed", true, 1, "0", false)
	key := newKey()
	first := f.change(s, http.MethodPut, path, update, key)
	expectAdmin(t, "update merchant", first, http.StatusOK, "")

	// A later change moves the revision past the one the update carried.
	expectAdmin(t, "disable merchant", f.change(s, http.MethodPost, path+"/disable", `{"reason":"stop new orders"}`, newKey()), http.StatusOK, "")

	again := f.change(s, http.MethodPut, path, update, key)
	expectAdmin(t, "replay after a later change", again, http.StatusOK, "")
	if again.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("the update did not replay")
	}
	var replayed adminapi.Merchant
	decode(t, again, &replayed)
	if replayed.Name == nil || *replayed.Name != "Renamed" || replayed.Rev == nil || *replayed.Rev != 2 {
		t.Fatalf("replayed merchant %s", again.Body)
	}
	if n := f.auditCount("merchant.update"); n != "1" {
		t.Errorf("%s merchant.update audit entries, want 1", n)
	}
}

// TestAdminMerchantProbeDoesNotHoldTheWriteTransaction: with one ordinary connection, an enabled Epusdt save still
// reaches the gateway probe. Holding the idempotency transaction while preparing would use both the connection and the
// time, so the probe would never start (and a real save would hold a database connection across the network call).
func TestAdminMerchantProbeDoesNotHoldTheWriteTransaction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"code":-1,"msg":"订单不存在"}`)); err != nil {
				return
			}
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	f := newAdminFixtureWithPool(t, exit)
	s := f.owner()
	site := f.siteID()

	rec := f.change(s, http.MethodPost, "/payments/merchants", epusdtMerchantJSON(site, "Epusdt", false, 0, server.URL, true), newKey())
	expectAdmin(t, "create epusdt merchant", rec, http.StatusCreated, "")
	var created adminapi.Merchant
	decode(t, rec, &created)
	if created.Id == nil {
		t.Fatalf("created merchant %s", rec.Body)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.doContext(
			ctx, http.MethodPut, "/payments/merchants/"+*created.Id,
			epusdtMerchantJSON("", "Epusdt", true, 1, server.URL, false), as(s), idempotent(newKey()),
		)
	}()
	select {
	case <-entered:
	case rec := <-done:
		t.Fatalf("the save finished before the probe: %d %s", rec.Code, rec.Body)
	case <-time.After(5 * time.Second):
		t.Fatal("the save never reached the probe; it held the only ordinary connection before probing")
	}
	select {
	case rec := <-done:
		t.Fatalf("the save finished while the probe was blocked: %d %s", rec.Code, rec.Body)
	default:
	}
	close(release)
	select {
	case rec := <-done:
		expectAdmin(t, "enable epusdt merchant", rec, http.StatusOK, "")
	case <-ctx.Done():
		t.Fatal("the save did not finish after the probe was released")
	}
}

//nolint:cyclop // read, save both fields, and save without them are one settings journey with one assertion each.
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
	if view.RefundSecondApprovalCredits == nil || view.RefundSecondApprovalCredits.Amount != "100000000000" {
		t.Fatalf("default refund limit %s", rec.Body)
	}

	body := `{"settings":{"open":true,"top_up_buffer_ppm":35000,"refund_second_approval_credits":{"amount":"250000000000","currency":"USD"}},"reason":"a wider buffer for this site"}`
	rec = f.change(s, http.MethodPut, path, body, newKey())
	expectAdmin(t, "set payment settings", rec, http.StatusOK, "")
	decode(t, rec, &view)
	if view.TopUpBufferPpm == nil || *view.TopUpBufferPpm != 35_000 ||
		view.RefundSecondApprovalCredits == nil || view.RefundSecondApprovalCredits.Amount != "250000000000" {
		t.Fatalf("saved buffer and limit %s", rec.Body)
	}
	f.expectAudit("billing.top_up_buffer.update", map[string]string{"detail::text": `{"after": 35000, "before": 20000}`})

	// An update without either field leaves both as they are.
	rec = f.change(s, http.MethodPut, path, `{"settings":{"open":false},"reason":"close collection"}`, newKey())
	expectAdmin(t, "set payment settings without buffer", rec, http.StatusOK, "")
	decode(t, rec, &view)
	if view.TopUpBufferPpm == nil || *view.TopUpBufferPpm != 35_000 ||
		view.RefundSecondApprovalCredits == nil || view.RefundSecondApprovalCredits.Amount != "250000000000" {
		t.Fatalf("buffer or limit after another save %s", rec.Body)
	}
}

type merchantSignals struct {
	mu       sync.Mutex
	raised   []alert.Signal
	resolved []alert.Signal
}

func (s *merchantSignals) Raise(kind, key, severity string, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raised = append(s.raised, alert.Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
	return nil
}

func (s *merchantSignals) RaiseOnce(_, kind, key, severity string, fields map[string]string) error {
	return s.Raise(kind, key, severity, fields)
}

func (s *merchantSignals) Resolve(kind, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved = append(s.resolved, alert.Signal{Kind: kind, Key: key, Resolved: true})
	return nil
}

// TestAdminMerchantUnreadableUsesTheNodesSignals proves the admin's paymentsService is constructed with the node's
// alert sink and identity, not a private instance that silently drops a decryption failure.
func TestAdminMerchantUnreadableUsesTheNodesSignals(t *testing.T) {
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	exit, err := egress.New(egress.Direct)
	if err != nil {
		t.Fatal(err)
	}
	sink := &merchantSignals{}
	f := startAdminNodeWithSignals(c, &testClock{now: time.Now()}, exit, sink, "test-node")
	if _, err = f.user("", "grant-platform", "--email", ownerEmail, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	s := f.owner()
	created := createMerchant(t, f, s, f.siteID())
	id, err := typeid.MustPrefix("mch").Decode(*created.Id)
	if err != nil {
		t.Fatal(err)
	}
	f.run(fmt.Sprintf("UPDATE payments.merchant_configs SET nonce = set_byte(nonce, 0, (get_byte(nonce, 0) + 1) %% 256) WHERE merchant_id = '%s'", id))
	rec := f.change(s, http.MethodPut, "/payments/merchants/"+*created.Id, merchantJSON("", "Renamed", true, 1, "0", false), newKey())
	if rec.Code == http.StatusOK {
		t.Fatalf("a merchant with a changed nonce saved: %s", rec.Body)
	}
	sink.mu.Lock()
	if len(sink.raised) != 1 || sink.raised[0].Kind != "payments.merchant_unreadable" || sink.raised[0].Key != "test-node/"+*created.Id {
		sink.mu.Unlock()
		t.Fatalf("raised %+v", sink.raised)
	}
	sink.mu.Unlock()
	// A later admin request is a new Payments; the shared node state must let its successful save resolve the event.
	rec = f.change(s, http.MethodPut, "/payments/merchants/"+*created.Id, merchantJSON("", "Renamed", true, 1, "0", true), newKey())
	expectAdmin(t, "save a readable merchant", rec, http.StatusOK, "")
	rec = f.change(s, http.MethodPost, "/payments/merchants/"+*created.Id+"/tests", `{"reason":"prove the new secrets open"}`, newKey())
	expectAdmin(t, "test the readable merchant", rec, http.StatusOK, "")
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.resolved) != 1 || sink.resolved[0].Key != "test-node/"+*created.Id {
		t.Fatalf("resolved %+v", sink.resolved)
	}
}
