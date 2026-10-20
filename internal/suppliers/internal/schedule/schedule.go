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
	"slices"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/catalog"
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
	// maxBlockedWait is the longest a job waits, holding the supplier's lease, for the supplier's limits to reopen so
	// that it can go on with the listing it was reading. It is shorter than leaseTTL, which the round's last commit
	// started: the lease must not run out while the job sleeps.
	maxBlockedWait = 4 * time.Minute
	// roundsBudget is how long one job may go on running rounds while the limits hold it up. It ends before the
	// queue's limit for a job (pg.Queue's jobTimeout, 10 minutes) cancels it.
	roundsBudget = 8 * time.Minute
)

// syncable are the statuses of a supplier that is synced: the others are paused, being removed, or need a person.
var syncable = []string{"connecting", "active", "low_balance"}

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
// one when the supplier is due again, which is when its backoff says. A round waits on the syncs queue only for other
// rounds.
func (SyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       pg.QueueSyncs,
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

// InsertOpts implements river.JobArgsWithInsertOpts: the sweep is a sync job too, and waits with the rounds it enqueues.
func (SweepArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: pg.QueueSyncs} }

// Schedule is the syncing of the suppliers on one node.
type Schedule struct {
	cfg      Config
	node     *persist.Node
	settings syncer.Settings
	budget   time.Duration // roundsBudget; a test shortens it
}

// New adds the workers and the periodic sweep to the queue, before it starts.
func New(cfg Config) (*Schedule, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	// The projection maps a supplier's upstream names by the catalog, and the snapshot it holds is refreshed when it has
	// gone stale, so no poller of its own is started here.
	cat, err := catalog.Open(cfg.DB, cfg.Scope, catalog.Config{Logger: cfg.Logger})
	if err != nil {
		return nil, err
	}
	node, err := persist.New(persist.Config{
		DB: cfg.DB, Scope: cfg.Scope, Keys: cfg.Keys, Holder: cfg.Holder, LeaseTTL: leaseTTL, Now: cfg.Now, Catalog: cat,
	})
	if err != nil {
		return nil, err
	}
	s := &Schedule{cfg: cfg, node: node, settings: settings(), budget: roundsBudget}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(s.workSync)); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(s.workSweep)); err != nil {
		return nil, err
	}
	cfg.Queue.Every(cfg.SweepEvery, func() river.JobArgs { return SweepArgs{} })
	return s, nil
}

// check reports what a node must be given to sync at all, before anything is opened.
func (cfg Config) check() error {
	if cfg.DB == nil || cfg.Keys == nil || cfg.Egress == nil || cfg.Queue == nil || cfg.Registry == nil || cfg.Now == nil || cfg.Logger == nil {
		return errors.New("schedule: a database, keys, an egress client, a queue, adapters, a clock and a logger are required")
	}
	if cfg.SweepEvery <= 0 {
		return errors.New("schedule: the sweep needs a positive interval")
	}
	return nil
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
		due, err := store.New().ListSyncDue(ctx, tx, store.ListSyncDueParams{Statuses: syncable, Now: s.cfg.Now(), Max: sweepBatch})
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

// sync runs the rounds of one supplier. A supplier another node is syncing is that node's, one that is no longer in a
// status that is synced (it was paused or removed after its job was queued) is left alone, and a round whose lease was
// taken over has stored nothing: none of these is a failure.
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
	if !slices.Contains(syncable, session.Supplier.Status) {
		return nil
	}
	if err := s.rounds(ctx, s.engine(ctx, session)); err != nil && !errors.Is(err, syncer.ErrFenced) {
		return err
	}
	return nil
}

// rounds runs the engine, and while the supplier's limits hold a task up for a while, waits for them to reopen and runs
// it again over the same adapter: a Manager keeps how far a listing got only for as long as it lives, and a listing of
// more pages than one window allows could otherwise never be finished. A job waits at most roundsBudget in all, so that
// it does not hold a worker of the queue for longer, which the time now says on its own: a limit whose time has already
// passed would otherwise keep the loop going; what is still held up after that is the next job's.
func (s *Schedule) rounds(ctx context.Context, engine *syncer.Engine) error {
	end := s.cfg.Now().Add(s.budget)
	for {
		if err := engine.Round(ctx); err != nil {
			return err
		}
		now, until := s.cfg.Now(), engine.Blocked()
		if until.IsZero() || !now.Before(end) || until.Sub(now) > maxBlockedWait || until.After(end) {
			return nil
		}
		timer := time.NewTimer(max(until.Sub(now), time.Second)) // a second at least, so that a limit that is already over cannot spin
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// engine makes the engine of a supplier over its adapter, which it opens (it logs in) only when a task first needs it.
// A supplier that cannot be reached at all, for it has an exit this node lacks or no adapter, fails there too, as a
// failure of every task: the engine backs off and the supplier is not due again at once, instead of staying the
// longest overdue and taking the place of the suppliers that can be synced in every sweep.
func (s *Schedule) engine(ctx context.Context, session *persist.Session) *syncer.Engine {
	m := &openOnUse{open: func() (adapter.Manager, error) {
		a, c, err := s.reach(session.Supplier)
		if err != nil {
			s.cfg.Logger.Error("a supplier cannot be synced", "supplier", session.Supplier.ID.String(), "err", err)
			return nil, err
		}
		return a.Open(ctx, c, session.Logins())
	}}
	return syncer.New(session.Store(), m, s.settings, s.cfg.Now)
}

// reach is the adapter of a supplier and the client that sends its requests.
func (s *Schedule) reach(sup persist.Supplier) (adapter.Adapter, adapter.Client, error) {
	if sup.Exit != string(egress.Direct) {
		return nil, nil, fmt.Errorf("schedule: supplier %s: the exit %q is not one this node has", sup.ID, sup.Exit)
	}
	a, ok := s.cfg.Registry.Get(sup.Kind)
	if !ok {
		return nil, nil, fmt.Errorf("schedule: supplier %s: no adapter of kind %q", sup.ID, sup.Kind)
	}
	buckets := client.NewSharedLimiter(s.cfg.DB, s.cfg.Scope, a.Buckets(),
		client.Scopes{Address: "ip:" + sup.Origin + "|" + sup.Exit, Account: "account:" + sup.ID.String()}, s.cfg.Now)
	c, err := client.New(sup.Origin, s.cfg.Egress, buckets, s.cfg.Now)
	if err != nil {
		return nil, nil, err
	}
	return a, c, nil
}
