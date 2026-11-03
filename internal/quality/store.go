package quality

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"
	"uuid"

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
			return persistHealthSignal(ctx, tx, c)
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
	// Retain the first transition and coalesce further changes in its ten-minute group. The group's kind says what the
	// endpoint is doing now: offline once a failed re-check confirms it, flapping once it came back.
	tag, err := tx.Exec(ctx, `UPDATE quality.health_events SET occurrences=occurrences+1,signal_epoch=$3,signal_kind=$4,
   kind=CASE WHEN $4='offline' THEN 'offline' ELSE 'flapping' END
   WHERE endpoint_id=$1 AND epoch=(SELECT max(epoch) FROM quality.health_events WHERE endpoint_id=$1)
   AND at >= $2 AND mode=$5 AND kind IN ('removed','owned_endpoint_removed','offline','restored','would_remove','would_restore','flapping')`,
		c.record.ID, at-int64(windowIdle), c.record.Epoch, c.kind, c.record.Mode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	_, err = tx.Exec(ctx, `INSERT INTO quality.health_events (endpoint_id,epoch,at,kind,cause,failures,sources,request_id,signal_epoch,signal_kind,mode)
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
	now := time.Now()
	if m.loop != nil {
		now = m.loop.now()
	}
	for _, ep := range endpoints {
		e := m.of(ep.ID)
		e.mu.Lock()
		// A routing build and the health loop both publish configurations. The build may have read an older supplier
		// snapshot and resume after the loop published a newer one; accepting that older generation would bring back a
		// configuration whose facts are already gone. Generation 0 is an unversioned test or a source that has not
		// reloaded yet; once a version has been published, a zero is older than it.
		if e.published > 0 && ep.Generation < e.published {
			e.mu.Unlock()
			continue
		}
		if ep.Generation > e.published {
			e.published = ep.Generation
		}
		e.credentials = ep.Credentials
		if cfg := configOf(ep); e.current != cfg {
			e.facts = nil                // facts belong to the configuration they were measured on
			e.evidence = classEvidence{} // and so do response fingerprints: another upstream's headers say nothing of this one
			e.class = m.fallbackClass()
			e.current = cfg
		}
		loaded := e.loaded
		e.owned = !ep.Platform()
		_, generation := probeFormat(ep.Formats)
		e.probeable = ep.Platform() && generation
		e.lastSeen = now
		e.mu.Unlock()
		if m.loop == nil || loaded {
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
