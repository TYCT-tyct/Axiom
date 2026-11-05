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
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

var (
	// ErrClosed is Admit's and Commit's answer once Close has begun.
	ErrClosed = errors.New("observability: the recorder is closed")
	// ErrBacklog is Admit's answer when the spill is full: no new call may be served until records are written.
	ErrBacklog = errors.New("observability: the call log's spill is full; refusing new calls")
	// ErrBroken is Admit's answer once a record could not be kept (a value the database refuses, a full disk, or a
	// damaged segment): serving another call would serve it unrecorded.
	ErrBroken = errors.New("observability: the call log cannot keep records; refusing new calls")
	// ErrPending is Close's answer when records remain in the spill for the next start.
	ErrPending = errors.New("observability: call records remain in the spill")
)

// Config sizes the Recorder. Production values come from cmd/axiom; tests use small ones.
type Config struct {
	// Dir is the persistent directory of the write-ahead spill. It must outlive the process: records a crashed node
	// had not written yet are replayed from here on the next start.
	Dir           string
	Capacity      int           // calls admitted but not yet written; Admit refuses beyond it, and the disk bound follows
	BatchSize     int           // rows per database write, at most
	FlushInterval time.Duration // how long the first record of a batch may wait for company
	RetryBase     time.Duration // the first wait after a failed write; it doubles up to RetryMax
	RetryMax      time.Duration
	WriteTimeout  time.Duration // one write attempt; after it the batch is retried (the outcome of a timed-out write is unknown)
	Meter         metric.Meter  // nil for none
	// Ledger reads, in the writer's transaction, the Amounts the ledger holds for the named calls. The writer asks it
	// only about the pending records of a batch, before inserting them: a settlement that committed while its record
	// stayed pending (the node died before the settled record was kept, or the settlement's last try committed after
	// it was given up) gives the record its amounts, and one that did not leaves the record pending (README「写入」).
	// cmd/axiom gives billing's; nil leaves every pending record as it is.
	Ledger Ledger
}

// Ledger is the Amounts of the named calls whose settlement committed, keyed by request ID; a call it has no settled
// charge for is absent. An error is a read that failed: the batch is retried.
type Ledger func(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]Amounts, error)

// queued is one call with its attempts, the unit a caller hands over and the writer keeps or loses whole.
type queued struct {
	call     row
	attempts []attemptRow
}

// Recorder writes call and attempt records from a bounded write-ahead spill. A call reserves room with Admit before it
// is admitted; its record is appended to the spill before the response ends, and the writer drains the spill into the
// database in order. When the spill is full Admit refuses new calls, so the node never serves a call it cannot record
// (README「写入」 and docs/domains/observability.md「写入路径」).
type Recorder struct {
	cfg    Config
	write  func(context.Context, []queued) error
	fatal  func(error) bool // the error says the records themselves are bad: retrying cannot help
	logger *slog.Logger
	spill  *spill
	// parking holds pending records until their call's settled record replaces them or Release moves them into the
	// spill. A parked record holds its slot's room, which is how Capacity also bounds the parked files.
	parking parking
	parked  atomic.Int64    // slots holding a parked record now: what Close reports left for the next start
	ctx     context.Context //nolint:containedctx // the writer's values, used for metrics on the record path too

	mu      sync.Mutex // guards closing
	closing bool

	giveUp context.CancelFunc // ends the writer's context: Close ran out of time waiting for the spill to drain
	done   chan struct{}      // closed when the writer has exited
	// outstanding is the calls admitted but not yet written: those still running plus the records in the spill. It
	// is the admission bound, so a burst cannot grow the spill past Capacity.
	outstanding atomic.Int64
	// active is the calls admitted and not yet past Commit or Release: their records may still be appended. A writer
	// that stops because the database refuses a row waits for it, so a call that already reserved a place can still
	// put its record in the spill before the append file closes.
	active  atomic.Int64
	broken  atomic.Bool
	quiet   chan struct{} // closed once broken and active is zero: the last in-flight call has appended or released
	quietON sync.Once
	lost    atomic.Int64 // calls not in the spill: unencodable or the spill refused them
	// The attempts of the calls above, for the same reasons.
	lostAttempts atomic.Int64

	written, attemptsWritten, retries, lostMetric, attemptsLostMetric metric.Int64Counter
}

