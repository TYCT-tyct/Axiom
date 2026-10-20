package secret

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

// addKey keeps an orphaned key left by an interrupted registration, so a retry never changes its material.
func addKey(path string, version uint32) error {
	keys, _, err := readKeyFile(keyFileVariable, path)
	if err != nil {
		return err
	}
	if _, ok := keys[version]; ok {
		return nil
	}
	var key [32]byte
	rand.Read(key[:])
	keys[version] = key
	return replaceKeys(path, keys)
}

func removeKey(path string, version uint32) error {
	keys, _, err := readKeyFile(keyFileVariable, path)
	if err != nil {
		return err
	}
	if _, ok := keys[version]; !ok {
		return nil
	}
	delete(keys, version)
	return replaceKeys(path, keys)
}

// replaceKeys never truncates the live ring. It must be a regular file in a protected, writable directory,
// not a symlink or a single-file bind mount: rename replaces the directory entry.
func replaceKeys(path string, keys map[uint32][32]byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fileError(keyFileVariable, "stat", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: replacement requires a regular file, not a symlink", keyFileVariable)
	}
	if err := removeLeftovers(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".keyring-*")
	if err != nil {
		return fileError(keyFileVariable, "create replacement", err)
	}
	defer os.Remove(file.Name()) //nolint:errcheck // After rename this name is absent; otherwise remove the private temporary file.
	if err := writeKeys(file, keys, info.Mode().Perm()); err != nil {
		return fileError(keyFileVariable, "write replacement", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fileError(keyFileVariable, "replace", err)
	}
	return syncKeyDirectory(filepath.Dir(path))
}

// removeLeftovers deletes the temporary files an interrupted replacement left in the key directory. Each holds the whole
// ring, so one left behind after a retirement would keep the old material on the node. The lifecycle lock is held by the
// caller, so no other replacement is writing one of these names.
func removeLeftovers(dir string) error {
	leftovers, err := filepath.Glob(filepath.Join(dir, ".keyring-*"))
	if err != nil {
		return fileError(keyFileVariable, "find leftover replacements", err)
	}
	for _, leftover := range leftovers {
		if err := os.Remove(leftover); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fileError(keyFileVariable, "remove leftover replacement", err)
		}
	}
	return nil
}

func writeKeys(file *os.File, keys map[uint32][32]byte, mode os.FileMode) (err error) {
	defer func() {
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}()
	versions := make([]uint32, 0, len(keys))
	for version := range keys {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	for _, version := range versions {
		key := keys[version]
		if _, err := fmt.Fprintf(file, "%d:%s\n", version, base64.StdEncoding.EncodeToString(key[:])); err != nil {
			return err
		}
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return file.Sync()
}

func syncKeyDirectory(path string) error {
	// Windows does not support syncing directory handles through os.File; deployments protect the directory's ACL.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path) // #nosec G304 -- The trusted key-file setting chooses its parent directory.
	if err != nil {
		return fileError(keyFileVariable, "open directory", err)
	}
	defer dir.Close() //nolint:errcheck // Sync below reports persistence failures.
	if err := dir.Sync(); err != nil {
		return fileError(keyFileVariable, "sync directory", err)
	}
	return nil
}
