package observability

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// sink stands in for the database: it keeps what is written, in order, and can be told to block or fail.
type sink struct {
	mu       sync.Mutex
	batches  [][]string    // request IDs per write that succeeded
	attempts int           // attempt rows in those writes
	calls    int           // writes, successful or not
	failing  int           // the next writes fail, this many
	gate     chan struct{} // when set, a write waits for it to close
	poison   string        // a write containing this request ID fails with a data error
}

var (
	errDown   = errors.New("database unavailable")
	errPoison = errors.New("malformed row")
)

func (s *sink) write(ctx context.Context, batch []queued) error {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failing > 0 {
		s.failing--
		return errDown
	}
	ids := make([]string, len(batch))
	attempts := 0
	for i, q := range batch {
		if q.call.ID == s.poison {
			return errPoison
		}
		ids[i] = q.call.ID
		attempts += len(q.attempts)
	}
	s.batches = append(s.batches, ids)
	s.attempts += attempts
	return nil
}

func (s *sink) stored() (ids []string, batches int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		ids = append(ids, b...)
	}
	return ids, len(s.batches)
}

func (s *sink) attemptsStored() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *sink) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *sink) open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.gate)
	s.gate = nil
}

func recorderOf(s *sink, cfg Config) *Recorder {
	if cfg.RetryBase == 0 {
		cfg.RetryBase, cfg.RetryMax = time.Millisecond, 4*time.Millisecond
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	return newRecorder(context.Background(), cfg, s.write, func(err error) bool { return errors.Is(err, errPoison) }, quiet)
}

func calls(n int) []CallRecord {
	out := make([]CallRecord, n)
	for i := range out {
		out[i] = record(uuid.NewV7(), now)
	}
	return out
}

func ids(records []CallRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID.String()
	}
	return out
}