// NewRecorder starts a Recorder that writes through db in scope, the platform scope: one batch holds the records of
// many accounts. The writer takes ctx's values, such as the tracing provider, but not its end: only Close stops it.
func NewRecorder(ctx context.Context, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) (*Recorder, error) {
	return newRecorder(ctx, cfg, dbWriter(db, scope, cfg.Ledger), isBadRow, logger)
}

// dbWriter writes a batch in one transaction in scope: the calls first, then their attempts, so a retried batch can
// never leave attempts whose call is missing. A pending record is completed from the ledger first, in the same
// transaction.
func dbWriter(db *pg.DB, scope pg.Scope, ledger Ledger) func(context.Context, []queued) error {
	return func(ctx context.Context, batch []queued) error {
		return db.Tx(ctx, scope, func(tx pg.Tx) error {
			if err := settlePending(ctx, tx, ledger, batch); err != nil {
				return err
			}
			calls, attempts, err := marshalQueued(batch)
			if err != nil {
				return err
			}
			queries := store.New()
			if _, err := queries.InsertCallLogs(ctx, tx, calls); err != nil {
				return err
			}
			if len(attempts) == 0 {
				return nil
			}
			_, err = queries.InsertCallAttempts(ctx, tx, attempts)
			return err
		})
	}
}

// settlePending gives the batch's pending records the amounts the ledger holds for them. It runs only when the batch
// has one, which is rare: the settlement was left to the background and then lost its record. The batch keeps what it
// is given, so a retry after a failed insert does not read the ledger again; the ledger's settled charge only moves by
// a correction, and the call log keeps one snapshot of it either way.
func settlePending(ctx context.Context, tx pg.Tx, ledger Ledger, batch []queued) error {
	if ledger == nil {
		return nil
	}
	var ids []uuid.UUID
	var pending map[uuid.UUID]*row
	for i := range batch {
		if call := &batch[i].call; call.BillingPending {
			id, err := uuid.Parse(call.ID)
			if err != nil {
				return fmt.Errorf("%w: request id %q: %w", errEncoding, call.ID, err)
			}
			if pending == nil {
				pending = map[uuid.UUID]*row{}
			}
			ids, pending[id] = append(ids, id), call
		}
	}
	if len(ids) == 0 {
		return nil
	}
	settled, err := ledger(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("read the ledger for %d pending call records: %w", len(ids), err)
	}
	for id, a := range settled {
		if call := pending[id]; call != nil {
			if err := call.settle(a); err != nil {
				return fmt.Errorf("%w: the ledger's amounts of %s: %w", errEncoding, call.ID, err)
			}
		}
	}
	return nil
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

func newRecorder(ctx context.Context, cfg Config, write func(context.Context, []queued) error, fatal func(error) bool, logger *slog.Logger) (*Recorder, error) {
	if cfg.Dir == "" || cfg.Capacity <= 0 || cfg.BatchSize <= 0 || cfg.FlushInterval <= 0 || cfg.RetryBase <= 0 ||
		cfg.RetryMax < cfg.RetryBase || cfg.WriteTimeout <= 0 {
		return nil, errors.New("observability: Config needs a spill directory, positive sizes and durations")
	}
	if logger == nil {
		logger = slog.Default()
	}
	sp, parked, err := openDisk(cfg, logger)
	if err != nil {
		return nil, err
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("observability")
	}
	run, giveUp := context.WithCancel(context.WithoutCancel(ctx))
	r := &Recorder{
		cfg: cfg, write: write, fatal: fatal, logger: logger, spill: sp, parking: parked, ctx: context.WithoutCancel(ctx),
		giveUp: giveUp, done: make(chan struct{}), quiet: make(chan struct{}),
	}
	r.outstanding.Store(sp.pendingCount())
	r.written = counter(cfg.Meter, "axiom.observability.records.written", "Call records stored.")
	r.attemptsWritten = counter(cfg.Meter, "axiom.observability.attempts.written", "Attempt records stored.")
	r.retries = counter(cfg.Meter, "axiom.observability.write.retries", "Batch writes that failed and were tried again.")
	r.lostMetric = counter(cfg.Meter, "axiom.observability.records.lost",
		"Call records not kept, by reason: invalid, spill or shutdown.")
	r.attemptsLostMetric = counter(cfg.Meter, "axiom.observability.attempts.lost",
		"Attempt records not kept, with their call and for the same reasons.")
	_, err = cfg.Meter.Int64ObservableGauge("axiom.observability.records.pending",
		metric.WithDescription("Call records in the spill waiting for the database."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(sp.pendingCount())
			return nil
		}))
	if err != nil {
		r.closeDisk()
		return nil, err // the instrument names are constants
	}
	go r.writer(run)
	return r, nil
}

