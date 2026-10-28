package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

var (
	// ErrClosed is Enqueue's answer once Close has begun.
	ErrClosed = errors.New("observability: the recorder is closed")
	// ErrQueueFull is Enqueue's answer when the queue is full: the record and its attempts were dropped and counted.
	// A caller on the call path may ignore it; it is never a reason to fail or slow the call.
	ErrQueueFull = errors.New("observability: the record queue is full; the record was dropped")
)

// Config sizes the Recorder. Production values come from cmd/axiom; tests use small ones.
type Config struct {
	QueueSize     int           // records waiting to be written; when full, Enqueue drops the new one
	BatchSize     int           // rows per write, at most
	FlushInterval time.Duration // how long the first record of a batch may wait for company
	RetryBase     time.Duration // the first wait after a failed write; it doubles up to RetryMax
	RetryMax      time.Duration
	WriteTimeout  time.Duration // one write attempt; after it the batch is retried (the outcome of a timed-out write is unknown)
	Meter         metric.Meter  // nil for none
}

// queued is one call with its attempts, the unit a caller hands over and the writer keeps or loses whole.
type queued struct {
	call     row
	attempts []attemptRow
}

// Recorder writes call and attempt records in the background. Enqueue is the only call on a request's path: it hands
// the record to a bounded queue without waiting. When the queue is full it drops the new record and counts it, so a
// slow database can never slow or fail a call (README「写入」 and docs/domains/observability.md「写入路径」).
type Recorder struct {
	cfg    Config
	write  func(context.Context, []queued) error
	fatal  func(error) bool // the error says the records themselves are bad: retrying cannot help
	logger *slog.Logger

	queue chan queued

	mu      sync.Mutex // guards closing and a send, so Close can close the queue without racing one
	closing bool

	giveUp       context.CancelFunc // ends the writer's context: Close ran out of time waiting for the queue to drain
	done         chan struct{}      // closed when the writer has exited
	inBatch      atomic.Int64
	lost         atomic.Int64 // calls not written: dropped at Enqueue or given up on by the writer
	lostAttempts atomic.Int64 // their attempts

	written, attemptsWritten, retries, lostMetric, attemptsLostMetric metric.Int64Counter
}

// NewRecorder starts a Recorder that writes through db in scope, the platform scope: one batch holds the records of
// many accounts. The writer takes ctx's values, such as the tracing provider, but not its end: only Close stops it.
func NewRecorder(ctx context.Context, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) *Recorder {
	return newRecorder(ctx, cfg, dbWriter(db, scope), isBadRow, logger)
}

