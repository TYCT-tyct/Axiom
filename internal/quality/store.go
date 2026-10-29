package quality

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Module supplies quality's own tables to migration and startup checks.
func Module() pg.Module {
	files, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err)
	}
	return pg.Module{Name: "quality", Migrations: files}
}

const healthColumns = `endpoint_id,epoch,opened_at,restored_at,next_probe_at,last_probe_at,probe_lease_until,
 backoff,failures,sources,cause,probe_result,request_id,probe_owner,probe_credential,warned,mode`

func scanHealth(row interface{ Scan(...any) error }) (r healthRecord, err error) {
	err = row.Scan(&r.ID, &r.Epoch, &r.Opened, &r.Restored, &r.Next, &r.Last, &r.Lease, &r.Backoff, &r.Failures, &r.Sources,
		&r.Cause, &r.Result, &r.Request, &r.Owner, &r.Credential, &r.Warned, &r.Mode)
	return
}

func (l *Loop) load(ctx context.Context) error {
	return l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+healthColumns+` FROM quality.endpoint_health`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanHealth(rows)
			if err != nil {
				return err
			}
			e := l.monitor.of(r.ID)
			e.mu.Lock()
			e.record = r
			e.loaded = true
			e.mu.Unlock()
		}
		return rows.Err()
	})
}

