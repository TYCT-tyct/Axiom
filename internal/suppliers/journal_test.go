package suppliers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

func (w *world) receipt(ref string) AttemptReceipt {
	return AttemptReceipt{
		supplier: w.supplier, credential: w.credential, endpoint: w.endpoint, schedule: w.sc,
		ref: ref, pricedAt: w.clock.now(),
	}
}

func newRequest() platform.RequestID { return platform.RequestID(uuid.NewV7()) }

func (w *world) entry(id platform.RequestID, seq platform.AttemptSeq, ref string, committed bool) JournalEntry {
	return JournalEntry{Request: id, Seq: seq, Receipt: w.receipt(ref), Committed: committed}
}

func (w *world) journalOf(id platform.RequestID) (entries []JournalEntry) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		entries, err = JournalOf(ctx, tx, id)
		return err
	})
	return entries
}

func (w *world) flush() {
	w.t.Helper()
	if err := w.svc.Flush(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// journalAttempt appends what an attempt does: it enters Send, the supplier's key arrives, the first content goes to the
// client, and entries follow that know neither.
func (w *world) journalAttempt(id platform.RequestID, seq platform.AttemptSeq) {
	w.svc.Journal(w.entry(id, seq, "", false))
	w.svc.Journal(w.entry(id, seq, "r", false))
	w.svc.Journal(w.entry(id, seq, "", false))
	w.svc.Journal(w.entry(id, seq, "r", true))
	w.svc.Journal(w.entry(id, seq, "", false))
}

// TestTheJournalOfManyRequestsIsOneStatement: the attempts of five requests, each reported five times, are fifteen rows
// and one write.
func TestTheJournalOfManyRequestsIsOneStatement(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	var calls, rowsWritten int
	w.svc.writeRows = func(ctx context.Context, rows []store.JournalRow) error {
		calls++
		rowsWritten += len(rows)
		return w.db.Tx(ctx, platformScope, func(tx pg.Tx) error { return store.UpsertJournal(ctx, tx, rows) })
	}
	for range 5 {
		id := newRequest()
		for seq := platform.AttemptSeq(1); seq <= 3; seq++ {
			w.journalAttempt(id, seq)
		}
	}
	if got := w.svc.JournalBuffered(); got != 15 {
		t.Fatalf("%d attempts buffered for 75 entries, want 15", got)
	}
	w.flush()
	if calls != 1 || rowsWritten != 15 || w.svc.JournalBuffered() != 0 {
		t.Errorf("%d writes of %d rows, %d left; want one write of 15", calls, rowsWritten, w.svc.JournalBuffered())
	}
}

// TestAJournalRowIsOnlyAddedTo: a key that arrived is not lost to a later entry without one, and a commit is not undone,
// in the buffer and in the table, however the writes are ordered.
func TestAJournalRowIsOnlyAddedTo(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	w.journalAttempt(id, 1)
	w.flush()
	if e := w.journalOf(id)[0]; e.Receipt.ref != "r" || !e.Committed {
		t.Fatalf("the written row: %+v", e)
	}
	// A node that only knows less writes later: the stored row keeps what it has.
	w.svc.Journal(w.entry(id, 1, "", false))
	w.flush()
	if e := w.journalOf(id)[0]; e.Receipt.ref != "r" || !e.Committed {
		t.Errorf("a later write took away what was stored: %+v", e)
	}
}

// TestJournalFlushedClosesWhenTheEntriesAreWritten: a request with nothing pending is flushed at once, one with entries
// after the write, and an entry appended after the call is waited for by the next call and not by this one.
func TestJournalFlushedClosesWhenTheEntriesAreWritten(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	if !isClosed(w.svc.JournalFlushed(newRequest())) {
		t.Error("a request with no entries is not flushed")
	}
	pending := newRequest()
	w.svc.Journal(w.entry(pending, 1, "a", false))
	waiting := w.svc.JournalFlushed(pending)
	if isClosed(waiting) {
		t.Fatal("flushed before the write")
	}
	w.flush()
	if !isClosed(waiting) || len(w.journalOf(pending)) != 1 {
		t.Error("not flushed after the write")
	}
	later := newRequest()
	w.svc.Journal(w.entry(later, 1, "", false))
	first := w.svc.JournalFlushed(later)
	w.svc.Journal(w.entry(later, 1, "x", true))
	second := w.svc.JournalFlushed(later)
	w.flush()
	if !isClosed(first) || !isClosed(second) {
		t.Error("a flush that wrote both entries left a waiter")
	}
}

// TestSettledDropsWhatIsNotWrittenAndReleasesWaiters: a settled request needs nothing more from the journal, so its
// unwritten rows are dropped and whoever waits for it is released.
func TestSettledDropsWhatIsNotWrittenAndReleasesWaiters(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	settled := newRequest()
	w.svc.Journal(w.entry(settled, 1, "b", false))
	waiting := w.svc.JournalFlushed(settled)
	w.svc.Settled(settled)
	if !isClosed(waiting) {
		t.Error("a settled request still waits")
	}
	w.flush()
	if got := len(w.journalOf(settled)); got != 0 || w.svc.JournalBuffered() != 0 {
		t.Errorf("%d rows written and %d buffered for a settled request, want none", got, w.svc.JournalBuffered())
	}
}

// settleAndStart ends the request a slot held and starts the next, which appends three attempts of three entries.
func (w *world) settleAndStart(running map[int]platform.RequestID, slot int) platform.RequestID {
	if old, ok := running[slot]; ok {
		w.svc.Settled(old)
	}
	id := newRequest()
	running[slot] = id
	for seq := platform.AttemptSeq(1); seq <= 3; seq++ {
		for range 3 { // begin, key, commit
			w.svc.Journal(w.entry(id, seq, "k", seq == 3))
		}
	}
	return id
}

// TestTheJournalNeverDropsAndStaysBounded: while the database does not take the writes, the buffer holds one row per
// attempt of the requests that are running, however many entries they append, and settled requests leave it; when
// the database is back everything still there is written and the waiters are released.
func TestTheJournalNeverDropsAndStaysBounded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	down := errors.New("database down")
	w.svc.writeRows = func(context.Context, []store.JournalRow) error { return down }
	const slots = 10 // requests running at once on this node
	running := map[int]platform.RequestID{}
	var waits []<-chan struct{}
	for round := range 200 {
		id := w.settleAndStart(running, round%slots)
		if round >= 190 {
			waits = append(waits, w.svc.JournalFlushed(id))
		}
		if err := w.svc.Flush(t.Context()); !errors.Is(err, down) {
			t.Fatalf("a failed write: %v", err)
		}
		if n := w.svc.JournalBuffered(); n > slots*3 {
			t.Fatalf("%d rows buffered after %d requests, the limit for %d slots is %d", n, round+1, slots, slots*3)
		}
	}
	wantReleased(t, waits, false)
	w.svc.writeRows = nil
	w.flush()
	wantReleased(t, waits, true)
	for slot, id := range running {
		if got := len(w.journalOf(id)); got != 3 {
			t.Errorf("slot %d: %d attempts of its running request were written, want 3", slot, got)
		}
	}
}

func wantReleased(t *testing.T, waits []<-chan struct{}, released bool) {
	t.Helper()
	for _, c := range waits {
		if isClosed(c) != released {
			t.Fatalf("a waiter is released = %v, want %v", !released, released)
		}
	}
}

// TestACrashBetweenSendAndSettleLeavesWhatCleanupNeeds: a node whose entries were written and which then disappeared
// has left, for each attempt, the supplier's key and whether content went to the client, and no cost: cleanup reads
// it from the journal.
func TestACrashBetweenSendAndSettleLeavesWhatCleanupNeeds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	w.svc.Journal(w.entry(id, 1, "first", false))
	w.svc.Journal(w.entry(id, 2, "", false))
	w.svc.Journal(w.entry(id, 3, "third", true))
	w.flush()
	// The node is gone: nothing of it is left but the database.
	entries := w.journalOf(id)
	if len(entries) != 3 {
		t.Fatalf("%d attempts in the journal, want 3", len(entries))
	}
	want := []struct {
		ref       string
		committed bool
	}{{"first", false}, {"", false}, {"third", true}}
	for i, e := range entries {
		if e.Receipt.ref != want[i].ref || e.Committed != want[i].committed || e.Seq != platform.AttemptSeq(i+1) ||
			e.Receipt.credential != w.credential || e.Receipt.endpoint != w.endpoint || e.Receipt.schedule != w.sc {
			t.Errorf("attempt %d: %+v", i+1, e)
		}
	}
	if costs := w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1", uuid.UUID(id)); costs != 0 {
		t.Errorf("%d attempt costs for a request that never settled", costs)
	}
}

