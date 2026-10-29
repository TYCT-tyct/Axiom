package payments

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestRefundCompletionPreparesHistoryBeforeOrderLock(t *testing.T) {
	w, o := refundWorld(t, fakePlugin{caps: CapRefund, refund: func(context.Context, RefundCall) (RefundState, error) { return RefundState{State: RefundPending}, nil }})
	r := w.approve(w.mustRefund(o, o.Credits, "prepare-completion"))
	row, _, err := w.p.refundAccess(t.Context(), w.p.platform, r.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	w.exec(fmt.Sprintf(`INSERT INTO billing.transactions(id,site_id,account_id,kind,request_id) VALUES (gen_random_uuid(),'%s','%s','settle',gen_random_uuid())`, w.siteA, w.account))
	super, err := pg.Open(t.Context(), w.super)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close()
	done := make(chan error, 1)
	err = super.Tx(t.Context(), w.p.platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "LOCK TABLE billing.entries IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		go func() { done <- w.p.saveRefundOutcome(t.Context(), row, "succeeded", "", false) }()
		waitForRefundHistory(w)
		_, err := tx.Exec(t.Context(), "SELECT id FROM payments.orders WHERE id=$1 FOR UPDATE NOWAIT", uuid.UUID(o.ID))
		return err
	})
	completionErr := <-done
	if err != nil {
		t.Fatal("refund held its order lock while preparing ledger history", err)
	}
	if completionErr != nil {
		t.Fatal(completionErr)
	}
	if w.mustGetRefund(r.ID).State != "succeeded" {
		t.Fatal("refund did not settle")
	}
}

func waitForRefundHistory(w *world) {
	w.t.Helper()
	for w.count(w.p.platform, `SELECT count(*) FROM pg_locks WHERE NOT granted AND relation='billing.entries'::regclass
        AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`) == 0 {
		runtime.Gosched()
	}
}
