package kv

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// holdEvery is how often the connection that holds the node's name is checked, and so how soon a lost name is known.
const holdEvery = 5 * time.Second

// defaultHeartbeatTimeout bounds one round of the heartbeat's database work (Deps.HeartbeatTimeout): a database that
// holds the node's row, or has stopped answering, must not hold back what the loop does for Valkey.
const defaultHeartbeatTimeout = 5 * time.Second

const (
	// BeatEvery is how often a node writes its row. 30 seconds without one is stale, so a node that misses two in a row
	// is gone, which a slow database answers before it happens. The key ring's lifecycle refreshes the distribution file
	// on the same period, so a version an operator writes reaches every node's report within one period.
	BeatEvery = 10 * time.Second
	// AliveFor is how recently a row must have been written to count a node alive: three missed beats. The key ring's
	// lifecycle also uses it: a report older than this no longer counts as a running node, and retirement waits this long
	// after every live node starts writing the active version.
	AliveFor = 30 * time.Second
	// forgetAfter is when a stale row is deleted. Longer than AliveFor, so two nodes never disagree about a row that is
	// about to expire; the table holds a handful of rows either way.
	forgetAfter = 60 * time.Second
)

// heartbeat is this node's row in platform.nodes: the machine that makes a wrong N visible, since nothing else notices
// a node configured without Valkey or a count set too low (README「配置与启动」).
type heartbeat struct {
	db      *pg.DB
	scope   pg.Scope
	session uuid.UUID
	id      string // the node's identity, which a restarted node keeps: its old row is its own, and is replaced
	keys    func() KeyReport
}

func newHeartbeat(db *pg.DB, scope pg.Scope, id string, keys func() KeyReport) *heartbeat {
	return &heartbeat{db: db, scope: scope, id: id, session: uuid.NewV7(), keys: keys}
}

// report is what this beat says about the ring. It runs on every write, so a reload that installs a new version is
// visible to the lifecycle on the next beat without a restart.
func (h *heartbeat) report() (versions []int64, active int64) {
	versions = []int64{}
	if h.keys == nil {
		return versions, 0
	}
	report := h.keys()
	for _, version := range report.Versions {
		versions = append(versions, int64(version))
	}
	return versions, int64(report.Active)
}

// startLock is the advisory lock that one node at a time holds while it counts the others and writes its own row. Without
// it two nodes that start together both count none, and both are let in.
const startLock = 0x61786e6f6465 // "axnode"

// errTooMany marks the refusal inside the transaction that makes it.
var errTooMany = errors.New("another live node reaches N")

// start writes this node's first row, and refuses to start when another live node already reaches N: it is N set too
// low, or a second node that reached this database without Valkey. The count and the write are one transaction under
// startLock, so they cannot interleave with another node's. It forgets the rows that went stale first, so a crash does
// not keep the row for longer than forgetAfter.
func (h *heartbeat) start(ctx context.Context, nodes int, live *atomic.Int64, m metrics) error {
	var others, alive int
	err := h.db.Tx(ctx, h.scope, func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(startLock)); err != nil {
			return err
		}
		if err := forget(ctx, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE id <> $1 AND seen_at > now() - make_interval(secs => $2)),
			count(*) FILTER (WHERE seen_at > now() - make_interval(secs => $2))
			FROM platform.nodes`, h.id, AliveFor.Seconds()).Scan(&others, &alive)
		if err != nil {
			return err
		}
		if others >= nodes {
			return errTooMany
		}
		if err := retireShares(ctx, tx, h.id, ""); err != nil {
			return err
		}
		versions, active := h.report()
		_, err = tx.Exec(ctx, `INSERT INTO platform.nodes (id, share_session, share_view, key_versions, key_active,
    supports_billing_preference, billing_preference_session)
   VALUES ($1, $2, '{}', $3, $4, true, $2)
   ON CONFLICT (id) DO UPDATE SET seen_at = now(), share_session = excluded.share_session, share_view = '{}', share_joining = true,
    key_versions = excluded.key_versions, key_active = excluded.key_active, supports_billing_preference = true,
    billing_preference_session = excluded.billing_preference_session,
    key_active_since = CASE WHEN platform.nodes.key_active IS DISTINCT FROM excluded.key_active
     THEN now() ELSE platform.nodes.key_active_since END`, h.id, h.session, versions, active)
		if err != nil {
			return err
		}
		return h.recordReadiness(ctx, tx)
	})
	if errors.Is(err, errTooMany) {
		return fmt.Errorf("%s: %d nodes are alive besides this one, which is not fewer than the %d configured: either "+
			"this node's %s is too low, or another node reached this database without %s (internal/platform/kv/README.md"+
			"「配置与启动」)", NodesVar, others, nodes, NodesVar, URLVar)
	}
	if err != nil {
		return fmt.Errorf("kv: register this node: %w", err)
	}
	live.Store(int64(alive + 1))
	m.nodes.Record(ctx, int64(alive+1))
	return nil
}

// beat writes this node's row, forgets the stale ones, and returns how many nodes are alive, this one included.
func (h *heartbeat) beat(ctx context.Context) (int, error) {
	var alive int
	err := h.db.Tx(ctx, h.scope, func(tx pg.Tx) error {
		if err := h.write(ctx, tx); err != nil {
			return err
		}
		if err := h.recordReadiness(ctx, tx); err != nil {
			return err
		}
		if err := forget(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM platform.nodes WHERE seen_at > now() - make_interval(secs => $1)`,
			AliveFor.Seconds()).Scan(&alive)
	})
	if err != nil {
		return 0, fmt.Errorf("kv: write this node's heartbeat: %w", err)
	}
	return alive, nil
}

