package secret

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

const (
	// loginLabel is what the login key is derived under: a key of its own, so that the API keys' hash key rotating, with
	// two versions at once, does not split one bucket between the nodes (internal/platform/kv/README.md「键」).
	loginLabel = "axiom kv login v1"
	// loginKeyLen is the length of the derived key, and of the master key it comes from.
	loginKeyLen = 32
)

// LoginKey is the HMAC key of the subjects in the shared login keys. It comes from the master key file's highest
// distributed version, never from the version the database made active for envelopes, so a new key distributed ahead of
// its activation does not change the login buckets; the derivation's own label keeps it independent of the API keys'
// hash key as that rotates (internal/platform/kv/README.md「键」).
type LoginKey [loginKeyLen]byte

// LoginKey derives the login key from the snapshot's highest version.
func (m *MasterKeys) LoginKey() (LoginKey, error) {
	master := m.keys[m.latest]
	derived, err := hkdf.Key(sha256.New, master[:], nil, loginLabel, loginKeyLen)
	if err != nil {
		return LoginKey{}, fmt.Errorf("secret: derive the login key: %w", err)
	}
	var key LoginKey
	copy(key[:], derived)
	return key, nil
}
