package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

// restoreKeyFile puts contents back where the fixture's key file lives, as a crash between a commit and the file
// replacement leaves it.
func restoreKeyFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil { //nolint:gosec // the path is the fixture's own temp file
		t.Fatal(err)
	}
}

func TestKeyringAddRecoversAfterFilePersisted(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("check")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"ALTER TABLE platform.audit_log ADD CONSTRAINT reject_add CHECK (action <> 'keyring.add') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Add(t.Context(), 2); err == nil {
		t.Fatal("add succeeded without audit")
	}
	orphaned, err := os.ReadFile(f.path)
	if err != nil || !bytes.Contains(orphaned, []byte("\n2:")) {
		t.Fatal("the failure did not leave the expected recoverable file")
	}
	_, status, err := f.r.Check(t.Context())
	if err != nil || status.Active != 1 || len(status.Readable) != 1 {
		t.Fatalf("an uncommitted addition changed the write version: %+v, %v", status, err)
	}
	if err := pgtest.Run(t.Context(), f.database.Superuser, "ALTER TABLE platform.audit_log DROP CONSTRAINT reject_add"); err != nil {
		t.Fatal(err)
	}
	f.run("add", "--version", "2")
	recovered, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(orphaned, recovered) {
		t.Fatal("retry replaced the material already persisted")
	}
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.add'"); got != "1" {
		t.Fatalf("retry recorded %s additions, want one", got)
	}
}

func TestKeyringRetireRecoversAfterMetadataCommitted(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	both, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.run("activate", "--version", "2")
	f.run("rewrap")
	f.run("retire", "--version", "1")
	// Reproduce a crash after the retirement commit but before the file replacement.
	restoreKeyFile(t, f.path, both)
	// An operator can clear the file left behind by rerunning retire; the tombstone is already committed, so this writes
	// no second audit entry.
	f.run("retire", "--version", "1")
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.retire'"); got != "1" {
		t.Fatalf("retry recorded %s retirements, want one", got)
	}
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.verifyRotated(ring)
}

// interruptedRetirement leaves a ring in the state a crash between a retirement's commit and its file edit leaves it:
// version 1 is tombstoned and unreferenced, but both versions are still in the file.
func interruptedRetirement(t *testing.T) rotationFixture {
	t.Helper()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	both, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.run("activate", "--version", "2")
	f.run("rewrap")
	f.run("retire", "--version", "1")
	restoreKeyFile(t, f.path, both)
	return f
}

// wantHealedRetirement is the state after pruning a version that no envelope references: the file and the ring lose it,
// and the already-committed retirement is not recorded a second time.
func wantHealedRetirement(t *testing.T, f rotationFixture, ring *secret.Keyring, status secret.KeyStatus) {
	t.Helper()
	if status.Active != 2 || len(status.Readable) != 1 || len(status.Retired) != 1 {
		t.Fatalf("status after the recovery: %+v", status)
	}
	f.verifyRotated(ring)
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.retire'"); got != "1" {
		t.Fatalf("prune recorded %s retirements, want one", got)
	}
}

// TestKeyringCheckPrunesAnInterruptedRetirement: a retirement commits its tombstone before it edits the file, so a crash
// between the two leaves a node holding a retired version. Check clears it when no envelope references it, so the state
// does not keep a restarting node down until an operator runs retire.
func TestKeyringCheckPrunesAnInterruptedRetirement(t *testing.T) {
	t.Parallel()
	f := interruptedRetirement(t)
	ring, status, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatalf("check after an interrupted retirement: %v", err)
	}
	wantHealedRetirement(t, f, ring, status)
}

// TestKeyringRefreshPrunesAnInterruptedRetirement: the running lifecycle heals the same state on a tick, and installs the
// ring it read, so a node that was offline during the retirement recovers without a restart.
func TestKeyringRefreshPrunesAnInterruptedRetirement(t *testing.T) {
	t.Parallel()
	f := interruptedRetirement(t)
	ring, err := secret.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.r.Refresh(t.Context(), ring)
	if err != nil {
		t.Fatalf("refresh after an interrupted retirement: %v", err)
	}
	wantHealedRetirement(t, f, ring, status)
}