// dbWriter writes a batch in one transaction in scope: the calls first, then their attempts, so a retried batch can
// never leave attempts whose call is missing.
func dbWriter(db *pg.DB, scope pg.Scope) func(context.Context, []queued) error {
	return func(ctx context.Context, batch []queued) error {
		calls, attempts, err := marshalQueued(batch)
		if err != nil {
			return err
		}
		return db.Tx(ctx, scope, func(tx pg.Tx) error {
			queries := store.New()
			if _, err := queries.InsertCallLogs(ctx, tx, calls); err != nil {
				return err
			}
			if attempts != nil {
				if _, err := queries.InsertCallAttempts(ctx, tx, attempts); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

// errEncoding marks a batch that cannot be encoded: the same rows fail the same way every time.
var errEncoding = errors.New("encode call records")

// isBadRow reports a failure that retrying the same rows cannot mend: the database refused a value, or encoding did.
func isBadRow(err error) bool { return pg.IsDataError(err) || errors.Is(err, errEncoding) }

// marshalQueued writes the batch as the two JSON arrays the insert statements take, one for calls and one for
// attempts. It returns a nil attempts array when the batch has none.
func marshalQueued(batch []queued) (calls, attempts []byte, err error) {
	callRows := make([]row, len(batch))
	var attemptRows []attemptRow
	for i, q := range batch {
		callRows[i] = q.call
		attemptRows = append(attemptRows, q.attempts...)
	}
	calls, err = jsonx.MarshalCanonical(callRows)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (%d calls, %d attempts): %w", errEncoding, len(callRows), len(attemptRows), err)
	}
	if len(attemptRows) == 0 {
		return calls, nil, nil
	}
	attempts, err = jsonx.MarshalCanonical(attemptRows)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (%d calls, %d attempts): %w", errEncoding, len(callRows), len(attemptRows), err)
	}
	return calls, attempts, nil
}

func newRecorder(ctx context.Context, cfg Config, write func(context.Context, []queued) error, fatal func(error) bool, logger *slog.Logger) *Recorder {
	if cfg.QueueSize <= 0 || cfg.BatchSize <= 0 || cfg.FlushInterval <= 0 || cfg.RetryBase <= 0 || cfg.RetryMax < cfg.RetryBase ||
		cfg.WriteTimeout <= 0 {
		panic("observability: Config needs positive sizes and durations") // a constant of cmd/axiom, fixed at build time
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("observability")
	}
	run, giveUp := context.WithCancel(context.WithoutCancel(ctx))
	r := &Recorder{
		cfg: cfg, write: write, fatal: fatal, logger: logger,
		queue: make(chan queued, cfg.QueueSize), giveUp: giveUp, done: make(chan struct{}),
	}
	r.written = counter(cfg.Meter, "axiom.observability.records.written", "Call records stored.")
	r.attemptsWritten = counter(cfg.Meter, "axiom.observability.attempts.written", "Attempt records stored.")
	r.retries = counter(cfg.Meter, "axiom.observability.write.retries", "Batch writes that failed and were tried again.")
	r.lostMetric = counter(cfg.Meter, "axiom.observability.records.lost",
		"Call records not written, by reason: queue_full, malformed or shutdown.")
	r.attemptsLostMetric = counter(cfg.Meter, "axiom.observability.attempts.lost",
		"Attempt records not written, with their call and for the same reasons.")
	_, err := cfg.Meter.Int64ObservableGauge("axiom.observability.queue.depth",
		metric.WithDescription("Call records waiting to be written."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(r.queue)))
			return nil
		}))
	if err != nil {
		panic(err) // the instrument names are constants
	}
	go r.writer(run)
	return r
}

func counter(m metric.Meter, name, description string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		panic(err) // the instrument names are constants
	}
	return c
}

// Enqueue queues rec and its attempts for writing, without waiting. It returns ErrQueueFull when the queue has no
// room, after counting the dropped call and attempts: a record is never worth slowing a call for. It returns
// ErrClosed once Close has begun, and an error for a record that could never be written.
func (r *Recorder) Enqueue(rec CallRecord) error {
	call, attempts, err := newRows(rec)
	if err != nil {
		return fmt.Errorf("call record %s: %w", rec.ID, err)
	}
	q := queued{call: call, attempts: attempts}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return ErrClosed
	}
	select {
	case r.queue <- q:
		r.mu.Unlock()
		return nil
	default:
		// Counted under the lock so Close, which also takes it, cannot finish before the loss is in the report.
		r.lose(context.Background(), q, "queue_full")
		r.mu.Unlock()
		return ErrQueueFull
	}
}

// Close stops accepting records and writes what is queued, retrying failures until ctx ends. It returns nil when
// everything was written; otherwise it logs and returns how many records were lost, including those dropped while the
// queue was full.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closing {
		r.closing = true
		close(r.queue) // no send can be in flight: Enqueue holds r.mu for its non-blocking send
	}
	r.mu.Unlock()
	select {
	case <-r.done:
	case <-ctx.Done():
		r.giveUp()
		<-r.done
	}
	r.giveUp()
	calls, attempts := r.lost.Load(), r.lostAttempts.Load()
	if calls > 0 {
		return fmt.Errorf("observability: %d call records were not written (%d attempts lost)", calls, attempts)
	}
	return nil
}

