package secret_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"axiom/internal/platform/secret"
)

func keyLine(version uint32, value byte) string {
	return fmt.Sprintf("%d:%s\n", version, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)))
}

func keyFile(t testing.TB, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadKeys(t testing.TB, content string) *secret.Keyring {
	t.Helper()
	keys, err := secret.Load(keyFile(t, content))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func seal(keys *secret.Keyring, plaintext string, aad []byte) secret.Sealed {
	return keys.Seal(secret.New(plaintext), aad)
}

func TestRoundTrip(t *testing.T) {
	keys := loadKeys(t, keyLine(10, 1)+keyLine(2, 2))
	for _, c := range []struct {
		plaintext string
		aad       []byte
	}{
		{"", []byte("aad")},
		{"credential", []byte{0}},
		{"credential\x00令牌\n", []byte(`["credentials","42","api_key"]`)},
		{string([]byte{0xff, 0xfe}), []byte("aad")},
	} {
		t.Run(fmt.Sprintf("length=%d", len(c.plaintext)), func(t *testing.T) {
			t.Parallel()
			sealed := seal(keys, c.plaintext, c.aad)
			checkFormat(t, sealed, 10)
			opened, err := keys.Open(sealed, c.aad)
			if err != nil || opened.Reveal() != c.plaintext {
				t.Fatalf("round trip failed: %v", err)
			}
			other := seal(keys, c.plaintext, c.aad)
			if bytes.Equal(sealed.Ciphertext, other.Ciphertext) || bytes.Equal(sealed.Nonce, other.Nonce) || bytes.Equal(sealed.WrappedDEK, other.WrappedDEK) {
				t.Fatal("sealing reused encrypted fields")
			}
		})
	}
}

func checkFormat(t *testing.T, sealed secret.Sealed, latest uint32) {
	t.Helper()
	if sealed.KeyVersion != latest {
		t.Errorf("written with version %d, want the highest, %d", sealed.KeyVersion, latest)
	}
	if len(sealed.Nonce) != nonceLen {
		t.Errorf("nonce is %d bytes, want %d", len(sealed.Nonce), nonceLen)
	}
	if len(sealed.WrappedDEK) != nonceLen+32+16 {
		t.Errorf("wrapped DEK is %d bytes, want nonce, key and tag: %d", len(sealed.WrappedDEK), nonceLen+32+16)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	keys := loadKeys(t, keyLine(1, 1)+keyLine(2, 1))
	aad := []byte(`["credentials","42","api_key"]`)
	original := seal(keys, "planted-plaintext", aad)
	other := seal(keys, "planted-plaintext", aad)
	fields := []struct {
		name string
		get  func(*secret.Sealed) *[]byte
	}{
		{"ciphertext", func(s *secret.Sealed) *[]byte { return &s.Ciphertext }},
		{"nonce", func(s *secret.Sealed) *[]byte { return &s.Nonce }},
		{"wrapped DEK", func(s *secret.Sealed) *[]byte { return &s.WrappedDEK }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for i := range *field.get(&original) {
				changed := original
				data := bytes.Clone(*field.get(&original))
				data[i] ^= 1
				*field.get(&changed) = data
				rejectOpen(t, keys, changed, aad)
			}
			for _, data := range [][]byte{nil, {1}, bytes.Repeat([]byte{1}, 100), *field.get(&other)} {
				changed := original
				*field.get(&changed) = data
				rejectOpen(t, keys, changed, aad)
			}
		})
	}
	for _, version := range []uint32{0, 1, 3, ^uint32(0)} {
		changed := original
		changed.KeyVersion = version
		rejectOpen(t, keys, changed, aad)
	}
	// GCM authenticates Nonce||Ciphertext as one string, so a byte moved across the field boundary must still fail.
	for _, moved := range []secret.Sealed{
		{Nonce: append(bytes.Clone(original.Nonce), original.Ciphertext[0]), Ciphertext: original.Ciphertext[1:]},
		{Nonce: original.Nonce[:nonceLen-1], Ciphertext: append([]byte{original.Nonce[nonceLen-1]}, original.Ciphertext...)},
	} {
		moved.WrappedDEK, moved.KeyVersion = original.WrappedDEK, original.KeyVersion
		rejectOpen(t, keys, moved, aad)
	}
	for _, wrongAAD := range []string{`["credentials","43","api_key"]`, `["other","42","api_key"]`, `["credentials","42","token"]`} {
		rejectOpen(t, keys, original, []byte(wrongAAD))
	}
	rejectOpen(t, loadKeys(t, keyLine(2, 3)), original, aad)
	rejectOpen(t, keys, secret.Sealed{}, aad)
}

// nonceLen is the documented length of both GCM nonces.
const nonceLen = 12

func rejectOpen(t testing.TB, keys *secret.Keyring, sealed secret.Sealed, aad []byte) {
	t.Helper()
	opened, err := keys.Open(sealed, aad)
	if err == nil || opened.Reveal() != "" {
		t.Fatal("unauthenticated input returned plaintext or succeeded")
	}
}

func TestRetainedVersionOpensOldData(t *testing.T) {
	aad := []byte("credential-location")
	sealed := seal(loadKeys(t, keyLine(2, 1)), "old-value", aad)
	current := loadKeys(t, keyLine(10, 2)+keyLine(2, 1))
	opened, err := current.Open(sealed, aad)
	if err != nil || opened.Reveal() != "old-value" {
		t.Fatalf("old version unreadable: %v", err)
	}
	rejectOpen(t, loadKeys(t, keyLine(10, 2)), sealed, aad)
}

// Without a location in the aad, a sealed value copied to another row or purpose would still open.
func TestEmptyAADPanics(t *testing.T) {
	keys := loadKeys(t, keyLine(1, 1))
	sealed := seal(keys, "value", []byte("aad"))
	for _, aad := range [][]byte{nil, {}} {
		for name, use := range map[string]func(){
			"seal": func() { keys.Seal(secret.New("value"), aad) },
			"open": func() { _, _ = keys.Open(sealed, aad) }, //nolint:errcheck // The call must panic before returning.
		} {
			t.Run(name, func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Fatal("empty aad accepted")
					}
				}()
				use()
			})
		}
	}
}

