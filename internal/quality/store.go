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
		if err := m.prepareEndpoint(ctx, ep, now); err != nil {
			return err
		}
	}
	return nil
}

// staleSnapshot reports whether generation is older than the newest snapshot this endpoint published. Generation 0 is
// an unversioned test or a source that has not reloaded yet; once a version has been published, a zero is older than it.
// The caller holds e.mu.
func staleSnapshot(e *State, generation uint64) bool {
	return e.published > 0 && generation < e.published
}

// prepareEndpoint publishes one endpoint's configuration and restores its durable health the first time it reappears.
// The stored score and configuration are read before the endpoint lock: the monitor lock must never be taken under it,
// or a commit (monitor then endpoint) could deadlock with a snapshot build (endpoint then monitor).
func (m *Monitor) prepareEndpoint(ctx context.Context, ep suppliers.Routable, now time.Time) error {
	return m.prepareEndpointWith(ctx, ep, now, nil)
}

// prepareEndpointWith is prepareEndpoint with a test-only barrier between the preliminary generation check and the
// serialized write. Production passes nil.
func (m *Monitor) prepareEndpointWith(ctx context.Context, ep suppliers.Routable, now time.Time, barrier func(suppliers.Routable)) error {
	e := m.of(ep.ID)
	e.mu.Lock()
	if staleSnapshot(e, ep.Generation) {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	if barrier != nil {
		barrier(ep)
	}

	// The preliminary check above avoids queueing a snapshot that is already stale. The check below, the shared
	// configuration write and the local publish are serialized: a newer generation may publish while this call waits
	// for prepareMu, and this call must not write its older configuration after that.
	e.prepareMu.Lock()
	defer e.prepareMu.Unlock()
	saved, hasSaved := m.savedScore(ep.ID)
	stored, hasStored := m.storedConfig(ep.ID)
	cfg := configOf(ep)
	e.mu.Lock()
	if staleSnapshot(e, ep.Generation) {
		e.mu.Unlock()
		return nil
	}
	changing := e.current != cfg
	established := e.current != (EndpointConfig{})
	e.mu.Unlock()
	epoch, err := m.benchmarkEpochFor(ctx, ep, cfg, saved, hasSaved, stored, hasStored, changing, established)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if staleSnapshot(e, ep.Generation) {
		e.mu.Unlock()
		return nil
	}
	if ep.Generation > e.published {
		e.published = ep.Generation
	}
	e.credentials = ep.Credentials
	e.publish(ep, saved, hasSaved, epoch, m.fallbackClass())
	loaded := e.loaded
	e.owned = !ep.Platform()
	_, generation := probeFormat(ep.Formats)
	e.probeable = ep.Platform() && generation
	e.lastSeen = now
	e.mu.Unlock()
	if m.loop == nil || loaded {
		return nil
	}
	return m.loop.restore(ctx, e)
}

// benchmarkEpochFor returns the generation the endpoint's current configuration has in the store. A configuration
// that is already stored needs no write; a brand-new endpoint does not write until it has a score to record, because
// Prepare is also called for endpoints a snapshot is only looking at. A restart has no local established configuration,
// so a stored row that differs from the authoritative configuration is synchronized; after that, a stale snapshot is
// rejected instead of flipping the row back.
func (m *Monitor) benchmarkEpochFor(ctx context.Context, ep suppliers.Routable, cfg EndpointConfig, saved benchmarkState, hasSaved bool, stored benchmarkConfig, hasStored, changing, established bool) (int64, error) {
	switch {
	case hasStored && stored.config == cfg:
		return stored.epoch, nil
	case ep.Platform() && hasStored && !established:
		// A restart loads the authoritative supplier configuration while the shared row still names the configuration
		// this process (or a previous one) last published. No local configuration has been established yet, so this is
		// the startup sync; once established, a stale snapshot must not flip the shared row back.
		return m.ensureConfig(ctx, ep)
	case ep.Platform() && ((hasSaved && saved.config == cfg) || (changing && established)):
		return m.ensureConfig(ctx, ep)
	}
	return 0, nil
}

// publish moves the endpoint to ep's configuration and adopts the stored score when it belongs to that configuration
// and generation. The caller holds e.mu. A configuration change, or a newer generation learned from another node,
// voids the held score; the generation is durable, so a restart or the configuration returning cannot resurrect it.
func (e *State) publish(ep suppliers.Routable, saved benchmarkState, hasSaved bool, epoch int64, fallback string) {
	cfg := configOf(ep)
	if e.current != cfg {
		if epoch > 0 && e.benchmarkEpoch > 0 && epoch < e.benchmarkEpoch {
			return // a concurrent Prepare already published a newer generation
		}
		e.facts = nil                // facts belong to the configuration they were measured on
		e.evidence = classEvidence{} // and so do response fingerprints: another upstream's headers say nothing of this one
		e.class = fallback
		e.benchmark = benchmarkState{} // a product score recorded on another configuration
		e.current = cfg
		e.benchmarkEpoch = epoch
	} else if epoch > e.benchmarkEpoch {
		// Another node changed the configuration and changed it back (or changed it while this node held the same
		// configuration): the shared generation moved, so the row this node holds is no longer evidence.
		e.benchmark = benchmarkState{}
		e.benchmarkEpoch = epoch
	}
	// The store is the shared record: adopt a score another node recorded, or one from before a restart, as soon as
	// the configuration and generation it names are the ones this endpoint publishes.
	if hasSaved && saved.config == cfg && saved.epoch == e.benchmarkEpoch && newerScore(saved, e.benchmark) {
		e.benchmark = saved
	}
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
