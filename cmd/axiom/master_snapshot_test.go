package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
)

// replaceMasterKey writes version 1 filled with fill at path, replacing the file as an operator would: a new file in
// the same directory, then a rename.
func replaceMasterKey(t *testing.T, path string, fill byte) {
	t.Helper()
	line := "1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)) + "\n"
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

// TestServeReadsTheMasterKeyFileOnce: the ring checked against the database and the login key come from the file as it
// was read before the database opened. The file is replaced in between with another key under the same version; a
// second read by the check would disagree with the version registered before the replacement and fail.
func TestServeReadsTheMasterKeyFileOnce(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, modules()...)
	path := keyFile(t, 'm')
	cfg, err := readSettings([]string{
		pg.DatabaseURLVar + "=" + database.App, masterKeyFileVar + "=" + path,
		keyHashFileVar + "=" + keyFile(t, 'h'), identityKeyFileVar + "=" + keyFile(t, 'i'),
	})
	if err != nil {
		t.Fatal(err)
	}
	console, err := loadConsole(cfg, testGatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	master, err := secret.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := master.LoginKey()
	if err != nil {
		t.Fatal(err)
	}
	checkSessionCursorKeySnapshot(t, console, master, cfg)

	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("shutdown telemetry: %v", err)
		}
	})
	// Register version 1's fingerprint while the file still holds m, as an earlier start of this node would have. A
	// second read after the replacement then disagrees with the database, not just with the login key.
	db, _, err := openCheckedDatabase(t.Context(), cfg, tel, master.Keyring(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	replaceMasterKey(t, path, 'x')
	db, ring, err := openCheckedDatabase(t.Context(), cfg, tel, console.master, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("the startup check read the file again: %v", err)
	}
	defer db.Close()
	if got := ring.Seal(secret.New("snapshot"), secret.Location{Table: "test", RowID: "1", Purpose: "snapshot"}).KeyVersion; got != 1 {
		t.Errorf("the ring was read again: version %d, want 1", got)
	}
	if console.loginKey != want {
		t.Error("the login key did not come from the same snapshot as the ring")
	}
}

// TestTheLoginKeyUsesTheHighestDistributedVersion: a node checks envelopes against the database's active version, but
// derives the login key from the file's highest distributed version, even when that version is only readable and the
// database writes with an older one (internal/platform/kv/README.md「键」).
func TestTheLoginKeyUsesTheHighestDistributedVersion(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2") // readable 2; the database still writes version 1
	master, err := secret.LoadMaster(f.path)
	if err != nil {
		t.Fatal(err)
	}
	ring, status, err := f.r.CheckRing(t.Context(), master.Keyring())
	if err != nil {
		t.Fatal(err)
	}
	if status.Active != 1 {
		t.Fatalf("active version %d, want the unactivated 1", status.Active)
	}
	if got := ring.Seal(secret.New("active"), secret.Location{Table: "test", RowID: "1", Purpose: "snapshot"}).KeyVersion; got != 1 {
		t.Fatalf("the ring writes version %d, want the database's active 1", got)
	}
	onlyHighest := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(onlyHighest, []byte(secondKeyLine(t, f.path)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	highest, err := secret.LoadMaster(onlyHighest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := highest.LoginKey()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := master.LoginKey(); err != nil || got != want {
		t.Fatalf("the login key did not use the highest distributed version: %v, %v", got, err)
	}
}

// secondKeyLine is version 2's line of a rotation fixture's key file, whose other line is version 1.
func secondKeyLine(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- the fixture's own key file.
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(content)), "\n") {
		if strings.HasPrefix(line, "2:") {
			return line
		}
	}
	t.Fatal("the fixture's file has no version 2")
	return ""
}

func checkSessionCursorKeySnapshot(t *testing.T, console consoleParts, master *secret.MasterKeys, cfg nodeSettings) {
	t.Helper()
	wantCursor, err := master.DeriveKey("axiom console call sessions cursor v1")
	if err != nil {
		t.Fatal(err)
	}
	if console.sessionCursorKey != wantCursor || console.sessionCursorKey == [32]byte(console.loginKey) {
		t.Fatal("cursor key did not use its independent master-key label")
	}
	cfg.identityKeyFile = keyFile(t, 'z')
	changedIdentity, err := loadConsole(cfg, testGatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	if changedIdentity.sessionCursorKey != console.sessionCursorKey {
		t.Fatal("identity rotation invalidated session cursors")
	}
}
