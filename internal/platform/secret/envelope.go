package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
)

const nonceSize = 12

// Sealed separates the data nonce for storage; WrappedDEK includes its own nonce.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	WrappedDEK []byte
	KeyVersion uint32
}

// Seal uses a fresh data key per value so future master key rotation need not reencrypt the data. An empty aad would
// let the value open in any location, so it is a caller bug and panics.
func (k *Keyring) Seal(plaintext Secret, aad []byte) Sealed {
	requireAAD(aad)
	dek := make([]byte, 32)
	// Since Go 1.24 crypto/rand.Read never returns an error; it crashes the program instead.
	rand.Read(dek)
	encrypted := newGCM(dek).Seal(nil, nil, []byte(plaintext.Reveal()), aad) // #nosec G407 -- NewGCMWithRandomNonce requires nil and generates the nonce internally.
	wrapped := k.keys[k.latest].Seal(nil, nil, dek, wrapAAD(k.latest, aad))  // #nosec G407 -- Same random-nonce AEAD as the data layer.
	return Sealed{
		// The full slice expression caps Nonce, so appending to it cannot overwrite Ciphertext in the shared array.
		Ciphertext: encrypted[nonceSize:], Nonce: encrypted[:nonceSize:nonceSize],
		WrappedDEK: wrapped, KeyVersion: k.latest,
	}
}

// Open authenticates both layers before exposing plaintext, which remains protected from accidental output. An empty
// aad panics, as in Seal.
func (k *Keyring) Open(sealed Sealed, aad []byte) (Secret, error) {
	requireAAD(aad)
	master, ok := k.keys[sealed.KeyVersion]
	if !ok {
		return Secret{}, errors.New("secret: unknown master key version")
	}
	// GCM authenticates Nonce||Ciphertext as one string, so only this check fixes the boundary between the fields:
	// without it, a byte moved from one to the other would still open.
	if len(sealed.Nonce) != nonceSize {
		return Secret{}, errors.New("secret: invalid nonce length")
	}
	dek, err := master.Open(nil, nil, sealed.WrappedDEK, wrapAAD(sealed.KeyVersion, aad))
	if err != nil {
		return Secret{}, errors.New("secret: data key authentication failed")
	}
	// Use fresh storage: Open must not mutate fields shared with another reader.
	encrypted := make([]byte, 0, nonceSize+len(sealed.Ciphertext))
	encrypted = append(encrypted, sealed.Nonce...)
	encrypted = append(encrypted, sealed.Ciphertext...)
	plaintext, err := newGCM(dek).Open(nil, nil, encrypted, aad)
	if err != nil {
		return Secret{}, errors.New("secret: data authentication failed")
	}
	return New(string(plaintext)), nil
}

func requireAAD(aad []byte) {
	if len(aad) == 0 {
		panic("secret: aad must bind the value to where it is stored")
	}
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
