package pg

import (
	"context"
	"uuid"
)

// NewScope builds any scope; production code gets scopes only from authorization, which does not exist yet.
func NewScope(platform bool, sites, accounts []uuid.UUID) Scope {
	return Scope{platform, sites, accounts}
}

// SessionScope is what the connection carries outside any transaction; it must always be empty.
func (db *DB) SessionScope(ctx context.Context) (string, error) {
	var scope string
	err := db.pool.QueryRow(ctx, `SELECT coalesce(current_setting('axiom.scope_platform', true), '') ||
		coalesce(current_setting('axiom.scope_sites', true), '') ||
		coalesce(current_setting('axiom.scope_accounts', true), '')`).Scan(&scope)
	return scope, err
}
