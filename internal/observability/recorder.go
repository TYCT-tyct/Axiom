package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// ErrClosed is Enqueue's answer once Close has begun.
var ErrClosed = errors.New("observability: the recorder is closed")

// Config sizes the Recorder. Production values come from cmd/axiom; tests use small ones.
type Config struct {
	QueueSize     int           // records waiting to be written; Enqueue waits when it is full
	BatchSize     int           // rows per write, at most
	FlushInterval time.Duration // how long the first record of a batch may wait for company
	RetryBase     time.Duration // the first wait after a failed write; it doubles up to RetryMax
	RetryMax      time.Duration
	WriteTimeout  time.Duration // one write attempt; after it the batch is retried (the outcome of a timed-out write is unknown)
	Meter         metric.Meter  // nil for none
}

// Recorder writes call records in the background. Enqueue is the only call on a request's path: it hands the record
// to a bounded queue and returns, and when the queue is full it waits, so a slow database slows requests down to the
// node's in-flight limit instead of losing records (docs/domains/observability.md「写入路径」).
type Recorder struct {
	cfg    Config
	write  func(context.Context, []row) error
	fatal  func(error) bool // the error says the rows themselves are bad: retrying cannot help
	logger *slog.Logger

	queue chan row
	stop  chan struct{} // closed by Close: Enqueue refuses, and a blocked Enqueue gives up

	mu      sync.Mutex // guards closing, which is set before Close waits for senders
	closing bool
	senders sync.WaitGroup

	giveUp context.CancelFunc // ends the writer's context: Close ran out of time waiting for the queue to drain
	done   chan struct{}      // closed when the writer has exited
	lost   int                // the writer's count of rows given up on; read after done

	written, retries, dropped metric.Int64Counter
}

// NewRecorder starts a Recorder that writes through db in scope, the platform scope: one batch holds the rows of
// many accounts. The writer takes ctx's values, such as the tracing provider, but not its end: only Close stops it.
func NewRecorder(ctx context.Context, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) *Recorder {
	return newRecorder(ctx, cfg, dbWriter(db, scope), isBadRow, logger)
}

// dbWriter writes a batch in one transaction in scope.
func dbWriter(db *pg.DB, scope pg.Scope) func(context.Context, []row) error {
	return func(ctx context.Context, rows []row) error {
		payload, err := marshalRows(rows)
		if err != nil {
			return err
		}
		return db.Tx(ctx, scope, func(tx pg.Tx) error {
			_, err := store.New().InsertCallLogs(ctx, tx, payload)
			return err
		})
	}
}

// errEncoding marks a batch that cannot be encoded: the same rows fail the same way every time.
var errEncoding = errors.New("encode call records")

// isBadRow reports a failure that retrying the same rows cannot mend: the database refused a value, or encoding did.
func isBadRow(err error) bool { return pg.IsDataError(err) || errors.Is(err, errEncoding) }

func marshalRows(rows []row) ([]byte, error) {
	payload, err := jsonx.MarshalCanonical(rows)
	if err != nil {
		return nil, fmt.Errorf("%w (%d rows): %w", errEncoding, len(rows), err)
	}
	return payload, nil
}

func newRecorder(ctx context.Context, cfg Config, write func(context.Context, []row) error, fatal func(error) bool, logger *slog.Logger) *Recorder {
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
		queue: make(chan row, cfg.QueueSize), stop: make(chan struct{}),
		giveUp: giveUp, done: make(chan struct{}),
	}
	r.written = counter(cfg.Meter, "axiom.observability.records.written", "Call records stored.")
	r.retries = counter(cfg.Meter, "axiom.observability.write.retries", "Batch writes that failed and were tried again.")
	r.dropped = counter(cfg.Meter, "axiom.observability.records.lost",
		"Call records given up on: refused by the database as malformed, or unwritten when shutdown ran out of time.")
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

// Enqueue queues rec for writing. It waits while the queue is full and returns ctx's error if ctx ends first, in
// which case rec is not queued: a caller that must not lose the record passes a context that ends only with the
// process's shutdown, not the client's disconnection. It returns ErrClosed once Close has begun, and an error for a
// record that could never be written.
func (r *Recorder) Enqueue(ctx context.Context, rec CallRecord) error {
	row, err := newRow(rec)
	if err != nil {
		return fmt.Errorf("call record %s: %w", rec.ID, err)
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return ErrClosed
	}
	r.senders.Add(1)
	r.mu.Unlock()
	defer r.senders.Done()
	select {
	case r.queue <- row:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.stop:
		return ErrClosed
	}
}

// Close stops accepting records and writes what is queued, retrying failures until ctx ends. It returns nil when
// everything was written; otherwise it logs and returns how many records were lost.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	first := !r.closing
	r.closing = true
	r.mu.Unlock()
	if first {
		close(r.stop)
		r.senders.Wait() // after this no one sends, so closing the queue is safe
		close(r.queue)
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		r.giveUp()
		<-r.done
	}
	r.giveUp()
	if r.lost > 0 {
		return fmt.Errorf("observability: %d call records were not written", r.lost)
	}
	return nil
}

// writer collects batches off the queue and writes them one at a time, in order.
func (r *Recorder) writer(ctx context.Context) {
	defer close(r.done)
	for {
		batch, open := r.collect()
		if len(batch) > 0 {
			r.flush(ctx, batch)
		}
		if !open {
			return
		}
	}
}

// collect takes up to BatchSize rows, waiting for the first and then at most FlushInterval for the rest. It reports
// false once the queue is closed and empty.
func (r *Recorder) collect() (batch []row, open bool) {
	first, ok := <-r.queue
	if !ok {
		return nil, false
	}
	batch = append(make([]row, 0, r.cfg.BatchSize), first)
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
// malformed is split to find the row at fault, which is logged and left out: one bad record must not stop every
// later one.
func (r *Recorder) flush(ctx context.Context, batch []row) {
	delay := r.cfg.RetryBase
	for {
		err := r.attempt(ctx, batch)
		if err == nil {
			r.written.Add(ctx, int64(len(batch)))
			return
		}
		switch {
		case ctx.Err() != nil:
			r.abandon(ctx, len(batch), err)
			return
		case r.fatal(err) && len(batch) > 1:
			mid := len(batch) / 2
			r.flush(ctx, batch[:mid])
			r.flush(ctx, batch[mid:])
			return
		case r.fatal(err):
			r.logger.Error("call record refused by the database", "request_id", batch[0].ID, "err", err)
			r.dropped.Add(ctx, 1)
			r.lost++
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
func (r *Recorder) attempt(ctx context.Context, batch []row) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	return r.write(ctx, batch)
}

// abandon counts the rows of a batch that shutdown could not wait for.
func (r *Recorder) abandon(ctx context.Context, n int, err error) {
	r.logger.Error("call records lost at shutdown", "records", n, "err", err)
	r.dropped.Add(ctx, int64(n))
	r.lost += n
}
