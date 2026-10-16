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
