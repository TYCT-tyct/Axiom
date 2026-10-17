package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestServeAdminListener: a node given AXIOM_ADMIN_ADDR serves the admin's API there and only there, behind the guard,
// and logs the address; the public listener has no admin routes and the admin listener has no console routes; a node
// without the variable has no admin listener.
func TestServeAdminListener(t *testing.T) {
	environ := func(extra ...string) []string {
		return append([]string{
			"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
			"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App, "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		}, extra...)
	}
	start := func(extra ...string) (adminAddr, publicAddr string, stop func()) {
		logs := make(records, 16)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- serve(ctx, environ(extra...), slog.New(slog.NewJSONHandler(logs, nil))) }()
		addrs := servingAddrs(t, logs, done)
		return addrs.Admin, addrs.Public, func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("the node did not shut down")
			}
		}
	}

	admin, public, stop := start("AXIOM_ADMIN_ADDR=127.0.0.1:0")
	if admin == "" {
		t.Fatal("the node did not log an admin address")
	}
	checkVersion(t, "http://"+admin+"/api/admin/v1/version")
	checkGuard(t, "http://"+admin+"/api/admin/v1/users")
	for _, c := range []struct{ addr, path string }{
		{admin, "/api/console/v1/version"},
		{public, "/api/admin/v1/version"},
		{public, "/api/admin/v1/users"},
	} {
		resp, err := get(t, "http://"+c.addr+c.path)
		if err != nil || resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s on %s: %v %v, want 404", c.path, c.addr, resp.Response, err)
		}
	}
	stop()
	if _, err := get(t, "http://"+admin+"/api/admin/v1/version"); err == nil {
		t.Error("the admin listener still answers after shutdown")
	}

	admin, _, stop = start()
	defer stop()
	if admin != "" {
		t.Errorf("a node without AXIOM_ADMIN_ADDR listens for the admin on %s", admin)
	}
}
