package secret

import (
	"bufio"
	"crypto/cipher"
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
	keys   map[uint32]cipher.AEAD
	latest uint32
}

// Load validates the entire file before publishing a usable keyring, so configuration errors stop startup.
func Load(path string) (*Keyring, error) {
	if path == "" {
		return nil, fmt.Errorf("%s: required", keyFileVariable)
	}
	file, err := os.Open(path) // #nosec G304 -- The trusted startup configuration selects the master key file.
	if err != nil {
		return nil, fileError("open", err)
	}
	defer file.Close() //nolint:errcheck // A file opened only for reading has nothing to flush.
	info, err := file.Stat()
	if err != nil {
		return nil, fileError("stat", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: want a regular file", keyFileVariable)
	}
	// Windows mode bits cannot express NTFS ACLs; deployment must restrict the file's ACL instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: permissions must deny all group and other access", keyFileVariable)
	}
	return readKeys(file)
}

func readKeys(reader io.Reader) (*Keyring, error) {
	ring := &Keyring{keys: make(map[uint32]cipher.AEAD)}
	scanner := bufio.NewScanner(reader)
	for line := 1; scanner.Scan(); line++ {
		version, master, err := parseKey(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", keyFileVariable, line, err)
		}
		if _, exists := ring.keys[version]; exists {
			return nil, fmt.Errorf("%s: line %d: duplicate version", keyFileVariable, line)
		}
		ring.keys[version] = master
		ring.latest = max(ring.latest, version)
	}
	if err := scanner.Err(); err != nil {
		return nil, fileError("read", err)
	}
	if len(ring.keys) == 0 {
		return nil, fmt.Errorf("%s: want at least one key", keyFileVariable)
	}
	return ring, nil
}

func parseKey(line string) (uint32, cipher.AEAD, error) {
	label, encoded, ok := strings.Cut(line, ":")
	if !ok {
		return 0, nil, errors.New("want version:base64 key")
	}
	version, err := strconv.ParseUint(label, 10, 32)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != label {
		return 0, nil, errors.New("want a canonical positive uint32 version")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || strings.ContainsAny(encoded, "\r\n") {
		return 0, nil, errors.New("want a padded standard base64 key")
	}
	if len(key) != 32 {
		return 0, nil, errors.New("key must decode to 32 bytes")
	}
	return uint32(version), newGCM(key), nil
}

func fileError(operation string, err error) error {
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
	return fmt.Errorf("%s: %s: %s", keyFileVariable, operation, reason)
}
