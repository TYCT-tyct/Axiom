package main

import (
	"bytes"
	"context"
	"encoding/json/v2" //nolint:depguard // decodes the program's own log records and console response; jsonx guards input from outside
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/observability"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

// TestServe runs a node on free ports: it reports ready and answers the console's API through the public listener,
// and once the context ends it shuts down and stops listening.
func TestServe(t *testing.T) {
	for _, nodes := range []int{1, 2} {
		t.Run(strconv.Itoa(nodes)+" cores", func(t *testing.T) {
			t.Parallel()
			testServe(t, nodes)
		})
	}
}

func testServe(t *testing.T, nodes int) {
	t.Helper()
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
		"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App, "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
	}
	if nodes == 2 {
		cache := kvtest.New(t)
		environ = append(environ, "AXIOM_CORE_NODES=2", "AXIOM_VALKEY_URL="+cache.URL, "AXIOM_VALKEY_PASSWORD_FILE="+cache.PasswordFile)
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	addrs := servingAddrs(t, logs, done)
	awaitRecord(t, logs, done, "ready") // /readyz turns 200 on a goroutine of its own, after "serving"

	for _, path := range []string{"/livez", "/readyz"} {
		if resp, err := get(t, "http://"+addrs.Ops+path); err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %v; want 200", path, err)
		}
	}
	checkVersion(t, "http://"+addrs.Public+"/api/console/v1/version")
	checkGuard(t, "http://"+addrs.Public+"/api/console/v1/site")
	checkPages(t, addrs)
	checkMetrics(t, addrs)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
	for _, addr := range []string{addrs.Public, addrs.Admin, addrs.Ops} {
		if _, err := get(t, "http://"+addr+"/"); err == nil {
			t.Errorf("%s still answers after shutdown", addr)
		}
	}
}

// checkPages: the public listener serves the website and the console under the policy, and none of the admin's files;
// the admin listener serves the admin. The admin's script is found from its own entry page, so the check holds for
// whatever the build names it.
func checkPages(t *testing.T, addrs addresses) {
	t.Helper()
	public, admin := "http://"+addrs.Public, "http://"+addrs.Admin
	for _, path := range []string{"/", "/console/", "/console/keys"} {
		resp, err := getAccepting(t, public+path, "text/html")
		if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" ||
			resp.Header.Get("Content-Security-Policy") == "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET %s on the public listener: %v %v", path, resp.Response, err)
		}
	}
	page, err := get(t, admin+"/")
	if err != nil || page.StatusCode != http.StatusOK {
		t.Fatalf("GET / on the admin listener: %v %v", page.Response, err)
	}
	script := regexp.MustCompile(`src="(/assets/index-[^"]+\.js)"`).FindSubmatch(page.body)
	if script == nil {
		t.Fatalf("the admin's entry page loads no script: %s", page.body)
	}
	checkAdminScript(t, public, admin, string(script[1]))
}

// checkAdminScript: the admin's script is cached for good on the admin listener and is not on the public one.
func checkAdminScript(t *testing.T, public, admin, script string) {
	t.Helper()
	if resp, err := get(t, admin+script); err != nil || resp.StatusCode != http.StatusOK ||
		resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("the admin's script on the admin listener: %v %v", resp.Response, err)
	}
	if resp, err := get(t, public+script); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("the admin's script on the public listener: %v %v, want 404", resp.Response, err)
	}
}

