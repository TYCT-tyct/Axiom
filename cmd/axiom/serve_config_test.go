package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestServeRefusesBadKeysAndSettings: the key files and the new settings are checked before the database is touched,
// every problem named at once, and no value or path quoted.
func TestServeRefusesBadKeysAndSettings(t *testing.T) {
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
		{"no key files", []string{database}, []string{"AXIOM_MASTER_KEY_FILE: required", "AXIOM_KEY_HASH_FILE: required"}},
		{
			"files that are not there",
			[]string{database, "AXIOM_MASTER_KEY_FILE=/planted-value/master", "AXIOM_KEY_HASH_FILE=" + good},
			[]string{"AXIOM_MASTER_KEY_FILE: open: file not found"},
		},
		{
			"a hashing key file that is no key file",
			[]string{database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + filepath.Join(t.TempDir(), "planted-value")},
			[]string{"AXIOM_KEY_HASH_FILE: open: file not found"},
		},
		{
			"settings that are no settings",
			[]string{
				database, "AXIOM_MASTER_KEY_FILE=" + good, "AXIOM_KEY_HASH_FILE=" + good, "AXIOM_HASH_CONCURRENCY=0",
				"AXIOM_HASH_WAIT=planted-value", "AXIOM_TRUSTED_PROXIES=planted-value", "AXIOM_HASH_CONCURENCY=4",
			},
			[]string{
				"AXIOM_HASH_CONCURRENCY: want a whole number of at least 1", "AXIOM_HASH_WAIT: want a positive duration",
				"AXIOM_TRUSTED_PROXIES: want IP addresses and networks", "AXIOM_HASH_CONCURENCY: unknown variable",
			},
		},
	}
	if runtime.GOOS != "windows" { // Windows has no mode bits to read
		cases = append(cases, struct {
			name    string
			environ []string
			want    []string
		}{
			"a key file others can read",
			[]string{database, "AXIOM_MASTER_KEY_FILE=" + readable, "AXIOM_KEY_HASH_FILE=" + good},
			[]string{"AXIOM_MASTER_KEY_FILE: permissions must deny all group and other access"},
		})
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		environ := append([]string{"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_OPS_ADDR=127.0.0.1:0"}, c.environ...)
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
