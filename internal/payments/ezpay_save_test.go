package payments

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"axiom/internal/platform/secret"
)

func TestEzPaySaveRequiresAuthenticatedQuery(t *testing.T) {
	w := newTestWorld(t, EasyPay{})
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api.php" || r.URL.Query().Get("act") != "order" {
			http.Error(wr, "wrong query", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("key") == theKey {
			ezPayWrite(wr, `{"code":-1,"msg":"订单不存在"}`)
			return
		}
		ezPayWrite(wr, `{"code":-1,"msg":"密钥错误"}`)
	}))
	t.Cleanup(server.Close)
	w.p.client = ezPayConfig(t, server.URL, server.Certificate()).Client
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA, MethodInput{Method: "usdt.tron", Upstream: "usdt_trc20", Enabled: true})
	in.Kind, in.Settings, in.Currency = "easypay", ezPaySettingsJSON(t, server.URL, "1001"), currency(t, "USD")
	m := w.mustSave(siteScope(w.siteA), in)
	if calls.Load() != 1 {
		t.Fatal("successful save did not probe exactly once", calls.Load())
	}

	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, map[string]secret.Secret{"key": secret.New("wrong")}
	if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/settings") {
		t.Fatal("wrong key saved an enabled merchant", err)
	}
	if calls.Load() != 2 {
		t.Fatal("bad key did not probe", calls.Load())
	}

	// Disabling must not need the gateway: the probe is for new payment traffic, not for stopping it.
	server.Close()
	in.Enabled, in.Secrets, in.Name = false, nil, "stopped"
	if saved, err := w.save(siteScope(w.siteA), in); err != nil || saved.Enabled {
		t.Fatal("disable required the gateway", saved.Enabled, err)
	}
	if calls.Load() != 2 {
		t.Fatal("disable probed the gateway", calls.Load())
	}
}
