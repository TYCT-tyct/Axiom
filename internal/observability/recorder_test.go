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
)

// sink stands in for the database: it keeps what is written, in order, and can be told to block or fail.
type sink struct {
	mu      sync.Mutex
	batches [][]string // request IDs per write that succeeded
	calls   int
	failing int           // the next writes fail, this many
	gate    chan struct{} // when set, a write waits for it to close
	poison  string        // a write containing this request ID fails with a data error
}

var (
	errDown   = errors.New("database unavailable")
	errPoison = errors.New("malformed row")
)

func (s *sink) write(ctx context.Context, rows []row) error {
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
	ids := make([]string, len(rows))
	for i, r := range rows {
		if r.ID == s.poison {
			return errPoison
		}
		ids[i] = r.ID
	}
	s.batches = append(s.batches, ids)
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

func (s *sink) attempts() int {
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
		if err := r.Enqueue(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBatchesFlushOnSizeAndOnTime: a full batch is written at once, however long the interval; a partial one waits
// for the interval and is then written alone.
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
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	s = &sink{}
	r = recorderOf(s, Config{QueueSize: 100, BatchSize: 100, FlushInterval: 30 * time.Millisecond})
	one := calls(1)
	enqueueAll(t, r, one)
	if got, _ := s.stored(); len(got) != 0 {
		t.Error("a partial batch was written before its interval")
	}
	eventually(t, "the interval to flush a partial batch", func() bool { got, _ := s.stored(); return len(got) == 1 })
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestFullQueueWaitsAndNeverDrops: with the writer stuck and the queue full, Enqueue blocks instead of returning or
// discarding; once the writer moves, every record is written, in order.
func TestFullQueueWaitsAndNeverDrops(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(s, Config{QueueSize: 2, BatchSize: 1, FlushInterval: time.Millisecond})
	records := calls(10)
	returned := make(chan int, len(records))
	go func() {
		for i, rec := range records {
			if err := r.Enqueue(t.Context(), rec); err != nil {
				t.Error(err)
			}
			returned <- i
		}
	}()
	// One record is in the stuck write, two fill the queue; the fourth Enqueue must not return.
	eventually(t, "the queue to fill", func() bool { return len(returned) >= 3 })
	time.Sleep(50 * time.Millisecond)
	if n := len(returned); n != 3 { // one in the stuck write, two in the queue
		t.Fatalf("%d Enqueue calls returned with the queue full and the writer stuck, want 3", n)
	}
	s.open()
	eventually(t, "every Enqueue to return", func() bool { return len(returned) == len(records) })
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.stored()
	if !slices.Equal(got, ids(records)) {
		t.Errorf("stored %d records, want all %d in order", len(got), len(records))
	}
}

// TestEnqueueGivesUpOnItsContext: a caller whose context ends while the queue is full is told, and its record is
// not queued.
func TestEnqueueGivesUpOnItsContext(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(s, Config{QueueSize: 1, BatchSize: 1, FlushInterval: time.Millisecond})
	records := calls(3)
	enqueueAll(t, r, records[:1])
	eventually(t, "the writer to take the first record", func() bool { return len(r.queue) == 0 })
	enqueueAll(t, r, records[1:2]) // fills the queue behind the stuck write
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := r.Enqueue(ctx, records[2]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Enqueue = %v, want the context's error", err)
	}
	s.open()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records[:2])) {
		t.Errorf("stored %v, want only the two queued records", got)
	}
}

// TestCloseDrainsTheQueue: every queued record is written before Close returns, although the interval is long and the
// batch not full, and Enqueue refuses afterwards.
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
	if err := r.Enqueue(t.Context(), records[0]); !errors.Is(err, ErrClosed) {
		t.Errorf("Enqueue after Close = %v, want ErrClosed", err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}

// TestFailedWritesRetryUntilTheyWork: a database that is down for a while loses nothing and writes each record once.
func TestFailedWritesRetryUntilTheyWork(t *testing.T) {
	t.Parallel()
	s := &sink{failing: 4}
	r := recorderOf(s, Config{
		QueueSize: 10, BatchSize: 10, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
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
	if n := s.attempts(); n != 5 {
		t.Errorf("%d write attempts, want 4 failures and 1 success", n)
	}
}

// TestCloseGivesUpWhenItsTimeRunsOut: a database that stays down cannot hold shutdown past its deadline; Close says
// how many records were lost.
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
}

// TestMalformedRecordDoesNotBlockTheRest: a row the database refuses is found by splitting the batch, logged and
// left out; every other record, before and after it, is written.
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
}

// TestRecorderWritesThroughTheDatabase: the whole path, on a real database, in the platform scope.
func TestRecorderWritesThroughTheDatabase(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	records := []CallRecord{record(a, now), record(a, now.Add(-time.Second)), record(uuid.NewV7(), now)}
	store(t, db, records...)
	if got := list(t, db, accountScope(a), wholeDay); len(got) != 2 {
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
	r, err := newRow(rec)
	if err != nil || len(r.EndUserHash) != 64 || strings.Contains(r.EndUserHash, "user-1") {
		t.Errorf("row hash %q (%v)", r.EndUserHash, err)
	}
}
