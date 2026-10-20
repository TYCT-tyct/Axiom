package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

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
	if err := os.WriteFile(f.path, both, 0o600); err != nil {
		t.Fatal(err)
	}
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
	if count, err := f.r.RewrapBatch(t.Context(), 100); err == nil || count != 0 {
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
	if err := os.WriteFile(f.path, append(active, []byte(relabeled)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Load(f.path); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Add(t.Context(), 3); err == nil {
		t.Fatal("registered retired material with a new version")
	}
}
