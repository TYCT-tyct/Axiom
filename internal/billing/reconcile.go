package billing

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// reconcileEvery is how often the ledger checks itself (docs/domains/billing.md「账本」). A node that starts, or a new
// leader of the queue, runs it again at once; it only reads.
const reconcileEvery = 24 * time.Hour

// maxProblemsLogged bounds the log of one run: a broken ledger can have a problem for every account, and the count says
// the rest.
const maxProblemsLogged = 20

// Reconcile lists what the daily reconciliation refuses: a transaction whose entries do not balance, a balance that
// differs from its book. It reads every tenant, so it runs in the platform scope.
func Reconcile(ctx context.Context, db *pg.DB, platform pg.Scope) (problems []string, err error) {
	err = db.Tx(ctx, platform, func(tx pg.Tx) error {
		problems, err = queries.Unbalanced(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("billing: reconcile: %w", err)
	}
	return problems, nil
}

// ReconcileConfig is what the daily reconciliation job needs: the database, the platform scope that only cmd/ builds,
// the queue the job runs on, where its findings go, and the meter of its counter (nil for none).
type ReconcileConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
	Meter  metric.Meter
}

// RegisterReconcile adds the daily reconciliation to the queue, before it starts. It cannot take a *Billing: the queue
// starts first and billing, which needs the holder, after, and the check needs only the database.
func RegisterReconcile(cfg ReconcileConfig) error {
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	r := &reconciler{
		db: cfg.DB, scope: cfg.Scope, logger: cfg.Logger,
		problems: counter(meter, "axiom.billing.reconcile.problems", "Problems the daily ledger reconciliation found."),
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(r.work)); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	cfg.Queue.Every(reconcileEvery, func() river.JobArgs { return reconcileArgs{} })
	return nil
}

// singleRun is what the args of a periodic job share: a run still waiting or working covers the next, so a node
// restarted over and over does not stack them, and the job waits on the money queue only for other money jobs.
type singleRun struct{}

// InsertOpts implements river.JobArgsWithInsertOpts.
func (singleRun) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// reconcileArgs is the job of one run.
type reconcileArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (reconcileArgs) Kind() string { return "billing.reconcile" }

type reconciler struct {
	db       *pg.DB
	scope    pg.Scope
	logger   *slog.Logger
	problems metric.Int64Counter
}

// work runs the reconciliation and reports what it refuses at error, and counts it. A ledger that does not reconcile is
// not a failed job: the job did its work, and retrying it would only find the same.
func (r *reconciler) work(ctx context.Context, _ *river.Job[reconcileArgs]) error {
	var problems []string
	err := r.db.Tx(ctx, r.scope, func(tx pg.Tx) error {
		var err error
		problems, err = queries.Unbalanced(ctx, tx)
		if err != nil {
			return err
		}
		if err = repairSettlementAlerts(ctx, tx); err != nil {
			return err
		}
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "billing.reconciliation_difference", Key: "ledger", Severity: "critical", Fields: map[string]string{"value": strconv.Itoa(len(problems))}, Resolved: len(problems) == 0})
	})
	if err != nil {
		return err
	}
	for i, p := range problems {
		if i == maxProblemsLogged {
			r.logger.Error("billing: more ledger problems than are listed", "problems", len(problems))
			break
		}
		r.logger.Error("billing: the ledger does not reconcile", "problem", p)
	}
	r.problems.Add(ctx, int64(len(problems)))
	return nil
}
