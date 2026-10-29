package suppliers

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

type syncAlertArgs struct {
	After uuid.UUID `json:"after"`
}

func (syncAlertArgs) Kind() string { return "suppliers.sync_alerts" }
func (syncAlertArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

// RegisterSyncAlerts registers the durable sync-event bridge independently of supplier network polling.
func RegisterSyncAlerts(cfg SyncConfig) error {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	w := syncAlertWorker{cfg: cfg}

	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(w.work)); err != nil {
		return err
	}
	cfg.Queue.Every(time.Minute, func() river.JobArgs { return syncAlertArgs{} })
	return nil
}

type syncAlertWorker struct{ cfg SyncConfig }

func (w syncAlertWorker) work(ctx context.Context, job *river.Job[syncAlertArgs]) error {
	cfg := w.cfg
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var ids []uuid.UUID
	err := cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		if job.Args.After == uuid.Nil() {
			if err := emitSupplierAvailability(ctx, tx, cfg.Clock()); err != nil {
				return err
			}
		}
		var err error
		ids, err = readSupplierAlertIDs(ctx, tx, `SELECT supplier_id FROM suppliers.sync_states WHERE supplier_id>$1 ORDER BY supplier_id LIMIT 20`, job.Args.After)
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error { return forwardSyncAlerts(ctx, tx, id) }); err != nil {
			return err
		}
	}
	if len(ids) < 20 {
		return nil
	}
	return cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		_, err := cfg.Queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: syncAlertArgs{After: ids[len(ids)-1]}}})
		return err
	})
}

type syncAlertPosition struct {
	Initialized bool
	Version     int64
	Seq         int
	Attention   map[string]syncAlertFault
	Stopped     map[string]syncAlertFault
}

type syncAlertEvent struct {
	version  int64
	seq      int
	kind     string
	subject  string
	why      string
	previous string
}

func forwardSyncAlerts(ctx context.Context, tx pg.Tx, supplier uuid.UUID) error {
	source := "suppliers.sync/" + supplier.String()
	raw, err := alert.LockPosition(ctx, tx, source)
	if err != nil {
		return err
	}
	p := syncAlertPosition{Attention: map[string]syncAlertFault{}, Stopped: map[string]syncAlertFault{}}
	if err = jsonx.Unmarshal(raw, &p); err != nil {
		return err
	}
	var state syncer.State
	var version int64
	if err = tx.QueryRow(ctx, `SELECT version,state FROM suppliers.sync_states WHERE supplier_id=$1`, supplier).Scan(&version, &raw); err != nil {
		return err
	}
	if err = jsonx.Unmarshal(raw, &state); err != nil {
		return err
	}
	if !p.Initialized {
		// Existing recovered history must not page operators on the first deployment. The state/version
		// fence makes this a consistent starting point; later commits always have a greater version.
		if err = initializeSyncAlerts(ctx, tx, supplier, state, version, &p); err != nil {
			return err
		}
		p.Initialized, p.Version, p.Seq = true, version, 2147483647
	}
	events, err := readSyncAlertEvents(ctx, tx, supplier, version, p)
	if err != nil {
		return err
	}
	for _, e := range events {
		if err = forwardSyncEvent(ctx, tx, supplier, e, state, &p); err != nil {
			return err
		}
		p.Version, p.Seq = e.version, e.seq
	}
	if len(events) < 1000 {
		if err = recoverSyncAlerts(ctx, tx, supplier, state, version, &p); err != nil {
			return err
		}
	}
	return alert.SavePosition(ctx, tx, source, p)
}

func readSyncAlertEvents(ctx context.Context, tx pg.Tx, id uuid.UUID, version int64, p syncAlertPosition) ([]syncAlertEvent, error) {
	rows, err := tx.Query(ctx, `SELECT version,seq,kind,subject,why,old_value FROM suppliers.sync_events WHERE supplier_id=$1 AND (version,seq)>($2,$3) AND version<=$4 ORDER BY version,seq LIMIT 1000`, id, p.Version, p.Seq, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []syncAlertEvent
	for rows.Next() {
		var e syncAlertEvent
		if err = rows.Scan(&e.version, &e.seq, &e.kind, &e.subject, &e.why, &e.previous); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func forwardSyncEvent(ctx context.Context, tx pg.Tx, supplier uuid.UUID, e syncAlertEvent, state syncer.State, p *syncAlertPosition) error {
	kind, key, resolved := "", supplier.String(), false
	switch e.kind {
	case "needs_attention":
		kind = "suppliers.sync_attention"
		if e.why == "the supplier rejected the account credentials" {
			kind = "suppliers.login_expired"
		}
		fault, err := syncFailureBaseline(e, state.Tasks[syncer.TaskBalance].LastOK)
		if err != nil {
			return err
		}
		p.Attention[kind] = fault
	case "task_halted":
		return forwardStoppedTask(ctx, tx, supplier, e, state, p)
	case "balance_low", "balance_recovered":
		kind, resolved = "suppliers.low_balance", e.kind == "balance_recovered"
	default:
		return nil
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: kind, Key: key, Severity: "warning", Resolved: resolved})
}

func recoverSyncAlerts(ctx context.Context, tx pg.Tx, id uuid.UUID, state syncer.State, version int64, p *syncAlertPosition) error {
	for _, kind := range []string{"suppliers.login_expired", "suppliers.sync_attention"} {
		fault, pending := p.Attention[kind]
		if pending && state.Status == syncer.Normal && fault.recovered(version, state.Tasks[syncer.TaskBalance].LastOK) {
			if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: kind, Key: id.String(), Severity: "info", Resolved: true}); err != nil {
				return err
			}
			delete(p.Attention, kind)
		}
	}
	for task := syncer.TaskBalance; task <= syncer.TaskCharges; task++ {
		fault, stopped := p.Stopped[task.String()]
		if stopped && !state.Tasks[task].Halted && fault.recovered(version, state.Tasks[task].LastOK) {
			if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.task_stopped", Key: id.String() + "/" + task.String(), Severity: "info", Resolved: true}); err != nil {
				return err
			}
			delete(p.Stopped, task.String())
		}
	}
	return nil
}

