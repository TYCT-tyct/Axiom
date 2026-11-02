package observability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// recorderOf starts a recorder on a sink. Every test gets its own spill directory; Capacity defaults to 100 for tests
// that want only the batching behavior.
func recorderOf(t *testing.T, s *sink, cfg Config) *Recorder {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = 100
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase, cfg.RetryMax = time.Millisecond, 4*time.Millisecond
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	r, err := newRecorder(context.Background(), cfg, s.write, func(err error) bool { return errors.Is(err, errPoison) }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crash(r) })
	return r
}

// recorderOfDB is recorderOf on the real database.
func recorderOfDB(t *testing.T, db *pg.DB, cfg Config) *Recorder {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = 100
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase, cfg.RetryMax = time.Millisecond, 4*time.Millisecond
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	r, err := newRecorder(context.Background(), cfg, dbWriter(db, platformScope), isBadRow, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crash(r) })
	return r
}

// crash stops a recorder the way a killed process would: no drain and no close, only the files on disk. It is also
// the cleanup for a test that already closed the recorder, where everything is a no-op.
func crash(r *Recorder) {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.spill.stopNow()
	r.giveUp()
	<-r.Done()
}

func calls(n int) []CallRecord {
	out := make([]CallRecord, n)
	for i := range out {
		out[i] = record(uuid.NewV7(), now)
	}
	return out
}

