package billing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers"
)

// A statement sent while the balance lock is blocked must not turn the holder check into an earlier snapshot.
func TestBatchedReserveFencesAfterTheBalanceLock(t *testing.T) {
	b := newBank(t, 1e6, 0, Config{})
	release, first := b.holdOpen(b.admitted, uuid.NewV7(), 100)
	second := make(chan error, 1)
	go func() { _, err := b.reserve(b.admitted, 100); second <- err }()
	b.waitForLockWait()
	if err := pgtest.Run(t.Context(), b.db.Superuser, fmt.Sprintf("UPDATE billing.holders SET expires_at = 'epoch' WHERE id = '%s'", b.billing.holder)); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, ErrTerminated) {
		t.Errorf("expired holder reserved: %v", err)
	}
	b.consistent()
}

func TestBatchedSettleSeesTheTerminalStateAfterItsLock(t *testing.T) {
	b := newBank(t, 1e6, 0, Config{})
	h := b.hold(100)
	held, release, first := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		first <- b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			c, err := b.billing.lock(t.Context(), tx, h.id, b.admitted.Account)
			if err != nil {
				return err
			}
			if _, err := c.settle(t.Context(), tx, Outcome{}); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	second := make(chan error, 1)
	go func() {
		_, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 50, 20, 0)))
		second <- err
	}()
	b.waitForLockWait()
	close(release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if charged := b.count("SELECT charged FROM billing.reservations"); charged != 0 {
		t.Errorf("released request charged %d", charged)
	}
	b.balanced()
	b.consistent()
}

type failingWrite struct {
	Suppliers
	request uuid.UUID
	atEnd   bool
}

func (s failingWrite) RecordAttempts(ctx context.Context, tx pg.Tx, attempts []suppliers.AttemptRecord) error {
	if !s.atEnd && uuid.UUID(attempts[0].Request) == s.request {
		_, err := tx.Exec(ctx, "SELECT 1/0")
		return err
	}
	return s.Suppliers.RecordAttempts(ctx, tx, attempts)
}

func (s failingWrite) EndRequest(ctx context.Context, tx pg.Tx, id, site, account uuid.UUID, at time.Time) error {
	if s.atEnd && id == s.request {
		_, err := tx.Exec(ctx, "SELECT 1/0")
		return err
	}
	return s.Suppliers.EndRequest(ctx, tx, id, site, account, at)
}

// Independent transactions isolate a failing call even when it fails after its cost or all its settlement writes.
// Retrying both the failed member and its successful peers must charge exactly once per request.
func TestPipelinedSettlementsIsolateFailures(t *testing.T) {
	t.Run("attempts", func(t *testing.T) { checkPipelinedSettlementFailure(t, false) })
	t.Run("end", func(t *testing.T) { checkPipelinedSettlementFailure(t, true) })
}

