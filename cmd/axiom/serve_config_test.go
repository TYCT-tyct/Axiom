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

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestCommandsUseTheConfiguredConnectionBudget(t *testing.T) {
	t.Parallel()
	environ := []string{
		"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App + "&pool_max_conns=8",
		"AXIOM_DB_COMPLETION_RESERVE=2",
	}
	for name, run := range map[string]func() error{
		"billing": func() error {
			return withDatabase(t.Context(), environ, func(db *pg.DB) error {
				if db.MaxConns() != 8 || db.CompletionConns() != 2 {
					t.Errorf("budget = %d, reserve = %d", db.MaxConns(), db.CompletionConns())
				}
				return nil
			})
		},
		"operator": func() error {
			return withOperator(t.Context(), environ, func(*accounts.Operator) error { return nil })
		},
		"catalog": func() error {
			return withCatalog(t.Context(), environ, func(*catalog.Catalog, *catalog.Fetcher) error { return nil })
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestServeChecksTheConnectionBudget refuses an impossible reserve and starts with a smaller deployment budget.
func TestServeChecksTheConnectionBudget(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t, modules()...).App
	const reserve = 2
	base := []string{
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_ADMIN_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		"AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_MASTER_KEY_FILE=" + keyFile(t, 'm'), "AXIOM_KEY_HASH_FILE=" + keyFile(t, 'h'),
		"AXIOM_IDENTITY_KEY_FILE=" + keyFile(t, 'i'), "AXIOM_DB_COMPLETION_RESERVE=2",
	}
	withPool := func(size int) []string {
		return append(slices.Clone(base), fmt.Sprintf("AXIOM_DATABASE_URL=%s&pool_max_conns=%d", url, size))
	}

	t.Run("reserve consumes the budget", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		err := serve(ctx, withPool(reserve), slog.New(slog.DiscardHandler))
		if err == nil {
			t.Fatal("the node started without ordinary connections")
		}
		for _, want := range []string{pg.DatabaseURLVar, "pool_max_conns 2", "completion reserve"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not say %q", err, want)
			}
		}
		if strings.Contains(err.Error(), url) {
			t.Errorf("%q quotes the connection string", err)
		}
	})

	t.Run("smaller than the default", func(t *testing.T) {
		logs := make(records, 16)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- serve(ctx, withPool(8), slog.New(slog.NewJSONHandler(logs, nil))) }()
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
				"AXIOM_DB_COMPLETION_RESERVE=0", "AXIOM_HASH_CONCURRENCY=0", "AXIOM_HASH_WAIT=planted-value", "AXIOM_TRUSTED_PROXIES=planted-value", "AXIOM_HASH_CONCURENCY=4",
			},
			[]string{
				"AXIOM_DB_COMPLETION_RESERVE: want a whole number of at least 1", "AXIOM_HASH_CONCURRENCY: want a whole number of at least 1", "AXIOM_HASH_WAIT: want a positive duration",
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
		{
			// Email is optional, but setting one of its variables arms all of them (#321).
			"half an email configuration",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_IDENTITY_KEY_FILE=" + good,
				"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_PASSWORD=planted-value",
			},
			[]string{"AXIOM_SMTP_FROM: required", "AXIOM_SMTP_USERNAME: required"},
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
