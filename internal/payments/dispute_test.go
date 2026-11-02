package payments

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

func disputeWorld(t *testing.T) (*world, Merchant, Order) {
	t.Helper()
	w := newTestWorld(t, fakePlugin{create: goodCheckout, caps: CapRefund | CapRefundKey})
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)
	o := w.mustPlace(1000, "dispute")
	w.receive(receipt(o))
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("order state = %s", state)
	}
	return w, merchant, o
}

func Dispute(provider, id, intent string, amount Money, action DisputeAction, status string) Notice {
	return Notice{Dispute: &DisputeNotice{
		Provider: provider, ID: id, PaymentIntent: intent, Amount: amount, Action: action, Status: status,
	}, Raw: "evt " + id}
}

//nolint:cyclop // the dispute lifecycle and its ledger assertions are one test
func TestDisputeAppliesOnceAndWinsBack(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	before := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID))
	n := Dispute("stripe", "dp_1", o.TradeNo, o.Total, DisputeOpen, "needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), n); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("state after chargeback = %s", state)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before-o.Credits {
		t.Fatalf("balance = %d, want %d", got, before-o.Credits)
	}
	var state string
	var credits, cash, fee, revision int64
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT state, credits, cash, fee, chargeback_revision FROM payments.order_disputes WHERE provider_id = 'dp_1'").Scan(&state, &credits, &cash, &fee, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "applied" || credits != o.Credits || cash != o.Total.Minor || fee != 0 || revision != 1 {
		t.Fatalf("dispute row = %s %d %d %d %d", state, credits, cash, fee, revision)
	}

	// The same event is idempotent: it neither moves money nor writes another order event.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), n); err != nil {
		t.Fatal(err)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before-o.Credits {
		t.Fatalf("duplicate changed balance to %d", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_events WHERE order_id = $1 AND kind = 'dispute'", o.ID); got != 1 {
		t.Fatalf("dispute events = %d", got)
	}

	won := Dispute("stripe", "dp_1", o.TradeNo, o.Total, DisputeWon, "won")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), won); err != nil {
		t.Fatal(err)
	}
	if got := int64(w.count(w.p.platform, "SELECT balance FROM billing.balances WHERE account_id = $1", o.AccountID)); got != before {
		t.Fatalf("balance after win = %d, want %d", got, before)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("a won dispute changed the order to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id = 'dp_1' AND state = 'reversed'"); got != 1 {
		t.Fatalf("dispute row state query = %d", got)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), won); err != nil {
		t.Fatalf("a repeated win: %v", err)
	}
}

func TestDisputeWarningThenOpen(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	warning := Dispute("stripe", "dp_2", o.TradeNo, o.Total, DisputeWarning, "warning_needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), warning); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("warning changed the order to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id = 'dp_2' AND state = 'recorded'"); got != 1 {
		t.Fatalf("warning row state query = %d", got)
	}
	// A warning alone never charges; the later needs_response does.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_2", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := w.stored(o.ID); state != "charged_back" {
		t.Fatalf("state after open = %s", state)
	}
}

func TestDisputeUnmatchedAndAmountMismatchBecomeDiscrepancies(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_x", "pi_unknown", o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got == 0 {
		t.Fatal("an unmatched dispute made no discrepancy")
	}
	before := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_y", o.TradeNo, Money{Currency: o.Total.Currency, Minor: o.Total.Minor + 1}, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != before+1 {
		t.Fatalf("amount mismatch discrepancies = %d, want %d", got, before+1)
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("a mismatched amount changed the order to %s", state)
	}
}

func TestDailyCashReconcilesChargebackAndReversal(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	day := w.accountingDay(o.ID)
	// The chargeback and the payment land on the same accounting day, so expected cash is paid total minus the
	// chargeback; a missing subtraction would open a ledger discrepancy here.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_cash", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != 0 {
		t.Fatalf("a chargeback opened %d discrepancies", got)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_cash", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state = 'open'"); got != 0 {
		t.Fatalf("a won chargeback opened %d discrepancies", got)
	}
}

func TestWonDisputeBeforeOpenDoesNotBookMoney(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	// Stripe can deliver the settlement before the event that opened the dispute. The win is recorded, and the late
	// open must not take money the win already refused.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_late", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_late", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
			t.Fatal(err)
		}
	}
	if state, _, _ := w.stored(o.ID); state != "paid" {
		t.Fatalf("late open moved a won dispute to %s", state)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.order_disputes WHERE provider_id='dp_late' AND status='won'"); got != 1 {
		t.Fatalf("a stale open overwrote the won status: %d rows still won", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind='chargeback'"); got != 0 {
		t.Fatalf("late open booked %d chargebacks", got)
	}
	if got := w.states.recorded(); len(got) != 0 {
		t.Fatalf("late open suspended the account: %+v", got)
	}
}

