package billing

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// TestReserveSeesPlanGrantedWhileWaitingForTheBalance: a grant holds the balance row while admission waits for it. After
// the grant commits, admission's plan read must start a new statement: the lock statement's snapshot predates the wait
// and still says there is no plan.
func TestReserveSeesPlanGrantedWhileWaitingForTheBalance(t *testing.T) {
	b := newBank(t, 100, 0, Config{})
	plan := b.makePlan(300, 3600)

	granted := make(chan struct{})
	release := make(chan struct{})
	grantDone := make(chan error, 1)
	go func() {
		grantDone <- b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			if err := CreditSubscription(t.Context(), tx, SubscriptionGrant{
				ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Plan: plan.ID,
				SiteCurrency: usd, Source: "gift", Actor: audit.System("billing.test"), Reason: "concurrent grant",
			}); err != nil {
				return err
			}
			close(granted)
			<-release
			return nil
		})
	}()
	<-granted

	pid := make(chan int, 1)
	type admission struct {
		hold *Hold
		err  error
	}
	admitted := make(chan admission, 1)
	go func() {
		var h *Hold
		err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			var backend int
			if err := tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&backend); err != nil {
				return err
			}
			pid <- backend
			var err error
			h, err = b.billing.reserveOne(t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 100}, []Quotable{b.exact})
			return err
		})
		admitted <- admission{hold: h, err: err}
	}()
	backend := <-pid
	waitForLock(t, b, backend)

	close(release)
	if err := <-grantDone; err != nil {
		t.Fatal(err)
	}
	got := <-admitted
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.hold == nil || !got.hold.plan() {
		t.Fatalf("the hold after the waiting grant is %+v, want a plan hold", got.hold)
	}
	if reserved := b.window(1).Reserved; reserved != 100 {
		t.Errorf("window reserved %d, want 100", reserved)
	}
	if reserved := b.count("SELECT reserved FROM billing.balances WHERE account_id = '" + b.admitted.Account.String() + "'"); reserved != 0 {
		t.Errorf("wallet reserved %d, want 0: the plan should have funded the call", reserved)
	}
	b.consistent()
}

// waitForLock waits until pid is blocked on a lock, so the test releases the grant only after admission has reached the
// balance row rather than merely started its transaction.
func waitForLock(t *testing.T, b *bank, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)", pid).Scan(&waiting)
		}); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("admission %d never blocked on the balance lock", pid)
}

// The missing window is fault injection by a separate privileged transaction, not an application deletion path.
// It commits after planning and before writing, so a successful batch must account for every distinct window.
func TestSettlementBatchChecksDistinctWindows(t *testing.T) {
	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(400, 3600), uuid.NewV7())
	batch := func() []Settlement {
		return []Settlement{
			{Hold: b.holdOn(b.exact, 100), Outcome: outcome(Committed, FinishStop, used(t, 60, 0, 0))},
			{Hold: b.holdOn(b.exact, 100), Outcome: outcome(Committed, FinishStop, used(t, 70, 0, 0))},
		}
	}
	items := batch()
	for range 2 {
		// The replay neither charges again nor expects an update for an already settled window.
		if _, _, err := b.billing.SettleBatch(t.Context(), b.admitted.Scope, items); err != nil {
			t.Fatal(err)
		}
		if w := b.window(1); w.Used != 130 || w.Reserved != 0 {
			t.Fatalf("window = %+v, want 130 used and no reservation", w)
		}
	}
	items = batch()
	snapshot := func() [4]int64 {
		return [4]int64{
			b.count("SELECT count(*) FROM billing.reservations WHERE state = 'pending'"),
			b.count("SELECT count(*) FROM billing.transactions"),
			b.count("SELECT sum(reserved) FROM billing.spend"),
			b.count("SELECT balance FROM billing.balances"),
		}
	}
	before := snapshot()
	err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		plans, err := b.billing.planSettlements(t.Context(), tx, items)
		if err != nil {
			return err
		}
		b.sql(fmt.Sprintf("DELETE FROM billing.subscription_windows WHERE subscription_id = '%s' AND seq = 1", sub.ID))
		return writeSettlements(t.Context(), tx, b.supplies, plans)
	})
	if err == nil || !strings.Contains(err.Error(), "settle the subscription windows wrote 0 of 1 rows") {
		t.Fatalf("settlement error = %v, want the missing window", err)
	}
	if after := snapshot(); after != before {
		t.Errorf("pending/transactions/reserved/balance = %v, want %v after rollback", after, before)
	}
	b.balanced()
}
