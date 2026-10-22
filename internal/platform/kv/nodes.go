package kv

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"axiom/internal/platform/pg"
)

// holdEvery is how often the connection that holds the node's name is checked, and so how soon a lost name is known.
const holdEvery = 5 * time.Second

// defaultHeartbeatTimeout bounds one round of the heartbeat's database work (Deps.HeartbeatTimeout): a database that
// holds the node's row, or has stopped answering, must not hold back what the loop does for Valkey.
const defaultHeartbeatTimeout = 5 * time.Second

const (
	// beatEvery is how often a node writes its row. 30 seconds without one is stale, so a node that misses two in a row
	// is gone, which a slow database answers before it happens.
	beatEvery = 10 * time.Second
	// AliveFor is how recently a row must have been written to count a node alive: three missed beats. The key ring's
	// lifecycle commands use it too: a node that may still be writing is a node that has to be stopped first.
	AliveFor = 30 * time.Second
	// forgetAfter is when a stale row is deleted. Longer than AliveFor, so two nodes never disagree about a row that is
	// about to expire; the table holds a handful of rows either way.
	forgetAfter = 60 * time.Second
)

// heartbeat is this node's row in platform.nodes: the machine that makes a wrong N visible, since nothing else notices
// a node configured without Valkey or a count set too low (README「配置与启动」).
type heartbeat struct {
	db    *pg.DB
	scope pg.Scope
	id    string // the node's identity, which a restarted node keeps: its old row is its own, and is replaced
}

func newHeartbeat(db *pg.DB, scope pg.Scope, id string) *heartbeat {
	return &heartbeat{db: db, scope: scope, id: id}
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
		return h.write(ctx, tx)
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

// write is this node's row, made or refreshed. now() is the database's clock, so every node's row is aged by one clock
// and a node's own clock being wrong cannot make it look alive or gone.
func (h *heartbeat) write(ctx context.Context, tx pg.Tx) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.nodes (id) VALUES ($1)
		ON CONFLICT (id) DO UPDATE SET seen_at = now()`, h.id)
	return err
}

// forget deletes the rows that went stale: longer than AliveFor, so that two nodes never disagree about a row that is
// about to expire, and the table holds a handful of rows either way.
func forget(ctx context.Context, tx pg.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM platform.nodes WHERE seen_at < now() - make_interval(secs => $1)`,
		forgetAfter.Seconds())
	return err
}

// stop deletes this node's row, so that a restart of a single node is not refused for a row only a crash would leave.
func (h *heartbeat) stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, beatEvery)
	defer cancel()
	err := h.db.Tx(ctx, h.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM platform.nodes WHERE id = $1`, h.id)
		return err
	})
	if err != nil {
		return fmt.Errorf("kv: delete this node's heartbeat: %w", err)
	}
	return nil
}
