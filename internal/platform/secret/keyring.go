package secret

import (
	"bufio"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

const keyFileVariable = "AXIOM_MASTER_KEY_FILE"

// keySnapshot is one immutable file read. The maps are read-only after construction, so a snapshot can be shared with
// concurrent seals and opens while a later read replaces it.
type keySnapshot struct {
	keys         map[uint32]cipher.AEAD
	latest       uint32
	fingerprints map[uint32][32]byte
	// confirmed is the non-retired version set the database has checked against this snapshot's fingerprints. A failed
	// add can leave a version in the file that the database never registered, and another node may later register the
	// same number with different material; the file still holds the orphan, but it must not be reported to the cluster
	// as a version this node can read.
	confirmed []uint32
}

// Keyring is a handle to an immutable snapshot. A running node replaces the snapshot after it reloads the distribution
// file, so every seal, open, rewrap and heartbeat report sees one whole ring and never a half-applied version set; callers
// keep this handle for the life of the process.
type Keyring struct {
	state atomic.Pointer[keySnapshot]
}

func (k *Keyring) snapshot() *keySnapshot {
	s := k.state.Load()
	if s == nil {
		// No caller may use a ring it did not load; a panic here names that bug instead of a nil dereference deeper in.
		panic("secret: a key ring must be loaded before use")
	}
	return s
}

// install replaces the ring atomically. A call already using the old snapshot keeps reading it until it returns.
func (k *Keyring) install(s *keySnapshot) { k.state.Store(s) }

// Report is the heartbeat's view of the ring from one snapshot: the versions the database confirmed this node holds,
// oldest first, and the one it writes. Both fields come from the same install, so a reload between them cannot report a
// version set that never existed. A version left in the file by a failed add, or one whose fingerprint does not match
// the registration, is not reportable: activation must wait until this node can actually read the registered version.
// A ring that has not completed Check or Refresh reports nothing, because the file alone does not prove the database
// accepted it. It carries identifiers only, never material.
func (k *Keyring) Report() (versions []uint32, active uint32) {
	s := k.snapshot()
	if s.confirmed == nil {
		return nil, 0
	}
	return slices.Clone(s.confirmed), s.latest
}

// withActive copies the snapshot with another write version. The maps are shared, so the copy costs no key material.
func (s *keySnapshot) withActive(active uint32) *keySnapshot {
	copied := *s
	copied.latest = active
	return &copied
}

// withConfirmed copies the snapshot with the versions the database checked against its fingerprints. The write version
// is installed separately by the caller, from the same check, so Report never pairs them across checks.
func (s *keySnapshot) withConfirmed(versions []uint32) *keySnapshot {
	copied := *s
	copied.confirmed = slices.Clone(versions)
	slices.Sort(copied.confirmed)
	return &copied
}

// MasterKeys is one read of the master key file. The ring a node checks and seals with and the login and mail keys
// derived from the file's highest version come from these bytes, so a file replaced during startup cannot mix two snapshots; the
// plaintext stays here and callers receive only the ring and the derived keys. It sits behind a closure, as in Secret,
// because fmt prints a struct's unexported fields by reflection and a bad-verb diagnostic (%w on a non-error) bypasses
// Format: a map field would be printed by both, while a function value prints as an address.
type MasterKeys struct { //nolint:recvcheck // the other methods read the snapshot; Format and LogValue take a value so a dereferenced copy redacts too
	material func() masterKeyMaterial
}

// masterKeyMaterial is what the closure in MasterKeys hands its methods: reachable only through the closure, never as a
// field fmt can walk.
type masterKeyMaterial struct {
	keys   map[uint32][32]byte
	latest uint32
}

// LoadMaster reads the master key file once and validates all of it.
func LoadMaster(path string) (*MasterKeys, error) {
	keys, latest, err := readKeyFile(keyFileVariable, path)
	if err != nil {
		return nil, err
	}
	return &MasterKeys{material: func() masterKeyMaterial { return masterKeyMaterial{keys: keys, latest: latest} }}, nil
}

// Keyring is the ring these keys make, with the file's highest version as the legacy write version. A database-backed
// node must use Rotation.CheckRing before sealing: the database's active version overrides this legacy default.
func (m *MasterKeys) Keyring() *Keyring {
	material := m.material()
	ring := &Keyring{}
	ring.state.Store(newSnapshot(material.keys, material.latest))
	return ring
}

// Format redacts every verb, the invalid ones included: fmt prints a struct's fields by reflection when a verb does not
// apply, so a String method alone would still let `%x` or a mistyped verb write the keys out.
func (MasterKeys) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redacted) //nolint:errcheck // fmt's output buffer cannot fail.
}

// LogValue keeps structured handlers from inspecting the keys behind the fields.
func (MasterKeys) LogValue() slog.Value { return slog.StringValue(redacted) }

// Load validates the entire file and uses its legacy highest write version. A database-backed node must use
// Rotation.Check before sealing: the database's active version overrides this legacy default.
func Load(path string) (*Keyring, error) {
	snapshot, err := loadSnapshot(path)
	if err != nil {
		return nil, err
	}
	ring := &Keyring{}
	ring.state.Store(snapshot)
	return ring, nil
}

// loadSnapshot reads and parses the file without a ring handle, for callers that replace a live ring.
func loadSnapshot(path string) (*keySnapshot, error) {
	keys, latest, err := readKeyFile(keyFileVariable, path)
	if err != nil {
		return nil, err
	}
	return newSnapshot(keys, latest), nil
}

func newSnapshot(keys map[uint32][32]byte, latest uint32) *keySnapshot {
	snapshot := &keySnapshot{
		keys:         make(map[uint32]cipher.AEAD, len(keys)),
		latest:       latest,
		fingerprints: make(map[uint32][32]byte, len(keys)),
	}
	for version, key := range keys {
		snapshot.keys[version] = newGCM(key[:])
		snapshot.fingerprints[version] = sha256.Sum256(key[:])
	}
	return snapshot
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
	// The opened file is what gets read, so its own mode is what the rule checks: a path replaced between a check and
	// the open must not decide which file's permissions were checked. Type and mode both come from the descriptor.
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
	// The path must still be that same file and not a link to one: whoever can point the variable at a link picks the
	// key the node seals under, while the deployment keeps its own file private, and the writer refuses to replace a
	// link for the same reason.
	at, err := os.Lstat(path)
	if err != nil {
		return nil, 0, fileError(variable, "stat", err)
	}
	if !at.Mode().IsRegular() || !os.SameFile(info, at) {
		return nil, 0, fmt.Errorf("%s: want a regular file", variable)
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
