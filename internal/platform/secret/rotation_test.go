package secret

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRing writes a master file with one distinct 32-byte key per version, the shape the deployment keeps.
func writeRing(t *testing.T, versions ...uint32) string {
	t.Helper()
	var contents strings.Builder
	for _, version := range versions {
		key := make([]byte, 32)
		binary.BigEndian.PutUint32(key[28:], version)
		fmt.Fprintf(&contents, "%d:%s\n", version, base64.StdEncoding.EncodeToString(key))
	}
	path := filepath.Join(t.TempDir(), "master")
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRestoredDatabaseNeedsTheKeyVersionsItReferences is the keyring half of the PITR runbook: a database restored to a
// time before a rotation still references the old readable version, so the current file, which retired that version,
// must be refused before the program starts; the historical file with both versions must open the old ciphertext.
func TestRestoredDatabaseNeedsTheKeyVersionsItReferences(t *testing.T) {
	t.Parallel()
	historicalPath := writeRing(t, 1, 2)
	currentPath := writeRing(t, 2)
	oldOnlyPath := writeRing(t, 1)

	historical, err := loadSnapshot(historicalPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := loadSnapshot(currentPath)
	if err != nil {
		t.Fatal(err)
	}

	fingerprint := historical.fingerprints[1]
	missing := newVersionCheck()
	missing.add(current, 1, fingerprint[:], "active", 0)
	if err := missing.err(); err == nil || !strings.Contains(err.Error(), "missing versions [1]") {
		t.Fatalf("a restored database whose version 1 is absent from the current file was accepted: %v", err)
	}

	present := newVersionCheck()
	present.add(historical, 1, fingerprint[:], "active", 0)
	if err := present.err(); err != nil {
		t.Fatalf("the historical file with version 1 was refused: %v", err)
	}

	historicalKeys, err := Load(historicalPath)
	if err != nil {
		t.Fatal(err)
	}
	oldOnlyKeys, err := Load(oldOnlyPath)
	if err != nil {
		t.Fatal(err)
	}
	currentKeys, err := Load(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	loc := Location{Table: "credentials", RowID: "42", Purpose: "api_key"}
	sealed := oldOnlyKeys.Seal(New("row restored to the target"), loc)
	if _, err := currentKeys.Open(sealed, loc); err == nil {
		t.Fatal("the current file opened a row whose key version it no longer holds")
	}
	opened, err := historicalKeys.Open(sealed, loc)
	if err != nil || opened.Reveal() != "row restored to the target" {
		t.Fatalf("the historical file did not open the restored row: %v", err)
	}
}
