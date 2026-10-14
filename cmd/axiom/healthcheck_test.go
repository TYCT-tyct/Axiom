package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
)

func TestHealthcheck(t *testing.T) {
	var health httpserver.Health
	node := httptest.NewServer(health.Handler())
	defer node.Close()
	_, port, err := net.SplitHostPort(node.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		addr    string
		ready   bool
		healthy bool
	}{
		{"ready", "127.0.0.1:" + port, true, true},
		{"not ready", "127.0.0.1:" + port, false, false},
		{"a listener on every address is probed on loopback", ":" + port, true, true},
		{"0.0.0.0 likewise", "0.0.0.0:" + port, true, true},
		{"nothing listening", "127.0.0.1:1", true, false},
	}
	for _, c := range cases {
		health.SetReady(c.ready)
		// Other AXIOM_* variables belong to serve and must not fail the check.
		err := healthcheck(t.Context(), []string{"AXIOM_OPS_ADDR=" + c.addr, "AXIOM_DATABASE_URL=x"})
		if (err == nil) != c.healthy {
			t.Errorf("%s: error %v, want healthy %v", c.name, err, c.healthy)
		}
	}
}

// TestHealthcheckTrustsOnlyTheNode: a redirect to a page that answers 200 is not readiness, and a node that accepts
// the connection but never answers fails the check within its own limit.
func TestHealthcheckTrustsOnlyTheNode(t *testing.T) {
	redirecting := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer redirecting.Close()
	silent, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := silent.Close(); err != nil {
			t.Error(err)
		}
	})
	go func() {
		for { // accept and hold every connection, never answering
			if _, err := silent.Accept(); err != nil {
				return
			}
		}
	}()
	for name, addr := range map[string]string{"a redirect": redirecting.Listener.Addr().String(), "a silent node": silent.Addr().String()} {
		start := time.Now()
		if err := healthcheck(t.Context(), []string{"AXIOM_OPS_ADDR=" + addr}); err == nil {
			t.Errorf("%s passed the check", name)
		}
		if elapsed := time.Since(start); elapsed > 2*probeTimeout {
			t.Errorf("%s: the check took %v", name, elapsed)
		}
	}
}
