package payments

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

func ezPayEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// TestEzPayOfficialImage is an opt-in check against the locally built official image at v1.1.9, without adapting its
// protocol. It needs a wallet and the default merchant (PID 1001) initialized. The core path proves the save probe,
// the authenticated query and a lost callback; the direct path proves a real signed callback reaches this process.
//
//	AXIOM_EZPAY_TEST_URL=http://127.0.0.1:6088 \
//	AXIOM_EZPAY_TEST_PUBLIC_HOST=203.0.113.10 \
//	go test ./internal/payments -run TestEzPayOfficialImage -count=1 -v
//
// The image must share the host network (or otherwise reach AXIOM_EZPAY_TEST_PUBLIC_HOST) for the callback.
//
//nolint:cyclop,gocognit // one end-to-end walk: save, create, query, lost callback and delivered callback
func TestEzPayOfficialImage(t *testing.T) {
	raw := os.Getenv("AXIOM_EZPAY_TEST_URL")
	if raw == "" {
		t.Skip("set AXIOM_EZPAY_TEST_URL to the initialized official image on loopback")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.User != nil {
		t.Fatal("official image check requires a loopback HTTP URL")
	}
	proxy := httputil.NewSingleHostReverseProxy(target) // #nosec G704 -- opt-in test endpoint is restricted above to literal IPv4 loopback
	tls := httptest.NewTLSServer(proxy)
	t.Cleanup(tls.Close)

	pid := ezPayEnv("AXIOM_EZPAY_TEST_PID", "1001")
	key := os.Getenv("AXIOM_EZPAY_TEST_KEY")
	if key == "" {
		t.Fatal("set AXIOM_EZPAY_TEST_KEY to the image merchant key")
	}
	w := newTestWorld(t, EasyPay{})
	w.p.client = ezPayConfig(t, tls.URL, tls.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt_trc20", Enabled: true})
	in.Kind, in.Settings, in.Currency = "easypay", ezPaySettingsJSON(t, tls.URL, pid), currency(t, "USD")
	in.Secrets = map[string]secret.Secret{"key": secret.New(key)}
	w.mustSave(siteScope(w.siteA), in)

	o, checkout, err := w.placeAs(w.accountA(), CreateOrder{
		Site: w.site(w.siteA, "USD"), Account: w.account, User: w.user, Method: "usdt.tron",
		Credits: 10 * yuan, Key: "ezpay-image-core", ReturnURL: "https://console.example.com/topup",
		ClientIP: netip.MustParseAddr("203.0.113.7"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ezPayVisit(t, tls.Client(), checkout.PayURL)
	ch, err := w.channel(o.MerchantID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := ch.Query(t.Context(), o.ID.TypeID(), "")
	if err != nil || pending.State != PayPending || pending.TradeNo == "" {
		t.Fatalf("image query did not find the pending order: %+v, %v", pending, err)
	}
	if _, err := ch.Query(t.Context(), OrderID(uuid.NewV7()).TypeID(), ""); err != nil {
		t.Fatal("a missing order must be pending, not an error", err)
	}

	ezPayRefundRefused(t, target.String(), pid, key)
	password := os.Getenv("AXIOM_EZPAY_TEST_ADMIN_PASSWORD")
	if password == "" {
		t.Fatal("set AXIOM_EZPAY_TEST_ADMIN_PASSWORD to the image admin password")
	}
	token := ezPayAdminToken(t, target.String(), password)
	ezPayMarkPaid(t, target.String(), token, pending.TradeNo)
	w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = transaction_timestamp() - interval '1 minute' WHERE id = '%s'", uuid.UUID(o.ID)))
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, o.Credits)
	paid, err := ch.Query(t.Context(), o.ID.TypeID(), "")
	if err != nil || paid.State != PayPaid || paid.Paid != (Money{currency(t, "USD"), 1000}) {
		t.Fatalf("image query did not confirm payment: %+v, %v", paid, err)
	}

	public := os.Getenv("AXIOM_EZPAY_TEST_PUBLIC_HOST")
	if public == "" {
		t.Log("no AXIOM_EZPAY_TEST_PUBLIC_HOST: skipping the delivered callback check")
		return
	}
	callbacks := make(chan url.Values, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		select {
		case callbacks <- r.URL.Query():
		default:
		}
		ezPayWrite(w, "success")
	})
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = server.Close() })   //nolint:errcheck // test server shutdown
	go func() { _ = server.Serve(listener) }() //nolint:errcheck // test server shutdown
	port := listener.Addr().(*net.TCPAddr).Port
	notify := fmt.Sprintf("http://%s:%d/notify", public, port)

	r := CreateRequest{
		Order: OrderID(uuid.NewV7()), Method: "usdt.tron", Upstream: "usdt_trc20",
		NotifyURL: notify, Total: Money{currency(t, "USD"), 1000}, Subject: "image callback",
		ReturnURL: "https://example.com/return", ClientIP: netip.MustParseAddr("203.0.113.7"),
	}
	checkout, err = ch.Create(t.Context(), r)
	if err != nil || checkout.PayURL == "" {
		t.Fatal("direct create", err)
	}
	ezPayVisit(t, tls.Client(), checkout.PayURL)
	direct, err := ch.Query(t.Context(), r.Order.TypeID(), "")
	if err != nil || direct.State != PayPending || direct.TradeNo == "" {
		t.Fatalf("direct query: %+v, %v", direct, err)
	}
	ezPayMarkPaid(t, target.String(), token, direct.TradeNo)
	var callback url.Values
	select {
	case callback = <-callbacks:
	case <-time.After(20 * time.Second):
		t.Fatal("the image did not deliver the callback")
	}
	notice, err := ch.Verify(t.Context(), Callback{Method: http.MethodGet, RawQuery: callback.Encode()})
	if err != nil || !notice.Success || notice.Order != r.Order.TypeID() || notice.Paid != (Money{currency(t, "USD"), 1000}) {
		t.Fatalf("delivered callback: %+v, %v", notice, err)
	}
}

func ezPayRefundRefused(t *testing.T, base, pid, key string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/api.php?act=refund&pid="+url.QueryEscape(pid)+"&key="+url.QueryEscape(key), nil) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // test response body
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err = jsonx.Unmarshal(data, &out); err != nil || out.Code != -1 || !strings.Contains(out.Msg, "不支持退款") {
		t.Fatalf("the image must refuse refunds: %+v, %v", out, err)
	}
}

func ezPayAdminToken(t *testing.T, base, password string) string {
	t.Helper()
	body, err := jsonx.MarshalCanonical(map[string]string{"username": "admin", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/admin/api/login", bytes.NewReader(body)) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // test response body
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Code  int    `json:"code"`
		Token string `json:"token"`
		Msg   string `json:"msg"`
	}
	if err = jsonx.Unmarshal(data, &out); err != nil || out.Code != 1 || out.Token == "" {
		t.Fatal("admin login", out.Code, out.Msg, err)
	}
	return out.Token
}

func ezPayMarkPaid(t *testing.T, base, token, trade string) {
	t.Helper()
	body := strings.NewReader(`{"tx_hash":"axiom-image-test","amount":"10.00"}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/admin/api/orders/"+url.PathEscape(trade)+"/paid", body) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // opt-in test endpoint is the loopback image
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // test response body
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err = jsonx.Unmarshal(data, &out); err != nil || out.Code != 1 {
		t.Fatal("mark paid", string(data), err)
	}
}

func ezPayVisit(t *testing.T, client *http.Client, raw string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal("open checkout", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("open checkout", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // test response body
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Request.URL.Path, "/cashier/") {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			t.Fatal("read checkout error", readErr)
		}
		t.Fatalf("checkout did not reach the cashier: %s %s", resp.Status, data)
	}
}
