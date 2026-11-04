package payments

import (
	"bytes"
	"crypto/x509"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// wpVerifyFixture is one callback and the channel that verifies it, shared by the verification subtests.
type wpVerifyFixture struct {
	t            *testing.T
	serverURL    string
	certificate  *x509.Certificate
	keys         *wpTestKeys
	channel      *wechatChannel
	order, trade string
	base         Callback
}

func newWPVerifyFixture(t *testing.T) *wpVerifyFixture {
	t.Helper()
	server := wpTestServer(t, http.NotFoundHandler())
	f := &wpVerifyFixture{
		t: t, serverURL: server.URL, certificate: server.Certificate(), keys: wpKeys(t),
		order: "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", trade: "4200000000202401011234567890",
	}
	f.channel = wpOpen(t, wpCertConfig(t, f.serverURL, f.certificate, true)).(*wechatChannel)
	f.base = wpTestCallback(t, wpTransaction(f.order, f.trade, 12345, "SUCCESS"), f.keys.platform, wpTestPlatformSerial(t))
	return f
}

// checkNotice asserts a verified callback's fields and that verifying it again still succeeds.
func (f *wpVerifyFixture) checkNotice() {
	f.t.Helper()
	notice, err := f.channel.Verify(f.t.Context(), f.base)
	if err != nil {
		f.t.Fatal(err)
	}
	if !notice.Relevant || !notice.Success || notice.Order != f.order || notice.TradeNo != f.trade ||
		notice.Paid.Minor != 12345 || notice.Paid.Currency != f.channel.currency ||
		notice.Raw != "wechatpay notification" {
		f.t.Fatalf("notice %+v", notice)
	}
	if _, err := f.channel.Verify(f.t.Context(), f.base); err != nil {
		f.t.Fatal("a replay no longer verifies", err)
	}
}

// checkRotation asserts the old certificate and both public keys still verify during a rotation.
func (f *wpVerifyFixture) checkRotation() {
	f.t.Helper()
	old := wpTestCallback(f.t, wpTransaction(f.order, f.trade, 12345, "SUCCESS"), f.keys.old,
		strings.ToUpper(f.keys.oldCert.SerialNumber.Text(16)))
	if _, err := f.channel.Verify(f.t.Context(), old); err != nil {
		f.t.Fatal("old certificate did not verify", err)
	}
	public := wpOpen(f.t, wpPublicConfig(f.t, f.serverURL, f.certificate, true)).(*wechatChannel)
	transaction := wpTransaction(f.order, f.trade, 12345, "SUCCESS")
	if _, err := public.Verify(f.t.Context(), wpTestCallback(f.t, transaction, f.keys.platform, wpTestKeyID)); err != nil {
		f.t.Fatal("public key did not verify", err)
	}
	if _, err := public.Verify(f.t.Context(), wpTestCallback(f.t, transaction, f.keys.old, wpTestOldKey)); err != nil {
		f.t.Fatal("old public key did not verify", err)
	}
}

// checkRefusals asserts tampering, a foreign signer, an unknown serial, a wrong APIv3 key, repeated headers and malformed
// callbacks are all refused.
func (f *wpVerifyFixture) checkRefusals() {
	f.t.Helper()
	tampered := f.base
	tampered.Body = bytes.Clone(f.base.Body)
	tampered.Body[len(tampered.Body)-2] ^= 1
	if _, err := f.channel.Verify(f.t.Context(), tampered); err == nil {
		f.t.Fatal("tampered callback verified")
	}
	foreign := wpTestCallback(f.t, wpTransaction(f.order, f.trade, 12345, "SUCCESS"), f.keys.other, wpTestPlatformSerial(f.t))
	if _, err := f.channel.Verify(f.t.Context(), foreign); err == nil {
		f.t.Fatal("callback signed by another key verified")
	}
	unknown := f.base
	unknown.Header = f.base.Header.Clone()
	unknown.Header.Set("Wechatpay-Serial", "DEADBEEF")
	if _, err := f.channel.Verify(f.t.Context(), unknown); err == nil {
		f.t.Fatal("unknown serial verified")
	}

	cfg := wpCertConfig(f.t, f.serverURL, f.certificate, false)
	cfg.Secrets["api_v3_key"] = secret.New(strings.Repeat("x", 32))
	if _, err := wpOpen(f.t, cfg).(*wechatChannel).Verify(f.t.Context(), f.base); err == nil {
		f.t.Fatal("callback decrypted with the wrong APIv3 key")
	}

	repeated := f.base
	repeated.Header = f.base.Header.Clone()
	repeated.Header.Add("Wechatpay-Timestamp", repeated.Header.Get("Wechatpay-Timestamp"))
	if _, err := f.channel.Verify(f.t.Context(), repeated); err == nil {
		f.t.Fatal("repeated header verified")
	}

	for name, mutate := range map[string]func(*Callback){
		"method":       func(c *Callback) { c.Method = http.MethodGet },
		"query":        func(c *Callback) { c.RawQuery = "x=1" },
		"content type": func(c *Callback) { c.Header.Set("Content-Type", "text/plain") },
		"oversized":    func(c *Callback) { c.Body = make([]byte, callbackMax+1) },
	} {
		f.t.Run(name, func(t *testing.T) {
			cb := f.base
			cb.Header = f.base.Header.Clone()
			mutate(&cb)
			if _, err := f.channel.Verify(t.Context(), cb); err == nil {
				t.Fatal("accepted")
			}
		})
	}

	for name, change := range map[string]func(map[string]any){
		"resource type": func(env map[string]any) { env["resource_type"] = "other" },
		"algorithm":     func(env map[string]any) { env["resource"].(map[string]any)["algorithm"] = "AEAD_AES_128_GCM" },
		"original type": func(env map[string]any) { env["resource"].(map[string]any)["original_type"] = "refund" },
		"nonce length":  func(env map[string]any) { env["resource"].(map[string]any)["nonce"] = "short" },
		"ciphertext": func(env map[string]any) {
			resource := env["resource"].(map[string]any)
			text := resource["ciphertext"].(string)
			resource["ciphertext"] = text[:len(text)-4] + "AAAA"
		},
	} {
		f.t.Run(name, func(t *testing.T) {
			if _, err := f.channel.Verify(t.Context(), wpTestResign(t, f.base, change)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// checkTransactions asserts a callback naming another app or merchant, or missing its order or trade, is refused.
func (f *wpVerifyFixture) checkTransactions() {
	f.t.Helper()
	for name, change := range map[string]func(map[string]any){
		"another app":           func(m map[string]any) { m["appid"] = "wx0000000000000000" },
		"another merchant":      func(m map[string]any) { m["mchid"] = "1900000002" },
		"no order":              func(m map[string]any) { delete(m, "out_trade_no") },
		"success without trade": func(m map[string]any) { delete(m, "transaction_id") },
	} {
		f.t.Run(name, func(t *testing.T) {
			tx := wpTransaction(f.order, f.trade, 12345, "SUCCESS")
			change(tx)
			if _, err := f.channel.Verify(t.Context(),
				wpTestCallback(t, tx, f.keys.platform, wpTestPlatformSerial(t))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// checkAmounts asserts an unusable amount becomes minor -1 and a missing one becomes 0, never the amount claimed.
func (f *wpVerifyFixture) checkAmounts() {
	f.t.Helper()
	missingAmount := wpTransaction(f.order, f.trade, 0, "SUCCESS")
	delete(missingAmount, "amount")
	overflow := wpTransaction(f.order, f.trade, 0, "SUCCESS")
	overflow["amount"] = map[string]any{"total": uint64(1) << 63, "currency": "CNY"}
	for name, tc := range map[string]struct {
		tx   map[string]any
		want int64
	}{
		"missing amount":  {missingAmount, 0},
		"string amount":   {wpAmount("12345"), -1},
		"negative amount": {wpAmount(-1), -1},
		"decimal amount":  {wpAmount(1.5), -1},
		"overflow amount": {overflow, -1},
		"USD amount":      {wpCurrencyAmount(12345, "USD"), -1},
	} {
		f.t.Run(name, func(t *testing.T) {
			notice, err := f.channel.Verify(t.Context(),
				wpTestCallback(t, tc.tx, f.keys.platform, wpTestPlatformSerial(t)))
			if err != nil {
				t.Fatal(err)
			}
			if notice.Paid.Minor != tc.want {
				t.Fatalf("minor %d, want %d", notice.Paid.Minor, tc.want)
			}
		})
	}
}

// checkEvents asserts a NOTPAY transaction is relevant and unsuccessful, and a refund event is not relevant.
func (f *wpVerifyFixture) checkEvents() {
	f.t.Helper()
	notPaid := wpTestCallback(f.t, wpTransaction(f.order, f.trade, 12345, "NOTPAY"), f.keys.platform, wpTestPlatformSerial(f.t))
	notice, err := f.channel.Verify(f.t.Context(), notPaid)
	if err != nil || !notice.Relevant || notice.Success {
		f.t.Fatalf("NOTPAY notice %+v %v", notice, err)
	}
	refund := wpTestCallbackEvent(f.t, "REFUND.SUCCESS", wpTransaction(f.order, f.trade, 12345, "SUCCESS"),
		f.keys.platform, wpTestPlatformSerial(f.t))
	notice, err = f.channel.Verify(f.t.Context(), refund)
	if err != nil || notice.Relevant {
		f.t.Fatalf("refund event notice %+v %v", notice, err)
	}
}

func (f *wpVerifyFixture) checkAck() {
	f.t.Helper()
	if status, body := f.channel.Ack(true); status != http.StatusOK || len(body) != 0 {
		f.t.Fatalf("success ack %d %q", status, body)
	}
	status, body := f.channel.Ack(false)
	if status != http.StatusInternalServerError || string(body) != `{"code":"FAIL","message":"失败"}` {
		f.t.Fatalf("failure ack %d %s", status, body)
	}
}

func TestWechatPayVerify(t *testing.T) {
	f := newWPVerifyFixture(t)
	f.checkNotice()
	f.checkRotation()
	f.checkRefusals()
	f.checkTransactions()
	f.checkAmounts()
	f.checkEvents()
	f.checkAck()
}

func TestWechatPayCallbackTimestampWindow(t *testing.T) {
	server := wpTestServer(t, http.NotFoundHandler())
	keys := wpKeys(t)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), true)).(*wechatChannel)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	channel.now = func() time.Time { return now }
	order, trade := "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "4200000000202401011234567890"
	transaction := wpTransaction(order, trade, 12345, "SUCCESS")
	// Each callback is re-signed over its target timestamp, so the out-of-window cases fail only on the time check.

	for _, tc := range []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"inside past", now.Add(-5 * time.Minute), true},
		{"past edge", now.Add(-5*time.Minute - time.Second), false},
		{"inside future", now.Add(5 * time.Minute), true},
		{"future edge", now.Add(5*time.Minute + time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callback := wpTestCallbackAt(t, transaction, keys.platform, wpTestPlatformSerial(t), tc.at)
			_, err := channel.Verify(t.Context(), callback)
			if (err == nil) != tc.ok {
				t.Fatalf("verified=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}

func wpTestResign(t *testing.T, callback Callback, change func(map[string]any)) Callback {
	t.Helper()
	var envelope map[string]any
	if err := jsonx.Unmarshal(callback.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	change(envelope)
	body := wpTestJSON(t, envelope)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "headernonce12345"
	signature := wpTestSign(timestamp+"\n"+nonce+"\n"+string(body)+"\n", wpKeys(t).platform)
	header := callback.Header.Clone()
	header.Set("Wechatpay-Timestamp", timestamp)
	header.Set("Wechatpay-Nonce", nonce)
	header.Set("Wechatpay-Signature", signature)
	return Callback{Method: callback.Method, Body: body, Header: header, RawQuery: callback.RawQuery}
}

func wpAmount(total any) map[string]any {
	tx := wpTransaction("ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "4200000000202401011234567890", 0, "SUCCESS")
	tx["amount"] = map[string]any{"total": total, "currency": "CNY"}
	return tx
}

func wpCurrencyAmount(total int64, currency string) map[string]any {
	tx := wpTransaction("ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "4200000000202401011234567890", total, "SUCCESS")
	tx["amount"] = map[string]any{"total": total, "currency": currency}
	return tx
}
