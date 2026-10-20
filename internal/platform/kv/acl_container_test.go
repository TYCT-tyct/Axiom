package kv_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// TestACLAllowsWhatTheProgramSends: the deployed user runs every use of K1, and the keys it writes are under ax:*; a
// NOPERM here means the compose ACL and the client disagree (README「权限」).
func TestACLAllowsWhatTheProgramSends(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	// The client is the deployed user, axiom: it is what runs the script and what writes and reads the keys.
	client := server.Open(t, kv.Deps{})
	if _, err := count(t, client, kv.NewScript(countScript), 100); err != nil {
		t.Fatalf("run the count as the deployed user: %v", err)
	}
	deployed := server.Client(t)
	for name, cmd := range map[string]valkey.Completed{
		"ping": deployed.B().Ping().Build(),
		"info": deployed.B().Info().Section("memory").Build(),
		"time": deployed.B().Time().Build(),
	} {
		if err := deployed.Do(t.Context(), cmd).Error(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestACLRefusesWhatTheProgramDoesNotSend: the user holds no permission to empty the cache, to list keys, to read the
// configuration, to run a script it was not sent, or to touch a key outside ax:* (README「权限」).
func TestACLRefusesWhatTheProgramDoesNotSend(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	deployed := server.Client(t)
	for _, tt := range []struct {
		name string
		cmd  valkey.Completed
	}{
		{"FLUSHALL", deployed.B().Flushall().Build()},
		{"KEYS", deployed.B().Keys().Pattern("*").Build()},
		{"CONFIG", deployed.B().ConfigGet().Parameter("*").Build()},
		{"EVALSHA", deployed.B().Evalsha().Sha1("0000000000000000000000000000000000000000").Numkeys(0).Build()},
		{"a key outside ax:", deployed.B().Set().Key("elsewhere").Value("1").Build()},
	} {
		err := deployed.Do(t.Context(), tt.cmd).Error()
		if err == nil {
			t.Errorf("%s was allowed", tt.name)
			continue
		}
		if reply, isReply := errors.AsType[*valkey.ValkeyError](err); !isReply || !strings.HasPrefix(reply.Error(), "NOPERM") {
			t.Errorf("%s: %v, want the server's NOPERM", tt.name, err)
		}
	}
}

// TestWrongPasswordRefusesTheStart: a password file that does not hold the user's password is a deployment mistake, so
// the start stops and names the two variables rather than serving on fallbacks (README「配置与启动」).
func TestWrongPasswordRefusesTheStart(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	cfg := server.Config(kvtest.Namespace())
	cfg.PasswordFile = writePassword(t, "wrong-password\n")
	_, err := kv.Open(t.Context(), cfg, kv.Deps{})
	if err == nil {
		t.Fatal("a client with the wrong password started")
	}
	for _, want := range []string{kv.URLVar, kv.PasswordFileVar, "WRONGPASS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

// TestUnreachableStartsOnFallbacks: a server that is not there is an outage, not a mistake: the node starts with the
// breaker open, and every access returns unavailable (README「配置与启动」「熔断」).
func TestUnreachableStartsOnFallbacks(t *testing.T) {
	t.Parallel()
	// The port the tests' own containers map is random; 1 is a port nothing listens on, so the probe finds no server.
	cfg := kv.Config{
		URL: "valkey://axiom@127.0.0.1:1", PasswordFile: writePassword(t, "password\n"), Nodes: 1,
		Namespace: kvtest.Namespace(),
	}
	seen := newReadings()
	client, err := kv.Open(t.Context(), cfg, kv.Deps{Meter: seen.meter})
	if err != nil {
		t.Fatalf("a node whose server is not there refused to start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(t, client) })
	if !client.Enabled() {
		t.Fatal("a node with a URL is in single-core mode")
	}
	if got := seen.breakerOpen(t); got != 1 {
		t.Errorf("breaker_open = %d after a probe that found no server", got)
	}
	if _, err := client.Do(t.Context(), kv.Precheck, kv.NewScript(countScript).Cmd(nil, "1000")); !errors.Is(err, kv.ErrUnavailable) {
		t.Fatalf("an access to a server that is not there: %v", err)
	}
}

// writePassword writes a password file of the given content, with the permissions the client requires.
func writePassword(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the password file: %v", err)
	}
	return path
}

// closeQuietly closes a client the test owns and reports a failure to remove its heartbeat row, which a client without a
// database has none of.
func closeQuietly(t *testing.T, client *kv.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Errorf("close the client: %v", err)
	}
}
