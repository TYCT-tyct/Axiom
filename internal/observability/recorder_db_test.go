package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// TestRestartDoesNotDuplicateCommittedRecords: the active segment keeps records the writer has already acknowledged
// (it is not deleted until it is full), so a crash makes the next process replay them. The inserts' ON CONFLICT makes
// that replay harmless.
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