// openDisk opens the spill and the parked records, and moves every record a dead process left parked into the spill.
func openDisk(cfg Config, logger *slog.Logger) (*spill, parking, error) {
	sp, err := openSpill(cfg.Dir, cfg.Capacity, syncDir)
	if err != nil {
		return nil, parking{}, err
	}
	parked, err := openParking(cfg.Dir, syncDir, logger)
	if err == nil {
		err = parked.recover(sp)
	}
	if err != nil {
		parked.close()
		sp.finish()
		return nil, parking{}, err
	}
	return sp, parked, nil
}

// closeDisk releases what openDisk opened; the directory lock goes last.
func (r *Recorder) closeDisk() {
	r.parking.close()
	r.spill.finish()
}

func counter(m metric.Meter, name, description string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		panic(err) // the instrument names are constants
	}
	return c
}

// Admit reserves room for one call's record. The caller must end with either the record's Commit or Release. It returns
// ErrBacklog when the spill holds all the records it may, and ErrBroken once a record could not be kept at all.
func (r *Recorder) Admit() (*Slot, error) {
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return nil, ErrClosed
	}
	if r.broken.Load() {
		return nil, ErrBroken
	}
	for {
		n := r.outstanding.Load()
		if n >= int64(r.cfg.Capacity) {
			return nil, ErrBacklog
		}
		if r.outstanding.CompareAndSwap(n, n+1) {
			r.active.Add(1)
			if r.broken.Load() {
				r.outstanding.Add(-1)
				r.finishSlot()
				return nil, ErrBroken
			}
			return &Slot{r: r}, nil
		}
	}
}

// finishSlot marks one admitted call done with the append path. Once a failure has stopped admission and the last call
// has finished, the writer may close the append file: no call that reserved room can still need it.
func (r *Recorder) finishSlot() {
	if r.active.Add(-1) == 0 && r.broken.Load() {
		r.quietON.Do(func() { close(r.quiet) })
	}
}

// Enqueue admits and commits one record in the caller's goroutine. It is the whole call-log write for code that is not
// a gateway call (tests, tools); the gateway reserves at admission with Admit and commits with the returned Slot. A
// pending record enqueued here is final: no settled record can replace it, so it goes into the spill as it is.
func (r *Recorder) Enqueue(rec CallRecord) error {
	slot, err := r.Admit()
	if err != nil {
		return err
	}
	defer slot.Release()
	return slot.Commit(rec)
}

// reject counts a record that could not be kept and stops accepting calls: the next one would be served unrecorded.
func (r *Recorder) reject(rec CallRecord, reason string, err error) {
	r.lost.Add(1)
	r.lostAttempts.Add(int64(len(rec.Attempts)))
	r.lostMetric.Add(r.ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	r.attemptsLostMetric.Add(r.ctx, int64(len(rec.Attempts)), metric.WithAttributes(attribute.String("reason", reason)))
	r.broken.Store(true)
	r.logger.Error("call record not kept; the node refuses new calls", "request_id", rec.ID.String(), "reason", reason, "err", err)
}

// Close stops accepting records and writes what the spill holds, retrying failures until ctx ends. It returns nil when
// everything is written; otherwise it reports records left in the spill or parked for the next start, and records that
// could not be kept at all.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.spill.close()
	select {
	case <-r.done:
	case <-ctx.Done():
		r.spill.stopNow()
		r.giveUp()
		<-r.done
	}
	r.giveUp()
	pending := r.spill.pendingCount() + r.parked.Load() // a parked record is moved into the spill at the next start
	lost, attempts := r.lost.Load(), r.lostAttempts.Load()
	if pending > 0 {
		return fmt.Errorf("%w: %d calls remain for the next start (%d calls and %d attempts were not kept)",
			ErrPending, pending, lost, attempts)
	}
	if lost > 0 {
		return fmt.Errorf("observability: %d call records were not kept (%d attempts)", lost, attempts)
	}
	return nil
}

// Done is closed once the writer goroutine has exited, which Close waits for before it returns. A test reads it to
// know that the Recorder it was given left nothing running.
func (r *Recorder) Done() <-chan struct{} { return r.done }

