package secret_test

import (
	"bytes"
	"testing"

	"axiom/internal/platform/secret"
)

func TestRewrapPreservesDataAndAuthenticatesLocation(t *testing.T) {
	t.Parallel()
	old := loadKeys(t, keyLine(1, 1))
	current := loadKeys(t, keyLine(1, 1)+keyLine(2, 2))
	sealed := old.Seal(secret.New("rotation plaintext"), here)
	rewrapped, err := current.Rewrap(sealed, here)
	if err != nil {
		t.Fatal(err)
	}
	if rewrapped.KeyVersion != 2 || !bytes.Equal(sealed.Ciphertext, rewrapped.Ciphertext) ||
		!bytes.Equal(sealed.Nonce, rewrapped.Nonce) || bytes.Equal(sealed.WrappedDEK, rewrapped.WrappedDEK) {
		t.Fatal("rewrap must change only the wrapped key and its version")
	}
	newOnly := loadKeys(t, keyLine(2, 2))
	opened, err := newOnly.Open(rewrapped, here)
	if err != nil || opened.Reveal() != "rotation plaintext" {
		t.Fatalf("open after removing old master: %v", err)
	}
	again, err := current.Rewrap(rewrapped, here)
	if err != nil || !bytes.Equal(again.WrappedDEK, rewrapped.WrappedDEK) {
		t.Fatalf("rewrap is not idempotent: %v", err)
	}
	if _, err := current.Rewrap(sealed, secret.Location{Table: here.Table, RowID: "another-row", Purpose: here.Purpose}); err == nil {
		t.Fatal("rewrap accepted a different AAD")
	}
	sealed.Ciphertext = bytes.Clone(sealed.Ciphertext)
	sealed.Ciphertext[0] ^= 1
	if _, err := current.Rewrap(sealed, here); err == nil {
		t.Fatal("rewrap hid corrupt data behind a new version")
	}
}
