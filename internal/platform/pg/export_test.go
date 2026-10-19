package pg

import (
	"context"
	"database/sql/driver"
	"time"
)

// SessionScope is what the connection carries outside any transaction; it must always be empty.
func (db *DB) SessionScope(ctx context.Context) (string, error) {
	var scope string
	err := db.pool.QueryRow(ctx, `SELECT coalesce(current_setting('axiom.scope_platform', true), '') ||
		coalesce(current_setting('axiom.scope_sites', true), '') ||
		coalesce(current_setting('axiom.scope_accounts', true), '')`).Scan(&scope)
	return scope, err
}

// LatestVersion is the newest of a module's migrations.
func LatestVersion(m Module) (int64, error) { return latestVersion(m) }

// ModuleOrder is the order migrations run in.
var ModuleOrder = moduleOrder

// Ordered is platform's own migrations and modules, in the order they run in.
func Ordered(modules ...Module) ([]Module, error) { return ordered(modules) }

// RunBounded runs f, giving up on it grace after ctx ends.
func RunBounded(ctx context.Context, grace time.Duration, stop, f func() error) error {
	return runBounded(ctx, grace, stop, f)
}

// NewTimedConnector wraps a database/sql connector so that each connect has one deadline.
func NewTimedConnector(inner driver.Connector, timeout time.Duration) driver.Connector {
	return timedConnector{inner, timeout}
}

// ScanDiscarded runs the queue's discard scan now, so a test need not wait for the periodic job.
func (q *Queue) ScanDiscarded(ctx context.Context) error { return q.scanDiscarded(ctx) }

// QueueWorkers is how many jobs a node works at once from the named queue, so that a test can fill it.
func QueueWorkers(name string) int {
	for _, queue := range queues {
		if queue.name == name {
			return queue.workers
		}
	}
	panic("pg: no queue " + name) // the callers are tests naming a constant
}

// QueueWorkersTotal is how many jobs a node works at once over the named queues, so that a test can exceed it.
func QueueWorkersTotal() int {
	total := 0
	for _, queue := range queues {
		total += queue.workers
	}
	return total
}
