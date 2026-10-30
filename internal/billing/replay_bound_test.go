package billing

import (
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// TestAdvanceRacesACredit: a credit commits while an unlocked replay has loaded the batches and waits to read the
// ledger, so the batches it loaded have no batch for the credit while the page it reads next, planned after the commit,
// holds the credit's transaction; the place does not move, so neither check of the place drops the round, and the bound
// fixed before the batches keeps both out of it (#361). An account whose only transaction is from before the sequence
// has a bound of 0, which has to hold as well.
func TestAdvanceRacesACredit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{"numbered", false},
		{"from before the sequence", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 100, 0, Config{})
			if tc.legacy {
				b.sql("UPDATE billing.transactions SET seq = NULL")
			}
			b.advanceRacedByACredit()
		})
	}
}

// advanceRacedByACredit commits a credit while an unlocked replay waits to read the ledger, and checks that the replay
// neither fails nor passes the credit: it stores the bound it fixed as the place, and the next replay, under the lock,
// takes the credit up and writes it off as expired.
func (b *bank) advanceRacedByACredit() {
	b.t.Helper()
	account := b.admitted.Account
	// The bound the replay has to fix is the account's greatest seq, which the credit has not reached yet.
	var bound int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		var err error
		bound, err = queries.ReplayThrough(b.t.Context(), tx, account)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	// The credit runs as the superuser, since the application may not lock the table.
	super, err := pg.Open(b.t.Context(), b.db.Superuser)
	if err != nil {
		b.t.Fatal(err)
	}
	defer super.Close()
	credit := Credit{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: account, Amount: 25, ExpiresAt: longAgo,
		Actor: audit.System("billing.test"), Reason: "race the replay",
	}
	done := make(chan error, 1)
	err = super.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		// The entries, which a page of movements sums: the replay loads the batches and then waits here, with the
		// credit uncommitted, and plans its page after the commit.
		if _, err := tx.Exec(b.t.Context(), "LOCK TABLE billing.entries IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		if err := AddCredit(b.t.Context(), tx, credit); err != nil {
			return err
		}
		go func() {
			_, err := advanceAllocation(b.t.Context(), b.app, b.platform, account, movementPage)
			done <- err
		}()
		b.waitForLockWait()
		return nil
	})
	if err != nil {
		b.t.Fatal(err)
	}
	if err := <-done; err != nil {
		b.t.Fatalf("the replay raced by a credit failed: %v", err)
	}
	// The credit is committed by now, at a seq above the bound, and the replay stored the bound as its place: what it
	// left out is left to the next replay.
	var place, creditSeq int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(b.t.Context(), `SELECT b.allocated_seq, t.seq FROM billing.balances b
			JOIN billing.transactions t ON t.account_id = b.account_id AND t.request_id = $1
			WHERE b.account_id = $2`, credit.ID, account).Scan(&place, &creditSeq)
	}); err != nil {
		b.t.Fatal(err)
	}
	if place != bound || creditSeq <= bound {
		b.t.Fatalf("the replay stands at %d, want the bound %d, with the credit at %d past it", place, bound, creditSeq)
	}
	if written := b.expire(); written != credit.Amount {
		b.t.Fatalf("the next replay wrote off %d, want the late credit's %d", written, credit.Amount)
	}
}

// TestReplayBoundAcrossPages: a credit is committed after an unlocked replay has fixed its bound and loaded the
// batches, and every page of the replay keeps that bound, so the credit stays out along with the batch it opened; the
// write-off that follows, under the lock, takes both up, and then the expiry transaction it writes itself.
func TestReplayBoundAcrossPages(t *testing.T) {
	t.Parallel()

	b := newBank(t, 100, 0, Config{})
	b.creditBatch(20, longAgo)
	through, a := b.replayAcrossPages()
	if a.at.seq != through || a.funded() != 120 {
		t.Fatalf("replayed to %d with %d funded, want %d and the 120 of the credits it loaded", a.at.seq, a.funded(), through)
	}
	var written int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		written, _, err = writeOffAccount(b.t.Context(), tx, b.admitted.Account, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if written != 50 {
		t.Fatalf("the locked replay wrote off %d, want both expiring batches' 50", written)
	}
	b.checkStored(b.admitted.Account)
}

// replayAcrossPages replays the bank's ledger a transaction to a page, holding the bound it fixed before the batches
// while another expiring credit is committed behind it, and returns that bound and the replay: no page passes the
// bound, and the late credit and its batch are left to the next replay. The credit is committed between the reads, not
// timed, so the pages read after it see its transaction and never the batch it opened.
func (b *bank) replayAcrossPages() (through int64, replayed *allocation) {
	b.t.Helper()
	ctx, account := b.t.Context(), b.admitted.Account
	late := Credit{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: account, Amount: 30, ExpiresAt: longAgo,
		Actor: audit.System("billing.test"), Reason: "behind the bound",
	}
	var bound int64
	var a *allocation
	if err := b.app.Tx(ctx, b.platform, func(tx pg.Tx) error {
		var err error
		if bound, err = queries.ReplayThrough(ctx, tx, account); err != nil {
			return err
		}
		if a, err = loadAllocation(ctx, tx, account); err != nil {
			return err
		}
		// Committed on a connection of its own, as the expiry's own credit is committed after its batches are loaded:
		// from here on a page that read past the bound would find the credit's transaction without the batch it opened.
		if err := b.app.Tx(ctx, b.platform, func(tx pg.Tx) error { return AddCredit(ctx, tx, late) }); err != nil {
			return err
		}
		for full := true; full; {
			if full, err = a.catchUpPage(ctx, tx, account, 1, bound); err != nil {
				return err
			}
			if a.at.seq > bound {
				return fmt.Errorf("a page passed the bound %d, at %d", bound, a.at.seq)
			}
		}
		return a.save(ctx, tx, account)
	}); err != nil {
		b.t.Fatal(err)
	}
	return bound, a
}
