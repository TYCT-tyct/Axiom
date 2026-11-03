// Package collector is the resident collector for suppliers whose per-call usage is a queue that is consumed once (CPA,
// internal/suppliers/README.md「CPA」). One periodic job drains every such supplier into suppliers.usage_records, then
// parses and inserts the rows as charge records. The queue cannot be re-read, so the staging row is the durable copy;
// an ingest repeated after a crash is harmless because charge records are unique by (supplier, request id).
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

const (
	drainEvery   = 5 * time.Second
	drainBatch   = 100
	drainBatches = 20
	ingestBatch  = 100
	holdEvery    = 30 * time.Second
	authBackoff  = 30 * time.Minute
)

// Config is what the collector needs from a node.
type Config struct {
	DB       *pg.DB
	Scope    pg.Scope
	Keys     *secret.Keyring
	Egress   *egress.Client
	Queue    *pg.Queue
	Registry *registry.Registry
	Now      func() time.Time
	Logger   *slog.Logger
}

// Args is the periodic drain job.
type Args struct{}

// Kind implements river.JobArgs.
func (Args) Kind() string { return "suppliers.usage_drain" }

// InsertOpts implements river.JobArgsWithInsertOpts: a drain that is still running is not stacked with another. The
// follower is not needed, because every interval starts a fresh pass and the queue keeps its records for a while.
func (Args) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: pg.QueueSyncs, MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		}},
	}
}

// Worker drains the suppliers of every adapter that declares a one-read usage queue.
type Worker struct {
	cfg Config

	mu      sync.Mutex
	blocked map[uuid.UUID]blockedLogin // a rejected management key, and the login version that was rejected
}

// Register adds the worker and the periodic job to a queue that has not started yet.
func Register(cfg Config) error {
	if err := cfg.check(); err != nil {
		return err
	}
	w := &Worker{cfg: cfg, blocked: map[uuid.UUID]blockedLogin{}}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(w.work)); err != nil {
		return fmt.Errorf("collector: add the drain worker: %w", err)
	}
	cfg.Queue.Every(drainEvery, func() river.JobArgs { return Args{} })
	return nil
}

func (cfg Config) check() error {
	if cfg.DB == nil || cfg.Keys == nil || cfg.Egress == nil || cfg.Queue == nil || cfg.Registry == nil || cfg.Now == nil || cfg.Logger == nil {
		return errors.New("collector: a database, keys, an egress client, a queue, adapters, a clock and a logger are required")
	}
	return nil
}

// supplier is the part of the supplier row the collector needs.
type supplier struct {
	id     uuid.UUID
	origin string
	exit   string
}

