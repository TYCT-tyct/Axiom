package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestServe runs a node on free ports: it reports ready and answers the console's API through the public listener,
// and once the context ends it shuts down and stops listening.
func TestServe(t *testing.T) {
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_OPS_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
		"AXIOM_DATABASE_URL=" + pgtest.New(t).App,
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	addrs := servingAddrs(t, logs, done)

	for _, path := range []string{"/livez", "/readyz"} {
		if resp, err := get(t, "http://"+addrs.Ops+path); err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %v; want 200", path, err)
		}
	}
	checkVersion(t, "http://"+addrs.Public+"/api/console/v1/version")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
	for _, addr := range []string{addrs.Public, addrs.Ops} {
		if _, err := get(t, "http://"+addr+"/"); err == nil {
			t.Errorf("%s still answers after shutdown", addr)
		}
	}
}

// TestServeRejectsBadConfiguration: the node does not start, names every wrong variable or the database problem, and
// quotes no value.
func TestServeRejectsBadConfiguration(t *testing.T) {
	database := pgtest.New(t)
	// taken is bound but never accepts: the system still completes connections to it, which then hear nothing, as
	// from a database server that has stalled.
	taken, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taken.Close(); err != nil {
			t.Error(err)
		}
	})
	cases := []struct {
		name    string
		environ []string
		want    []string
	}{
		{
			"invalid and unknown variables",
			[]string{"AXIOM_SHUTDOWN_TIMEOUT=planted-value", "AXIOM_OPS_ADDR=planted-value", "AXIOM_OPS_ADRR=planted-value"},
			[]string{"AXIOM_SHUTDOWN_TIMEOUT: want", "AXIOM_OPS_ADDR: want", "AXIOM_OPS_ADRR: unknown variable", "AXIOM_DATABASE_URL: required"},
		},
		{
			"a taken address",
			[]string{"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_OPS_ADDR=" + taken.Addr().String(), "AXIOM_DATABASE_URL=" + database.App},
			[]string{"AXIOM_OPS_ADDR: "},
		},
		{
			"a database login that escapes row-level security",
			[]string{"AXIOM_DATABASE_URL=" + database.Superuser},
			[]string{"database: the database login can act as roles that row-level security cannot hold"},
		},
		{
			"a database server that never answers",
			[]string{"AXIOM_STARTUP_TIMEOUT=1s", "AXIOM_DATABASE_URL=postgres://planted-value:planted-value@" + taken.Addr().String() + "/planted-value"},
			[]string{"database: AXIOM_DATABASE_URL: no answer within AXIOM_STARTUP_TIMEOUT"},
		},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) // a node that wrongly starts stops here
		err := serve(ctx, c.environ, slog.New(slog.DiscardHandler))
		cancel()
		if err == nil {
			t.Fatalf("%s: the node started", c.name)
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not say %q", c.name, err, want)
			}
		}
		if strings.Contains(err.Error(), "planted-value") || strings.Contains(err.Error(), taken.Addr().String()) ||
			strings.Contains(err.Error(), database.Superuser) {
			t.Errorf("%s: error %q quotes a value", c.name, err)
		}
	}
}

// records receives log records: the JSON handler writes each record with a single Write.
type records chan []byte

func (r records) Write(p []byte) (int, error) {
	r <- bytes.Clone(p)
	return len(p), nil
}

// checkVersion: the console's version endpoint answers through the whole public stack, uncached, with a request ID,
// and does not tell which Go toolchain built the program.
func checkVersion(t *testing.T, url string) {
	t.Helper()
	resp, err := get(t, url)
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Version string }
	if err := json.Unmarshal(resp.body, &body, json.MatchCaseInsensitiveNames(true)); err != nil || body.Version != version() || strings.Contains(body.Version, runtime.Version()) ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Request-ID") == "" {
		t.Errorf("GET %s: %d %v %s (%v)", url, resp.StatusCode, resp.Header, resp.body, err)
	}
}

// addresses are where a node listens, as it logs them.
type addresses struct {
	Msg         string `json:"msg"`
	Public, Ops string `json:",case:ignore"`
}

// servingAddrs reads log records until the node reports the addresses it listens on.
func servingAddrs(t *testing.T, logs records, done <-chan error) addresses {
	t.Helper()
	for {
		select {
		case line := <-logs:
			var record addresses
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("log record %q: %v", line, err)
			}
			if record.Msg == "serving" {
				return record
			}
		case err := <-done:
			t.Fatalf("the node stopped before serving: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the node did not start")
		}
	}
}

// response is a response read in full.
type response struct {
	*http.Response
	body []byte
}

func get(t *testing.T, url string) (response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	body, err := io.ReadAll(resp.Body)
	return response{resp, body}, errors.Join(err, resp.Body.Close())
}
