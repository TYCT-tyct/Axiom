package kv

import (
	"context"
	"fmt"
	"slices"
	"time"

	"axiom/internal/platform/pg"
)

// ShareNode is a core incarnation and its enforced membership. Positive Remaining reserves concurrency; departed
// entries stay after that to reserve RPM until their old minute ends. An empty View belongs to a joining process
// or a legacy binary, whose former full share is reserved until it confirms membership.
type ShareNode struct {
	ID        string
	View      []string
	Remaining time.Duration // database heartbeat lifetime remaining when membership was read
}

// ShareLease leaves a heartbeat round's timeout before AliveFor: a core stops admitting before peers can reclaim it.
const ShareLease = AliveFor - defaultHeartbeatTimeout

// ShareView reads membership using the same database clock and timeout as the node registry. Identities include the
// process incarnation, so a restart cannot inherit acknowledgements from the process it replaces.
func (c *Client) ShareView(ctx context.Context) ([]ShareNode, error) {
	var nodes []ShareNode
	err := c.hear.db.ReadTx(ctx, c.hear.scope, func(tx pg.Tx) error {
		var err error
		nodes, err = shareNodes(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("kv: read credential share members: %w", err)
	}
	return nodes, nil
}

func shareNodes(ctx context.Context, tx pg.Tx) ([]ShareNode, error) {
	rows, err := tx.Query(ctx, `SELECT id || '/' || coalesce(share_session::text, 'legacy'), share_view,
  (extract(epoch FROM (seen_at + make_interval(secs => $1) - clock_timestamp())) * 1000000)::bigint
  FROM platform.nodes WHERE seen_at > clock_timestamp() - make_interval(secs => $2)
  UNION ALL SELECT id, share_view,
   (extract(epoch FROM (expires_at - make_interval(secs => $3) - clock_timestamp())) * 1000000)::bigint
   FROM platform.credential_share_departures
   WHERE expires_at > clock_timestamp() ORDER BY 1`, AliveFor.Seconds(), (AliveFor + shareWindow).Seconds(), shareWindow.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []ShareNode
	for rows.Next() {
		var n ShareNode
		var micros int64
		if err := rows.Scan(&n.ID, &n.View, &micros); err != nil {
			return nil, err
		}
		n.Remaining = time.Duration(micros) * time.Microsecond
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// ShareID is this process's membership identity, ordered by node name before the incarnation.
func (c *Client) ShareID() string { return c.hear.id + "/" + c.hear.session.String() }

// ConfirmShares publishes only a view still current at the write boundary. nil withdraws a stale local confirmation;
// after a lease expires the process must join again, rather than revive a former share.
func (c *Client) ConfirmShares(ctx context.Context, view []string) error {
	return c.hear.db.Tx(ctx, c.hear.scope, func(tx pg.Tx) error { return c.hear.confirmShares(ctx, tx, view) })
}

func (h *heartbeat) confirmShares(ctx context.Context, tx pg.Tx, view []string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(startLock)); err != nil {
		return err
	}
	if view != nil {
		nodes, err := shareNodes(ctx, tx)
		if err != nil {
			return err
		}
		var ids []string
		for _, n := range nodes {
			if n.Remaining > 0 {
				ids = append(ids, n.ID)
			}
		}
		if !slices.Equal(ids, view) {
			return fmt.Errorf("kv: credential share membership changed")
		}
	}
	result, err := tx.Exec(ctx, `UPDATE platform.nodes SET share_view = $3
   WHERE id = $1 AND share_session = $2 AND seen_at > clock_timestamp() - make_interval(secs => $4)`,
		h.id, h.session, view, AliveFor.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("kv: credential share identity is no longer live")
	}
	return nil
}

// shareWindow covers the account's conservative one-second rolling-minute buckets.
const shareWindow = 61 * time.Second

// retireShares runs before a row disappears or changes incarnation. The last possible admission is bounded by the
// old heartbeat's lifetime; replacing an incarnation also reserves concurrency until that lease expires, because a
// process that lost its name may not have noticed yet. For a drained graceful exit only its RPM minute remains.
// Its minute must expire before survivors can reuse its RPM partition.
func retireShares(ctx context.Context, tx pg.Tx, id string, session string) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.credential_share_departures (id, share_view, expires_at)
  SELECT id || '/' || coalesce(share_session::text, 'legacy'), share_view,
   CASE WHEN $2 = '' THEN seen_at + make_interval(secs => $3)
    ELSE least(clock_timestamp(), seen_at + make_interval(secs => $3)) END + make_interval(secs => $4)
  FROM platform.nodes WHERE id = $1 AND ($2 = '' OR share_session::text = $2)
  ON CONFLICT (id) DO UPDATE SET expires_at = greatest(platform.credential_share_departures.expires_at, excluded.expires_at)`,
		id, session, AliveFor.Seconds(), shareWindow.Seconds())
	return err
}
