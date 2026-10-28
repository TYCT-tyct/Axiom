package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestErrorExitsRedactKeys(t *testing.T) {
	getenv := func(string) string { return key }
	for _, tc := range []struct{ name, format, scenario, root, request string }{
		{"format", key, "s", "", ""},
		{"scenario", "chat", key + "!", "", ""},
		{"root", "chat", "s", filepath.Join(t.TempDir(), key), ""},
		{"request file", "chat", "s", t.TempDir(), filepath.Join(t.TempDir(), key)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := record(t.Context(), getenv, tc.root, tc.format, tc.scenario, tc.request)
			if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "[REDACTED]") {
				t.Fatalf("got %v, want a redacted error", err)
			}
		})
	}
}

func TestMainRedactsTransportErrors(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Run("transport and main stderr", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.WriteFile(".env", []byte("OPENAI_API_KEY="+key+"\nOPENAI_BASE_URL=http://recorder.invalid/v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func(c *http.Client) { client = c }(client)
		cause := errors.New("connection failed with " + key)
		client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })}
		root, request := t.TempDir(), requestFile(t, `{"stream":true}`)
		err := record(t.Context(), env("http://recorder.invalid/v1"), root, "chat", "s", request)
		if !errors.Is(err, cause) || strings.Contains(err.Error(), key) {
			t.Fatalf("got %v, want a redacted transport error retaining its cause", err)
		}
		var stderr bytes.Buffer
		if code := run([]string{root, "chat", "s", request}, &stderr); code != 1 || strings.Contains(stderr.String(), key) || !strings.Contains(stderr.String(), "[REDACTED]") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
}

func TestMainRedactsDotenvError(t *testing.T) {
	t.Run("dotenv error printed by main", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.Mkdir(".env", 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENAI_API_KEY", ".env")
		var stderr bytes.Buffer
		if code := run([]string{"unused", "chat", "s", "unused"}, &stderr); code != 1 || strings.Contains(stderr.String(), ".env") || !strings.Contains(stderr.String(), "[REDACTED]") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
}

func TestHeadersResetIdle(t *testing.T) {
	for _, tc := range []struct {
		name          string
		headers, body time.Duration
		timeout       bool
	}{
		{"slow headers and slow first byte", 6 * time.Second, 6 * time.Second, false},
		{"headers exceed idle", 11 * time.Second, 0, true},
		{"body exceeds idle after headers", 6 * time.Second, 11 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer func(c *http.Client, d time.Duration) { client, idleTimeout = c, d }(client, idleTimeout)
				idleTimeout = 10 * time.Second
				client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return delayedResponse(r, tc.headers, tc.body)
				})}
				data, err := send(t.Context(), formats["chat"], "http://recorder.invalid/v1", key, []byte(`{}`))
				if tc.timeout != errors.Is(err, errIdle) || !tc.timeout && (err != nil || string(data) != "done") {
					t.Fatalf("got %q (%v), want timeout %t", data, err, tc.timeout)
				}
			})
		})
	}
}

func delayedResponse(r *http.Request, headers, body time.Duration) (*http.Response, error) {
	select {
	case <-time.After(headers):
	case <-r.Context().Done():
		return nil, context.Cause(r.Context())
	}
	reader, writer := io.Pipe()
	go func(ctx context.Context) {
		select {
		case <-time.After(body):
			_, err := io.WriteString(writer, "done")
			_ = writer.CloseWithError(err)
		case <-ctx.Done():
			_ = writer.CloseWithError(context.Cause(ctx))
		}
	}(r.Context())
	return &http.Response{StatusCode: http.StatusOK, Body: reader, Header: make(http.Header)}, nil
}
