package secret_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/secret"
)

// TestMACKeys: each version keys plain HMAC-SHA256 with its own key, newest first, and a bad file is reported under
// the MAC file's variable, not the master key's.
func TestMACKeys(t *testing.T) {
	keys, err := secret.LoadMAC(keyFile(t, keyLine(2, 7)+keyLine(9, 8)))
	if err != nil {
		t.Fatal(err)
	}
	if got := keys.Versions(); !slices.Equal(got, []uint32{9, 2}) {
		t.Errorf("versions %v, want 9 then 2", got)
	}
	for version, value := range map[uint32]byte{2: 7, 9: 8} {
		want := hmac.New(sha256.New, bytes.Repeat([]byte{value}, 32))
		want.Write([]byte("sk-data"))
		if got := keys.Sum(version, []byte("sk-data")); !bytes.Equal(got[:], want.Sum(nil)) {
			t.Errorf("version %d: %x", version, got)
		}
	}
	if _, err := secret.LoadMAC(keyFile(t, "")); err == nil || !strings.HasPrefix(err.Error(), "AXIOM_KEY_HASH_FILE: ") {
		t.Errorf("an empty file: %v", err)
	}
}
