package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServe runs a node on a free port: it reports ready, and once the context ends it shuts down and stops listening.
func TestServe(t *testing.T) {
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, []string{"AXIOM_OPS_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s"}, slog.New(slog.NewJSONHandler(logs, nil)))
	}()
	ops := servingAddr(t, logs, done)

	for _, path := range []string{"/livez", "/readyz"} {
		if code, err := get(t, "http://"+ops+path); err != nil || code != http.StatusOK {
			t.Errorf("GET %s = %d, %v; want 200", path, code, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := get(t, "http://"+ops+"/livez"); err == nil {
		t.Error("the ops listener still answers after shutdown")
	}
}

func TestServeRejectsBadConfiguration(t *testing.T) {
	err := serve(t.Context(), []string{"AXIOM_SHUTDOWN_TIMEOUT=soon", "AXIOM_OPS_ADRR=:1"}, slog.New(slog.DiscardHandler))
	for _, want := range []string{"AXIOM_SHUTDOWN_TIMEOUT", "AXIOM_OPS_ADRR: unknown variable"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %s", err, want)
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
