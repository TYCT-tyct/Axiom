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
	"strings"
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

func seal(keys *secret.Keyring, plaintext string, loc secret.Location) secret.Sealed {
	return keys.Seal(secret.New(plaintext), loc)
}

var here = secret.Location{Table: "credentials", RowID: "42", Purpose: "api_key"}

func TestRoundTrip(t *testing.T) {
	keys := loadKeys(t, keyLine(10, 1)+keyLine(2, 2))
	for _, c := range []struct {
		plaintext string
		loc       secret.Location
	}{
		{"", here},
		{"credential", secret.Location{Table: "\x00", RowID: "\x00", Purpose: "\x00"}},
		{"credential\x00令牌\n", secret.Location{Table: "凭证", RowID: "0190a3c4-7b1e-7000-8000-000000000042", Purpose: "api_key"}},
		{string([]byte{0xff, 0xfe}), secret.Location{Table: string([]byte{0xff}), RowID: strings.Repeat("9", 1000), Purpose: "p"}},
	} {
		t.Run(fmt.Sprintf("length=%d", len(c.plaintext)), func(t *testing.T) {
			t.Parallel()
			sealed := seal(keys, c.plaintext, c.loc)
			checkFormat(t, sealed, 10)
			opened, err := keys.Open(sealed, c.loc)
			if err != nil || opened.Reveal() != c.plaintext {
				t.Fatalf("round trip failed: %v", err)
			}
			other := seal(keys, c.plaintext, c.loc)
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
	keys := loadKeys(t, keyLine(1, 1)+keyLine(2, 2))
	original := seal(keys, "planted-plaintext", here)
	other := seal(keys, "planted-plaintext", here)
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
				rejectOpen(t, keys, changed, here)
			}
			for _, data := range [][]byte{nil, {1}, bytes.Repeat([]byte{1}, 100), *field.get(&other)} {
				changed := original
				*field.get(&changed) = data
				rejectOpen(t, keys, changed, here)
			}
		})
	}
	for _, version := range []uint32{0, 1, 3, ^uint32(0)} {
		changed := original
		changed.KeyVersion = version
		rejectOpen(t, keys, changed, here)
	}
	// GCM authenticates Nonce||Ciphertext as one string, so a byte moved across the field boundary must still fail.
	for _, moved := range []secret.Sealed{
		{Nonce: append(bytes.Clone(original.Nonce), original.Ciphertext[0]), Ciphertext: original.Ciphertext[1:]},
		{Nonce: original.Nonce[:nonceLen-1], Ciphertext: append([]byte{original.Nonce[nonceLen-1]}, original.Ciphertext...)},
	} {
		moved.WrappedDEK, moved.KeyVersion = original.WrappedDEK, original.KeyVersion
		rejectOpen(t, keys, moved, here)
	}
	rejectOpen(t, loadKeys(t, keyLine(2, 3)), original, here)
	rejectOpen(t, keys, secret.Sealed{}, here)
}

// Each pair differs in one field, or only in where the bytes split into fields; a value sealed at either location must
// not open at the other.
func TestLocationsDoNotCollide(t *testing.T) {
	keys := loadKeys(t, keyLine(1, 1))
	loc := func(table, row, purpose string) secret.Location {
		return secret.Location{Table: table, RowID: row, Purpose: purpose}
	}
	for _, pair := range [][2]secret.Location{
		{here, loc("other", "42", "api_key")},
		{here, loc("credentials", "43", "api_key")},
		{here, loc("credentials", "42", "token")},
		{loc("ab", "c", "p"), loc("a", "bc", "p")},
		{loc("t", "ab", "c"), loc("t", "a", "bc")},
		{loc("ab", "c", "d"), loc("a", "b", "cd")},
		{loc("a", "b", "c"), loc("c", "b", "a")},
		{loc("a", "a", "b"), loc("a", "b", "a")},
		// A zero byte cannot act as a separator.
		{loc("a\x00b", "c", "p"), loc("a", "b\x00c", "p")},
		// Nor can a field's bytes pass for the next field's length prefix.
		{loc("t\x00\x00\x00\x01r", "p", "x"), loc("t", "r", "p")},
	} {
		t.Run(fmt.Sprintf("%q/%q", pair[0], pair[1]), func(t *testing.T) {
			rejectOpen(t, keys, seal(keys, "value", pair[0]), pair[1])
			rejectOpen(t, keys, seal(keys, "value", pair[1]), pair[0])
		})
	}
}

// nonceLen is the documented length of both GCM nonces.
const nonceLen = 12

