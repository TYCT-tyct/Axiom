package pg

import "context"

// SessionScope is what the connection carries outside any transaction; it must always be empty.
func (db *DB) SessionScope(ctx context.Context) (string, error) {
	var scope string
	err := db.pool.QueryRow(ctx, `SELECT coalesce(current_setting('axiom.scope_platform', true), '') ||
		coalesce(current_setting('axiom.scope_sites', true), '') ||
		coalesce(current_setting('axiom.scope_accounts', true), '')`).Scan(&scope)
	return scope, err
}

// ModuleOrder is the order migrations run in.
var ModuleOrder = moduleOrder

// Ordered is platform's own migrations and modules, in the order they run in.
func Ordered(modules ...Module) ([]Module, error) { return ordered(modules) }
