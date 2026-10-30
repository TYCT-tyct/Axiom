package secret

import "errors"

// Rewrap authenticates the envelope at loc and changes only its wrapped data key and master version.
// It does not expose plaintext and leaves an envelope already at the write version byte-for-byte unchanged.
func (k *Keyring) Rewrap(sealed Sealed, loc Location) (Sealed, error) {
	return k.snapshot().rewrap(sealed, loc)
}

func (s *keySnapshot) rewrap(sealed Sealed, loc Location) (Sealed, error) {
	if _, ok := s.keys[sealed.KeyVersion]; !ok || sealed.KeyVersion == 0 {
		return Sealed{}, errors.New("secret: unknown master key version")
	}
	if _, err := s.open(sealed, loc); err != nil {
		return Sealed{}, err
	}
	if sealed.KeyVersion == s.latest {
		return sealed, nil
	}
	aad := loc.encode()
	dek, err := s.keys[sealed.KeyVersion].Open(nil, nil, sealed.WrappedDEK, wrapAAD(sealed.KeyVersion, aad))
	if err != nil {
		return Sealed{}, errors.New("secret: data key authentication failed")
	}
	sealed.WrappedDEK = s.keys[s.latest].Seal(nil, nil, dek, wrapAAD(s.latest, aad)) // #nosec G407 -- The AEAD generates its own nonce.
	sealed.KeyVersion = s.latest
	return sealed, nil
}

// open is Open on one snapshot, so a rewrap authenticates and rewraps against the same version set even if the live ring
// is replaced in between.
func (s *keySnapshot) open(sealed Sealed, loc Location) (Secret, error) {
	aad := loc.encode()
	master, ok := s.keys[sealed.KeyVersion]
	if !ok {
		return Secret{}, errors.New("secret: unknown master key version")
	}
	if len(sealed.Nonce) != nonceSize {
		return Secret{}, errors.New("secret: invalid nonce length")
	}
	dek, err := master.Open(nil, nil, sealed.WrappedDEK, wrapAAD(sealed.KeyVersion, aad))
	if err != nil {
		return Secret{}, errors.New("secret: data key authentication failed")
	}
	encrypted := make([]byte, 0, nonceSize+len(sealed.Ciphertext))
	encrypted = append(encrypted, sealed.Nonce...)
	encrypted = append(encrypted, sealed.Ciphertext...)
	plaintext, err := newGCM(dek).Open(nil, nil, encrypted, aad)
	if err != nil {
		return Secret{}, errors.New("secret: data authentication failed")
	}
	return New(string(plaintext)), nil
}