func TestSecondDisputeOnOnePaymentNeedsAPerson(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_first", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_second", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM billing.transactions WHERE kind='chargeback'"); got != 1 {
		t.Fatalf("a second dispute booked %d chargebacks", got)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open' AND detail->>'kind'='dispute_second'"); got != 1 {
		t.Fatalf("a second dispute opened %d discrepancies", got)
	}
}

func TestDisputeTimestampsUseTheLedgerDayAcrossMidnight(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	// Put the payment's batch on the previous UTC day, then make the chargeback and its reversal post just before
	// today's UTC midnight. The clock in this process is not the ledger's clock: the dispute row must take the
	// transaction time, or the two days' reconciliation each see a different day for the same cash.
	old := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	w.exec(fmt.Sprintf("UPDATE billing.credit_batches SET created_at='%s'::timestamptz WHERE id='%s'", old.Format(time.RFC3339), uuid.UUID(o.ID)))
	w.exec(`CREATE FUNCTION billing.previous_dispute_day() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind IN ('chargeback','chargeback_reverse') THEN NEW.created_at := date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 microsecond'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER previous_dispute_day BEFORE INSERT ON billing.transactions FOR EACH ROW EXECUTE FUNCTION billing.previous_dispute_day()`)

	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_midnight", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, `SELECT count(*) FROM payments.order_disputes d JOIN billing.transactions t
		ON t.request_id=d.order_id AND t.attempt_seq=0 AND t.kind='chargeback' AND t.revision=d.chargeback_revision
		WHERE d.provider_id='dp_midnight' AND d.applied_at=t.created_at`); got != 1 {
		t.Fatalf("the applied time does not match the chargeback transaction: %d", got)
	}
	var day time.Time
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT date_trunc('day',applied_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' FROM payments.order_disputes WHERE provider_id='dp_midnight'`).Scan(&day)
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open'"); got != 0 {
		t.Fatalf("the chargeback's ledger day opened %d discrepancies", got)
	}

	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_midnight", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, `SELECT count(*) FROM payments.order_disputes d JOIN billing.transactions t
		ON t.request_id=d.order_id AND t.attempt_seq=0 AND t.kind='chargeback_reverse' AND t.revision=d.chargeback_revision
		WHERE d.provider_id='dp_midnight' AND d.reversed_at=t.created_at`); got != 1 {
		t.Fatalf("the reversed time does not match the reversal transaction: %d", got)
	}
	if err := w.p.reconcileMerchant(t.Context(), merchant.ID, day); err != nil {
		t.Fatal(err)
	}
	if got := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies WHERE state='open'"); got != 0 {
		t.Fatalf("the reversal's ledger day opened %d discrepancies", got)
	}
}

func TestDisputeSuspendsTheAccountUntilAnOperatorLiftsIt(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	warning := Dispute("stripe", "dp_suspend", o.TradeNo, o.Total, DisputeWarning, "warning_needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), warning); err != nil {
		t.Fatal(err)
	}
	if got := w.states.recorded(); len(got) != 0 {
		t.Fatalf("a warning suspended the account: %+v", got)
	}
	open := Dispute("stripe", "dp_suspend", o.TradeNo, o.Total, DisputeOpen, "needs_response")
	for range 2 {
		if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), open); err != nil {
			t.Fatal(err)
		}
	}
	got := w.states.recorded()
	if len(got) != 1 || got[0].account != accounts.AccountID(o.AccountID) || !got[0].suspended ||
		!strings.Contains(got[0].reason, "stripe/dp_suspend") {
		t.Fatalf("suspension calls = %+v", got)
	}
	// A win reverses the money but does not lift the suspension: only an operator does.
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), Dispute("stripe", "dp_suspend", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if got := w.states.recorded(); len(got) != 1 {
		t.Fatalf("a win changed the suspension: %+v", got)
	}
}

func TestDisputeLostSuspendsTheAccount(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	lost := Dispute("stripe", "dp_lost", o.TradeNo, o.Total, DisputeLost, "lost")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), lost); err != nil {
		t.Fatal(err)
	}
	got := w.states.recorded()
	if len(got) != 1 || got[0].account != accounts.AccountID(o.AccountID) || !got[0].suspended {
		t.Fatalf("lost dispute suspension = %+v", got)
	}
}

// realAccounts returns the accounts module's service, so a suspension takes the real account row and alert locks
// instead of the recording fake.
func (w *world) realAccounts() *accounts.Service {
	w.t.Helper()
	path := filepath.Join(w.t.TempDir(), "mac-key")
	line := "1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32))) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		w.t.Fatal(err)
	}
	hashes, err := secret.LoadMAC(path)
	if err != nil {
		w.t.Fatal(err)
	}
	service, err := accounts.NewService(w.db, accounts.Config{Platform: w.p.platform, Hashes: hashes})
	if err != nil {
		w.t.Fatal(err)
	}
	return service
}

// createDisputeAccount gives the test world's account a row the accounts module can suspend.
func (w *world) createDisputeAccount() {
	w.t.Helper()
	tier := uuid.NewV7()
	// The site's default tier points at a tier of the same site, so the two references are inserted in one transaction
	// and checked together.
	err := w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts.sites(id,name,status,key_prefix,registration,default_tier_id)
			 VALUES($1,'Pay','active','pay-','invite',$2)`, []any{w.siteA, tier}},
			{`INSERT INTO accounts.tiers(id,site_id,name) VALUES($1,$2,'Default')`, []any{tier, w.siteA}},
			{`INSERT INTO accounts.accounts(id,site_id,kind,name,status,tier_id)
			 VALUES($1,$2,'personal','Payer','active',$3)`, []any{w.account, w.siteA, tier}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(w.t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) accountStatus() string {
	w.t.Helper()
	var status string
	err := w.db.ReadTx(w.t.Context(), w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), `SELECT status FROM accounts.accounts WHERE id=$1`, w.account).Scan(&status)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return status
}

