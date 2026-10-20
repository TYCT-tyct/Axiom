package kv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// passwordFile writes a password file with the given content and mode, and returns its path.
func passwordFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write the password file: %v", err)
	}
	// os.WriteFile is subject to the umask, so the mode the test asks for is set again.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("set the password file's mode: %v", err)
	}
	return path
}

// TestConfigRefusesBadSettings covers the rows of README「配置与启动」 that need no server: each names its variable and
// none quotes a value, which may be a secret in the wrong setting.
func TestConfigRefusesBadSettings(t *testing.T) {
	t.Parallel()
	good := passwordFile(t, "s3cret\n", 0o600)
	const planted = "planted-value"
	for _, tt := range []struct {
		name  string
		cfg   Config
		want  []string // every one of these is in the error
		never []string // and none of these is
	}{
		{
			name: "no URL and one node", cfg: Config{Nodes: 1}, // single-core mode: nothing is checked further
		},
		{
			name: "no URL and two nodes", cfg: Config{Nodes: 2},
			want: []string{NodesVar, URLVar, "single node"},
		},
		{
			name: "no nodes", cfg: Config{Nodes: 0}, want: []string{NodesVar, "at least 1"},
		},
		{
			name: "a URL that is not one", cfg: Config{URL: "planted-value", PasswordFile: good, Nodes: 1},
			want: []string{URLVar}, never: []string{planted},
		},
		{
			name: "another scheme", cfg: Config{URL: "redis://axiom@valkey:6379", PasswordFile: good, Nodes: 1},
			want: []string{URLVar, "valkey://"}, never: []string{"redis://"},
		},
		{
			name: "no user", cfg: Config{URL: "valkey://valkey:6379", PasswordFile: good, Nodes: 1},
			want: []string{URLVar, "user"},
		},
		{
			name: "no port", cfg: Config{URL: "valkey://axiom@valkey", PasswordFile: good, Nodes: 1},
			want: []string{URLVar, "host:port"},
		},
		{
			name: "a password in the URL", cfg: Config{URL: "valkey://axiom:" + planted + "@valkey:6379", PasswordFile: good, Nodes: 1},
			want: []string{URLVar, PasswordFileVar}, never: []string{planted},
		},
		{
			name: "a database path", cfg: Config{URL: "valkey://axiom@valkey:6379/0", PasswordFile: good, Nodes: 1},
			want: []string{URLVar, "nothing else"},
		},
		{
			name: "a URL without a password file", cfg: Config{URL: "valkey://axiom@valkey:6379", Nodes: 1},
			want: []string{PasswordFileVar, "required"},
		},
		{
			name: "a password file that is not there",
			cfg:  Config{URL: "valkey://axiom@valkey:6379", PasswordFile: filepath.Join(t.TempDir(), planted), Nodes: 1},
			want: []string{PasswordFileVar, "file not found"}, never: []string{planted},
		},
		{
			name: "a password file with an empty password",
			cfg:  Config{URL: "valkey://axiom@valkey:6379", PasswordFile: passwordFile(t, "\n", 0o600), Nodes: 1},
			want: []string{PasswordFileVar, "want a password"},
		},
		{
			name: "a password file with a space",
			cfg:  Config{URL: "valkey://axiom@valkey:6379", PasswordFile: passwordFile(t, planted+" more\n", 0o600), Nodes: 1},
			want: []string{PasswordFileVar, "printable"}, never: []string{planted},
		},
		{
			name: "a password file too long",
			cfg:  Config{URL: "valkey://axiom@valkey:6379", PasswordFile: passwordFile(t, strings.Repeat("a", 129), 0o600), Nodes: 1},
			want: []string{PasswordFileVar, "at most"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// An ended context: the cases below are refused before anything is dialled, and the ones that pass must not
			// reach a server either, which the empty Deps.DB leaves them no heartbeat to write through.
			_, err := Open(t.Context(), tt.cfg, Deps{})
			if tt.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%+v was accepted", tt.cfg)
			}
			says(t, err, tt.want, tt.never)
		})
	}
}

// says checks that an error mentions every one of want and none of never.
func says(t *testing.T, err error, want, never []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%q does not say %q", err, w)
		}
	}
	for _, n := range never {
		if strings.Contains(err.Error(), n) {
			t.Errorf("%q quotes %q", err, n)
		}
	}
}

// TestPasswordFilePermissions: a file the group or others may read is refused on the systems whose mode bits mean
// something, as the master key files are (internal/platform/secret/keyring.go).
func TestPasswordFilePermissions(t *testing.T) {
	t.Parallel()
	path := passwordFile(t, "s3cret\n", 0o644)
	_, err := readPassword(path)
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("a file on a system without mode bits: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "deny all group and other access") {
		t.Fatalf("a world-readable password file: %v", err)
	}
}

// TestPasswordFileIsARegularFile: a directory, or a pipe, is not a password file.
func TestPasswordFileIsARegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readPassword(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a directory as the password file: %v", err)
	}
}

// TestPasswordStripsTheNewline: a deployment writes the password as one line, and the newline is not part of it; a
// Windows editor's CRLF is not either.
func TestPasswordStripsTheNewline(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"s3cret", "s3cret\n", "s3cret\r\n"} {
		path := passwordFile(t, content, 0o600)
		got, err := readPassword(path)
		if err != nil || got != "s3cret" {
			t.Fatalf("%q read as %q, %v", content, got, err)
		}
	}
}

// TestSingleCoreMode: without a URL the client is the single-core one, it holds no connection, and its clock is the
// local one, uncorrected.
func TestSingleCoreMode(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	client, err := Open(t.Context(), Config{Nodes: 1}, Deps{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if client.Enabled() {
		t.Error("single-core mode reports an enabled cache")
	}
	if got := client.Nodes(); got != 1 {
		t.Errorf("nodes = %d, want 1", got)
	}
	if got := client.Now(); !got.Equal(now) {
		t.Errorf("now = %v, want %v", got, now)
	}
	if _, err := client.Do(t.Context(), Precheck, serverMemory()); err == nil {
		t.Error("single-core mode ran a command")
	}
	if err := client.Close(t.Context()); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestResolveKeepsWhatItRead: the URL is parsed and the password file read once, when serve reads its settings, and Open
// uses that: a file removed in between does not change the start, which would otherwise read it a second time, after the
// database had been opened.
func TestResolveKeepsWhatItRead(t *testing.T) {
	t.Parallel()
	path := passwordFile(t, "s3cret\n", 0o600)
	cfg := Config{URL: "valkey://axiom@127.0.0.1:1", PasswordFile: path, Nodes: 1}
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.resolved == nil || cfg.resolved.password != "s3cret" || cfg.resolved.username != "axiom" {
		t.Fatalf("resolved = %+v", cfg.resolved)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	client, err := Open(t.Context(), cfg, Deps{}) // nothing listens on port 1: the node starts through the outage
	if err != nil {
		t.Fatalf("open after the file was removed: %v", err)
	}
	if err := client.Close(t.Context()); err != nil {
		t.Errorf("close: %v", err)
	}
}