func rejectOpen(t testing.TB, keys *secret.Keyring, sealed secret.Sealed, loc secret.Location) {
	t.Helper()
	opened, err := keys.Open(sealed, loc)
	if err == nil || opened.Reveal() != "" {
		t.Fatal("unauthenticated input returned plaintext or succeeded")
	}
}

func TestRetainedVersionOpensOldData(t *testing.T) {
	sealed := seal(loadKeys(t, keyLine(2, 1)), "old-value", here)
	current := loadKeys(t, keyLine(10, 2)+keyLine(2, 1))
	opened, err := current.Open(sealed, here)
	if err != nil || opened.Reveal() != "old-value" {
		t.Fatalf("old version unreadable: %v", err)
	}
	rejectOpen(t, loadKeys(t, keyLine(10, 2)), sealed, here)
}

// An empty field would let a value open at every location that leaves the same field empty.
func TestEmptyLocationFieldPanics(t *testing.T) {
	keys := loadKeys(t, keyLine(1, 1))
	sealed := seal(keys, "value", here)
	for name, loc := range map[string]secret.Location{
		"table":   {RowID: "42", Purpose: "api_key"},
		"row ID":  {Table: "credentials", Purpose: "api_key"},
		"purpose": {Table: "credentials", RowID: "42"},
		"all":     {},
	} {
		for use, call := range map[string]func(){
			"seal": func() { keys.Seal(secret.New("value"), loc) },
			"open": func() { _, _ = keys.Open(sealed, loc) }, //nolint:errcheck // The call must panic before returning.
		} {
			t.Run(name+"/"+use, func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Fatal("empty field accepted")
					}
				}()
				call()
			})
		}
	}
}

func TestIndependentDataKeys(t *testing.T) {
	keys := loadKeys(t, keyLine(1, masterByte))
	first := openByHand(t, seal(keys, "same-plaintext", here), here)
	second := openByHand(t, seal(keys, "same-plaintext", here), here)
	if len(first) != 32 || len(second) != 32 || bytes.Equal(first, second) {
		t.Fatal("values must use distinct AES-256 data keys")
	}
}

// masterByte fills the master key that openByHand rebuilds on its own.
const masterByte = 1

// openByHand deliberately re-implements the stored format documented in the README, location encoding included,
// instead of calling the package, so a change to the format fails here. It opens both layers and returns the data key.
func openByHand(t *testing.T, sealed secret.Sealed, loc secret.Location) []byte {
	t.Helper()
	encoded := []byte("axiom/secret/location/v1\x00")
	for _, field := range []string{loc.Table, loc.RowID, loc.Purpose} {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(field))) // #nosec G115 -- Test fields are short.
		encoded = append(encoded, field...)
	}
	bound := binary.BigEndian.AppendUint32([]byte("axiom/secret/dek/v1\x00"), sealed.KeyVersion)
	bound = append(bound, encoded...)
	dek, err := newGCM(t, bytes.Repeat([]byte{masterByte}, 32)).Open(nil, sealed.WrappedDEK[:nonceLen], sealed.WrappedDEK[nonceLen:], bound)
	if err != nil {
		t.Fatalf("documented envelope format cannot unwrap the key: %v", err)
	}
	plaintext, err := newGCM(t, dek).Open(nil, sealed.Nonce, sealed.Ciphertext, encoded)
	if err != nil || string(plaintext) != "same-plaintext" {
		t.Fatalf("documented envelope format cannot open the data: %v", err)
	}
	return dek
}

func newGCM(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func FuzzOpen(f *testing.F) {
	keys := loadKeys(f, keyLine(1, 1))
	valid := seal(keys, "fuzz-value", here)
	f.Add(valid.Ciphertext, valid.Nonce, valid.WrappedDEK, valid.KeyVersion, here.Table, here.RowID, here.Purpose)
	f.Add([]byte{}, []byte{}, []byte{}, uint32(0), "\x00", "\x00", "\x00")
	f.Fuzz(func(t *testing.T, ciphertext, nonce, wrapped []byte, version uint32, table, row, purpose string) {
		if table == "" || row == "" || purpose == "" {
			t.Skip("an empty location field panics by design")
		}
		sealed := secret.Sealed{Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrapped, KeyVersion: version}
		opened, err := keys.Open(sealed, secret.Location{Table: table, RowID: row, Purpose: purpose})
		if err != nil && opened.Reveal() != "" {
			t.Fatal("failed authentication returned plaintext")
		}
	})
}
