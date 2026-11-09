package pg

import (
	"context"
	"errors"
	"hash/fnv"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
)

// ErrHeld is what Hold returns when another session already holds the name.
var ErrHeld = errors.New("pg: the name is held by another session")

// Hold is a name held by one session of the database for as long as the holder lives: a session-level advisory lock on a
// connection of its own. The ownership belongs to the connection, so it ends when the connection does, whatever way the
// process went: a crashed process is gone from the database a moment after the system closes its sockets, and the name is
// free for whoever starts next, with no timer to wait for. While the holder lives it checks the connection, and when the
// connection is lost Lost is closed: whoever relies on the name is no longer its owner and must stop acting as such until
// Retake gives it back.
type Hold struct {
	db       *DB
	name     string
	every    time.Duration
	tag      string // the application_name of every session this hold and its retakes open (Retake)
	conn     *pgx.Conn
	lost     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// Hold takes the name, with pg_try_advisory_lock on a connection that leaves the pool, so that it is not counted in
// PoolSize: a process with a Hold has one connection more than the pool's ceiling. It returns ErrHeld at once when
// another session holds the name. every is how often the connection is checked, and how long a check may take. Names
// become 64-bit keys by a hash, so two names can in theory meet; the callers' names are few and chosen.
func (db *DB) Hold(ctx context.Context, name string, every time.Duration) (*Hold, error) {
	return db.hold(ctx, name, every, "axiom-hold-"+uuid.NewV4().String())
}

// hold is Hold for the hold lineage tag: the session is named tag before it tries the lock, so whatever became of the
// try, Retake can find the session by its name.
func (db *DB) hold(ctx context.Context, name string, every time.Duration, tag string) (*Hold, error) {
	borrowed, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn := borrowed.Hijack()
	// A process that dies is gone from the server at once, because the system closes its sockets. A host that dies, or a
	// network that is cut, closes nothing: the server finds the half-open connection by its own keepalive, which is two
	// hours by default, and the name would be held that long against the node's restart. So the session asks for probes
	// every 5 s, three of them unanswered, and for unacknowledged data to be given up after 20 s: the server frees the name
	// within about 20 to 30 s of the host going. A Unix socket ignores the settings, and has no network to be cut.
	_, err = conn.Exec(ctx, `SELECT set_config('application_name', $1, false)`, tag)
	if err == nil {
		_, err = conn.Exec(ctx, holdKeepalive)
	}
	if err != nil {
		_ = conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the setting's error is the one to report
		return nil, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, holdKey(name)).Scan(&got); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the lock query's error is the one to report
		return nil, err
	}
	if !got {
		return nil, errors.Join(ErrHeld, conn.Close(context.WithoutCancel(ctx)))
	}
	h := &Hold{
		db: db, name: name, every: every, tag: tag, conn: conn,
		lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go h.watch(context.WithoutCancel(ctx), every)
	return h, nil
}

// ErrStale is what Retake returns while the server still keeps a session of this hold, which it could not end in time;
// the name is still this holder's, and a later Retake takes it.
var ErrStale = errors.New("pg: the server still keeps a session of the hold")

// Retake takes the name again, on a new connection, after the hold was lost (Lost) and once the database answers. The
// server may still keep sessions of this hold that the holder has lost: the one that held the name until the server's
// keepalive finds its connection gone (holdKeepalive), and one of an earlier Retake whose lock the server granted while
// its answer never arrived. Each holds the name until the server drops it. Every session of a hold and its retakes
// carries the hold's tag as its application_name, set before it tries the lock, so Retake ends them all by that name
// before it tries: ErrHeld from Retake means another session holds the name, never one of this holder's own. Call it on
// the latest hold of a lineage only, which is lost and needs no Release.
func (h *Hold) Retake(ctx context.Context) (*Hold, error) {
	var ended []bool
	rows, err := h.db.pool.Query(ctx, `SELECT pg_terminate_backend(pid, $2) FROM pg_stat_activity
		WHERE application_name = $1 AND pid <> pg_backend_pid()`, h.tag, h.every.Milliseconds())
	if err == nil {
		ended, err = pgx.CollectRows(rows, pgx.RowTo[bool])
	}
	if err != nil {
		return nil, err
	}
	if slices.Contains(ended, false) {
		return nil, ErrStale
	}
	return h.db.hold(ctx, h.name, h.every, h.tag)
}

// holdKeepalive is what the holder's session sets on itself, in milliseconds for tcp_user_timeout and in seconds for the rest.
const holdKeepalive = `SET tcp_keepalives_idle = 5; SET tcp_keepalives_interval = 5; SET tcp_keepalives_count = 3; SET tcp_user_timeout = 20000`

// holdKey is the advisory lock key of a name.
func holdKey(name string) int64 {
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(name)) // a hash never fails to take bytes
	return int64(sum.Sum64())      //nolint:gosec // the key is the hash's bits, which wrap on purpose
}

// Lost is closed when the connection that holds the name has been lost, which is when the holder stops being the owner.
func (h *Hold) Lost() <-chan struct{} { return h.lost }

// watch checks the connection until the hold is released or the connection fails a check.
func (h *Hold) watch(ctx context.Context, every time.Duration) {
	defer close(h.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
		}
		check, cancel := context.WithTimeout(ctx, every)
		err := h.conn.Ping(check)
		cancel()
		if err != nil {
			close(h.lost)
			_ = h.conn.Close(ctx) //nolint:errcheck // the connection is already failing; closing it frees the name if the server still has it
			return
		}
	}
}

// Release lets the name go: it stops the checks and closes the connection, which frees the name at the server. It may be
// called again, and does nothing then.
func (h *Hold) Release(ctx context.Context) error {
	var err error
	h.stopOnce.Do(func() {
		close(h.stop)
		<-h.done
		err = h.conn.Close(ctx)
	})
	return err
}