func (w *Worker) work(ctx context.Context, _ *river.Job[Args]) error {
	var errs []error
	for _, kind := range w.cfg.Registry.Kinds() {
		a, ok := w.cfg.Registry.Get(kind)
		if !ok {
			continue
		}
		if _, ok := a.(adapter.UsageQueueSource); !ok {
			continue
		}
		sups, err := w.suppliers(ctx, kind)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, sup := range sups {
			if err := w.one(ctx, a, sup); err != nil {
				w.cfg.Logger.Error("draining a supplier's usage queue", "supplier", sup.id.String(), "kind", string(kind), "err", err)
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) suppliers(ctx context.Context, kind adapter.Kind) ([]supplier, error) {
	var out []supplier
	err := w.cfg.DB.ReadTx(ctx, w.cfg.Scope, func(tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, origin, mgmt_egress_id FROM suppliers.suppliers
WHERE kind = $1 AND status IN ('connecting', 'active', 'low_balance') ORDER BY id`, string(kind))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s supplier
			if err := rows.Scan(&s.id, &s.origin, &s.exit); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

func (w *Worker) one(ctx context.Context, a adapter.Adapter, sup supplier) error {
	if sup.exit != string(egress.Direct) {
		return nil // only the direct exit exists today; another node handles the supplier
	}
	if w.blockedNow(ctx, sup.id) {
		return nil
	}
	hold, err := w.hold(ctx, sup.id)
	if errors.Is(err, pg.ErrHeld) {
		return nil // another node is draining this supplier
	}
	if err != nil {
		return err
	}
	defer w.release(ctx, hold, sup.id)

	raw, version, err := persist.ReadLogin(ctx, w.cfg.DB, w.cfg.Scope, w.cfg.Keys, sup.id)
	if errors.Is(err, pg.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	uq, err := w.open(ctx, a, sup, raw)
	if err != nil {
		return err
	}
	return w.drain(ctx, sup, uq, version, hold)
}

// blockedNow reports whether a rejected management key still stops this supplier. A replaced login (a new version)
// clears the stop at once, so a person's fix is not held back by the old key's failure.
func (w *Worker) blockedNow(ctx context.Context, id uuid.UUID) bool {
	until, version, ok := w.blockState(id)
	if !ok {
		return false
	}
	_, current, err := persist.ReadLogin(ctx, w.cfg.DB, w.cfg.Scope, w.cfg.Keys, id)
	if err == nil && current != version {
		w.unblock(id)
		return false
	}
	return w.cfg.Now().Before(until)
}

func (w *Worker) hold(ctx context.Context, id uuid.UUID) (*pg.Hold, error) {
	return w.cfg.DB.Hold(ctx, "axiom.usage."+id.String(), holdEvery)
}

func (w *Worker) release(ctx context.Context, hold *pg.Hold, id uuid.UUID) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := hold.Release(release); err != nil {
		w.cfg.Logger.Error("releasing a usage drain", "supplier", id.String(), "err", err)
	}
}

func (w *Worker) open(ctx context.Context, a adapter.Adapter, sup supplier, raw []byte) (adapter.UsageQueue, error) {
	limiter := client.NewSharedLimiter(w.cfg.DB, w.cfg.Scope, a.Buckets(),
		client.Scopes{Address: "ip:" + sup.origin + "|" + sup.exit, Account: "account:" + sup.id.String()}, w.cfg.Now)
	c, err := client.New(sup.origin, w.cfg.Egress, limiter, w.cfg.Now)
	if err != nil {
		return nil, err
	}
	mgr, err := a.Open(ctx, c, staticLogin{raw: raw})
	if err != nil {
		return nil, err
	}
	uq, ok := mgr.(adapter.UsageQueue)
	if !ok {
		return nil, fmt.Errorf("collector: the adapter %q opened a manager without a usage queue", a.Kind())
	}
	return uq, nil
}

func (w *Worker) drain(ctx context.Context, sup supplier, uq adapter.UsageQueue, version int64, hold *pg.Hold) error {
	for range drainBatches {
		select {
		case <-hold.Lost():
			return nil
		default:
		}
		records, err := uq.DrainUsage(ctx, drainBatch)
		if err != nil {
			drainErr := w.drainError(sup, version, err)
			if ingestErr := w.ingest(ctx, sup.id, uq); ingestErr != nil {
				return errors.Join(drainErr, ingestErr)
			}
			return drainErr
		}
		if len(records) == 0 {
			break
		}
		if err := w.stage(ctx, sup.id, records); err != nil {
			return err
		}
	}
	return w.ingest(ctx, sup.id, uq)
}

func (w *Worker) stage(ctx context.Context, supplier uuid.UUID, records []adapter.UsageRecord) error {
	rows := make([]store.UsageRecordRow, len(records))
	for i, r := range records {
		rows[i].Record = r.Raw
		if r.Ref != "" {
			id := string(r.Ref)
			rows[i].RequestID = &id
		}
	}
	var err error
	for attempt := range 3 {
		err = w.cfg.DB.Tx(ctx, w.cfg.Scope, func(tx pg.Tx) error {
			return store.InsertUsageRecords(ctx, tx, supplier, rows)
		})
		if err == nil {
			return nil
		}
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return fmt.Errorf("collector: stage %d records: %w", len(records), err)
}

func (w *Worker) drainError(sup supplier, version int64, err error) error {
	if errors.Is(err, adapter.ErrAuthRejected) {
		w.block(sup.id, version)
		w.cfg.Logger.Error("CPA rejected the management key; polling stops until a person replaces it",
			"supplier", sup.id.String())
		return nil
	}
	if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
		w.cfg.Logger.Debug("usage drain delayed", "supplier", sup.id.String(), "until", blocked.Until)
		return nil
	}
	if errors.Is(err, adapter.ErrBucketsFull) {
		return nil
	}
	return err
}

func (w *Worker) ingest(ctx context.Context, supplier uuid.UUID, uq adapter.UsageQueue) error {
	for {
		rows, err := w.pending(ctx, supplier)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := w.ingestBatch(ctx, supplier, uq, rows); err != nil {
			return err
		}
		if len(rows) < ingestBatch {
			return nil
		}
	}
}

func (w *Worker) pending(ctx context.Context, supplier uuid.UUID) ([]store.UsageRecordRow, error) {
	var rows []store.UsageRecordRow
	err := w.cfg.DB.ReadTx(ctx, w.cfg.Scope, func(tx pg.Tx) error {
		var err error
		rows, err = store.ListNewUsageRecords(ctx, tx, supplier, ingestBatch)
		return err
	})
	return rows, err
}

func (w *Worker) ingestBatch(ctx context.Context, supplier uuid.UUID, uq adapter.UsageQueue, rows []store.UsageRecordRow) error {
	var charges []adapter.Charge
	var good, bad []uuid.UUID
	for _, row := range rows {
		ch, err := uq.ParseUsage(row.Record)
		if err != nil {
			bad = append(bad, row.ID)
			continue
		}
		charges, good = append(charges, ch), append(good, row.ID)
	}
	if len(bad) > 0 {
		w.cfg.Logger.Error("usage records cannot be read", "supplier", supplier.String(), "count", len(bad))
	}
	return w.cfg.DB.Tx(ctx, w.cfg.Scope, func(tx pg.Tx) error {
		if err := store.MarkUsageRecordsRejected(ctx, tx, supplier, bad, "unreadable"); err != nil {
			return err
		}
		if len(charges) > 0 {
			if _, err := reconcile.Ingest(ctx, tx, supplier, charges); err != nil {
				return err
			}
		}
		return store.DeleteUsageRecords(ctx, tx, supplier, good)
	})
}

type blockedLogin struct {
	until   time.Time
	version int64
}

func (w *Worker) blockState(id uuid.UUID) (time.Time, int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, ok := w.blocked[id]
	return b.until, b.version, ok
}

func (w *Worker) block(id uuid.UUID, version int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.blocked[id] = blockedLogin{until: w.cfg.Now().Add(authBackoff), version: version}
}

func (w *Worker) unblock(id uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.blocked, id)
}

// staticLogin is the login the collector read outside the sync lease. It never writes: the collector does not refresh a
// CPA management key.
type staticLogin struct{ raw []byte }

func (s staticLogin) Load(context.Context) ([]byte, int64, error) { return s.raw, 0, nil }

func (staticLogin) Swap(context.Context, int64, []byte) error { return adapter.ErrStaleLogin }
