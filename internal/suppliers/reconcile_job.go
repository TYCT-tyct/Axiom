package suppliers

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// ReconcileJobConfig supplies the queue, platform-scoped database and alert logger.
// Clock is nil for time.Now; the reconciliation policy is DefaultReconcileConfig.
type ReconcileJobConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
	Clock  func() time.Time
}

// RegisterReconcile registers before Queue.Start. Only the queue leader schedules
// periodic passes; any worker may execute one. A restart also schedules a pass.
func RegisterReconcile(cfg ReconcileJobConfig) error {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	r := &reconcileJob{deps: cfg, policy: DefaultReconcileConfig()}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(r.work)); err != nil {
		return fmt.Errorf("suppliers: register reconciliation: %w", err)
	}
	cfg.Queue.Every(time.Minute, func() river.JobArgs { return reconcileArgs{} })
	return nil
}

// After is durable pagination, not worker-local state: another node can pick up
// the next page. Periodic passes start at the beginning to revisit missing charges.
type reconcileArgs struct {
	After ReconcileCursor `json:"after"`
}

func (reconcileArgs) Kind() string { return "suppliers.reconcile" }

// InsertOpts implements river.JobArgsWithInsertOpts: the next page of a run covers the run, and the reconciliation is a
// money job, so it waits only for other money jobs.
func (reconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

type reconcileJob struct {
	deps   ReconcileJobConfig
	policy ReconcileConfig
}

func (r *reconcileJob) work(ctx context.Context, job *river.Job[reconcileArgs]) error {
	cfg := r.policy
	cfg.After = job.Args.After
	err := r.deps.DB.Tx(ctx, r.deps.Scope, func(tx pg.Tx) error {
		report, err := Reconcile(ctx, tx, r.deps.Clock(), cfg)
		if err != nil {
			return err
		}
		if !report.Next.At.IsZero() {
			_, err = r.deps.Queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: reconcileArgs{After: report.Next}}})
			if err != nil {
				return err
			}
		}
		// Like pg's discarded-job alerts, report before commit: a crash cannot
		// commit a delisting without reporting it. A rollback can repeat alerts;
		// charge revisions and events remain transactional and idempotent.
		r.report(ctx, report)
		return persistReconcileAlerts(ctx, tx, report)
	})
	if err != nil {
		return fmt.Errorf("suppliers: reconcile: %w", err)
	}
	return nil
}

func persistReconcileAlerts(ctx context.Context, tx pg.Tx, report ReconcileReport) error {
	sources := make([]string, len(report.Unmatched))
	for i, a := range report.Unmatched {
		sources[i] = fmt.Sprintf("suppliers.unmatched/%s/%d", a.Request, a.Seq)
	}
	unmatched, err := alert.ClaimSources(ctx, tx, sources)
	if err != nil {
		return err
	}
	for _, finding := range []struct {
		key, severity string
		count         int
	}{{"under_estimated", "critical", len(report.Delisted)}, {"under_estimated_unchanged", "warning", len(report.UnderUnchanged)}, {"over_estimated", "warning", len(report.Over)}, {"held", "warning", len(report.Held)}, {"unmatched", "warning", int(unmatched)}, {"invalid", "critical", len(report.Violations)}} {
		if finding.count > 0 {
			if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.reconciliation_difference", Key: finding.key, Severity: finding.severity, Fields: map[string]string{"value": strconv.Itoa(finding.count)}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *reconcileJob) report(ctx context.Context, report ReconcileReport) {
	for _, id := range report.Delisted {
		r.deps.Logger.ErrorContext(ctx, "suppliers.reconcile.under_estimated", "endpoint_id", id.String())
	}
	for _, id := range report.UnderUnchanged {
		r.deps.Logger.WarnContext(ctx, "suppliers.reconcile.under_estimated_unchanged", "charge_id", id.String())
	}
	for _, id := range report.Over {
		r.deps.Logger.WarnContext(ctx, "suppliers.reconcile.over_estimated", "charge_id", id.String())
	}
	for _, id := range report.Held {
		r.deps.Logger.WarnContext(ctx, "suppliers.reconcile.held", "charge_id", id.String())
	}
	for _, a := range report.Unmatched {
		r.deps.Logger.WarnContext(ctx, "suppliers.reconcile.unmatched", "request_id", a.Request.String(), "attempt_seq", a.Seq)
	}
	for _, problem := range report.Violations {
		r.deps.Logger.ErrorContext(ctx, "suppliers.reconcile.invalid", "problem", problem)
	}
}
