package accounts_test

import (
	"testing"
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
	func() {
		defer close(release)
		awaitWaitingQuery(t, x.admin(t), "-- name: ResetUser%", 1)
	}()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform.mail_outbox", "accounts.password_reset_tokens"} {
		if got := texts(t, x.db, platform, "SELECT count(*)::text FROM "+table); got[0] != "0" {
			t.Fatalf("stale mailbox received reset: %s %v", table, got)
		}
	}
}
