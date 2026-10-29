package payments

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
)

// An opt-in check against the locally installed official image, without adapting its protocol. The published
// Epusdt releases (v0.0.1 through v2.0.0) and master register only the EPay create route, so the merchant query
// stays missing and an enabled account must be refused.
func TestEpusdtOfficialImageQueryGate(t *testing.T) {
	raw := os.Getenv("AXIOM_EPUSDT_TEST_URL")
	if raw == "" {
		t.Skip("set AXIOM_EPUSDT_TEST_URL to the initialized official image on loopback")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.User != nil {
		t.Fatal("official image check requires a loopback HTTP URL")
	}
	proxy := httputil.NewSingleHostReverseProxy(target) // #nosec G704 -- opt-in test endpoint is restricted above to literal IPv4 loopback
	var queryStatus atomic.Int64
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.URL.Path == "/payments/epay/v1/order/create-transaction/api.php" {
			queryStatus.Store(int64(r.StatusCode))
		}
		return nil
	}
	tls := httptest.NewTLSServer(proxy)
	t.Cleanup(tls.Close)
	w := newTestWorld(t, EasyPay{})
	w.p.client = easyPayConfig(t, tls.URL, tls.Certificate()).Client
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt.tron", Enabled: true})
	in.Kind = "easypay"
	in.Settings = epusdtConfig(t, tls.URL+"/payments/epay/v1/order/create-transaction")
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatal("official image without query saved a merchant", err)
	}
	if queryStatus.Load() != http.StatusNotFound {
		t.Fatal("expected the pinned image's missing query route", queryStatus.Load())
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.merchants") != 0 {
		t.Fatal("failed compatibility gate persisted a merchant")
	}
	t.Log("official Epusdt query returned HTTP 404; merchant save refused before any order or credit")
}