func (w *world) endRequest(id platform.RequestID, at time.Time) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return EndRequest(ctx, tx, id, nil, nil, at) })
}

func (w *world) purge(now time.Time) (purged int64) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		purged, err = PurgeJournal(ctx, tx, now)
		return err
	})
	return purged
}

// TestAJournalIsPurgedTwoDaysAfterItsRequestEnded: what billing says has ended decides, not what this module can see. A
// request with attempt costs whose cleanup is still pending keeps its journal however old; one that ended a moment ago
// keeps it though the rows are old; two days after it ended it goes; and a request never told to have ended keeps it.
func TestAJournalIsPurgedTwoDaysAfterItsRequestEnded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	now := time.Now()
	pending, justEnded, longEnded, never := newRequest(), newRequest(), newRequest(), newRequest()
	for i, id := range []platform.RequestID{pending, justEnded, longEnded, never} {
		w.svc.Journal(w.entry(id, 1, "ref-"+string(rune('a'+i)), true))
	}
	w.flush()
	w.record(w.attempt(pending, 1, "ref-a")) // cleanup wrote the attempt and the request waits to be corrected
	w.endRequest(justEnded, now.Add(-time.Hour))
	w.endRequest(longEnded, now.Add(-JournalRetention-time.Minute))
	w.endRequest(longEnded, now) // told again: the first time stands
	if purged := w.purge(now.Add(30 * 24 * time.Hour)); purged != 2 {
		t.Errorf("purged %d rows a month on, want the two of the requests that ended", purged)
	}
	for name, id := range map[string]platform.RequestID{"pending cleanup": pending, "never ended": never} {
		if len(w.journalOf(id)) != 1 {
			t.Errorf("the journal of a request with %s was removed", name)
		}
	}
}

