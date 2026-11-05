package observability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestRestartDoesNotDuplicateCommittedRecords: the writer records the acknowledged position and deletes a drained
// segment, so a crash after the records reached the database normally replays nothing. Should a prefix still replay
// (a crash between the commit and the checkpoint), the inserts' ON CONFLICT makes it harmless.
func TestRestartDoesNotDuplicateCommittedRecords(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	records := []CallRecord{record(a, now), record(a, now.Add(-time.Second)), record(a, now.Add(-2*time.Second))}
	dir := t.TempDir()
	r := recorderOfDB(t, db, Config{Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Millisecond})
	enqueueAll(t, r, records)
	eventually(t, "the records to reach the database", func() bool { return r.spill.pendingCount() == 0 })
	crash(r)

	next := recorderOfDB(t, db, Config{Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Millisecond})
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := list(t, db, accountScope(a), day(a)); len(got) != len(records) {
		t.Errorf("%d rows after the replay, want %d: replay must not duplicate", len(got), len(records))
	}
}

// TestAPendingRecordIsReplacedOrKept: a pending record is parked, and only one record of its call reaches the call log:
// the settled one when it comes, the pending one when the settlement goes to the cleanup or the node dies first. A
// parked record that outlives its settled record (the node died between the two) is replayed after it and changes
// nothing. The table is only inserted into, so none of this depends on how batches fall.
func TestAPendingRecordIsReplacedOrKept(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	settledCall := record(uuid.NewV7(), now)
	settled := storedCall{1, 2, settledCall.PriceNano, false, settledCall.Free}
	pending := storedCall{1, 2, 0, true, false}
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, r *Recorder, slot *Slot, settled CallRecord)
		want storedCall
	}{
		{"replaced by its settled record", func(t *testing.T, _ *Recorder, slot *Slot, rec CallRecord) {
			commit(t, slot, rec)
		}, settled},
		{"left to the cleanup", func(_ *testing.T, _ *Recorder, slot *Slot, _ CallRecord) { slot.Release() }, pending},
		{"parked when the node died", func(_ *testing.T, r *Recorder, _ *Slot, _ CallRecord) { crash(r) }, pending},
		{"parked after its settled record when the node died", func(t *testing.T, r *Recorder, slot *Slot, rec CallRecord) {
			parked := readParked(t, r.cfg.Dir, rec.ID)
			commit(t, slot, rec)
			crash(r)
			writeParked(t, r.cfg.Dir, rec.ID, parked) // the removal after the settled record did not survive
		}, settled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := settledCall
			rec.ID = uuid.NewV7()
			dir := t.TempDir()
			r := recorderOfDB(t, db, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour})
			slot, err := r.Admit()
			if err != nil {
				t.Fatal(err)
			}
			commit(t, slot, pendingOf(rec))
			tc.end(t, r, slot, rec)
			crash(r) // a no-op when the case already stopped it
			if err := recorderOfDB(t, db, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour}).Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := readStoredCall(t, db, rec.ID); got != tc.want {
				t.Errorf("stored %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestADamagedParkedRecordFailsTheStart: a parked record whose bytes do not match their checksum may be a call that was
// served, so the recorder refuses to start rather than drop it, and names the file.
func TestADamagedParkedRecordFailsTheStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := recorderOf(t, &sink{}, Config{Dir: dir, BatchSize: 10, FlushInterval: time.Hour})
	slot, err := r.Admit()
	if err != nil {
		t.Fatal(err)
	}
	rec := record(uuid.NewV7(), now)
	commit(t, slot, pendingOf(rec))
	crash(r)
	b := readParked(t, dir, rec.ID)
	b[len(b)-1] ^= 1
	writeParked(t, dir, rec.ID, b)
	cfg := Config{
		Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Hour,
		RetryBase: time.Millisecond, RetryMax: time.Millisecond, WriteTimeout: time.Second,
	}
	_, err = newRecorder(t.Context(), cfg, (&sink{}).write, isBadRow, quiet)
	if !errors.Is(err, errDamagedPark) || !strings.Contains(err.Error(), name(rec.ID.String())) {
		t.Errorf("start with a damaged parked record = %v, want a checksum error naming its file", err)
	}
}

// readParked and writeParked read and replace a call's parked file in the spill directory dir.
func readParked(t *testing.T, dir string, id uuid.UUID) []byte {
	t.Helper()
	root := parkedRoot(t, dir)
	b, err := root.ReadFile(name(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeParked(t *testing.T, dir string, id uuid.UUID, b []byte) {
	t.Helper()
	if err := parkedRoot(t, dir).WriteFile(name(id.String()), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func parkedRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join(dir, parkDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}

func commit(t *testing.T, slot *Slot, rec CallRecord) {
	t.Helper()
	if err := slot.Commit(rec); err != nil {
		t.Fatal(err)
	}
}

// pendingOf is rec recorded before its settlement: no usage, price, price version or waiver.
func pendingOf(rec CallRecord) CallRecord {
	rec.BillingPending, rec.PriceNano, rec.Free, rec.PriceVersion = true, 0, false, ""
	rec.Usage, rec.PhysicalInput = nil, nil
	return rec
}

// storedCall is what the call log holds of one call: its rows, its attempts and the settlement's columns.
type storedCall struct {
	rows, attempts int64
	price          int64
	pending, free  bool
}

func readStoredCall(t *testing.T, db *pg.DB, id uuid.UUID) (c storedCall) {
	t.Helper()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), coalesce(max(price_nano), -1), coalesce(bool_and(billing_pending), false),
			coalesce(bool_and(free_of_charge), false), (SELECT count(*) FROM observability.call_attempt WHERE request_id = $1)
			FROM observability.call_log WHERE id = $1`, id.String()).Scan(&c.rows, &c.price, &c.pending, &c.free, &c.attempts)
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRecorderWritesThroughTheDatabase: the whole path, on a real database, in the platform scope.
func TestRecorderWritesThroughTheDatabase(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	records := []CallRecord{record(a, now), record(a, now.Add(-time.Second)), record(uuid.NewV7(), now)}
	keep(t, db, records...)
	if got := list(t, db, accountScope(a), day(a)); len(got) != 2 {
		t.Errorf("account A has %d calls stored, want 2", len(got))
	}
}

// TestEndUserHashIsKeyed: the hash depends on the key and on the identifier, is stable, and is what the row holds.
func TestEndUserHashIsKeyed(t *testing.T) {
	t.Parallel()
	a, b := HashEndUser([]byte("key one"), "user-1"), HashEndUser([]byte("key two"), "user-1")
	if a == b || a == HashEndUser([]byte("key one"), "user-2") || a != HashEndUser([]byte("key one"), "user-1") || a == (EndUserHash{}) {
		t.Error("the end-user hash is not a stable function of the key and the identifier")
	}
	rec := record(uuid.NewV7(), now)
	rec.EndUser = a
	r, attempts, err := newRows(rec)
	if err != nil || len(r.EndUserHash) != 64 || strings.Contains(r.EndUserHash, "user-1") || len(attempts) != 2 {
		t.Errorf("row hash %q, %d attempts (%v)", r.EndUserHash, len(attempts), err)
	}
}

// TestBadAttemptStopsWritesAndKeepsTheSpill: the attempt insert refuses a 65-character error_kind. Because the call
// and its attempts share one transaction, the whole batch rolls back; the records stay in the spill and admission
// refuses, rather than later calls being written while this one is missing.
func TestBadAttemptStopsWritesAndKeepsTheSpill(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	before, bad, after := record(a, now), record(a, now.Add(-time.Second)), record(a, now.Add(-2*time.Second))
	bad.Attempts[0].ErrorKind = strings.Repeat("x", 65)
	r := recorderOfDB(t, db, Config{
		Capacity: 10, BatchSize: 3, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond,
	})
	enqueueAll(t, r, []CallRecord{before, bad, after})
	eventually(t, "the database to refuse the bad attempt", func() bool { return r.broken.Load() })
	if _, err := r.Admit(); !errors.Is(err, ErrBroken) {
		t.Fatalf("Admit after the bad attempt = %v, want ErrBroken", err)
	}
	if got := r.spill.pendingCount(); got != 3 {
		t.Fatalf("%d records in the spill, want 3", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, ErrPending) {
		t.Fatalf("Close = %v, want ErrPending", err)
	}
	if got := list(t, db, accountScope(a), day(a)); len(got) != 0 {
		t.Errorf("%d calls stored, want 0 while the batch cannot commit", len(got))
	}
}

// TestArchivedRecordIsNotReplayed: a record already stored must not be replayed after its month was archived and the
// partition dropped. The original key is gone, so ON CONFLICT no longer deduplicates and the replay would land in the
// default partition, which retention never archives or cleans.
func TestArchivedRecordIsNotReplayed(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	a := uuid.NewV7()
	rec := record(a, now)
	dir := t.TempDir()
	r := recorderOfDB(t, db, Config{Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Millisecond})
	enqueueAll(t, r, []CallRecord{rec})
	eventually(t, "the record to reach the database", func() bool { return r.spill.pendingCount() == 0 })
	crash(r)

	// The month's archive committed and its partition was dropped: the row is no longer in the hot table.
	if err := pgtest.Run(t.Context(), database.Superuser,
		fmt.Sprintf(`DELETE FROM observability.call_log WHERE id = '%s'`, rec.ID)); err != nil {
		t.Fatal(err)
	}

	next := recorderOfDB(t, db, Config{Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Millisecond})
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := list(t, db, accountScope(a), day(a)); len(got) != 0 {
		t.Errorf("%d rows after the restart, want 0: an acknowledged record was inserted again", len(got))
	}
}
