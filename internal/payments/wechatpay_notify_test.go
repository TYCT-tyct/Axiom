package payments

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

//nolint:cyclop,gocognit // one branch per callback edge; the table documents the protocol
func TestWechatPayVerify(t *testing.T) {
	server := wpTestServer(t, http.NotFoundHandler())
	keys := wpKeys(t)
	channel := wpOpen(t, wpCertConfig(t, server.URL, server.Certificate(), true)).(*wechatChannel)
	order, trade := "ord_01ARZ3NDEKTSV4RRFFQ69G5FAV", "4200000000202401011234567890"
	base := wpTestCallback(t, wpTransaction(order, trade, 12345, "SUCCESS"), keys.platform, wpTestPlatformSerial(t))
	notice, err := channel.Verify(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !notice.Relevant || !notice.Success || notice.Order != order || notice.TradeNo != trade || notice.Paid.Minor != 12345 ||
		notice.Paid.Currency != channel.currency || notice.Raw != "wechatpay notification" {
		t.Fatalf("notice %+v", notice)
	}
	if _, err := channel.Verify(t.Context(), base); err != nil {
		t.Fatal("a replay no longer verifies", err)
	}

	// The old certificate and the old public key remain valid during a rotation.
	old := wpTestCallback(t, wpTransaction(order, trade, 12345, "SUCCESS"), keys.old, strings.ToUpper(keys.oldCert.SerialNumber.Text(16)))
	if _, err := channel.Verify(t.Context(), old); err != nil {
		t.Fatal("old certificate did not verify", err)
	}
	public := wpOpen(t, wpPublicConfig(t, server.URL, server.Certificate(), true)).(*wechatChannel)
	if _, err := public.Verify(t.Context(), wpTestCallback(t, wpTransaction(order, trade, 12345, "SUCCESS"), keys.platform, wpTestKeyID)); err != nil {
		t.Fatal("public key did not verify", err)
	}
	if _, err := public.Verify(t.Context(), wpTestCallback(t, wpTransaction(order, trade, 12345, "SUCCESS"), keys.old, wpTestOldKey)); err != nil {
		t.Fatal("old public key did not verify", err)
	}

	tampered := base
	tampered.Body = bytes.Clone(base.Body)
	tampered.Body[len(tampered.Body)-2] ^= 1
	if _, err := channel.Verify(t.Context(), tampered); err == nil {
		t.Fatal("tampered callback verified")
	}
	if _, err := channel.Verify(t.Context(), wpTestCallback(t, wpTransaction(order, trade, 12345, "SUCCESS"), keys.other, wpTestPlatformSerial(t))); err == nil {
		t.Fatal("callback signed by another key verified")
	}
	unknown := base
	unknown.Header = base.Header.Clone()
	unknown.Header.Set("Wechatpay-Serial", "DEADBEEF")
	if _, err := channel.Verify(t.Context(), unknown); err == nil {
		t.Fatal("unknown serial verified")
	}

	cfg := wpCertConfig(t, server.URL, server.Certificate(), false)
	cfg.Secrets["api_v3_key"] = secret.New(strings.Repeat("x", 32))
	wrongKey := wpOpen(t, cfg).(*wechatChannel)
	if _, err := wrongKey.Verify(t.Context(), base); err == nil {
		t.Fatal("callback decrypted with the wrong APIv3 key")
	}

	for name, change := range map[string]func(map[string]any){
		"another app":           func(m map[string]any) { m["appid"] = "wx0000000000000000" },
		"another merchant":      func(m map[string]any) { m["mchid"] = "1900000002" },
		"no order":              func(m map[string]any) { delete(m, "out_trade_no") },
		"success without trade": func(m map[string]any) { delete(m, "transaction_id") },
	} {
		t.Run(name, func(t *testing.T) {
			tx := wpTransaction(order, trade, 12345, "SUCCESS")
			change(tx)
			if _, err := channel.Verify(t.Context(), wpTestCallback(t, tx, keys.platform, wpTestPlatformSerial(t))); err == nil {
				t.Fatal("accepted")
			}
		})
	}

	repeated := base
	repeated.Header = base.Header.Clone()
	repeated.Header.Add("Wechatpay-Timestamp", repeated.Header.Get("Wechatpay-Timestamp"))
	if _, err := channel.Verify(t.Context(), repeated); err == nil {
		t.Fatal("repeated header verified")
	}
	for name, mutate := range map[string]func(*Callback){
		"method":       func(c *Callback) { c.Method = http.MethodGet },
		"query":        func(c *Callback) { c.RawQuery = "x=1" },
		"content type": func(c *Callback) { c.Header.Set("Content-Type", "text/plain") },
		"oversized":    func(c *Callback) { c.Body = make([]byte, callbackMax+1) },
	} {
		t.Run(name, func(t *testing.T) {
			cb := base
			cb.Header = base.Header.Clone()
			mutate(&cb)
			if _, err := channel.Verify(t.Context(), cb); err == nil {
				t.Fatal("accepted")
			}
		})
	}

	missingAmount := wpTransaction(order, trade, 0, "SUCCESS")
	delete(missingAmount, "amount")
	overflow := wpTransaction(order, trade, 0, "SUCCESS")
	overflow["amount"] = map[string]any{"total": uint64(1) << 63, "currency": "CNY"}
	for name, tx := range map[string]map[string]any{
		"missing amount":  missingAmount,
		"string amount":   wpAmount("12345"),
		"negative amount": wpAmount(-1),
		"decimal amount":  wpAmount(1.5),
		"overflow amount": overflow,
		"USD amount":      wpCurrencyAmount(12345, "USD"),
	} {
		t.Run(name, func(t *testing.T) {
			notice, err := channel.Verify(t.Context(), wpTestCallback(t, tx, keys.platform, wpTestPlatformSerial(t)))
			if err != nil {
				t.Fatal(err)
			}
			want := int64(-1)
			if name == "missing amount" {
				want = 0
			}
			if notice.Paid.Minor != want {
				t.Fatalf("minor %d, want %d", notice.Paid.Minor, want)
			}
		})
	}

	notPaid := wpTransaction(order, trade, 12345, "NOTPAY")
	notice, err = channel.Verify(t.Context(), wpTestCallback(t, notPaid, keys.platform, wpTestPlatformSerial(t)))
	if err != nil || !notice.Relevant || notice.Success {
		t.Fatalf("NOTPAY notice %+v %v", notice, err)
	}
	refundEvent := wpTestCallbackEvent(t, "REFUND.SUCCESS", wpTransaction(order, trade, 12345, "SUCCESS"), keys.platform, wpTestPlatformSerial(t))
	notice, err = channel.Verify(t.Context(), refundEvent)
	if err != nil || notice.Relevant {
		t.Fatalf("refund event notice %+v %v", notice, err)
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
		t.Run(name, func(t *testing.T) {
			rewritten := wpTestResign(t, base, change)
			if _, err := channel.Verify(t.Context(), rewritten); err == nil {
				t.Fatal("accepted")
			}
		})
	}

	if status, body := channel.Ack(true); status != http.StatusOK || len(body) != 0 {
		t.Fatalf("success ack %d %q", status, body)
	}
	status, body := channel.Ack(false)
	if status != http.StatusInternalServerError || string(body) != `{"code":"FAIL","message":"失败"}` {
		t.Fatalf("failure ack %d %s", status, body)
	}
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
