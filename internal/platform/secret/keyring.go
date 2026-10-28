package secret

import (
	"bufio"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const keyFileVariable = "AXIOM_MASTER_KEY_FILE"

// Keyring is immutable after Load so encryption and decryption can share it across goroutines.
type Keyring struct {
	keys         map[uint32]cipher.AEAD
	latest       uint32
	fingerprints map[uint32][32]byte
}

// MasterKeys is one read of the master key file. The ring a node checks and seals with and the login key derived from
// the file's highest version come from these bytes, so a file replaced during startup cannot mix two snapshots; the
// plaintext stays here and callers receive only the ring and the derived key.
type MasterKeys struct {
	keys   map[uint32][32]byte
	latest uint32
}

// LoadMaster reads the master key file once and validates all of it.
func LoadMaster(path string) (*MasterKeys, error) {
	keys, latest, err := readKeyFile(keyFileVariable, path)
	if err != nil {
		return nil, err
	}
	return &MasterKeys{keys: keys, latest: latest}, nil
}

// Keyring is the ring these keys make, with the file's highest version as the legacy write version. A database-backed
// node must use Rotation.CheckRing before sealing: the database's active version overrides this legacy default.
func (m *MasterKeys) Keyring() *Keyring {
	ring := &Keyring{keys: make(map[uint32]cipher.AEAD, len(m.keys)), latest: m.latest, fingerprints: make(map[uint32][32]byte, len(m.keys))}
	for version, key := range m.keys {
		ring.keys[version] = newGCM(key[:])
		ring.fingerprints[version] = sha256.Sum256(key[:])
	}
	return ring
}

// Load validates the entire file and uses its legacy highest write version. A database-backed node must use
// Rotation.Check before sealing: the database's active version overrides this legacy default.
func Load(path string) (*Keyring, error) {
	keys, err := LoadMaster(path)
	if err != nil {
		return nil, err
	}
	return keys.Keyring(), nil
}

// LoadSecret reads the file variable names, in the keyring's format and with its protection, and returns the newest
// key: the one a secret a node derives everything from is made of, where the derivation must not change with a stored
// version (the gateway's identity secret). Rotating it changes every value derived from it, which is why it is a file
// of its own rather than one of the keyring's.
func LoadSecret(variable, path string) ([]byte, error) {
	keys, latest, err := readKeyFile(variable, path)
	if err != nil {
		return nil, err
	}
	key := keys[latest]
	return key[:], nil
}

// readKeyFile reads a protected file of version:base64 lines, each a 32-byte key, and returns the keys by version
// and the highest version. Errors name variable, the setting that chose the file, and never its path or contents.
func readKeyFile(variable, path string) (map[uint32][32]byte, uint32, error) {
	if path == "" {
		return nil, 0, fmt.Errorf("%s: required", variable)
	}
	file, err := os.Open(path) // #nosec G304 -- The trusted startup configuration selects the key file.
	if err != nil {
		return nil, 0, fileError(variable, "open", err)
	}
	defer file.Close() //nolint:errcheck // A file opened only for reading has nothing to flush.
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fileError(variable, "stat", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: want a regular file", variable)
	}
	// Windows mode bits cannot express NTFS ACLs; deployment must restrict the file's ACL instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, 0, fmt.Errorf("%s: permissions must deny all group and other access", variable)
	}
	return readKeys(variable, file)
}

func readKeys(variable string, reader io.Reader) (map[uint32][32]byte, uint32, error) {
	keys := make(map[uint32][32]byte)
	var latest uint32
	// The random-nonce limit counts uses of the key material, not of a version, so a reused key must not hide behind a
	// new version number.
	versions := make(map[[32]byte]uint32)
	scanner := bufio.NewScanner(reader)
	for line := 1; scanner.Scan(); line++ {
		version, key, err := parseKey(scanner.Text())
		if err != nil {
			return nil, 0, fmt.Errorf("%s: line %d: %w", variable, line, err)
		}
		if _, exists := keys[version]; exists {
			return nil, 0, fmt.Errorf("%s: line %d: duplicate version", variable, line)
		}
		if previous, exists := versions[key]; exists {
			return nil, 0, fmt.Errorf("%s: line %d: key already used by version %d", variable, line, previous)
		}
		versions[key] = version
		keys[version] = key
		latest = max(latest, version)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fileError(variable, "read", err)
	}
	if len(keys) == 0 {
		return nil, 0, fmt.Errorf("%s: want at least one key", variable)
	}
	return keys, latest, nil
}

func parseKey(line string) (uint32, [32]byte, error) {
	label, encoded, ok := strings.Cut(line, ":")
	if !ok {
		return 0, [32]byte{}, errors.New("want version:base64 key")
	}
	version, err := strconv.ParseUint(label, 10, 32)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != label {
		return 0, [32]byte{}, errors.New("want a canonical positive uint32 version")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || strings.ContainsAny(encoded, "\r\n") {
		return 0, [32]byte{}, errors.New("want a padded standard base64 key")
	}
	if len(key) != 32 {
		return 0, [32]byte{}, errors.New("key must decode to 32 bytes")
	}
	return uint32(version), [32]byte(key), nil
}

func fileError(variable, operation string, err error) error {
	// OS errors can carry the path, which may itself be an accidentally pasted key.
	reason := "failed"
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason = "file not found"
	case errors.Is(err, os.ErrPermission):
		reason = "permission denied"
	case errors.Is(err, bufio.ErrTooLong):
		reason = "line too long"
	}
	return fmt.Errorf("%s: %s: %s", variable, operation, reason)
}
