package secret

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestKeyFileReplacedAtOpenIsRefused replaces the path at the moment that matters: the opener runs right where the read
// begins, and puts a 0644 file with valid keys at the path first. The read must refuse it. The same file, loaded through
// the production opener without a replacement, must succeed, so a refusal cannot come from the seam refusing
// everything, and the test needs no second goroutine, sleep or retry: it runs the interleaving once, in one order,
// every time.
func TestKeyFileReplacedAtOpenIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	dir := t.TempDir()
	path, good, bad := filepath.Join(dir, "master"), filepath.Join(dir, "good"), filepath.Join(dir, "bad")
	writeTestKey(t, good, 0o600, 1)
	writeTestKey(t, bad, 0o644, 2)
	if err := os.Link(good, path); err != nil {
		t.Fatal(err)
	}
	keys, _, err := readKeyFileWith("AXIOM_MASTER_KEY_FILE", path, swapOnOpen(bad))
	if keys != nil || err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("a 0644 file put in place at the open gave keys %v and error %v; want a permissions refusal", keys, err)
	}
	linkKeyFile(t, good, path)
	keys, latest, err := readKeyFileWith("AXIOM_MASTER_KEY_FILE", path, os.Open)
	if err != nil || len(keys) != 1 || latest != 1 || keys[1][0] != 1 || keys[1][31] != 1 {
		t.Fatalf("the unchanged file gave keys %v, version %d and error %v; want the 0600 file's key", keys, latest, err)
	}
}

// swapOnOpen returns the opener the seam takes: it renames source over the path, then opens the path, so the file the
// read sees is never the one the path named when the checks ran.
func swapOnOpen(source string) func(string) (*os.File, error) {
	return func(name string) (*os.File, error) {
		if err := os.Rename(source, name); err != nil {
			return nil, err
		}
		return os.Open(name) // #nosec G304 -- The test builds both paths it uses.
	}
}

// linkKeyFile puts source at path, replacing what is there.
func linkKeyFile(t *testing.T, source, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, path); err != nil {
		t.Fatal(err)
	}
}

// writeTestKey writes one version's key, 32 bytes of value, with an explicit mode: os.WriteFile alone leaves the mode to
// the process's umask.
func writeTestKey(t *testing.T, path string, mode os.FileMode, version byte) {
	t.Helper()
	line := fmt.Sprintf("%d:%s\n", version, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{version}, 32)))
	if err := os.WriteFile(path, []byte(line), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
