package main

import (
	"net"
	"net/http/httptest"
	"testing"

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