// TestKeyringCheckKeepsReferencedRetiredMaterial: a tombstone with a stored envelope still pointing at the version is not
// a completed retirement. The check must refuse and leave the only key that can read that envelope in place rather than
// clearing the file into unrecoverable data; only a damaged or hand-edited database reaches this state, which is exactly
// when deleting the key would be worst.
func TestKeyringCheckKeepsReferencedRetiredMaterial(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"UPDATE platform.key_versions SET state='retired' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("check accepted a retired version that stored envelopes reference: %v", err)
	}
	if contents, err := os.ReadFile(f.path); err != nil || !bytes.Contains(contents, []byte("1:")) {
		t.Fatal("the check deleted a key that stored envelopes still reference")
	}
}

func TestKeyringInventoryFailsClosed(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	r := f.r
	r.Stores = r.Stores[:2]
	if _, _, err := r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("omitted credential store was not detected: %v", err)
	}
}

func TestKeyringUnknownReferencedVersionFailsCheck(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("check")
	// A restored row whose master was omitted from the restored ring must stop deployment even though
	// the current write version is present. No decryption or printing of the row is needed.
	if err := pgtest.Run(t.Context(), f.database.Superuser, "UPDATE accounts.users SET totp_key_version=99"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "[99]") {
		t.Fatalf("missing referenced master was not reported: %v", err)
	}
}

func TestKeyringRefusesCorruptBatchWithoutAdvancing(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"UPDATE accounts.users SET totp_ciphertext=set_byte(totp_ciphertext, 0, get_byte(totp_ciphertext, 0) # 1)"); err != nil {
		t.Fatal(err)
	}
	if count, _, err := f.r.RewrapBatch(t.Context(), secret.RewrapCursor{}, 100); err == nil || count != 0 {
		t.Fatalf("corruption advanced a checkpoint: count=%d, err=%v", count, err)
	}
	if got := f.text("SELECT count(*)::text FROM platform.audit_log WHERE action='keyring.rewrap'"); got != "0" {
		t.Fatal("failed authentication recorded progress")
	}
	for _, row := range f.before {
		if got := f.read(row); got.KeyVersion != 1 {
			t.Fatal("failed batch advanced a version")
		}
	}
}

func TestKeyringRetiredMaterialCannotUseAnotherVersion(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	old, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	f.run("rewrap")
	f.run("retire", "--version", "1")
	active, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	// A unique fingerprint tombstone also refuses old material relabeled after it left the live file.
	relabeled := strings.Replace(string(old), "1:", "3:", 1)
	restoreKeyFile(t, f.path, append(active, []byte(relabeled)...))
	if _, err := secret.Load(f.path); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Add(t.Context(), 3); err == nil {
		t.Fatal("registered retired material with a new version")
	}
}

func TestKeyringCursorRechecksEarlierRows(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	f.run("add", "--version", "2")
	f.run("add", "--version", "3")
	f.run("activate", "--version", "2")
	count, cursor, err := f.r.RewrapBatch(t.Context(), secret.RewrapCursor{}, 1)
	if err != nil || count != 1 {
		t.Fatalf("first batch: %d, %v", count, err)
	}
	// Another run moves the completed row to V3, then activation returns to V2: the version alone cannot detect this.
	f.run("activate", "--version", "3")
	if count, _, err := f.r.RewrapBatch(t.Context(), secret.RewrapCursor{}, 1); err != nil || count != 1 {
		t.Fatalf("interleaved batch: %d, %v", count, err)
	}
	f.run("activate", "--version", "2")
	for range 5 {
		count, next, err := f.r.RewrapBatch(t.Context(), cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		if count != 0 {
			continue
		}
		for _, row := range f.before {
			if got := f.read(row); got.KeyVersion != 2 {
				t.Fatalf("rewrap finished with version %d in %s", got.KeyVersion, row.store.Table)
			}
		}
		return
	}
	t.Fatal("rewrap did not finish after the interleaved run")
}