// writer drains the spill into the database, one batch at a time, in spill order.
func (r *Recorder) writer(ctx context.Context) {
	defer close(r.done)
	defer r.closeDisk()
	for {
		batch, end, open, err := r.spill.nextBatch(r.cfg.BatchSize, r.cfg.FlushInterval)
		if err != nil {
			r.markBroken()
			r.logger.Error("the call log spill cannot be read; the node refuses new calls", "err", err)
			r.stopDraining(ctx)
			return
		}
		if len(batch) > 0 && !r.flush(ctx, batch, end) {
			return
		}
		if !open {
			return
		}
	}
}

// stopDraining keeps the append file open after the database stopped accepting rows until every call that already
// reserved room has appended or released. Those calls finish their upstream work after the bad row was found; closing
// the file under them would lose records that had room and a healthy disk.
func (r *Recorder) stopDraining(ctx context.Context) {
	select {
	case <-r.quiet:
	case <-ctx.Done():
	}
}

// markBroken stops admission and, when no admitted call is still appending, releases a writer waiting to close the
// append file. The check after the store is what orders it against finishSlot: either the decrement sees broken, or
// this load sees the decremented count.
func (r *Recorder) markBroken() {
	r.broken.Store(true)
	if r.active.Load() == 0 {
		r.quietON.Do(func() { close(r.quiet) })
	}
}

// flush writes one batch, retrying with a growing wait until it succeeds or the writer stops. A failed write stays in
// the spill: the next try, or the next process, sees the same records and the tables' ON CONFLICT makes a retry after
// an unknown commit harmless. A bad row is not dropped: it stops the database writes and admission until an operator
// fixes it, because a later row would otherwise pass a record that is missing.
func (r *Recorder) flush(ctx context.Context, batch []queued, end position) bool {
	delay := r.cfg.RetryBase
	logged := false
	for {
		err := r.attempt(ctx, batch)
		if err == nil {
			r.accepted(ctx, batch, end)
			return true
		}
		if ctx.Err() != nil {
			r.logger.Warn("call records left in the spill for the next start", "records", len(batch), "err", err)
			return false
		}
		if r.fatalBatch(err, batch) {
			r.stopDraining(ctx)
			return false
		}
		if !logged {
			r.logger.Warn("writing call records failed, retrying", "records", len(batch), "wait", delay, "err", err)
			logged = true
		}
		r.retries.Add(ctx, 1)
		if !waitRetry(ctx, delay) {
			return false
		}
		delay = min(delay*2, r.cfg.RetryMax)
	}
}

// waitRetry sleeps with jitter so a database coming back does not receive every node's batch at the same instant.
func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay/2 + rand.N(delay/2+1)) //nolint:gosec // jitter, not a secret
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// accepted records a committed batch and frees its segment space.
func (r *Recorder) accepted(ctx context.Context, batch []queued, end position) {
	if err := r.spill.ack(end, len(batch)); err != nil {
		// The records are in the database; only their segment and checkpoint could not be reclaimed. Keeping them
		// means the next start replays them, which ON CONFLICT makes harmless.
		r.logger.Warn("acknowledged call records could not be reclaimed; they stay in the spill", "records", len(batch), "err", err)
	}
	r.outstanding.Add(-int64(len(batch)))
	r.written.Add(ctx, int64(len(batch)))
	var attempts int64
	for _, q := range batch {
		attempts += int64(len(q.attempts))
	}
	r.attemptsWritten.Add(ctx, attempts)
}

// fatalBatch stops admission for a failure the same rows will repeat. The batch stays in the spill so an operator can
// repair it; later records must not pass a missing one. The append path stays open until the calls that already
// reserved room have finished, so their records are kept too.
func (r *Recorder) fatalBatch(err error, batch []queued) bool {
	if !r.fatal(err) {
		return false
	}
	if r.broken.CompareAndSwap(false, true) {
		r.logger.Error("call records refused by the database; the node refuses new calls",
			"request_id", batch[0].call.ID, "records", len(batch), "err", err)
	}
	if r.active.Load() == 0 {
		r.quietON.Do(func() { close(r.quiet) })
	}
	return true
}

// attempt is one write, cut off after WriteTimeout: a database that stops answering must not hold the only writer.
func (r *Recorder) attempt(ctx context.Context, batch []queued) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	return r.write(ctx, batch)
}

// AlertSample reads the records waiting for the database and the cumulative losses.
func (r *Recorder) AlertSample() (int, int64) {
	return int(r.spill.pendingCount()), r.lost.Load()
}
