package secret_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"axiom/internal/platform/secret"
)

func loginKeyOf(t testing.TB, content string) secret.LoginKey {
	t.Helper()
	master, err := secret.LoadMaster(keyFile(t, content))
	if err != nil {
		t.Fatal(err)
	}
	key, err := master.LoginKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestLoginKey: the login key is HKDF-SHA256 under its own label, taken from the file's highest version whatever order
// the lines are in, and it changes with the master key. The fixed vector catches replacing the derivation with the
// master key itself or renaming the label.
func TestLoginKey(t *testing.T) {
	t.Parallel()
	const want = "93f12231920429a87c22b10d76f68e21a9d71ca18c8805b5fca0bfc102c91538"
	if got := loginKeyOf(t, keyLine(1, 'a')); hex.EncodeToString(got[:]) != want {
		t.Fatal("HKDF label or key changed")
	}
	highest := loginKeyOf(t, keyLine(3, 'a')+keyLine(7, 'b')+keyLine(5, 'c'))
	if highest != loginKeyOf(t, keyLine(7, 'b')) || highest == loginKeyOf(t, keyLine(5, 'c')) {
		t.Fatal("the login key is not the highest distributed version's")
	}
	if loginKeyOf(t, keyLine(1, 'a')) == loginKeyOf(t, keyLine(1, 'b')) {
		t.Fatal("the login key ignores the master key")
	}
}

// TestMasterKeysAreOneSnapshot: the ring and the login key come from the one read, so replacing the file after
// LoadMaster changes neither; a second read would have used version 4 and key d.
func TestMasterKeysAreOneSnapshot(t *testing.T) {
	t.Parallel()
	path := keyFile(t, keyLine(1, 'a')+keyLine(2, 'b'))
	master, err := secret.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	want := loginKeyOf(t, keyLine(2, 'b'))
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, []byte(keyLine(3, 'c')+keyLine(4, 'd')), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if got := master.Keyring().Seal(secret.New("snapshot"), here).KeyVersion; got != 2 {
		t.Fatalf("the ring was read again: version %d, want 2", got)
	}
	if got, err := master.LoginKey(); err != nil || got != want {
		t.Fatalf("the login key was read again: %v, %v", got, err)
	}
}