// eventually waits for cond, which the writer goroutine makes true.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func enqueueAll(t *testing.T, r *Recorder, records []CallRecord) {
	t.Helper()
	for _, rec := range records {
		if err := r.Enqueue(rec); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBatchesFlushOnSizeAndOnTime: a full batch is written at once, however long the interval; a partial one waits
// for the interval and is then written alone. Attempts travel in the same write as their call.
func TestBatchesFlushOnSizeAndOnTime(t *testing.T) {
	t.Parallel()
	s := &sink{}
	r := recorderOf(s, Config{QueueSize: 100, BatchSize: 3, FlushInterval: time.Hour})
	records := calls(3)
	enqueueAll(t, r, records)
	eventually(t, "a full batch to be written without waiting for the interval", func() bool {
		got, batches := s.stored()
		return batches == 1 && slices.Equal(got, ids(records))
	})
	if got := s.attemptsStored(); got != 2*len(records) {
		t.Errorf("%d attempts written with the calls, want %d", got, 2*len(records))
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// A partial batch waits for its interval: with an interval this test cannot outlast, nothing is written before
	// Close, which writes it.
	s = &sink{}
	r = recorderOf(s, Config{QueueSize: 100, BatchSize: 100, FlushInterval: time.Hour})
	one := calls(1)
	enqueueAll(t, r, one)
	if got, _ := s.stored(); len(got) != 0 {
		t.Error("a partial batch was written before its interval")
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(one)) {
		t.Errorf("stored %v on Close, want %v", got, ids(one))
	}

	// ...and the interval writes it when it passes.
	s = &sink{}
	r = recorderOf(s, Config{QueueSize: 100, BatchSize: 100, FlushInterval: 30 * time.Millisecond})
	enqueueAll(t, r, one)
	eventually(t, "the interval to flush a partial batch", func() bool { got, _ := s.stored(); return len(got) == 1 })
	if got := s.attemptsStored(); got != 2 {
		t.Errorf("%d attempts written with the one call, want 2", got)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestFullQueueDropsWithoutBlocking: with the writer stuck and the queue full, Enqueue returns at once and says the
// record was dropped; it never waits for room. The records accepted before it are still written, and Close reports the
// lost call and its attempts.
func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(s, Config{QueueSize: 2, BatchSize: 1, FlushInterval: time.Millisecond})
	records := calls(6)
	enqueueAll(t, r, records[:1])
	eventually(t, "the writer to take the first record", func() bool { depth, _ := r.AlertSample(); return len(r.queue) == 0 && depth == 1 })
	enqueueAll(t, r, records[1:3]) // two fill the queue behind the stuck write
	eventually(t, "the queue to fill", func() bool { return len(r.queue) == 2 })

	done := make(chan error, 1)
	go func() { done <- r.Enqueue(records[3]) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("Enqueue on a full queue = %v, want ErrQueueFull", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	if depth, lost := r.AlertSample(); depth != 3 || lost != 1 {
		t.Fatalf("alert sample depth=%d lost=%d", depth, lost)
	}
	s.open()
	err := r.Close(t.Context())
	if err == nil || !strings.Contains(err.Error(), "1 call records") || !strings.Contains(err.Error(), "2 attempts") {
		t.Errorf("Close = %v, want it to report the one dropped call and its two attempts", err)
	}
	got, _ := s.stored()
	if !slices.Equal(got, ids(records[:3])) {
		t.Errorf("stored %v, want the three records queued before the drop", got)
	}
	if got := s.attemptsStored(); got != 6 {
		t.Errorf("%d attempts stored, want 6", got)
	}
}

// TestCloseDrainsTheQueue: every queued call and its attempts are written before Close returns, although the interval
// is long and the batch not full, and Enqueue refuses afterwards.
func TestCloseDrainsTheQueue(t *testing.T) {
	t.Parallel()
	s := &sink{}
	r := recorderOf(s, Config{QueueSize: 100, BatchSize: 1000, FlushInterval: time.Hour})
	records := calls(7)
	enqueueAll(t, r, records)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("stored %d of %d records after Close", len(got), len(records))
	}
	if got := s.attemptsStored(); got != 2*len(records) {
		t.Errorf("%d attempts stored after Close, want %d", got, 2*len(records))
	}
	if err := r.Enqueue(records[0]); !errors.Is(err, ErrClosed) {
		t.Errorf("Enqueue after Close = %v, want ErrClosed", err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}

// TestFailedWritesRetryUntilTheyWork: a database that is down for a while loses nothing, keeps the batch order, and
// writes each call and attempt once.
func TestFailedWritesRetryUntilTheyWork(t *testing.T) {
	t.Parallel()
	s := &sink{failing: 4}
	// One batch of exactly the three records, so the four failures and the success are the same write: a short
	// interval would let a loaded machine split them, and each write would then fail and retry on its own.
	r := recorderOf(s, Config{
		QueueSize: 10, BatchSize: 3, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: 4 * time.Millisecond,
	})
	records := calls(3)
	enqueueAll(t, r, records)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("stored %v, want %v exactly once", got, ids(records))
	}
	if got := s.attemptsStored(); got != 2*len(records) {
		t.Errorf("%d attempts stored, want %d exactly once", got, 2*len(records))
	}
	if n := s.writes(); n != 5 {
		t.Errorf("%d write attempts, want 4 failures and 1 success", n)
	}
}

// TestCloseGivesUpWhenItsTimeRunsOut: a database that stays down cannot hold shutdown past its deadline; Close says
// how many calls and attempts were lost.
func TestCloseGivesUpWhenItsTimeRunsOut(t *testing.T) {
	t.Parallel()
	s := &sink{failing: 1 << 30}
	r := recorderOf(s, Config{
		QueueSize: 10, BatchSize: 2, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
		RetryMax: 2 * time.Millisecond,
	})
	enqueueAll(t, r, calls(5))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := r.Close(ctx)
	if err == nil {
		t.Fatal("Close reported success with every write failing")
	}
	if want := "5 call records"; !strings.Contains(err.Error(), want) {
		t.Errorf("Close = %q, want it to count %s", err, want)
	}
	if want := "10 attempts"; !strings.Contains(err.Error(), want) {
		t.Errorf("Close = %q, want it to count %s", err, want)
	}
}

// TestMalformedRecordDoesNotBlockTheRest: a record the database refuses is found by splitting the batch, logged and
// left out with its attempts; every other call, before and after it, is written.
func TestMalformedRecordDoesNotBlockTheRest(t *testing.T) {
	t.Parallel()
	records := calls(6)
	s := &sink{poison: records[2].ID.String()}
	r := recorderOf(s, Config{
		QueueSize: 10, BatchSize: 6, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond,
	})
	enqueueAll(t, r, records)
	err := r.Close(t.Context())
	if err == nil {
		t.Error("Close hid the lost record")
	}
	got, _ := s.stored()
	want := slices.Delete(slices.Clone(ids(records)), 2, 3)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("stored %d records, want the other 5", len(got))
	}
	if n := s.attemptsStored(); n != 10 {
		t.Errorf("%d attempts stored, want the 10 of the good calls", n)
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

// TestBadRowDoesNotBlockTheQueueThroughTheRealEncoder: a row the encoder refuses (invalid UTF-8) between good ones is
// found by splitting, left out with its attempts and counted; the good rows reach the database. The row is put on the
// queue directly, because Enqueue rejects such text at the boundary (TestEnqueueRejectsText).
func TestBadRowDoesNotBlockTheQueueThroughTheRealEncoder(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	r := newRecorder(t.Context(), Config{
		QueueSize: 10, BatchSize: 3, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond, WriteTimeout: 5 * time.Second,
	}, dbWriter(db, platformScope), isBadRow, quiet)
	var records []queued
	for _, rec := range []CallRecord{record(a, now), record(a, now.Add(-time.Second)), record(a, now.Add(-2*time.Second))} {
		call, attempts, err := newRows(rec)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, queued{call: call, attempts: attempts})
	}
	records[1].call.ClientApp = "\xff\xfe"
	for _, q := range records {
		r.queue <- q
	}
	if err := r.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "1 call records") {
		t.Errorf("Close = %v, want it to report the one lost row", err)
	}
	if got := list(t, db, accountScope(a), day(a)); len(got) != 2 {
		t.Errorf("%d rows stored, want the 2 good ones", len(got))
	}
	if n := attemptsOf(t, db, a); n != 4 {
		t.Errorf("%d attempts stored, want the 4 of the good calls", n)
	}
}

// TestBadAttemptRollsBackItsCallAndIsIsolated: the attempt insert refuses a 65-character error_kind. Because the call
// and its attempts share one transaction, the call insert rolls back too; splitting the batch then leaves out exactly
// that call and its attempts, while both neighbours are written whole.
func TestBadAttemptRollsBackItsCallAndIsIsolated(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	before, bad, after := record(a, now), record(a, now.Add(-time.Second)), record(a, now.Add(-2*time.Second))
	bad.Attempts[0].ErrorKind = strings.Repeat("x", 65)
	r := newRecorder(t.Context(), Config{
		QueueSize: 10, BatchSize: 10, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond, WriteTimeout: 5 * time.Second,
	}, dbWriter(db, platformScope), isBadRow, quiet)
	enqueueAll(t, r, []CallRecord{before, bad, after})
	err := r.Close(t.Context())
	if err == nil || err.Error() != "observability: 1 call records were not written (2 attempts lost)" {
		t.Errorf("Close = %v, want exactly one call and its two attempts lost", err)
	}

	count := func(table, column string, id uuid.UUID) int {
		t.Helper()
		var n int
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT count(*) FROM observability."+table+" WHERE "+column+" = $1", id).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, rec := range []CallRecord{before, after} {
		if n := count("call_log", "id", rec.ID); n != 1 {
			t.Errorf("the good call %s has %d rows, want 1", rec.ID, n)
		}
		if n := count("call_attempt", "request_id", rec.ID); n != 2 {
			t.Errorf("the good call %s has %d attempts, want 2", rec.ID, n)
		}
	}
	if n := count("call_log", "id", bad.ID); n != 0 {
		t.Errorf("the refused call has %d rows, want 0 after the rollback", n)
	}
	if n := count("call_attempt", "request_id", bad.ID); n != 0 {
		t.Errorf("the refused call has %d attempts, want 0 after the rollback", n)
	}
}

// TestEnqueueRejectsText: text that cannot be stored is refused when it is queued, so it never reaches a batch, and
// the same holds for an attempt's text.
func TestEnqueueRejectsText(t *testing.T) {
	t.Parallel()
	r := recorderOf(&sink{}, Config{QueueSize: 1, BatchSize: 1, FlushInterval: time.Millisecond})
	defer r.Close(t.Context()) //nolint:errcheck // nothing was queued
	for name, edit := range map[string]func(*CallRecord){
		"bad UTF-8 in the client app":        func(c *CallRecord) { c.ClientApp = "\xff" },
		"a NUL in the model":                 func(c *CallRecord) { c.Model = "a\x00b" },
		"bad UTF-8 in an attempt's egress":   func(c *CallRecord) { c.Attempts[0].Egress = "\xff" },
		"a NUL in an attempt's error kind":   func(c *CallRecord) { c.Attempts[1].ErrorKind = "a\x00b" },
		"an attempt with an unknown outcome": func(c *CallRecord) { c.Attempts[0].Outcome = "maybe" },
	} {
		rec := record(uuid.NewV7(), now)
		edit(&rec)
		if err := r.Enqueue(rec); err == nil {
			t.Errorf("%s was queued", name)
		}
	}
}

// TestEnqueueRejectsAnIncompleteAttempt: the logs cannot say what happened without an endpoint, a supplier, times or
// a sequence that runs from one; such a record is refused before it reaches the queue.
func TestEnqueueRejectsAnIncompleteAttempt(t *testing.T) {
	t.Parallel()
	r := recorderOf(&sink{}, Config{QueueSize: 1, BatchSize: 1, FlushInterval: time.Millisecond})
	defer r.Close(t.Context()) //nolint:errcheck // nothing was queued
	for name, edit := range map[string]func(*CallRecord){
		"a nil endpoint":      func(c *CallRecord) { c.Attempts[0].Endpoint = uuid.Nil() },
		"a nil supplier":      func(c *CallRecord) { c.Attempts[1].Supplier = uuid.Nil() },
		"no start time":       func(c *CallRecord) { c.Attempts[0].StartedAt = time.Time{} },
		"finish before start": func(c *CallRecord) { c.Attempts[1].FinishedAt = c.Attempts[1].StartedAt.Add(-time.Second) },
		"a sequence gap":      func(c *CallRecord) { c.Attempts[1].Seq = 3 },
		"four attempts":       func(c *CallRecord) { c.Attempts = append(c.Attempts, c.Attempts[1], c.Attempts[1]) },
	} {
		rec := record(uuid.NewV7(), now)
		edit(&rec)
		if err := r.Enqueue(rec); err == nil {
			t.Errorf("%s was queued", name)
		}
	}
}

// TestHungWriteTimesOutAndRecovers: a write that never answers is cut off after WriteTimeout and retried, so the writer
// is not held for good by one dead connection.
func TestHungWriteTimesOutAndRecovers(t *testing.T) {
	t.Parallel()
	hung := make(chan struct{})
	var once sync.Once
	s := &sink{}
	write := func(ctx context.Context, batch []queued) error {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			close(hung)
			<-ctx.Done()
			return ctx.Err()
		}
		return s.write(ctx, batch)
	}
	r := newRecorder(t.Context(), Config{
		QueueSize: 4, BatchSize: 2, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
		RetryMax: 2 * time.Millisecond, WriteTimeout: 30 * time.Millisecond,
	}, write, func(error) bool { return false }, quiet)
	records := calls(2)
	enqueueAll(t, r, records)
	<-hung
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("stored %v after the hung write, want %v", got, ids(records))
	}
	if n := s.attemptsStored(); n != 4 {
		t.Errorf("%d attempts stored after the hung write, want 4", n)
	}
}
