package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// Retention is the hot window of the call log: a monthly partition whose whole month is older than HotDays is copied
// to the object store and dropped from PostgreSQL once that copy is confirmed. Zero keeps every month. It is admin
// data; the saved row overrides defaultRetention.
type Retention struct {
	HotDays int `json:"hot_days"`
}

// ErrRetention rejects a window outside the contract at the admin boundary and reports a saved one that is damaged.
var ErrRetention = errors.New("observability: invalid retention")

// defaultRetention is three months, longer than the console's oldest query window and short enough that the call log
// does not grow without bound. A deployment with no object store keeps everything regardless: there is no confirmed
// archive to drop, and losing records is worse than keeping them.
func defaultRetention() Retention { return Retention{HotDays: 90} }

func (r Retention) validate() error {
	if r.HotDays < 0 || r.HotDays > 3650 {
		return ErrRetention
	}
	return nil
}

// GetRetention returns the saved window, or the code default before the first save.
func GetRetention(ctx context.Context, tx pg.Tx) (Retention, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT body FROM observability.retention WHERE singleton`).Scan(&raw)
	if errors.Is(err, pg.ErrNoRows) {
		return defaultRetention(), nil
	}
	if err != nil {
		return Retention{}, err
	}
	var r Retention
	if err = jsonx.Unmarshal(raw, &r); err != nil {
		return Retention{}, err
	}
	// The row is a boundary like a request: a value outside the contract would otherwise schedule an archive window
	// that the code cannot represent. The write path refuses these, so this only reports damage.
	if err = r.validate(); err != nil {
		return Retention{}, fmt.Errorf("observability: saved retention: %w", err)
	}
	return r, nil
}

// SetRetention validates and saves the window. A missing row is created; the singleton constraint keeps it to one.
func SetRetention(ctx context.Context, tx pg.Tx, r Retention) error {
	if err := r.validate(); err != nil {
		return err
	}
	raw, err := jsonx.MarshalCanonical(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observability.retention(singleton,body) VALUES(true,$1)
 ON CONFLICT(singleton) DO UPDATE SET body=$1`, raw)
	return err
}

// The retention job's fixed names. retentionJobRole is created by the migration bootstrap and owns the two log tables,
// because PostgreSQL lets only a table's owner create or drop its partitions (00015_retention.sql). archivePrefix
// versions the object layout; a future format change writes a new prefix instead of misreading old objects.
const (
	retentionJobRole       = "axiom_retention_job"
	retentionEvery         = 24 * time.Hour
	partitionsAhead        = 12
	archivePrefix          = "call-log/v1/"
	archiveManifestVersion = 1
	archiveMonthBudget     = 2 * time.Minute
	// retentionRoleCheckTimeout bounds the startup check: a login that cannot set the job role must fail serve with a
	// clear message, not hang on a lock or a slow database.
	retentionRoleCheckTimeout = 10 * time.Second
)