// onlySegment returns the one segment file in dir, ignoring the lock and checkpoint files.
func onlySegment(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var segments []string
	for _, entry := range entries {
		if _, ok := spillSeq(entry.Name()); ok {
			segments = append(segments, filepath.Join(dir, entry.Name()))
		}
	}
	if len(segments) != 1 {
		t.Fatalf("%d segments, want 1: %v", len(segments), segments)
	}
	return segments[0]
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
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
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
	r := recorderOf(t, s, Config{Capacity: 100, BatchSize: 3, FlushInterval: time.Hour})
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
	r = recorderOf(t, s, Config{Capacity: 100, BatchSize: 100, FlushInterval: time.Hour})
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
	r = recorderOf(t, s, Config{Capacity: 100, BatchSize: 100, FlushInterval: 30 * time.Millisecond})
	enqueueAll(t, r, one)
	eventually(t, "the interval to flush a partial batch", func() bool { got, _ := s.stored(); return len(got) == 1 })
	if got := s.attemptsStored(); got != 2 {
		t.Errorf("%d attempts written with the one call, want 2", got)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestFullSpillRefusesWithoutBlocking: with the writer stuck and every reservation taken, Admit and Enqueue return at
// once and lose no record; the two calls already admitted are still written when the writer recovers.
func TestFullSpillRefusesWithoutBlocking(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(t, s, Config{Capacity: 2, BatchSize: 1, FlushInterval: time.Millisecond})
	records := calls(4)
	enqueueAll(t, r, records[:1])
	eventually(t, "the writer to take the first record", func() bool { return r.spill.pendingCount() == 1 })
	enqueueAll(t, r, records[1:2])

	done := make(chan error, 1)
	go func() { done <- r.Enqueue(records[2]) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBacklog) {
			t.Fatalf("Enqueue with every reservation taken = %v, want ErrBacklog", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full spill")
	}
	if lost := r.lost.Load(); lost != 0 {
		t.Fatalf("%d records lost when admission refused one; refusal must not drop an existing record", lost)
	}
	if got := r.spill.pendingCount(); got != 2 {
		t.Fatalf("%d records in the spill, want 2", got)
	}

	s.open()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records[:2])) {
		t.Errorf("stored %v, want the two records accepted before the refusal", got)
	}
}

// TestReservationRelease: a call that ends without a record gives its room back; a committed record keeps the room
// until the writer acknowledges it.
func TestReservationRelease(t *testing.T) {
	t.Parallel()
	s := &sink{gate: make(chan struct{})}
	r := recorderOf(t, s, Config{Capacity: 1, BatchSize: 1, FlushInterval: time.Hour})
	first, err := r.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Admit(); !errors.Is(err, ErrBacklog) {
		t.Fatalf("second Admit = %v, want ErrBacklog", err)
	}
	first.Release()
	second, err := r.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(record(uuid.NewV7(), now)); err != nil {
		t.Fatal(err)
	}
	second.Release() // after Commit this must not free the writer's room
	if _, err := r.Admit(); !errors.Is(err, ErrBacklog) {
		t.Fatalf("Admit after a committed Release = %v, want ErrBacklog until the write", err)
	}
	s.open()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestCloseDrainsTheSpill: every call and its attempts are written before Close returns, although the interval is
// long and the batch not full, and Enqueue refuses afterwards.
func TestCloseDrainsTheSpill(t *testing.T) {
	t.Parallel()
	s := &sink{}
	r := recorderOf(t, s, Config{Capacity: 100, BatchSize: 1000, FlushInterval: time.Hour})
	records := calls(7)
	enqueueAll(t, r, records)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.stored(); !slices.Equal(got, ids(records)) {
		t.Errorf("stored %v after Close, want %v", got, ids(records))
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
	r := recorderOf(t, s, Config{
		Capacity: 10, BatchSize: 3, FlushInterval: time.Hour, RetryBase: time.Millisecond,
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

// TestCloseDeadlineLeavesTheSpill: a database that stays down cannot hold shutdown past its deadline; Close says how
// many calls remain, and the next process replays them.
func TestCloseDeadlineLeavesTheSpill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := &sink{failing: 1 << 30}
	r := recorderOf(t, s, Config{
		Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Millisecond, RetryBase: time.Millisecond,
		RetryMax: 2 * time.Millisecond,
	})
	records := calls(5)
	enqueueAll(t, r, records)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := r.Close(ctx)
	if !errors.Is(err, ErrPending) || !strings.Contains(err.Error(), "5 calls remain") {
		t.Fatalf("Close = %v, want ErrPending with 5 calls", err)
	}

	// The process gets a healthy database: opening the same directory replays the five records, in order. Its writer
	// waits at the gate until the count is read, or a first batch could already be acknowledged.
	healthy := &sink{gate: make(chan struct{})}
	next := recorderOf(t, healthy, Config{
		Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Hour,
	})
	if got := next.spill.pendingCount(); got != 5 {
		t.Fatalf("the next recorder found %d records, want 5", got)
	}
	healthy.open()
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestDataErrorStopsAdmissionWithoutDropping: a record the database refuses (the fake sink's poison) is a bug or an
// impossible value, not a reason to serve later calls without a log. Everything stays in the spill and admission
// refuses until an operator fixes it.
func TestDataErrorStopsAdmissionWithoutDropping(t *testing.T) {
	t.Parallel()
	records := calls(3)
	s := &sink{poison: records[1].ID.String()}
	r := recorderOf(t, s, Config{
		Capacity: 10, BatchSize: 3, FlushInterval: time.Hour, RetryBase: time.Millisecond,
		RetryMax: time.Millisecond,
	})
	enqueueAll(t, r, records)
	eventually(t, "the data error to stop the writer", func() bool { return r.broken.Load() })
	if _, err := r.Admit(); !errors.Is(err, ErrBroken) {
		t.Fatalf("Admit after a data error = %v, want ErrBroken", err)
	}
	if got := r.spill.pendingCount(); got != 3 {
		t.Fatalf("%d records in the spill, want 3: a bad row must not be dropped", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, ErrPending) || !strings.Contains(err.Error(), "3 calls remain") {
		t.Fatalf("Close = %v, want ErrPending with 3 calls", err)
	}
}

// TestCrashReplaysTheSpillInOrder: records appended before the process dies are written by the next process, in the
// order they were appended, with their attempts.
func TestCrashReplaysTheSpillInOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := &sink{failing: 1 << 30}
	r := recorderOf(t, first, Config{
		Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Hour,
	})
	records := calls(5)
	enqueueAll(t, r, records)
	eventually(t, "all five records in the spill", func() bool { return r.spill.pendingCount() == 5 })
	crash(r)

	nextSink := &sink{}
	next := recorderOf(t, nextSink, Config{
		Dir: dir, Capacity: 10, BatchSize: 2, FlushInterval: time.Hour,
	})
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := nextSink.stored()
	if !slices.Equal(got, ids(records)) {
		t.Errorf("replayed %v, want %v in order", got, ids(records))
	}
	if n := nextSink.attemptsStored(); n != 2*len(records) {
		t.Errorf("%d attempts replayed, want %d", n, 2*len(records))
	}
}

// TestTornTailIsDropped: a crash in the middle of the last append leaves a partial record, which the next open
// truncates; the complete records before it still replay.
func TestTornTailIsDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := recorderOf(t, &sink{failing: 1 << 30}, Config{
		Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Hour,
	})
	records := calls(2)
	enqueueAll(t, r, records)
	eventually(t, "both records in the spill", func() bool { return r.spill.pendingCount() == 2 })
	crash(r)

	path := onlySegment(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-3); err != nil {
		t.Fatal(err)
	}

	nextSink := &sink{}
	next := recorderOf(t, nextSink, Config{Dir: dir, Capacity: 10, BatchSize: 10, FlushInterval: time.Hour})
	if got := next.spill.pendingCount(); got != 1 {
		t.Fatalf("the torn tail left %d records, want 1", got)
	}
	if err := next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := nextSink.stored(); !slices.Equal(got, ids(records[:1])) {
		t.Errorf("replayed %v, want only the complete first record", got)
	}
}

// TestTornSegmentHeaderIsDropped: a crash while a segment's header was being written leaves a prefix with no record
// behind it; the next open drops it instead of refusing to start over something no caller was ever told was kept.
func TestTornSegmentHeaderIsDropped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "call-0000000000000001.wal"), []byte("AXIOM-WAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := openSpill(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.finish()
	if got := sp.pendingCount(); got != 0 {
		t.Errorf("%d records, want none from a torn header", got)
	}
}