// checkMetrics: the ops listener serves Prometheus text with the request just made, counted by route pattern and
// status class only, and the recorder's queue gauge; the public listener does not serve it. Unknown paths, one of them
// with an identifier in it, share one series each (the pages' "/", or "/api/" under the API) instead of making one each.
func checkMetrics(t *testing.T, addrs addresses) {
	t.Helper()
	for _, path := range []string{"/api/console/v1/nothing-here", "/api/console/v1/key_01hxyz0000000000000000000a"} {
		if _, err := get(t, "http://"+addrs.Public+path); err != nil {
			t.Fatal(err)
		}
	}
	if resp, err := get(t, "http://"+addrs.Public+"/metrics"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /metrics on the public listener: %v %v, want 404", resp.Response, err)
	}
	resp, err := get(t, "http://"+addrs.Ops+"/metrics")
	if err != nil || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("GET /metrics on the ops listener: %v %v", resp.Response, err)
	}
	text := string(resp.body)
	for _, want := range []string{
		`axiom_http_server_requests_total{outcome="completed",route="GET /api/console/v1/version",status_class="2xx"} 1`,
		`axiom_http_server_requests_total{outcome="completed",route="/",status_class="4xx"} 2`,
		`axiom_http_server_requests_total{outcome="completed",route="/api/",status_class="4xx"} 2`,
		`axiom_http_server_duration_seconds_count{outcome="completed",route="GET /api/console/v1/version",status_class="2xx"} 1`,
		"axiom_observability_queue_depth 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the metrics lack %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"key_01hxyz", "nothing-here", "request_id", "account", "user", "planted"} {
		if strings.Contains(text, leak) {
			t.Errorf("the metrics mention %q: a label or value that is not bounded", leak)
		}
	}
}

// TestServeRejectsBadConfiguration: the node does not start, names every wrong variable or the database problem, and
// quotes no value.
func TestServeRejectsBadConfiguration(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, modules()...)
	orphaned := pgtest.New(t, modules()...)
	if err := pgtest.Run(t.Context(), orphaned.Superuser, `INSERT INTO suppliers.suppliers (owner_scope, name, kind,
		origin, upstream_account, native_unit, settlement_currency, mgmt_egress_id)
		VALUES ('platform', 'Orphan', 'ghost', 'https://ghost.example', 'a', 'usd', 'USD', 'direct')`); err != nil {
		t.Fatal(err)
	}
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
			[]string{"AXIOM_SHUTDOWN_TIMEOUT=planted-value", "AXIOM_PROBES_ADDR=planted-value", "AXIOM_PROBES_ADRR=planted-value"},
			[]string{"AXIOM_SHUTDOWN_TIMEOUT: want", "AXIOM_PROBES_ADDR: want", "AXIOM_PROBES_ADRR: unknown variable", "AXIOM_DATABASE_URL: required"},
		},
		{
			"a taken address",
			[]string{"AXIOM_PROBES_ADDR=" + taken.Addr().String(), "AXIOM_DATABASE_URL=" + database.App},
			[]string{"AXIOM_PROBES_ADDR: "},
		},
		{
			"a taken admin address",
			[]string{"AXIOM_ADMIN_ADDR=" + taken.Addr().String(), "AXIOM_DATABASE_URL=" + database.App},
			[]string{"AXIOM_ADMIN_ADDR: "},
		},
		{
			"a database login that escapes row-level security",
			[]string{"AXIOM_DATABASE_URL=" + database.Superuser},
			[]string{"database: the database login can act as roles that row-level security cannot hold"},
		},
		{
			"a supplier whose adapter this program lacks",
			[]string{"AXIOM_DATABASE_URL=" + orphaned.App},
			[]string{`"Orphan" (kind "ghost")`},
		},
		{
			"a database server that never answers",
			[]string{"AXIOM_STARTUP_TIMEOUT=1s", "AXIOM_DATABASE_URL=postgres://planted-value:planted-value@" + taken.Addr().String() + "/planted-value"},
			[]string{"database: AXIOM_DATABASE_URL: no answer within AXIOM_STARTUP_TIMEOUT"},
		},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) // a node that wrongly starts stops here
		// Free ports unless the case sets its own, so a node that wrongly starts takes no fixed port.
		environ := append([]string{
			"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
			"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
		}, c.environ...)
		err := serve(ctx, environ, slog.New(slog.DiscardHandler))
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

// records receives log records: the JSON handler writes each record with a single Write. Once the buffer is full,
// records are dropped rather than blocking the node: the tests read only up to "serving" and "ready", the first records.
type records chan []byte

