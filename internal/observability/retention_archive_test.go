package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestRetentionWritesTempFilesUnderTMPDIR: the archive's temporary file lives in the directory the deployment gives
// it, not a hard-coded /tmp (the container's root filesystem is read-only and only its mounted volume is writable).
func TestRetentionWritesTempFilesUnderTMPDIR(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	store := newFakeStore()
	if _, err := newTestRetainer(t, retention, store).writeArchive(t.Context(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if store.putPath == "" {
		t.Fatal("the archive uploaded no file-backed object")
	}
	if got := filepath.Dir(store.putPath); got != dir {
		t.Errorf("temporary file is in %s, want TMPDIR %s", got, dir)
	}
}

// TestRetentionRewritesATruncatedArchive: a complete manifest that names both tables, with one object shorter than its
// recorded size, is not trusted; the job rewrites that object from the partition before dropping anything. Removing the
// size comparison in confirm makes this test fail on the digest or length check below, because the truncated object
// would survive and the partition would be gone.
//
//nolint:cyclop // the fixture, the run and the integrity checks are one scenario.
func TestRetentionRewritesATruncatedArchive(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store := newFakeStore()
	r := newTestRetainer(t, retention, store)
	manifest, err := r.writeArchive(t.Context(), month)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != 2 {
		t.Fatalf("the fixture manifest names %d tables, want 2", len(manifest.Tables))
	}
	target, ok := manifest.entry("call_log")
	if !ok {
		t.Fatal("the fixture manifest has no call_log")
	}
	raw := store.objects[target.Key]
	if len(raw) < 2 {
		t.Fatalf("%s is only %d bytes", target.Key, len(raw))
	}
	store.objects[target.Key] = raw[:len(raw)/2]
	if err := r.run(t.Context(), time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the partition stayed after the archive was rewritten")
	}
	manifest = readManifest(t, store, "2026-10")
	if len(manifest.Tables) != 2 {
		t.Fatalf("the rewritten manifest names %d tables, want 2", len(manifest.Tables))
	}
	for _, entry := range manifest.Tables {
		raw := store.objects[entry.Key]
		if int64(len(raw)) != entry.Bytes {
			t.Errorf("%s is %d bytes, manifest says %d", entry.Key, len(raw), entry.Bytes)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Errorf("%s digest does not match the rewritten manifest", entry.Key)
		}
		if lines := ungzipCSV(t, raw); len(lines) != int(entry.Rows)+1 {
			t.Errorf("%s has %d CSV lines, want %d rows and a header", entry.Key, len(lines), entry.Rows)
		}
	}
}

// TestRetentionRearchivesAfterAWriteDuringAConfirmFailure: an archive whose confirmation failed leaves the partition in
// place. A late retry writes another record into that month before the next run; the next run must not drop the month on
// the old objects, but copy it again. Removing the row-count check in dropMonth makes this test see the old manifest and
// lose the new row.
func TestRetentionRearchivesAfterAWriteDuringAConfirmFailure(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.headErr = errors.New("bucket head down")
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err == nil {
		t.Fatal("a failed confirmation reported success")
	}
	if !partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the partition was dropped although confirmation failed")
	}
	late := record(account, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	keep(t, db, late)
	if got := countRows(t, db, `SELECT count(*) FROM observability.call_log WHERE id = $1`, late.ID); got != 1 {
		t.Fatalf("the late record was not written: %d rows", got)
	}
	store.headErr = nil
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the partition was not dropped after the month was archived again")
	}
	manifest := readManifest(t, store, "2026-10")
	for _, entry := range manifest.Tables {
		raw := store.objects[entry.Key]
		if int64(len(raw)) != entry.Bytes {
			t.Errorf("%s is %d bytes, manifest says %d", entry.Key, len(raw), entry.Bytes)
		}
		if entry.Table == "call_log" && entry.Rows != 2 {
			t.Errorf("call_log archived %d rows, want the late row too", entry.Rows)
		}
	}
}
