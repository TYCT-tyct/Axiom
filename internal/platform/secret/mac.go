package secret

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"maps"
	"slices"
)

const macFileVariable = "AXIOM_KEY_HASH_FILE"

// MACKeys keys HMAC-SHA256 for secrets that are stored and found by their hash, such as API keys: a hash under a
// secret key cannot be checked against guesses by whoever reads the table. The file has the master key file's format
// and protection; versions let the key rotate while hashes made under the old one are still found. Immutable after
// LoadMAC, so goroutines share it.
type MACKeys struct {
	keys     map[uint32][32]byte
	versions []uint32 // newest first
}

// LoadMAC reads the file AXIOM_KEY_HASH_FILE names, as Load reads the master key file.
func LoadMAC(path string) (*MACKeys, error) {
	keys, _, err := readKeyFile(macFileVariable, path)
	if err != nil {
		return nil, err
	}
	versions := slices.SortedFunc(maps.Keys(keys), func(a, b uint32) int { return cmp.Compare(b, a) })
	return &MACKeys{keys, versions}, nil
}

// Versions lists the key versions, newest first; the first is the one new hashes are made with.
func (m *MACKeys) Versions() []uint32 { return slices.Clone(m.versions) }

// Sum is HMAC-SHA256 of data under the key of version, which must be one of Versions: another is a caller bug and
// panics, since no hash could be right.
func (m *MACKeys) Sum(version uint32, data []byte) [32]byte {
	key, ok := m.keys[version]
	if !ok {
		panic("secret: no MAC key of this version")
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(data)
	return [32]byte(mac.Sum(nil))
}
