package billing

import (
	"errors"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

func TestBalanceNoticeJournalRetriesAndPaidCredits(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	p := b.paid(10_000_000_000, Payment{currency(t, "USD"), 1000, 0}, uuid.Nil())
	for range 2 {
		if err := b.creditPaid(p); err != nil {
			t.Fatal(err)
		}
	}
	var notices []accounts.BalanceNotice
	sentinel := errors.New("recipient rolled back")
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		if err := ConsumeBalanceNotices(t.Context(), tx, func([]accounts.BalanceNotice) error { return sentinel }); err != nil {
			return err
		}
		t.Fatal("no journal")
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback: %v", err)
	}
	for range 2 {
		if err = b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			return ConsumeBalanceNotices(t.Context(), tx, func(n []accounts.BalanceNotice) error { notices = append(notices, n...); return nil })
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(notices) != 2 {
		t.Fatalf("one wallet movement and one paid receipt: %+v", notices)
	}
	values := map[int64][2]int64{}
	for _, n := range notices {
		if n.Site != p.Site || n.Account != p.Account {
			t.Fatalf("tenant: %+v", n)
		}
		values[n.TopUp] = [2]int64{n.Previous, n.Balance}
	}
	want := map[int64][2]int64{0: {0, p.Credited}, p.Credited: {0, 0}}
	if diff := cmp.Diff(want, values); diff != "" {
		t.Fatalf("journal (-want +got): %s", diff)
	}
}

func TestBalanceNoticePreservesIntermediateChanges(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	// Ordinary credit is not paid, and every intermediate decrease survives before consumption.
	b.credit(20)
	b.sql("UPDATE billing.balances SET balance=90", "UPDATE billing.balances SET balance=80", "UPDATE billing.balances SET balance=200", "UPDATE billing.balances SET balance=70")
	var notices []accounts.BalanceNotice
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return ConsumeBalanceNotices(t.Context(), tx, func(n []accounts.BalanceNotice) error { notices = append(notices, n...); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 5 {
		t.Fatalf("lost intermediate movement: %+v", notices)
	}
	for _, n := range notices {
		if n.TopUp != 0 {
			t.Fatalf("gift became top-up: %+v", n)
		}
	}
}

func TestBalanceNoticeConsumersClaimDistinctRows(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.credit(100)
	claimed, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			return ConsumeBalanceNotices(t.Context(), tx, func([]accounts.BalanceNotice) error {
				close(claimed)
				select {
				case <-release:
					return nil
				case <-t.Context().Done():
					return t.Context().Err()
				}
			})
		})
	}()
	select {
	case <-claimed:
	case err := <-first:
		t.Fatalf("first consumer claimed no row: %v", err)
	}
	var count int
	err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return ConsumeBalanceNotices(t.Context(), tx, func(n []accounts.BalanceNotice) error { count += len(n); return nil })
	})
	close(release)
	firstErr := <-first
	if err != nil || firstErr != nil || count != 0 {
		t.Fatalf("concurrent claims: %d %v %v", count, err, firstErr)
	}
}

func TestBalanceNoticeConsumptionIsBoundedAndBatched(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	b.sql(`INSERT INTO billing.balance_notices(site_id,account_id,previous,balance,top_up)
 SELECT site_id,account_id,1,0,0 FROM billing.balances CROSS JOIN generate_series(1,1500)`)
	for _, want := range []int{1000, 500, 0} {
		calls, count := 0, 0
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			return ConsumeBalanceNotices(t.Context(), tx, func(n []accounts.BalanceNotice) error { calls++; count = len(n); return nil })
		}); err != nil {
			t.Fatal(err)
		}
		if count != want || (want > 0 && calls != 1) || (want == 0 && calls != 0) {
			t.Fatalf("batch: calls=%d count=%d want=%d", calls, count, want)
		}
	}
}
