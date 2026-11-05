package observability

import (
	"context"
	"errors"
	"fmt"
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

// TestASettledRecordCompletesItsPendingOne: a call recorded before its settlement is one row, which its settled record
// completes whether the two reach the database in one batch or in two, and which no replay of either changes again; a
// pending record whose settlement never came stays pending, without amounts. Each run is a recorder that drains its
// records in one batch, as a restart replays the spill.
func TestASettledRecordCompletesItsPendingOne(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	settled := record(uuid.NewV7(), now)
	for _, tc := range []struct {
		name string
		runs [][]bool // each run's records, true for the pending one
		want storedCall
	}{
		{"in one batch", [][]bool{{true, false}}, storedCall{1, 2, settled.PriceNano, false, settled.Free}},
		{"in two batches", [][]bool{{true}, {false}}, storedCall{1, 2, settled.PriceNano, false, settled.Free}},
		{"replayed after it", [][]bool{{true, false}, {true, false}, {true}}, storedCall{1, 2, settled.PriceNano, false, settled.Free}},
		{"never settled", [][]bool{{true}, {true}}, storedCall{1, 2, 0, true, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settled := settled
			settled.ID = uuid.NewV7()
			pending := settled
			pending.BillingPending, pending.PriceNano, pending.Free, pending.PriceVersion = true, 0, false, ""
			pending.Usage, pending.PhysicalInput = nil, nil
			for _, run := range tc.runs {
				writeRun(t, db, run, pending, settled)
			}
			if got := readStoredCall(t, db, settled.ID); got != tc.want {
				t.Errorf("stored %+v, want %+v", got, tc.want)
			}
		})
	}
}

// writeRun writes one recorder's records, the pending one for true and the settled one for false, and drains them.
func writeRun(t *testing.T, db *pg.DB, run []bool, pending, settled CallRecord) {
	t.Helper()
	r := recorderOfDB(t, db, Config{BatchSize: 10, FlushInterval: time.Hour})
	for _, isPending := range run {
		rec := settled
		if isPending {
			rec = pending
		}
		if err := r.Enqueue(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
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
		return tx.QueryRow(t.Context(), `SELECT count(*), max(price_nano), bool_and(billing_pending), bool_and(free_of_charge),
			(SELECT count(*) FROM observability.call_attempt WHERE request_id = $1)
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
