package main

import (
	"context"
	"fmt"

	"axiom/internal/accounts"
	"axiom/internal/platform/config"
	"axiom/internal/platform/pg"
)

// modules is the migrations of every module that has them: migrate applies them, serve refuses a database without
// them. pg orders them, after platform's own.
func modules() []pg.Module { return []pg.Module{accounts.Migrations()} }

// migrate brings the database up to this program. A deployment runs it before starting nodes of a new version.
func migrate(ctx context.Context, environ []string) error {
	env := config.New(environ)
	adminURL := env.Required("AXIOM_MIGRATE_DATABASE_URL")
	if err := env.Err(); err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	return pg.Migrate(ctx, adminURL, modules()...)
}