func (r records) Write(p []byte) (int, error) {
	select {
	case r <- bytes.Clone(p):
	default:
	}
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

// checkGuard: the console's other operations are behind the guard: at a host no site registers, which is the address
// this test dials, the answer is the contract's 404, uncached.
func checkGuard(t *testing.T, url string) {
	t.Helper()
	resp, err := get(t, url)
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Error struct{ Code string } }
	if err := json.Unmarshal(resp.body, &body, json.MatchCaseInsensitiveNames(true)); err != nil || resp.StatusCode != http.StatusNotFound ||
		body.Error.Code != "not_found" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("GET %s: %d %s (%v)", url, resp.StatusCode, resp.body, err)
	}
}

// addresses are where a node listens, as it logs them.
type addresses struct {
	Msg                string `json:"msg"`
	Public, Admin, Ops string `json:",case:ignore"`
}

// servingAddrs reads log records until the node reports the addresses it listens on.
func servingAddrs(t *testing.T, logs records, done <-chan error) addresses {
	t.Helper()
	return awaitRecord(t, logs, done, "serving")
}

// awaitRecord reads log records until the node logs msg. The node logs "ready" after it has set the probes' readiness.
func awaitRecord(t *testing.T, logs records, done <-chan error, msg string) addresses {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second) // one deadline: other records must not extend it
	defer timeout.Stop()
	for {
		select {
		case line := <-logs:
			var record addresses
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("log record %q: %v", line, err)
			}
			if record.Msg == msg {
				return record
			}
		case err := <-done:
			t.Fatalf("the node stopped before logging %q: %v", msg, err)
		case <-timeout.C:
			t.Fatalf("the node did not log %q", msg)
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
	return getAccepting(t, url, "")
}

// getAccepting is get with an Accept header, as a browser navigating to a page sends one.
func getAccepting(t *testing.T, url, accept string) (response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	body, err := io.ReadAll(resp.Body)
	return response{resp, body}, errors.Join(err, resp.Body.Close())
}

// TestStartupTimeoutCoversTheFallbackCheck: a node whose check of the fallback prices waits on a lock fails within
// AXIOM_STARTUP_TIMEOUT, as one whose database does not answer, instead of hanging before its listeners open.
func TestStartupTimeoutCoversTheFallbackCheck(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, modules()...)
	admin, err := pg.Open(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "LOCK TABLE billing.fallbacks IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	<-locked
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_STARTUP_TIMEOUT=1s",
		"AXIOM_DATABASE_URL=" + database.App, "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
	}
	served := make(chan error, 1)
	go func() { served <- serve(t.Context(), environ, slog.New(slog.DiscardHandler)) }()
	select {
	case err := <-served:
		if !errors.Is(err, errNoAnswer) {
			t.Errorf("serve = %v, want errNoAnswer", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve still waits on the lock 10s past a startup timeout of 1s")
	}
	close(unlock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestFailedStartLeavesNoRecorderWriter: a node that fails after the call-record writer started, here on a taken
// address, closes it on the way out, so the writer has exited when serve returns. The test holds the Recorder that
// this call created, so no other node's writer, parallel or earlier, can change the outcome.
func TestFailedStartLeavesNoRecorderWriter(t *testing.T) {
	t.Parallel()
	taken, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close() //nolint:errcheck // a test listener
	environ := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=" + taken.Addr().String(), "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'),
		"AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'), "AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'), "AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App,
	}
	var recorder *observability.Recorder
	record := func(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, logger *slog.Logger) *observability.Recorder {
		recorder = startRecorder(ctx, db, tel, logger)
		return recorder
	}
	if err := serveWith(t.Context(), environ, slog.New(slog.DiscardHandler), directEgress, record); err == nil {
		t.Fatal("the node started on a taken address")
	}
	if recorder == nil {
		t.Fatal("the node failed before it started the recorder")
	}
	select {
	case <-recorder.Done():
	default:
		t.Fatal("the call-record writer is still running after serve returned")
	}
}