// CheckRetentionRole verifies that the retention login can act as the job role and use the migration's schema grants.
// It runs at startup when AXIOM_RETENTION_DATABASE_URL is set: a database migrated before the deployment created the
// login has the job role but no membership, and the job would otherwise fail on every SET ROLE while the node served.
func CheckRetentionRole(ctx context.Context, db *pg.DB, scope pg.Scope) error {
	ctx, cancel := context.WithTimeout(ctx, retentionRoleCheckTimeout)
	defer cancel()
	var ok bool
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		if err := tx.SetRole(ctx, retentionJobRole); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT CASE
 WHEN to_regclass('observability.retention') IS NULL THEN false
 ELSE has_schema_privilege(current_user, 'observability', 'CREATE')
  AND has_table_privilege(current_user, 'observability.retention', 'SELECT')
END`).Scan(&ok)
	})
	if err != nil {
		return fmt.Errorf("retention login cannot act as %s; run deploy/postgres/roles.sh: %w", retentionJobRole, err)
	}
	if !ok {
		return fmt.Errorf("retention login lacks the observability grants of %s: run migrations and deploy/postgres/roles.sh", retentionJobRole)
	}
	return nil
}

// ArchiveStore is the part of platform/objectstore the archive uses. A nil Store turns archiving off: partitions are
// kept even past the window, because a drop without a confirmed archive loses records.
type ArchiveStore interface {
	Put(ctx context.Context, up objectstore.Upload) (objectstore.ObjectInfo, error)
	Get(ctx context.Context, key string) (*objectstore.Object, error)
	Head(ctx context.Context, key string) (objectstore.ObjectInfo, error)
}

// RetentionConfig is what the retention job needs: the database, the platform scope only cmd/ builds, the queue it
// runs on, the archive store (nil when the deployment has no bucket), where its log goes and the meter for its counters
// (nil for none).
type RetentionConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Store  ArchiveStore
	Logger *slog.Logger
	Meter  metric.Meter
}

// RegisterRetention adds the call log's partition maintenance to the archive queue: a run every day, on whichever node
// leads the queue. Each run creates the next twelve months' partitions and archives the months that left the hot
// window; a run that is still working covers the next, so a restart loop does not stack archives.
func RegisterRetention(cfg RetentionConfig) error {
	r := newRetainer(cfg)
	work := river.WorkFunc(func(ctx context.Context, _ *river.Job[retentionArgs]) error {
		return r.run(ctx, time.Now().UTC())
	})
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), work); err != nil {
		return fmt.Errorf("observability: %w", err)
	}
	cfg.Queue.Every(retentionEvery, func() river.JobArgs { return retentionArgs{} })
	return nil
}

// retentionArgs is the job of one run.
type retentionArgs struct{}

// Kind implements river.JobArgs.
func (retentionArgs) Kind() string { return "observability.retention" }

// InsertOpts implements river.JobArgsWithInsertOpts: a run still waiting or working covers the next, and the archive
// queue keeps housekeeping away from money work.
func (retentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

func newRetainer(cfg RetentionConfig) *retainer {
	meter := cfg.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("observability")
	}
	return &retainer{
		db: cfg.DB, scope: cfg.Scope, store: cfg.Store, logger: cfg.Logger,
		partitions: counter(meter, "axiom.observability.retention.partitions", "Call-log partitions archived or dropped."),
	}
}

type retainer struct {
	db         *pg.DB
	scope      pg.Scope
	store      ArchiveStore
	logger     *slog.Logger
	partitions metric.Int64Counter
}

// run is one retention pass. It creates the partitions ahead whatever the configuration, then archives and drops the
// months past the window; the store being nil or the window being 0 keeps them. One month's failure does not keep the
// others from completing, and is returned so the queue retries; a month already archived and confirmed by an earlier
// run is only dropped, so a crash between the upload and the drop repairs itself.
func (r *retainer) run(ctx context.Context, now time.Time) error {
	if err := r.ensurePartitions(ctx, now); err != nil {
		return err
	}
	window, err := r.window(ctx)
	if err != nil {
		return err
	}
	if r.store == nil {
		r.logger.Info("observability: call-log retention keeps every partition: no object store is configured")
		return nil
	}
	if window.HotDays == 0 {
		r.logger.Info("observability: call-log retention keeps every partition: the hot window is 0")
		return nil
	}
	months, err := r.pastWindow(ctx, now.AddDate(0, 0, -window.HotDays))
	if err != nil {
		return err
	}
	var errs []error
	for _, month := range months {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		// Leave time to drop what was just archived; the next run continues an archive that does not fit.
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < archiveMonthBudget {
			r.logger.Info("observability: call-log retention stops for this run", "remaining_months", len(months))
			break
		}
		if err := r.archiveMonth(ctx, month); err != nil {
			r.logger.ErrorContext(ctx, "observability: call-log retention failed for a month", "month", monthKey(month), "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", monthKey(month), err))
		}
	}
	return errors.Join(errs...)
}

// window reads the saved hot window in its own transaction, so the caller holds no database transaction during the
// bucket transfers.
func (r *retainer) window(ctx context.Context) (Retention, error) {
	var window Retention
	err := r.db.ReadTx(ctx, r.scope, func(tx pg.Tx) error {
		var err error
		window, err = GetRetention(ctx, tx)
		return err
	})
	return window, err
}

// ensurePartitions creates the current month and the next partitionsAhead months for both tables. It checks for each
// partition before creating it, so a steady state takes no locks on the parent and does not rewrite the policies of
// partitions that already exist. A partition that a month's writes already reached through the default partition
// makes the create fail (PostgreSQL cannot move its rows); the run reports it instead of guessing.
func (r *retainer) ensurePartitions(ctx context.Context, now time.Time) error {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return r.db.Tx(ctx, r.scope, func(tx pg.Tx) error {
		if err := tx.SetRole(ctx, retentionJobRole); err != nil {
			return err
		}
		for i := 0; i <= partitionsAhead; i++ {
			month := first.AddDate(0, i, 0)
			for _, table := range []string{"call_log", "call_attempt"} {
				if err := ensurePartition(ctx, tx, table, month); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func ensurePartition(ctx context.Context, tx pg.Tx, table string, month time.Time) error {
	name := table + "_" + month.Format("2006_01")
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "observability."+name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	upper := month.AddDate(0, 1, 0)
	create := fmt.Sprintf(`CREATE TABLE observability.%s PARTITION OF observability.%s
 FOR VALUES FROM (%s) TO (%s)`, name, table,
		quoteDate(month), quoteDate(upper))
	if _, err := tx.Exec(ctx, create); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	// Partitions do not inherit policies, and the startup audit wants forced row-level security on every table. The
	// expression is 00005_scope_once's statement-level form; a per-row form is an audit failure.
	policy := fmt.Sprintf(`ALTER TABLE observability.%s ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON observability.%s USING (
	(SELECT platform.in_platform_scope())
	OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
	OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))`, name, name)
	if _, err := tx.Exec(ctx, policy); err != nil {
		return fmt.Errorf("protect %s: %w", name, err)
	}
	return nil
}

// quoteDate writes a date literal in the form the migrations use: a date, so PostgreSQL reads it in the server's
// timezone exactly as the partition bounds that already exist were read.
func quoteDate(t time.Time) string { return "'" + t.Format("2006-01-02") + "'" }