// Done is closed once the writer goroutine has exited, which Close waits for before it returns. A test reads it to
// know that the Recorder it was given left nothing running.
func (r *Recorder) Done() <-chan struct{} { return r.done }

// writer collects batches off the queue and writes them one at a time, in order.
func (r *Recorder) writer(ctx context.Context) {
	defer close(r.done)
	for {
		batch, open := r.collect()
		if len(batch) > 0 {
			r.inBatch.Store(int64(len(batch)))
			r.flush(ctx, batch)
			r.inBatch.Store(0)
		}
		if !open {
			return
		}
	}
}

// collect takes up to BatchSize records, waiting for the first and then at most FlushInterval for the rest. It reports
// false once the queue is closed and empty.
func (r *Recorder) collect() (batch []queued, open bool) {
	first, ok := <-r.queue
	if !ok {
		return nil, false
	}
	batch = append(make([]queued, 0, r.cfg.BatchSize), first)
	timer := time.NewTimer(r.cfg.FlushInterval)
	defer timer.Stop()
	for len(batch) < r.cfg.BatchSize {
		select {
		case next, ok := <-r.queue:
			if !ok {
				return batch, false
			}
			batch = append(batch, next)
		case <-timer.C:
			return batch, true
		}
	}
	return batch, true
}

// flush writes batch, retrying with a growing wait until it succeeds or Close gives up. A batch the database calls
// malformed is split to find the record at fault, which is logged and left out: one bad record must not stop every
// later one.
func (r *Recorder) flush(ctx context.Context, batch []queued) {
	delay := r.cfg.RetryBase
	for {
		err := r.attempt(ctx, batch)
		if err == nil {
			r.written.Add(ctx, int64(len(batch)))
			var attempts int64
			for _, q := range batch {
				attempts += int64(len(q.attempts))
			}
			r.attemptsWritten.Add(ctx, attempts)
			return
		}
		switch {
		case ctx.Err() != nil:
			r.abandon(ctx, batch, err)
			return
		case r.fatal(err) && len(batch) > 1:
			mid := len(batch) / 2
			r.flush(ctx, batch[:mid])
			r.flush(ctx, batch[mid:])
			return
		case r.fatal(err):
			r.logger.Error("call record refused by the database", "request_id", batch[0].call.ID,
				"attempts", len(batch[0].attempts), "err", err)
			r.lose(ctx, batch[0], "malformed")
			return
		}
		r.retries.Add(ctx, 1)
		r.logger.Warn("writing call records failed, retrying", "records", len(batch), "wait", delay, "err", err)
		select {
		case <-time.After(delay/2 + rand.N(delay/2+1)): //nolint:gosec // jitter, not a secret
		case <-ctx.Done():
		}
		delay = min(delay*2, r.cfg.RetryMax)
	}
}

// attempt is one write, cut off after WriteTimeout: a database that stops answering must not hold the only writer.
func (r *Recorder) attempt(ctx context.Context, batch []queued) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	return r.write(ctx, batch)
}

// abandon counts a batch that shutdown could not wait for.
func (r *Recorder) abandon(ctx context.Context, batch []queued, err error) {
	var attempts int
	for _, q := range batch {
		attempts += len(q.attempts)
	}
	r.logger.Error("call records lost at shutdown", "records", len(batch), "attempts", attempts, "err", err)
	for _, q := range batch {
		r.lose(ctx, q, "shutdown")
	}
}

// lose counts one call and its attempts as not written, for the reason given.
func (r *Recorder) lose(ctx context.Context, q queued, reason string) {
	r.lost.Add(1)
	r.lostAttempts.Add(int64(len(q.attempts)))
	r.lostMetric.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	r.attemptsLostMetric.Add(ctx, int64(len(q.attempts)), metric.WithAttributes(attribute.String("reason", reason)))
}

// AlertSample reads bounded queue depth and cumulative losses without joining the writer's lock.
func (r *Recorder) AlertSample() (int, int64) {
	return len(r.queue) + int(r.inBatch.Load()), r.lost.Load()
}