func checkPipelinedSettlementFailure(t *testing.T, atEnd bool) {
	t.Helper()
	b := newBank(t, 1e6, 0, Config{})
	holds := make([]*Hold, 12)
	for i := range holds {
		holds[i] = b.hold(100)
	}
	b.billing.suppliers = failingWrite{Suppliers: b.supplies, request: holds[5].id, atEnd: atEnd}
	o := outcome(Committed, FinishStop, used(t, 50, 20, 0))
	results := make([]error, len(holds))
	var wg sync.WaitGroup
	for i, h := range holds {
		wg.Go(func() { _, results[i] = b.billing.Settle(t.Context(), h, o) })
	}
	wg.Wait()
	for i, err := range results {
		if i == 5 {
			if pg.SQLState(err) != "22012" {
				t.Errorf("failed request: %v", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("peer %d: %v", i, err)
		}
	}
	if n := b.count(fmt.Sprintf("SELECT count(*) FROM billing.transactions WHERE request_id = '%s'", holds[5].id)); n != 0 {
		t.Errorf("failed request left %d ledger events", n)
	}
	b.balanced()
	b.consistent()
	b.billing.suppliers = b.supplies
	checkSettlementRetries(b, holds, o)
	checkSettlementRetries(b, holds, o)
	want := int64(len(holds)) * 70
	if n := b.count("SELECT sum(charged) FROM billing.reservations"); n != want {
		t.Errorf("total charged %d, want %d", n, want)
	}
	b.balanced()
	b.consistent()
	b.replayed(b.admitted.Account)
}

func checkSettlementRetries(b *bank, holds []*Hold, o Outcome) {
	b.t.Helper()
	for _, h := range holds {
		f, err := b.billing.Settle(b.t.Context(), h, o)
		if err != nil || f.Charged.CeilAmount() != 70 {
			b.t.Fatalf("retry charged %v: %v", f.Charged, err)
		}
	}
}

// Admission, extension and settlement must agree on refusals when their source rows are missing.
func TestFundsReadsShareRefusals(t *testing.T) {
	b := newBank(t, 1e6, 0, Config{})
	missing := b.admitted
	missing.Account = uuid.NewV7()
	b.billing.holder = uuid.NewV7()
	for _, tc := range []struct {
		name string
		read func(pg.Tx) error
		want error
	}{
		{"balance", func(tx pg.Tx) error {
			_, err := lockAccount(t.Context(), tx, missing.Account)
			return err
		}, errNoBalance},
		{"admission balance", func(tx pg.Tx) error {
			_, _, err := b.billing.reserveSources(t.Context(), tx, missing)
			return err
		}, errNoBalance},
		{"settlement balance", func(tx pg.Tx) error {
			_, _, err := settlementRows(t.Context(), tx, uuid.NewV7(), missing.Account)
			return err
		}, errNoBalance},
		{"holder", func(tx pg.Tx) error {
			return b.billing.fence(t.Context(), tx)
		}, ErrTerminated},
		{"admission holder", func(tx pg.Tx) error {
			_, _, err := b.billing.reserveSources(t.Context(), tx, b.admitted)
			return err
		}, ErrTerminated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.app.Tx(t.Context(), b.admitted.Scope, tc.read); !errors.Is(err, tc.want) {
				t.Errorf("refusal = %v, want %v", err, tc.want)
			}
		})
	}
}

// Distinct money, counter, window and result values catch positional mistakes that Go's identical scalar types cannot.
func TestBatchRowsMatchGeneratedQueries(t *testing.T) {
	b := newBank(t, 1e6, -29, Config{})
	h := b.hold(100)
	b.sql(
		"UPDATE billing.balances SET frozen = 23",
		fmt.Sprintf("UPDATE billing.spend SET spent = CASE subject WHEN '%s' THEN 41 ELSE 53 END", b.admitted.Workspace),
		fmt.Sprintf("INSERT INTO billing.spend(subject,window_start,site_id,account_id,spent) VALUES ('%s',date_trunc('day',clock_timestamp(),'UTC') - interval '400 days','%s','%s',17)", b.admitted.Workspace, b.admitted.Site, b.admitted.Account),
		"UPDATE billing.reservations SET charged_seq = 3, entry = 0, charged = 37, price = 43, waived = true, estimated = false, profit = 59, attempt_entries = '{0,-1,2}', key_window = key_window - interval '1 day'",
	)
	a := b.admitted
	a.WorkspaceBudget = &Limit{Amount: 1e5, Reset: ResetNever}
	a.KeyLimit = &Limit{Amount: 9999, Reset: ResetDaily}
	var wantBalance, reserveBalance, settleBalance LockBalanceRow
	var wantUsage, usage ReadUsageRow
	var wantRow, row LockReservationRow
	err := b.app.Tx(t.Context(), a.Scope, func(tx pg.Tx) error {
		var err error
		wantBalance, err = queries.LockBalance(t.Context(), tx, a.Account)
		if err != nil {
			return err
		}
		wantUsage, err = a.usage(t.Context(), tx, nil)
		if err != nil {
			return err
		}
		wantRow, err = queries.LockReservation(t.Context(), tx, h.id)
		if err != nil {
			return err
		}
		reserveBalance, usage, err = b.billing.reserveSources(t.Context(), tx, a)
		if err != nil {
			return err
		}
		settleBalance, row, err = settlementRows(t.Context(), tx, h.id, a.Account)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reserveBalance, wantBalance) || !reflect.DeepEqual(usage, wantUsage) {
		t.Errorf("reservation batch differs: balance %+v / %+v; usage %+v / %+v", reserveBalance, wantBalance, usage, wantUsage)
	}
	if !reflect.DeepEqual(settleBalance, wantBalance) || !reflect.DeepEqual(row, wantRow) {
		t.Errorf("settlement batch differs: balance %+v / %+v; row %+v / %+v", settleBalance, wantBalance, row, wantRow)
	}
}

func TestPostWaitsBeforeUpdatingBalanceAndNumbering(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	firstID, secondID := uuid.NewV7(), uuid.NewV7()
	entered, release := make(chan struct{}), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	put := func(id uuid.UUID, amount int64, hold bool) error {
		return b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			_, err := post(t.Context(), tx, event{site: b.admitted.Site, account: b.admitted.Account, kind: "credit", request: id}, line{bookGiftExpense, amount, siteCredits}, line{bookUserBalance, -amount, siteCredits})
			if hold {
				close(entered)
				<-release
			}
			return err
		})
	}
	go func() { first <- put(firstID, 5, true) }()
	<-entered
	go func() { second <- put(secondID, 7, false) }()
	b.waitForLockWait()
	close(release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 12 {
		t.Errorf("concurrent posts left balance %d, want 12", n)
	}
	if n := b.count(fmt.Sprintf("SELECT CASE WHEN (SELECT seq FROM billing.transactions WHERE request_id = '%s') < (SELECT seq FROM billing.transactions WHERE request_id = '%s') THEN 1 ELSE 0 END", firstID, secondID)); n != 1 {
		t.Error("ledger sequence did not follow commit order")
	}
	b.balanced()
	b.consistent()
}
