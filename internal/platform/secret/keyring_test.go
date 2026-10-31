package secret_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
)

// writeKeyFile writes one version's key to path with an explicit mode: os.WriteFile alone leaves the mode subject to the
// process's umask.
func writeKeyFile(t testing.TB, path string, mode os.FileMode, version uint32, value byte) {
	t.Helper()
	if err := os.WriteFile(path, []byte(keyLine(version, value)), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidKeyFiles(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	cases := []struct{ name, content, reason string }{
		{"empty", "", "at least one"},
		{"blank", "\n", "line 1"},
		{"blank after key", keyLine(1, 1) + "\n", "line 2"},
		{"missing separator", "planted-key", "version:base64"},
		{"bad base64", "1:planted-key", "base64"},
		{"extra separator", "1:" + key + ":", "base64"},
		// The same 32 bytes with non-zero unused bits in the last character: not the canonical encoding.
		{"non-canonical padding bits", "1:" + key[:42] + "h=", "base64"},
		{"short key", "1:" + base64.StdEncoding.EncodeToString([]byte("planted-key")), "32 bytes"},
		{"long key", "1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 33))), "32 bytes"},
		{"missing key", "1:", "32 bytes"},
		{"duplicate", keyLine(1, 1) + keyLine(1, 2), "duplicate"},
		{"reused key", keyLine(1, 1) + keyLine(3, 2) + keyLine(2, 1), "line 3: key already used by version 1"},
		{"zero", "0:" + key, "version"},
		{"negative", "-1:" + key, "version"},
		{"overflow", "4294967296:" + key, "version"},
		{"text version", "planted-key:" + key, "version"},
		{"leading zero", "01:" + key, "version"},
		{"leading plus", "+1:" + key, "version"},
		{"whitespace", "1: " + key, "base64"},
		{"embedded carriage return", "1:" + key[:10] + "\r" + key[10:], "base64"},
		{"oversized line", "1:" + strings.Repeat("x", 70000), "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := keyFile(t, c.content)
			keys, err := secret.Load(path)
			if keys != nil || err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("want %q, got %v", c.reason, err)
			}
			if !strings.Contains(err.Error(), "AXIOM_MASTER_KEY_FILE") || strings.Contains(err.Error(), "planted-key") || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), path) {
				t.Fatal("error lacks context or reveals input")
			}
		})
	}
}

func TestKeyFilePaths(t *testing.T) {
	for _, c := range []struct{ path, reason string }{
		{"", "required"},
		{filepath.Join(t.TempDir(), "planted-key"), "not found"},
		{t.TempDir(), "regular file"},
	} {
		keys, err := secret.Load(c.path)
		if keys != nil || err == nil || !strings.Contains(err.Error(), c.reason) || strings.Contains(err.Error(), "planted-key") {
			t.Fatalf("want %q, got %v", c.reason, err)
		}
	}
}

// TestKeyFileSymlinkRefused: the variable must name the file, not a link to one. The writer refuses to replace a link
// because rename replaces the directory entry, so a link would read one file and rotate another; the reader refuses it
// for the same reason: whoever can point the variable at a link chooses the key material.
func TestKeyFileSymlinkRefused(t *testing.T) {
	target := keyFile(t, keyLine(1, 1))
	link := filepath.Join(t.TempDir(), "master-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	keys, err := secret.Load(link)
	if keys != nil || err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a symlink to a key file loaded as %v; want a regular-file refusal", err)
	}
	if strings.Contains(err.Error(), link) || strings.Contains(err.Error(), target) {
		t.Fatal("the refusal quotes a path")
	}
}

// TestKeyFileSwapDuringLoadIsRefused replaces the path while it is being read: a 0600 key file and a 0644 one alternate
// at the same name, and a load may only ever return the 0600 file's key. The mode has to be read from the opened file,
// not from the path: a check on the path passes on the 0600 file and then opens whichever file was renamed over it.
func TestKeyFileSwapDuringLoadIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	dir := t.TempDir()
	path, good, bad := filepath.Join(dir, "master"), filepath.Join(dir, "good"), filepath.Join(dir, "bad")
	writeKeyFile(t, good, 0o600, 1, 1)
	writeKeyFile(t, bad, 0o644, 2, 2)
	if err := os.Link(good, path); err != nil {
		t.Fatal(err)
	}
	stop := swapKeyFiles(t, dir, path, good, bad)
	for i := range 4000 {
		got, err := secret.LoadSecret("AXIOM_IDENTITY_KEY_FILE", path)
		if err == nil && string(got) == strings.Repeat("\x02", 32) {
			stop()
			t.Fatalf("iteration %d loaded the 0644 file's key through a path checked as 0600", i)
		}
	}
	stop()
}

// swapKeyFiles alternates good and bad at path until stop is called, which also waits for the last replacement. It
// reports a failed replacement with t.Errorf, the only failure call that may run in another goroutine.
func swapKeyFiles(t *testing.T, dir, path, good, bad string) (stop func()) {
	t.Helper()
	stopCh, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		next := filepath.Join(dir, "next")
		// The first replacement is the other file: renaming a name onto a link of the same inode does nothing and
		// leaves the source behind, so alternating from the file the path already names would stop after one flip.
		for i := 1; ; i++ {
			select {
			case <-stopCh:
				return
			default:
			}
			source := bad
			if i%2 == 0 {
				source = good
			}
			if err := replaceKeyFile(source, next, path); err != nil {
				t.Errorf("replacing the key file: %v", err)
				return
			}
			time.Sleep(20 * time.Microsecond)
		}
	}()
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(stopCh)
		<-done
	}
}

func TestKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	for _, mode := range []os.FileMode{0o600, 0o400, 0o640, 0o604, 0o666, 0o620, 0o601} {
		path := keyFile(t, keyLine(1, 1))
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := secret.Load(path)
		if mode&0o077 == 0 {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("mode %o: want permissions error, got %v", mode, err)
		}
	}
}

func TestKeyFileLineEndings(t *testing.T) {
	for _, content := range []string{
		keyLine(^uint32(0), 1),
		strings.TrimSuffix(keyLine(1, 1), "\n"),
		keyLine(1, 1),
		strings.ReplaceAll(keyLine(1, 1)+keyLine(2, 2), "\n", "\r\n"),
	} {
		loadKeys(t, content)
	}
}

// TestKeyMaterialFormatting: the types that hold key bytes are printed into a log line or an error as easily as a
// Secret is, so they redact every verb rather than the verbs their authors remembered. The verbs that bypass a method
// are the point: fmt's bad-verb diagnostic for %w on a non-error, and its reflection over the fields of a struct, which
// skips methods for an unexported field. Both used to print the map.
func TestKeyMaterialFormatting(t *testing.T) {
	path := keyFile(t, keyLine(1, 1))
	master, err := secret.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	mac, err := secret.LoadMAC(path)
	if err != nil {
		t.Fatal(err)
	}
	direct := []any{master, *master, mac, *mac}
	wrapped := []any{
		struct{ m secret.MasterKeys }{*master},
		struct{ M secret.MasterKeys }{*master},
		struct{ m secret.MACKeys }{*mac},
		struct{ M secret.MACKeys }{*mac},
		[]secret.MasterKeys{*master},
		map[string]secret.MACKeys{"m": *mac},
	}
	formats := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%b", "%o", "%U", "%t", "%f", "%e", "%g", "%20.3s", "%+020v", "%#q", "%Z", "%w", "%!w"}
	key := bytes.Repeat([]byte{1}, 32) // keyLine(1, 1)'s bytes
	for _, format := range formats {
		// A bad-verb diagnostic does not call Format at all, so %w cannot show the marker; what it must not show is a key.
		redacts := format != "%w"
		for _, shape := range direct {
			output := fmt.Sprintf(format, shape)
			t.Run(fmt.Sprintf("%s/direct", format), func(t *testing.T) {
				if redacts {
					assertRedacted(t, output)
				}
				assertNoKeyBytes(t, output, key)
			})
		}
		for _, shape := range wrapped {
			output := fmt.Sprintf(format, shape)
			t.Run(fmt.Sprintf("%s/wrapped", format), func(t *testing.T) { assertNoKeyBytes(t, output, key) })
		}
	}
	var logs bytes.Buffer
	for _, handler := range []slog.Handler{
		slog.NewTextHandler(&logs, nil), slog.NewJSONHandler(&logs, nil),
	} {
		logs.Reset()
		slog.New(handler).Info("keys", slog.Any("master", master), slog.Any("mac", mac), slog.Any("wrapped", struct{ m secret.MasterKeys }{*master}))
		assertNoKeyBytes(t, logs.String(), key)
	}
}

// assertNoKeyBytes fails when output carries any usual rendering of key: its bytes in decimal, hex or base64, or raw.
func assertNoKeyBytes(t *testing.T, output string, key []byte) {
	t.Helper()
	forms := []string{fmt.Sprintf("%v", key), hex.EncodeToString(key), base64.StdEncoding.EncodeToString(key), string(key)}
	for _, form := range forms {
		if strings.Contains(output, form) {
			t.Fatalf("output carries the key (%d-byte form): %s", len(form), output)
		}
	}
}

// TestReportWaitsForCheck: a ring read from a file alone has checked no fingerprint, so it must not let a heartbeat
// claim its versions are distributed; only Check or Refresh, which compares them with the database, may do that.
func TestReportWaitsForCheck(t *testing.T) {
	ring, err := secret.Load(keyFile(t, keyLine(1, 1)+keyLine(2, 2)))
	if err != nil {
		t.Fatal(err)
	}
	if versions, active := ring.Report(); versions != nil || active != 0 {
		t.Fatalf("an unchecked ring reports %v active %d, want nothing", versions, active)
	}
}

// TestLoadSecretIsTheNewestKey: the secret a node derives identifiers from is the newest key of its file, whatever order
// the lines are in, and a file that is no key file names the variable that chose it.
func TestLoadSecretIsTheNewestKey(t *testing.T) {
	got, err := secret.LoadSecret("AXIOM_IDENTITY_KEY_FILE", keyFile(t, keyLine(3, 3)+keyLine(7, 7)+keyLine(5, 5)))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("\a", 32); string(got) != want {
		t.Errorf("the secret is %q, want version 7's key %q", got, want)
	}
	if _, err := secret.LoadSecret("AXIOM_IDENTITY_KEY_FILE", filepath.Join(t.TempDir(), "planted-value")); err == nil ||
		!strings.Contains(err.Error(), "AXIOM_IDENTITY_KEY_FILE") || strings.Contains(err.Error(), "planted-value") {
		t.Errorf("a missing file gave %v, want an error naming the variable and not the path", err)
	}
}

// replaceKeyFile puts source at path with a link and a rename, so the path never disappears and each replacement is
// atomic.
func replaceKeyFile(source, next, path string) error {
	if err := os.Link(source, next); err != nil {
		return err
	}
	return os.Rename(next, path)
}