// recordReadiness republishes whether every live node supports the funding preference. Every new node does this on
// every beat; an old node's row stays false, so a mixed deployment keeps the safe default until the old node is gone.
func (*heartbeat) recordReadiness(ctx context.Context, tx pg.Tx) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.billing_preference_readiness (id, enabled)
  SELECT true, coalesce(bool_and(coalesce(supports_billing_preference, false)
    AND billing_preference_session IS NOT NULL AND billing_preference_session = share_session), true)
  FROM platform.nodes WHERE seen_at > now() - make_interval(secs => $1)
  ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, updated_at = clock_timestamp()`, AliveFor.Seconds())
	return err
}

// write is this node's row, made or refreshed. now() is the database's clock, so every node's row is aged by one clock
// and a node's own clock being wrong cannot make it look alive or gone.
func (h *heartbeat) write(ctx context.Context, tx pg.Tx) error {
	versions, active := h.report()
	_, err := tx.Exec(ctx, `INSERT INTO platform.nodes (id, share_session, share_view, key_versions, key_active,
    supports_billing_preference, billing_preference_session)
  VALUES ($1, $2, '{}', $3, $4, true, $2)
  ON CONFLICT (id) DO UPDATE SET seen_at = now(),
   share_joining = platform.nodes.share_joining OR platform.nodes.seen_at <= now() - make_interval(secs => $5),
   key_versions = excluded.key_versions, key_active = excluded.key_active, supports_billing_preference = true,
   billing_preference_session = excluded.billing_preference_session,
   key_active_since = CASE WHEN platform.nodes.key_active IS DISTINCT FROM excluded.key_active
    THEN now() ELSE platform.nodes.key_active_since END
  WHERE platform.nodes.share_session = excluded.share_session`,
		h.id, h.session, versions, active, AliveFor.Seconds())
	return err
}

// forget deletes the rows that went stale: longer than AliveFor, so that two nodes never disagree about a row that is
// about to expire, and the table holds a handful of rows either way.
func forget(ctx context.Context, tx pg.Tx) error {
	// Persist the RPM reservation before garbage-collecting a crashed process's heartbeat.
	if _, err := tx.Exec(ctx, `INSERT INTO platform.credential_share_departures (id, share_view, expires_at)
  SELECT id || '/' || coalesce(share_session::text, 'legacy'), share_view,
   seen_at + make_interval(secs => $1) FROM platform.nodes
  WHERE seen_at < clock_timestamp() - make_interval(secs => $2)
  ON CONFLICT (id) DO NOTHING`, (AliveFor + shareWindow).Seconds(), forgetAfter.Seconds()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.credential_share_departures WHERE expires_at <= clock_timestamp()`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM platform.nodes WHERE seen_at < now() - make_interval(secs => $1)`,
		forgetAfter.Seconds())
	return err
}

// stop deletes this node's row, so that a restart of a single node is not refused for a row only a crash would leave.
func (h *heartbeat) stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, BeatEvery)
	defer cancel()
	err := h.db.Tx(ctx, h.scope, func(tx pg.Tx) error {
		if err := retireShares(ctx, tx, h.id, h.session.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM platform.nodes WHERE id = $1 AND share_session = $2`, h.id, h.session)
		return err
	})
	if err != nil {
		return fmt.Errorf("kv: delete this node's heartbeat: %w", err)
	}
	return nil
}