func saveHealth(ctx context.Context, tx pg.Tx, c change, now time.Time) (bool, error) {
	r := c.record
	_, err := tx.Exec(ctx, `INSERT INTO quality.endpoint_health (endpoint_id,epoch,request_id,probe_owner,probe_credential,mode)
 VALUES ($1,0,$2,$2,$2,$3) ON CONFLICT DO NOTHING`, r.ID, uuid.UUID{}, r.Mode)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE quality.endpoint_health SET epoch=$2,opened_at=$3,restored_at=$4,next_probe_at=$5,
 last_probe_at=$6,probe_lease_until=$7,backoff=$8,failures=$9,sources=$10,cause=$11,probe_result=$12,
 request_id=$13,probe_owner=$14,probe_credential=$15,warned=$16,mode=$17 WHERE endpoint_id=$1 AND epoch=$18
 AND ($19='00000000-0000-0000-0000-000000000000'::uuid OR
 (probe_owner=$19 AND probe_lease_until=$20 AND probe_lease_until>$21))`,
		r.ID, r.Epoch, r.Opened, r.Restored, r.Next, r.Last, r.Lease, r.Backoff, r.Failures, r.Sources, r.Cause, r.Result, r.Request, r.Owner, r.Credential, r.Warned, r.Mode, c.before, c.probeOwner, c.probeLease, now.UnixNano())
	return err == nil && tag.RowsAffected() == 1, err
}

// Persist one transition and its event atomically. The epoch also makes an ambiguous commit safe to retry.
func (l *Loop) write(ctx context.Context, c change) error {
	e := l.monitor.of(c.record.ID)
	e.mu.Lock()
	stale := c.generation != e.generation
	if stale {
		e.pending--
	}
	e.mu.Unlock()
	if stale {
		return nil
	}
	current, err := l.persist(ctx, c)
	if err != nil {
		return fmt.Errorf("quality: persist health: %w", err)
	}
	e.mu.Lock()
	e.pending--
	if current != c.record {
		e.generation++
		e.record = current
		e.window = window{}
	}
	e.mu.Unlock()
	if current == c.record && c.kind != "" {
		l.transitionMetric(ctx, c)
	}
	return nil
}

func (l *Loop) persist(ctx context.Context, c change) (healthRecord, error) {
	var current healthRecord
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		won, err := saveHealth(ctx, tx, c, l.now())
		if err != nil {
			return err
		}
		current, err = scanHealth(tx.QueryRow(ctx, `SELECT `+healthColumns+` FROM quality.endpoint_health WHERE endpoint_id=$1`, c.record.ID))
		if err != nil || !won || c.kind == "" {
			return err
		}
		if err := recordEvent(ctx, tx, c); err != nil {
			return err
		}
		if c.record.Mode == Enforce {
			return alert.Persist(ctx, tx, uuid.NewV7(), healthSignal(c))
		}
		return nil
	})
	return current, err
}

func recordEvent(ctx context.Context, tx pg.Tx, c change) error {
	at := c.record.Opened
	if at == 0 {
		at = c.record.Restored
	}
	if c.kind != "still_down" {
		// Retain the first transition and coalesce further flapping in its ten-minute group.
		tag, err := tx.Exec(ctx, `UPDATE quality.health_events SET occurrences=occurrences+1,signal_epoch=$3,signal_kind=$4
   WHERE endpoint_id=$1 AND epoch=(SELECT max(epoch) FROM quality.health_events WHERE endpoint_id=$1)
   AND at >= $2 AND mode=$5 AND kind IN ('removed','owned_endpoint_removed','restored','would_remove','would_restore','flapping')`, c.record.ID, at-int64(windowIdle), c.record.Epoch, c.kind, c.record.Mode)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = tx.Exec(ctx, `UPDATE quality.health_events SET kind='flapping' WHERE endpoint_id=$1
    AND epoch=(SELECT max(epoch) FROM quality.health_events WHERE endpoint_id=$1)`, c.record.ID)
			return err
		}
	}

	_, err := tx.Exec(ctx, `INSERT INTO quality.health_events (endpoint_id,epoch,at,kind,cause,failures,sources,request_id,signal_epoch,signal_kind,mode)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$2,$4,$9)`, c.record.ID, c.record.Epoch, at, c.kind, c.record.Cause, c.record.Failures, c.record.Sources, c.record.Request, c.record.Mode)
	return err
}

func (l *Loop) claim(ctx context.Context, e *State, now time.Time) (healthRecord, bool, error) {
	e.mu.Lock()
	r := e.record
	pending := e.pending
	e.mu.Unlock()
	if pending != 0 {
		return r, false, nil
	}
	var won bool
	var current healthRecord
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE quality.endpoint_health SET probe_owner=$3,probe_lease_until=$4,mode=$6
   WHERE endpoint_id=$1 AND epoch=$2 AND opened_at>0 AND next_probe_at<=$5 AND probe_lease_until<=$5`,
			r.ID, r.Epoch, l.owner, now.Add(probeLease).UnixNano(), now.UnixNano(), l.monitor.mode)
		won = err == nil && tag.RowsAffected() == 1
		if err != nil {
			return err
		}
		current, err = scanHealth(tx.QueryRow(ctx, `SELECT `+healthColumns+` FROM quality.endpoint_health WHERE endpoint_id=$1`, r.ID))
		return err
	})
	if err != nil {
		return r, false, err
	}
	e.mu.Lock()
	if e.pending == 0 && e.record.Epoch == r.Epoch {
		e.record = current
	} else {
		won = false
	}
	e.mu.Unlock()
	return current, won, nil
}

// Prepare restores reappearing endpoints before a routing snapshot can publish a new live pointer.
func (m *Monitor) Prepare(ctx context.Context, endpoints []suppliers.Routable) error {
	if m.loop == nil {
		return nil
	}
	for _, ep := range endpoints {
		e := m.Live(ep.ID, ep.Credentials)
		e.mu.Lock()
		loaded := e.loaded
		e.owned = !ep.Platform
		e.lastSeen = m.loop.now()
		e.mu.Unlock()
		if loaded {
			continue
		}
		err := m.loop.restore(ctx, e)
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) restore(ctx context.Context, e *State) error {
	return l.db.ReadTx(ctx, l.scope, func(tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+healthColumns+` FROM quality.endpoint_health WHERE endpoint_id=$1`, e.id)
		if err != nil {
			return err
		}
		defer rows.Close()
		var r healthRecord
		found := rows.Next()
		if found {
			r, err = scanHealth(rows)
			if err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if found && e.pending == 0 {
			e.record = r
		}
		e.loaded = true
		return nil
	})
}