func initializeSyncAlerts(ctx context.Context, tx pg.Tx, id uuid.UUID, state syncer.State, version int64, p *syncAlertPosition) error {
	if state.Status == syncer.NeedsAttention {
		if err := forwardSyncEvent(ctx, tx, id, syncAlertEvent{kind: "needs_attention", why: state.Reason, version: version}, state, p); err != nil {
			return err
		}
	}
	for task := syncer.TaskBalance; task <= syncer.TaskCharges; task++ {
		if state.Tasks[task].Halted {
			if err := forwardSyncEvent(ctx, tx, id, syncAlertEvent{kind: "task_halted", subject: task.String(), version: version}, state, p); err != nil {
				return err
			}
		}
	}
	if state.LowNotified {
		return forwardSyncEvent(ctx, tx, id, syncAlertEvent{kind: "balance_low"}, state, p)
	}
	return nil
}

func emitSupplierAvailability(ctx context.Context, tx pg.Tx, now time.Time) error {
	const source = "suppliers.availability"
	raw, err := alert.LockPosition(ctx, tx, source)
	if err != nil {
		return err
	}
	var position struct{ After uuid.UUID }
	if err = jsonx.Unmarshal(raw, &position); err != nil {
		return err
	}
	available, err := availableSupplierSources(ctx, tx, now)
	if err != nil {
		return err
	}
	ids, err := readSupplierAlertIDs(ctx, tx, `SELECT id FROM suppliers.suppliers WHERE status IN ('connecting','active','low_balance','needs_attention') AND id>$1 ORDER BY id LIMIT 100`, position.After)
	if err != nil {
		return err
	}
	var configured bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM suppliers.suppliers WHERE status IN ('connecting','active','low_balance','needs_attention'))`).Scan(&configured); err != nil {
		return err
	}
	position.After = uuid.Nil()
	if len(ids) == 100 {
		position.After = ids[len(ids)-1]
	}
	if err = alert.SavePosition(ctx, tx, source, position); err != nil {
		return err
	}
	for _, id := range ids {
		if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.sources_unavailable", Key: id.String(), Severity: "critical", Resolved: available[id]}); err != nil {
			return err
		}
	}
	if !configured {
		return nil
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.exhausted", Key: "all", Severity: "critical", Resolved: len(available) > 0, Fields: map[string]string{"available": fmt.Sprint(len(available))}})
}

func readSupplierAlertIDs(ctx context.Context, tx pg.Tx, query string, after uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, query, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func availableSupplierSources(ctx context.Context, tx pg.Tx, now time.Time) (map[uuid.UUID]bool, error) {
	rows, err := store.New().ListRoutable(ctx, tx)
	if err != nil {
		return nil, err
	}
	available := map[uuid.UUID]bool{}
	for _, row := range rows {
		if row.ExpiresAt == nil || now.Before(*row.ExpiresAt) {
			available[row.SupplierID] = true
		}
	}
	return available, nil
}

// A recovery is a newer successful observation, independent of wall-clock skew between nodes and PostgreSQL.
type syncAlertFault struct {
	Version int64
	LastOK  time.Time
}

func (f syncAlertFault) recovered(version int64, lastOK time.Time) bool {
	return version > f.Version && !lastOK.IsZero() && !lastOK.Equal(f.LastOK)
}

func syncFailureBaseline(e syncAlertEvent, latest time.Time) (syncAlertFault, error) {
	// Old nodes omit the baseline. Conservatively wait for one more success observed by this consumer.
	if e.previous != "" {
		var err error
		latest, err = time.Parse(time.RFC3339Nano, e.previous)
		if err != nil {
			return syncAlertFault{}, fmt.Errorf("suppliers: invalid sync failure baseline: %w", err)
		}
	}
	return syncAlertFault{Version: e.version, LastOK: latest}, nil
}

func forwardStoppedTask(ctx context.Context, tx pg.Tx, supplier uuid.UUID, e syncAlertEvent, state syncer.State, p *syncAlertPosition) error {
	for task := syncer.TaskBalance; task <= syncer.TaskCharges; task++ {
		if task.String() != e.subject {
			continue
		}
		fault, err := syncFailureBaseline(e, state.Tasks[task].LastOK)
		if err != nil {
			return err
		}
		p.Stopped[e.subject] = fault
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.task_stopped", Key: supplier.String() + "/" + e.subject, Severity: "warning"})
	}
	return nil
}
