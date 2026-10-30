package gateway

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/pg"
)

// waiters is the number of requests of the class that wait in the pool for a connection.
func (p *Pool) waiters(c Class) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.classes[c].waiting.len()
}

// lockKeys holds the key table locked from a superuser connection until the returned function is called, so that a
// lookup of a key waits for the database where it can be counted. The returned count is the lookups waiting.
func (r *rig) lockKeys() (waiting func() int, unlock func()) {
	r.t.Helper()
	admin, err := pg.Open(r.t.Context(), r.database.Superuser)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(admin.Close)
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- admin.Tx(context.WithoutCancel(r.t.Context()), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(r.t.Context(), "LOCK TABLE accounts.api_keys IN ACCESS EXCLUSIVE MODE"); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	unlock = sync.OnceFunc(func() {
		close(release)
		if err := <-done; err != nil {
			r.t.Error(err)
		}
	})
	r.t.Cleanup(unlock)
	waiting = func() (n int) {
		if err := admin.Tx(r.t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(r.t.Context(), `SELECT count(*) FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE '%key_tenant%'`).Scan(&n)
		}); err != nil {
			r.t.Fatal(err)
		}
		return n
	}
	return waiting, unlock
}

// TestColdKeyFloodLeavesTheReservedConnections: a flood of keys the gateway has not seen takes at most the
// connections of the admission class, however many are sent and however long the database takes, so a settlement
// still finds a connection (internal/gateway/README.md G2). The lookups are held at the database by a lock; the pool
// of the database has the size of the gateway's, so a lookup that the gateway let through uncounted would take a
// connection the settlement needs.
func TestColdKeyFloodLeavesTheReservedConnections(t *testing.T) {
	t.Parallel()
	const size, reserve, flood = 8, 2, 24
	r := newRig(t, func(o *rigOptions) {
		o.conns = size
		o.config = func(c *Config) { c.PoolSize, c.PoolReserve = size, reserve }
	})
	a := r.newAccount(1e9)
	keys := make([]string, flood)
	for i := range keys {
		keys[i], _ = r.newKey(a)
	}
	waiting, unlock := r.lockKeys()
	requests := make([]func() (reply, string), flood)
	for i := range requests {
		requests[i] = func() (reply, string) { return r.do(http.MethodGet, "/v1/models", keys[i], nil) }
	}
	answers := together(func() {
		// Steady: more lookups were asked for than the class allows, and the database has no more than it allows. A lookup
		// that was let through uncounted shows as a seventh at the database.
		waitFor(t, "the lookups to reach their limit", func() bool {
			n := waiting()
			return n > size-reserve || n == size-reserve && r.limits.Pool.waiters(Admission) > 0
		})
		if n := waiting(); n > size-reserve {
			t.Errorf("%d lookups hold database connections, want at most %d", n, size-reserve)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		if err := submitCompletion(ctx, r.limits.Funds, "settler"); err != nil {
			t.Errorf("a settlement was not given a connection: %v", err)
		}
		if err := r.db.Tx(pg.CompletionContext(ctx), platformScope, func(tx pg.Tx) error { _, err := tx.Exec(ctx, "SELECT 1"); return err }); err != nil {
			t.Errorf("a transaction found no free database connection: %v", err)
		}
		unlock()
	}, requests...)
	for i, an := range answers {
		if an.resp.StatusCode != http.StatusOK && an.resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("flood request %d: %d %s", i, an.resp.StatusCode, an.body)
		}
	}
	waitFor(t, "the lookups to give their connections back", func() bool { return r.limits.Pool.inUse() == 0 })
}