func TestIndependentDataKeys(t *testing.T) {
	keys := loadKeys(t, keyLine(1, masterByte))
	aad := []byte("credential-location")
	first := unwrapDataKey(t, seal(keys, "same-plaintext", aad), aad)
	second := unwrapDataKey(t, seal(keys, "same-plaintext", aad), aad)
	if len(first) != 32 || len(second) != 32 || bytes.Equal(first, second) {
		t.Fatal("values must use distinct AES-256 data keys")
	}
}

// masterByte fills the master key that unwrapDataKey rebuilds on its own.
const masterByte = 1

// unwrapDataKey deliberately re-implements the wrap format documented in the README instead of calling the package,
// so a change to the stored format fails here.
func unwrapDataKey(t *testing.T, sealed secret.Sealed, aad []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{masterByte}, 32))
	if err != nil {
		t.Fatal(err)
	}
	master, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	bound := binary.BigEndian.AppendUint32([]byte("axiom/secret/dek/v1\x00"), sealed.KeyVersion)
	bound = append(bound, aad...)
	dek, err := master.Open(nil, sealed.WrappedDEK[:nonceLen], sealed.WrappedDEK[nonceLen:], bound)
	if err != nil {
		t.Fatalf("documented envelope format cannot unwrap the key: %v", err)
	}
	return dek
}

func FuzzOpen(f *testing.F) {
	keys := loadKeys(f, keyLine(1, 1))
	valid := seal(keys, "fuzz-value", []byte("aad"))
	f.Add(valid.Ciphertext, valid.Nonce, valid.WrappedDEK, valid.KeyVersion, []byte("aad"))
	f.Add([]byte{}, []byte{}, []byte{}, uint32(0), []byte{0})
	f.Fuzz(func(t *testing.T, ciphertext, nonce, wrapped []byte, version uint32, aad []byte) {
		if len(aad) == 0 {
			t.Skip("empty aad panics by design")
		}
		opened, err := keys.Open(secret.Sealed{Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrapped, KeyVersion: version}, aad)
		if err != nil && opened.Reveal() != "" {
			t.Fatal("failed authentication returned plaintext")
		}
	})
}
