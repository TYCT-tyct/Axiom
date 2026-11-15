package accounts_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestEmailChangeBlocksOldAddressResetIssuance(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	request := x.requestReset(t, x.email, "en")
	ready, release := make(chan struct{}), make(chan struct{})
	held, processed := make(chan error, 1), make(chan error, 1)
	go func() {
		held <- x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "UPDATE accounts.users SET email='next@example.com' WHERE id=$1", uuid.UUID(x.user)); err != nil {
				return err
			}
			close(ready)
			<-release
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-held:
		t.Fatal(err)
	}
	go func() { processed <- x.recovery.ProcessPasswordReset(t.Context(), request) }()
	waited := waitForProfileLocks(t, x, "-- name: ResetUser%", 1)
	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	if !waited {
		t.Fatal("reset did not wait at the email lookup")
	}
	for _, table := range []string{"platform.mail_outbox", "accounts.password_reset_tokens"} {
		if got := texts(t, x.db, platform, "SELECT count(*)::text FROM "+table); got[0] != "0" {
			t.Fatalf("stale mailbox received reset: %s %v", table, got)
		}
	}
}

// The deadline only bounds a stuck test; the observed PostgreSQL lock decides when to commit the email change.
func waitForProfileLocks(t *testing.T, x recoveryWorld, pattern string, count int) bool {
	t.Helper()
	watcher := x.admin(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		waiting := false
		err := watcher.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) >= $2 FROM pg_stat_activity
                WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, pattern, count).Scan(&waiting)
		})
		if err != nil {
			return false
		}
		if waiting {
			return true
		}
	}
	return false
}