// waitForBlocker waits until a session is queued behind blocker's transaction. A row lock wait usually shows up as a
// wait for the locking transaction, not as a lock on the table, so this asks which sessions blocker blocks.
func waitForBlocker(t *testing.T, w *world, blocker int32, disputed <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-disputed:
			t.Fatalf("the dispute finished before it waited for the balance row: %v", err)
		default:
		}
		var waiting int
		err := w.db.ReadTx(t.Context(), w.p.platform, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
				WHERE $1 = ANY(pg_blocking_pids(pid))`, blocker).Scan(&waiting)
		})
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the dispute never waited for the balance row")
}

// TestDisputeTakesTheBalanceLockBeforeTheAlertLock interleaves a billing correction (balance, then alert) with a dispute
// (money, then suspension, then alert). A dispute that suspends before it books the chargeback would hold the alert
// lock while waiting for the balance the correction holds, and neither transaction could finish.
func TestDisputeTakesTheBalanceLockBeforeTheAlertLock(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	held := make(chan int32, 1)
	release := make(chan struct{})
	correction := make(chan error, 1)
	go func() {
		correction <- w.db.Tx(ctx, w.p.platform, func(tx pg.Tx) error {
			var pid int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT 1 FROM billing.balances WHERE account_id=$1 FOR UPDATE`,
				w.account); err != nil {
				return err
			}
			held <- pid
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
				Kind: "billing.charge_correction_borne", Key: "review", Severity: "critical",
				Fields: map[string]string{"value": "1"},
			})
		})
	}()
	blocker := <-held

	disputed := make(chan error, 1)
	go func() {
		disputed <- w.p.dispute(ctx, MerchantID(merchant.ID),
			Dispute("stripe", "dp_order", o.TradeNo, o.Total, DisputeOpen, "needs_response"))
	}()
	waitForBlocker(t, w, blocker, disputed)
	close(release)
	if err := <-correction; err != nil {
		t.Fatalf("the correction could not persist while the dispute held the balance row: %v", err)
	}
	if err := <-disputed; err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("account status = %s", got)
	}
}

// TestOldNodeDisputeSuspendsWhenANewNodeSeesIt covers the mixed-version window: an older node books the chargeback
// without recording a suspension, so a later event on a new node must still suspend the account, once.
func TestOldNodeDisputeSuspendsWhenANewNodeSeesIt(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	open := Dispute("stripe", "dp_old", o.TradeNo, o.Total, DisputeOpen, "needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), open); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("account status = %s", got)
	}
	// The row looks like an old node's: applied without a mark. An operator lifted the account in the meantime.
	w.exec("UPDATE payments.order_disputes SET suspended_at = NULL WHERE provider_id = 'dp_old'")
	w.exec(fmt.Sprintf("UPDATE accounts.accounts SET status = 'active' WHERE id = '%s'", w.account))
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), open); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("a repeat did not repair the old node's dispute: %s", got)
	}
	// The mark now keeps a repeat from suspending an account an operator lifted again.
	w.exec(fmt.Sprintf("UPDATE accounts.accounts SET status = 'active' WHERE id = '%s'", w.account))
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), open); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "active" {
		t.Fatalf("a repeat suspended an account an operator had lifted: %s", got)
	}
}

func TestPollRepairsADisputeAnOldNodeAppliedWithoutSuspension(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID),
		Dispute("stripe", "dp_sweep", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	w.exec("UPDATE payments.order_disputes SET suspended_at = NULL WHERE provider_id = 'dp_sweep'")
	w.exec(fmt.Sprintf("UPDATE accounts.accounts SET status = 'active' WHERE id = '%s'", w.account))
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("the poll did not repair the dispute: %s", got)
	}
	var marked bool
	if err := w.db.ReadTx(t.Context(), w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT suspended_at IS NOT NULL FROM payments.order_disputes
			WHERE provider_id = 'dp_sweep'`).Scan(&marked)
	}); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("the repaired dispute was not marked")
	}
}
