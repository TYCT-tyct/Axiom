package main

import (
	"bytes"
	"context"
	"encoding/json/v2" //nolint:depguard // decodes the program's own log records; jsonx guards input from outside
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/pg"
)

// TestMain lets a test run the program in a process of its own, which exit codes and signals need: after "--" the test
// binary is axiom, with the words after it as its command line.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--" {
		os.Args = append([]string{"axiom"}, os.Args[2:]...)
		main()
		return
	}
	os.Exit(m.Run())
}

// command returns the test binary as the axiom program, with every AXIOM_* variable of the test's own environment
// removed so that only environ configures it.
func command(t *testing.T, environ []string, args ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, append([]string{"--"}, args...)...) //nolint:gosec // the test binary itself, with the test's own arguments
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "AXIOM_") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, environ...)
	return cmd
}

// runAxiom runs the program to its end and returns what it printed and its exit code.
func runAxiom(t *testing.T, environ []string, args ...string) (string, int) {
	t.Helper()
	cmd := command(t, environ, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run %q: %v", strings.Join(args, " "), err)
	}
	return out.String(), exit.ExitCode()
}

// TestMainExitCodes: a successful command exits 0, a command that fails exits 1, and a command line main does not
// accept exits 2 with the usage.
func TestMainExitCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		environ []string
		code    int
		printed string
	}{
		{"no command", nil, nil, 2, "usage: axiom"},
		{"an unknown command", []string{"frobnicate"}, nil, 2, "usage: axiom"},
		{"version", []string{"version"}, nil, 0, "axiom "},
		{"migrate without its database", []string{"migrate"}, nil, 1, "AXIOM_MIGRATE_DATABASE_URL: required"},
		{"serve without its database", []string{"serve"}, nil, 1, "AXIOM_DATABASE_URL: required"},
		{"healthcheck with nothing listening", []string{"healthcheck"}, []string{"AXIOM_PROBES_ADDR=" + freePort(t)}, 1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, code := runAxiom(t, c.environ, c.args...)
			if code != c.code {
				t.Errorf("exit code %d, want %d: %s", code, c.code, out)
			}
			if !strings.Contains(out, c.printed) {
				t.Errorf("printed %q, want it to contain %q", out, c.printed)
			}
		})
	}
}

// TestSecondSignalEndsTheNodeAtOnce: the first signal starts a graceful shutdown, which waits for the request in
// flight; the second signal ends the process at once instead of waiting out the shutdown timeout.
func TestSecondSignalEndsTheNodeAtOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot send os.Interrupt to another process")
	}
	c := withOwner(t)
	// A table lock holds the sign-in in flight: without a request to wait for, the node would stop before the second
	// signal and the test would prove nothing.
	watcher, err := pg.Open(t.Context(), c.database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(watcher.Close)
	free, freed := holdRow(t, watcher, "LOCK TABLE accounts.users IN ACCESS EXCLUSIVE MODE")
	defer free()

	p := startAxiom(t, []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_DATABASE_URL=" + c.database.App, "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_SHUTDOWN_TIMEOUT=1m", // long enough that only the second signal can explain a quick exit
	}, "serve")
	serving := p.until(t, "serving")

	// The owner's sign-in waits for the users' table, so the request stays in flight for shutdown to wait on.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	answered := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+serving.Public+"/api/console/v1/auth/sign-in",
			strings.NewReader(`{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`))
		if err != nil {
			answered <- err
			return
		}
		req.Host = consoleHost
		req.Header.Set("Origin", "https://"+consoleHost)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			err = errors.Join(err, resp.Body.Close())
		}
		answered <- err
	}()
	waitFor(t, func() bool {
		var waiting int
		err := watcher.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND usename = 'axiom_app' AND wait_event_type = 'Lock'`).Scan(&waiting)
		})
		return err == nil && waiting > 0
	})

	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	p.until(t, "shutting down")
	start := time.Now()
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("the node stopped before the second signal: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- p.wait() }()
	select {
	case err := <-waited:
		if err == nil {
			t.Error("the second signal ended the node successfully; want the default action to end it")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("the second signal took %v to end the node, want at once", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal did not end the node at once")
	}
	free()
	if err := <-freed; err != nil {
		t.Error(err)
	}
	cancel()
	select {
	case err := <-answered:
		if err == nil {
			t.Error("the sign-in answered although the node was ended")
		}
	case <-time.After(5 * time.Second):
		t.Error("the sign-in is still waiting")
	}
}

// program is axiom running in a process of its own, with its standard output read as log records.
type program struct {
	cmd     *exec.Cmd
	records chan addresses
	stderr  lockedBuffer
	waitErr error
	waiting sync.Once
}

func startAxiom(t *testing.T, environ []string, args ...string) *program {
	t.Helper()
	p := &program{cmd: command(t, environ, args...), records: make(chan addresses, 64)}
	p.cmd.Stdout, p.cmd.Stderr = p, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A test that failed before its own wait must not leave the program running.
		_ = p.cmd.Process.Kill() //nolint:errcheck // a program already gone needs no killing
		_ = p.wait()             //nolint:errcheck // the test reports the exit status where it matters
	})
	return p
}

// wait waits for the program to end, however many callers ask: Wait may be called once, and a failing cleanup and the
// test itself both need its result.
func (p *program) wait() error {
	p.waiting.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

// Write reads the program's log records. The JSON handler writes each record in one call.
func (p *program) Write(b []byte) (int, error) {
	var record addresses
	if err := json.Unmarshal(b, &record); err == nil {
		select {
		case p.records <- record:
		default: // a full channel must not stall the node; the test reads only the first records
		}
	}
	return len(b), nil
}

// until waits for the program to log the record with msg.
func (p *program) until(t *testing.T, msg string) addresses {
	t.Helper()
	for {
		select {
		case record := <-p.records:
			if record.Msg == msg {
				return record
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the program logged no %q: %s", msg, p.stderr.String())
		}
	}
}

// lockedBuffer collects the program's standard error, which another goroutine writes while the test reads it.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
