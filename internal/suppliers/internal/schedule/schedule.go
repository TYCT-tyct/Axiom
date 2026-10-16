// Package schedule runs the sync engine of every supplier on the job queue (internal/suppliers/README.md「调度」). A
// periodic sweep enqueues a job for each supplier whose round is due and that no node is syncing; the job takes the
// supplier's lease, runs a round of the engine over the supplier's adapter, and gives the lease back.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
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
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

const (
	// leaseTTL is how long a node may go without committing before another may take its supplier over. A round commits
	// after every task and every page of charge records, and one request of the supplier takes 30 seconds at most.
	leaseTTL = 5 * time.Minute
	// sweepBatch is the most suppliers one sweep enqueues; the rest are the next sweep's, the longest-overdue first.
	sweepBatch = 200
	// releaseTimeout is how long giving a lease back may take once a round is over, whatever ended it.
	releaseTimeout = 10 * time.Second
)

// Config is what a node needs to sync suppliers.
type Config struct {
	DB *pg.DB
	// Scope is the platform scope, which only cmd/ may build: syncing sees every supplier.
	Scope  pg.Scope
	Keys   *secret.Keyring
	Egress *egress.Client
	Queue  *pg.Queue
	// Registry is the adapters of the program.
	Registry *registry.Registry
	// Holder names this node in the leases it takes.
	Holder string
	// SweepEvery is how often suppliers that are due are looked for; a supplier is synced at most this long after a
	// round of it falls due.
	SweepEvery time.Duration
	Now        func() time.Time
	Logger     *slog.Logger
}

// SyncArgs is the job of one round of one supplier. At most one is waiting or running per supplier, and a finished one
// does not hold up the next: it is the supplier's next_due, not the job, that says when it is time.
type SyncArgs struct {
	Supplier string `json:"supplier"`
}

// Kind implements river.JobArgs.
func (SyncArgs) Kind() string { return "suppliers.sync" }

// InsertOpts implements river.JobArgsWithInsertOpts. A job that fails is not retried by the queue: the sweep makes a new
// one when the supplier is due again, which is when its backoff says.
func (SyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 1,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
			rivertype.JobStateScheduled,
		}},
	}
}

// SweepArgs is the periodic job that finds the suppliers that are due.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "suppliers.sync_sweep" }

// Schedule is the syncing of the suppliers on one node.
type Schedule struct {
	cfg      Config
	node     *persist.Node
	settings syncer.Settings
}

// New adds the workers and the periodic sweep to the queue, before it starts.
func New(cfg Config) (*Schedule, error) {
	if cfg.DB == nil || cfg.Keys == nil || cfg.Egress == nil || cfg.Queue == nil || cfg.Registry == nil || cfg.Now == nil || cfg.Logger == nil {
		return nil, errors.New("schedule: a database, keys, an egress client, a queue, adapters, a clock and a logger are required")
	}
	if cfg.SweepEvery <= 0 {
		return nil, errors.New("schedule: the sweep needs a positive interval")
	}
	node, err := persist.New(persist.Config{
		DB: cfg.DB, Scope: cfg.Scope, Keys: cfg.Keys, Holder: cfg.Holder, LeaseTTL: leaseTTL, Now: cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	s := &Schedule{cfg: cfg, node: node, settings: settings()}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(s.workSync)); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(s.workSweep)); err != nil {
		return nil, err
	}
	cfg.Queue.Every(cfg.SweepEvery, func() river.JobArgs { return SweepArgs{} })
	return s, nil
}

// settings are the engine's defaults, with the backoff spread by a tenth so that suppliers that failed together do not
// try again together.
func settings() syncer.Settings {
	s := syncer.Defaults()
	s.Jitter = func(d time.Duration) time.Duration { return d + rand.N(d/10+1) } //nolint:gosec // spreads retries in time; nothing depends on it being unpredictable
	return s
}

func (s *Schedule) workSweep(ctx context.Context, _ *river.Job[SweepArgs]) error { return s.sweep(ctx) }

func (s *Schedule) workSync(ctx context.Context, job *river.Job[SyncArgs]) error {
	id, err := uuid.Parse(job.Args.Supplier)
	if err != nil {
		return fmt.Errorf("schedule: the job names no supplier: %w", err)
	}
	return s.sync(ctx, id)
}

// sweep enqueues a round for each supplier that is due and not held, in the transaction that reads which they are.
func (s *Schedule) sweep(ctx context.Context) error {
	return s.cfg.DB.Tx(ctx, s.cfg.Scope, func(tx pg.Tx) error {
		due, err := store.New().ListSyncDue(ctx, tx, store.ListSyncDueParams{Now: s.cfg.Now(), Max: sweepBatch})
		if err != nil || len(due) == 0 {
			return err
		}
		jobs := make([]river.InsertManyParams, len(due))
		for i, id := range due {
			jobs[i] = river.InsertManyParams{Args: SyncArgs{Supplier: id.String()}}
		}
		_, err = s.cfg.Queue.InsertManyTx(ctx, tx, jobs)
		return err
	})
}

// sync runs a round of one supplier. A supplier another node is syncing is that node's, and a round whose lease was
// taken over has stored nothing: neither is a failure.
func (s *Schedule) sync(ctx context.Context, id uuid.UUID) error {
	session, err := s.node.Acquire(ctx, id)
	if errors.Is(err, persist.ErrLeaseHeld) {
		return nil
	} else if err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		if err := session.Release(releaseCtx); err != nil {
			s.cfg.Logger.Error("giving back a supplier's lease", "supplier", id.String(), "err", err)
		}
	}()
	engine, err := s.engine(ctx, session)
	if err != nil {
		return err
	}
	if err := engine.Round(ctx); err != nil && !errors.Is(err, syncer.ErrFenced) {
		return err
	}
	return nil
}

// engine makes the engine of a supplier over its adapter, which it logs in with only when a task first needs it.
func (s *Schedule) engine(ctx context.Context, session *persist.Session) (*syncer.Engine, error) {
	sup := session.Supplier
	if sup.Exit != string(egress.Direct) {
		return nil, fmt.Errorf("schedule: supplier %s: the exit %q is not one this node has", sup.ID, sup.Exit)
	}
	a, ok := s.cfg.Registry.Get(sup.Kind)
	if !ok {
		return nil, fmt.Errorf("schedule: supplier %s: no adapter of kind %q", sup.ID, sup.Kind)
	}
	buckets := client.NewSharedLimiter(s.cfg.DB, s.cfg.Scope, a.Buckets(),
		client.Scopes{Address: "ip:" + sup.Origin + "|" + sup.Exit, Account: "account:" + sup.ID.String()}, s.cfg.Now)
	c, err := client.New(sup.Origin, s.cfg.Egress, buckets, s.cfg.Now)
	if err != nil {
		return nil, err
	}
	m := &openOnUse{open: func() (adapter.Manager, error) { return a.Open(ctx, c, session.Logins()) }}
	return syncer.New(session.Store(), m, s.settings, s.cfg.Now), nil
}
