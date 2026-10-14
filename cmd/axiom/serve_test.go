package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestServe runs a node on a free port: it reports ready, and once the context ends it shuts down and stops listening.
func TestServe(t *testing.T) {
	environ := []string{"AXIOM_OPS_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_DATABASE_URL=" + pgtest.New(t).App}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	ops := servingAddr(t, logs, done)

	for _, path := range []string{"/livez", "/readyz"} {
		if code, err := get(t, "http://"+ops+path); err != nil || code != http.StatusOK {
			t.Errorf("GET %s = %d, %v; want 200", path, code, err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
	if _, err := get(t, "http://"+ops+"/livez"); err == nil {
		t.Error("the ops listener still answers after shutdown")
	}
}

// TestServeRejectsBadConfiguration: the node does not start, names every wrong variable or the database problem, and
// quotes no value.
func TestServeRejectsBadConfiguration(t *testing.T) {
	database := pgtest.New(t)
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
			[]string{"AXIOM_OPS_ADDR=" + taken.Addr().String(), "AXIOM_DATABASE_URL=" + database.App},
			[]string{"AXIOM_OPS_ADDR: "},
		},
		{
			"a database login that escapes row-level security",
			[]string{"AXIOM_DATABASE_URL=" + database.Superuser},
			[]string{"database: the database login can act as roles that bypass row-level security"},
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

// servingAddr reads log records until the node reports the address it listens on.
func servingAddr(t *testing.T, logs records, done <-chan error) string {
	t.Helper()
	for {
		select {
		case line := <-logs:
			var record struct {
				Msg string `json:"msg"`
				Ops string `json:"ops"`
			}
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("log record %q: %v", line, err)
			}
			if record.Msg == "serving" {
				return record.Ops
			}
		case err := <-done:
			t.Fatalf("the node stopped before serving: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the node did not start")
		}
	}
}

func get(t *testing.T, url string) (int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	return resp.StatusCode, resp.Body.Close()
}
