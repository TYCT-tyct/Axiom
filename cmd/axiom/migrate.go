package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/config"
	"axiom/internal/platform/pg"
)

// defaultMigrateTimeout bounds a whole run of migrate. Long DDL (an index on a big table) is expected, so it is generous,
// but a run that never ends would hold goose's session lock and block the next deployment.
const defaultMigrateTimeout = 30 * time.Minute

// migrate brings the database up to this program. A deployment runs it before starting nodes of a new version. Each
// connection is made within AXIOM_MIGRATE_CONNECT_TIMEOUT and the whole run ends within AXIOM_MIGRATE_TIMEOUT, both
// checked before anything is connected.
func migrate(ctx context.Context, environ []string) error {
	env := config.New(environ)
	adminURL := env.Required(pg.MigrateDatabaseURLVar)
	connectTimeout := env.Duration("AXIOM_MIGRATE_CONNECT_TIMEOUT", pg.DefaultConnectTimeout)
	timeout := env.Duration("AXIOM_MIGRATE_TIMEOUT", defaultMigrateTimeout)
	err := env.Err()
	if err == nil && connectTimeout >= timeout {
		err = errors.New("AXIOM_MIGRATE_CONNECT_TIMEOUT: must be shorter than AXIOM_MIGRATE_TIMEOUT")
	}
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = pg.MigrateWith(ctx, adminURL, connectTimeout, modules()...)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: the run did not finish within AXIOM_MIGRATE_TIMEOUT (%s)", err, timeout)
	}
	return err
}
