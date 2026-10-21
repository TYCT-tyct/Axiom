package secret

import "errors"

// Rewrap authenticates the envelope at loc and changes only its wrapped data key and master version.
// It does not expose plaintext and leaves an envelope already at the write version byte-for-byte unchanged.
func (k *Keyring) Rewrap(sealed Sealed, loc Location) (Sealed, error) {
	if _, err := k.Open(sealed, loc); err != nil {
		return Sealed{}, err
	}
	if sealed.KeyVersion == k.latest {
		return sealed, nil
	}
	aad := loc.encode()
	dek, err := k.keys[sealed.KeyVersion].Open(nil, nil, sealed.WrappedDEK, wrapAAD(sealed.KeyVersion, aad))
	if err != nil {
		return Sealed{}, errors.New("secret: data key authentication failed")
	}
	sealed.WrappedDEK = k.keys[k.latest].Seal(nil, nil, dek, wrapAAD(k.latest, aad)) // #nosec G407 -- The AEAD generates its own nonce.
	sealed.KeyVersion = k.latest
	return sealed, nil
}
