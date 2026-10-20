package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"axiom/internal/gateway"
	"axiom/internal/platform/pgtest"
)

// TestServeRefusesAPoolBelowTheGate: the gateway hands out Config.PoolSize database connections and keeps the last
// Config.PoolReserve of them for completions, so a connection string that shrinks the pool below them would queue the
// ones the reserve promised. The node refuses to start and names the variable and both sizes, not the string; a pool
// of exactly that size starts.
func TestServeRefusesAPoolBelowTheGate(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t, modules()...).App
	gate := gateway.DefaultConfig().PoolSize
	base := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'),
	}
	withPool := func(size int) []string {
		return append(slices.Clone(base), fmt.Sprintf("AXIOM_DATABASE_URL=%s&pool_max_conns=%d", url, size))
	}

	t.Run("below the gate", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		err := serve(ctx, withPool(gate-1), slog.New(slog.DiscardHandler))
		if err == nil {
			t.Fatal("the node started with fewer connections than the gateway hands out")
		}
		for _, want := range []string{"AXIOM_DATABASE_URL", fmt.Sprintf("pool_max_conns %d", gate-1), fmt.Sprintf("%d database connections", gate)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not say %q", err, want)
			}
		}
		if strings.Contains(err.Error(), url) {
			t.Errorf("%q quotes the connection string", err)
		}
	})

	t.Run("at the gate", func(t *testing.T) {
		logs := make(records, 16)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- serve(ctx, withPool(gate), slog.New(slog.NewJSONHandler(logs, nil))) }()
		servingAddrs(t, logs, done)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the node did not shut down")
		}
	})
}

// TestServeRefusesBadKeysAndSettings: the key files and the new settings are checked before the database is touched,
// every problem named at once, and no value or path quoted.
func TestServeRefusesBadKeysAndSettings(t *testing.T) {
	t.Parallel()
	// The database is never reached: every case fails first.
	database := "AXIOM_DATABASE_URL=postgres://planted-value:planted-value@" + net.JoinHostPort("127.0.0.1", "1") + "/planted-value"
	good := keyFile(t, 'g')
	readable := filepath.Join(t.TempDir(), "readable")
	if err := os.WriteFile(readable, []byte("1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o644); err != nil { //nolint:gosec // the point of the test
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		environ []string
		want    []string
	}{
		{"no key files", []string{database}, []string{
			"AXIOM_MASTER_KEY_FILE: required", "AXIOM_KEY_HASH_FILE: required", "AXIOM_IDENTITY_KEY_FILE: required",
		}},
		{
			"files that are not there",
			[]string{database, "AXIOM_MASTER_KEY_FILE=/planted-value/master", "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good},
			[]string{"AXIOM_MASTER_KEY_FILE: open: file not found"},
		},
		{
			"a hashing key file that is no key file",
			[]string{database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + filepath.Join(t.TempDir(), "planted-value"), "AXIOM_IDENTITY_KEY_FILE=" + good},
			[]string{"AXIOM_KEY_HASH_FILE: open: file not found"},
		},
		{
			// The identity key is a file of its own, so its absence is the one the node names (#205).
			"an identity key file that is not there",
			[]string{database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=/planted-value/identity"},
			[]string{"AXIOM_IDENTITY_KEY_FILE: open: file not found"},
		},
		{
			"settings that are no settings",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_HASH_CONCURRENCY=0", "AXIOM_HASH_WAIT=planted-value", "AXIOM_TRUSTED_PROXIES=planted-value", "AXIOM_HASH_CONCURENCY=4",
			},
			[]string{
				"AXIOM_HASH_CONCURRENCY: want a whole number of at least 1", "AXIOM_HASH_WAIT: want a positive duration",
				"AXIOM_TRUSTED_PROXIES: want IP addresses and networks", "AXIOM_HASH_CONCURENCY: unknown variable",
			},
		},
		{
			// K1 to K3 are not all in yet: with login and stickiness still in the process, a second core node would
			// multiply the login attempts (internal/platform/kv/README.md「落地顺序」).
			"more than one core node",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_CORE_NODES=2", "AXIOM_VALKEY_URL=valkey://axiom@planted-value:6379", "AXIOM_VALKEY_PASSWORD_FILE=" + good,
			},
			[]string{"AXIOM_CORE_NODES: 2 nodes are not supported yet"},
		},
		{
			"a node ID that is no name",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_NODE_ID=planted value",
			},
			[]string{"AXIOM_NODE_ID: want a name"},
		},
		{
			"more than one core node and no Valkey",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_CORE_NODES=2",
			},
			[]string{"AXIOM_CORE_NODES: 2 nodes are not supported yet"},
		},
		{
			// The cache's URL and password file are read with the other settings, before the database, which is not there.
			"a Valkey URL that is no URL",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_VALKEY_URL=planted-value", "AXIOM_VALKEY_PASSWORD_FILE=" + good,
			},
			[]string{"AXIOM_VALKEY_URL: want"},
		},
		{
			"a Valkey password file that is not there",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_VALKEY_URL=valkey://axiom@planted-value:6379", "AXIOM_VALKEY_PASSWORD_FILE=/planted-value/password",
			},
			[]string{"AXIOM_VALKEY_PASSWORD_FILE: open: file not found"},
		},
	}
	if runtime.GOOS != "windows" { // Windows has no mode bits to read
		cases = append(cases, struct {
			name    string
			environ []string
			want    []string
		}{
			"a key file others can read",
			[]string{database, "AXIOM_MASTER_KEY_FILE=" + readable, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good},
			[]string{"AXIOM_MASTER_KEY_FILE: permissions must deny all group and other access"},
		})
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		environ := append([]string{"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0"}, c.environ...)
		err := serve(ctx, environ, slog.New(slog.DiscardHandler))
		cancel()
		if err == nil {
			t.Fatalf("%s: the node started", c.name)
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q does not say %q", c.name, err, want)
			}
		}
		if strings.Contains(err.Error(), "planted-value") || strings.Contains(err.Error(), good) || strings.Contains(err.Error(), readable) {
			t.Errorf("%s: %q quotes a value or a path", c.name, err)
		}
	}
}
