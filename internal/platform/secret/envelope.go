package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
)

const nonceSize = 12

// Sealed separates the data nonce for storage; WrappedDEK includes its own nonce.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	WrappedDEK []byte
	KeyVersion uint32
}

// Location names where a sealed value is stored. Both encryption layers authenticate it, so a value moved to another
// table, row or purpose fails to open. Every field is required: an empty one is a caller bug and panics.
type Location struct {
	Table, RowID, Purpose string
}

// encode is the only encoding of a location. Stored values depend on it, so it never changes; a new encoding would
// need a new label. The label separates it from other uses of the keys, and the length prefixes make it injective:
// without them, ("ab", "c") and ("a", "bc") would encode alike.
func (l Location) encode() []byte {
	encoded := []byte("axiom/secret/location/v1\x00")
	for _, field := range []string{l.Table, l.RowID, l.Purpose} {
		if field == "" {
			panic("secret: every location field must be set")
		}
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(field))) // #nosec G115 -- An identifier is never 4 GiB.
		encoded = append(encoded, field...)
	}
	return encoded
}

// Seal uses a fresh data key per value so future master key rotation need not reencrypt the data.
func (k *Keyring) Seal(plaintext Secret, loc Location) Sealed {
	snapshot := k.snapshot()
	aad := loc.encode()
	dek := make([]byte, 32)
	// Since Go 1.24 crypto/rand.Read never returns an error; it crashes the program instead.
	rand.Read(dek)
	encrypted := newGCM(dek).Seal(nil, nil, []byte(plaintext.Reveal()), aad)                     // #nosec G407 -- NewGCMWithRandomNonce requires nil and generates the nonce internally.
	wrapped := snapshot.keys[snapshot.latest].Seal(nil, nil, dek, wrapAAD(snapshot.latest, aad)) // #nosec G407 -- Same random-nonce AEAD as the data layer.
	return Sealed{
		// The full slice expression caps Nonce, so appending to it cannot overwrite Ciphertext in the shared array.
		Ciphertext: encrypted[nonceSize:], Nonce: encrypted[:nonceSize:nonceSize],
		WrappedDEK: wrapped, KeyVersion: snapshot.latest,
	}
}

// Open authenticates both layers before exposing plaintext, which remains protected from accidental output. The caller
// rebuilds loc from where it read the row, never from data stored beside the ciphertext.
func (k *Keyring) Open(sealed Sealed, loc Location) (Secret, error) {
	return k.snapshot().open(sealed, loc)
}

// newGCM only receives 32-byte keys: data keys from Seal, data keys whose wrap GCM authenticated, and master keys
// whose length parseKey checked. An error is therefore a broken invariant, not a condition to handle.
func newGCM(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func wrapAAD(version uint32, aad []byte) []byte {
	bound := []byte("axiom/secret/dek/v1\x00")
	bound = binary.BigEndian.AppendUint32(bound, version)
	return append(bound, aad...)
}