// TestTheRetentionIsFromTheEndNotFromTheWrite: at the time of purging, a journal written long ago whose request ended a
// moment ago stays, and one whose request ended two days ago goes.
func TestTheRetentionIsFromTheEndNotFromTheWrite(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	now := time.Now()
	fresh, old := newRequest(), newRequest()
	w.svc.Journal(w.entry(fresh, 1, "a", true))
	w.svc.Journal(w.entry(old, 1, "b", true))
	w.flush()
	w.endRequest(fresh, now.Add(-time.Minute))
	w.endRequest(old, now.Add(-JournalRetention-time.Second))
	if purged := w.purge(now); purged != 1 {
		t.Fatalf("purged %d rows, want only the one whose request ended two days ago", purged)
	}
	if len(w.journalOf(fresh)) != 1 || len(w.journalOf(old)) != 0 {
		t.Errorf("rows left: %d of the request that ended a minute ago, %d of the one that ended two days ago", len(w.journalOf(fresh)), len(w.journalOf(old)))
	}
}

// TestABlockedWriteDoesNotStopTheLoop: a write that does not return is given up at the flush timeout, logged, and tried
// again with the rows still in the buffer; when the database answers they are written and the waiters released.
func TestABlockedWriteDoesNotStopTheLoop(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.svc.cfg.FlushEvery, w.svc.cfg.FlushTimeout = 5*time.Millisecond, 30*time.Millisecond
	var calls atomic.Int32
	w.svc.writeRows = func(ctx context.Context, rows []store.JournalRow) error {
		if calls.Add(1) <= 3 {
			<-ctx.Done() // the database does not answer
			return ctx.Err()
		}
		return w.db.Tx(ctx, platformScope, func(tx pg.Tx) error { return store.UpsertJournal(ctx, tx, rows) })
	}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { _ = w.svc.Run(ctx) }) //nolint:errcheck // the final flush is read from the database below
	id := newRequest()
	w.svc.Journal(w.entry(id, 1, "a", true))
	waiting := w.svc.JournalFlushed(id)
	select {
	case <-waiting:
	case <-time.After(20 * time.Second):
		t.Fatal("the loop never got past a blocked write")
	}
	cancel()
	wg.Wait()
	if calls.Load() < 4 || len(w.journalOf(id)) != 1 {
		t.Errorf("%d write attempts, %d rows written; want at least 4 and 1", calls.Load(), len(w.journalOf(id)))
	}
}

// TestRunWritesEverySecondAndOnceMoreAtTheEnd: the loop writes the buffer on its own, and what is appended just before it
// is stopped is written by the last flush.
func TestRunWritesEverySecondAndOnceMoreAtTheEnd(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.svc.cfg.FlushEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { _ = w.svc.Run(ctx) }) //nolint:errcheck // the final flush's result is read from the database below
	first := newRequest()
	w.svc.Journal(w.entry(first, 1, "a", false))
	select {
	case <-w.svc.JournalFlushed(first):
	case <-time.After(10 * time.Second):
		t.Fatal("the loop never wrote the journal")
	}
	last := newRequest()
	w.svc.Journal(w.entry(last, 1, "b", true))
	cancel()
	wg.Wait()
	if len(w.journalOf(first)) != 1 || len(w.journalOf(last)) != 1 {
		t.Errorf("rows written: %d and %d, want 1 and 1", len(w.journalOf(first)), len(w.journalOf(last)))
	}
}
