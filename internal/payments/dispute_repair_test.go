package payments

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestDisputeSuspensionBackfillMarksOnlyBookedDisputes runs the migration's backfill over a warning that never suspended
// the account and over disputes that did take money. The warning stays unmarked so its open event suspends; the booked
// rows are marked so a later event does not suspend again.
//
//nolint:cyclop,gocognit // the migration fixture, the backfill and the warning's open are one event sequence.
func TestDisputeSuspensionBackfillMarksOnlyBookedDisputes(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	insert := `INSERT INTO payments.order_disputes
		(id, site_id, account_id, order_id, merchant_id, provider, provider_id, payment_intent, amount, currency,
		 reason, status, state, credits, cash, fee, chargeback_revision, applied_at, reversed_at)
		VALUES ($1,$2,$3,$4,$5,'stripe',$6,$7,$8,'USD','test',$9,$10,$11,$12,$13,$14,$15,$16)`
	now := time.Now()
	for _, row := range []struct {
		id, providerID, state, status string
		applied, reversed             bool
	}{
		{"dp_warn", "dp_warn", "recorded", "warning_needs_response", false, false},
		{"dp_applied", "dp_applied", "applied", "needs_response", true, false},
		{"dp_reversed", "dp_reversed", "reversed", "won", true, true},
	} {
		var appliedAt, reversedAt any
		if row.applied {
			appliedAt = now
		}
		if row.reversed {
			reversedAt = now
		}
		revision := 0
		if row.applied {
			revision = 1
		}
		if err := w.tx(w.p.platform, func(tx pg.Tx) error {
			_, err := tx.Exec(w.t.Context(), insert, uuid.NewV7(), o.SiteID, o.AccountID, o.ID, merchant.ID,
				row.providerID, o.TradeNo, o.Total.Minor, row.status, row.state, int64(100), int64(100), int64(0),
				revision, appliedAt, reversedAt)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := fs.ReadFile(Migrations().Migrations, "00012_refund_approval.sql")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), "UPDATE payments.order_disputes SET suspended_at")
	end := strings.Index(string(raw)[start:], ";")
	if start < 0 || end < 0 {
		t.Fatal("the dispute backfill is not in the migration")
	}
	w.exec(string(raw)[start : start+end+1])
	for providerID, want := range map[string]bool{"dp_warn": false, "dp_applied": true, "dp_reversed": true} {
		var marked bool
		if err := w.tx(w.p.platform, func(tx pg.Tx) error {
			return tx.QueryRow(w.t.Context(), `SELECT suspended_at IS NOT NULL FROM payments.order_disputes
				WHERE provider_id=$1`, providerID).Scan(&marked)
		}); err != nil {
			t.Fatal(err)
		}
		if marked != want {
			t.Fatalf("%s marked=%v, want %v", providerID, marked, want)
		}
	}
	// The warning's later open is the event that suspends the account.
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID),
		Dispute("stripe", "dp_warn", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("account status after the warning's open = %s", got)
	}
}

// TestWonDisputeRepairsAnOldNodesMissedSuspension covers an older node that booked the open, lost the suspension, and
// left the row applied; the win on a new node reverses the money and must record the missing suspension.
func TestWonDisputeRepairsAnOldNodesMissedSuspension(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	open := Dispute("stripe", "dp_wonrepair", o.TradeNo, o.Total, DisputeOpen, "needs_response")
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID), open); err != nil {
		t.Fatal(err)
	}
	w.exec("UPDATE payments.order_disputes SET suspended_at = NULL WHERE provider_id = 'dp_wonrepair'")
	w.exec(fmt.Sprintf("UPDATE accounts.accounts SET status = 'active' WHERE id = '%s'", w.account))
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID),
		Dispute("stripe", "dp_wonrepair", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("the win did not repair the missed suspension: %s", got)
	}
}

func TestPollRepairsAReversedDisputeWithoutSuspension(t *testing.T) {
	w, merchant, o := disputeWorld(t)
	w.createDisputeAccount()
	w.p.accounts = w.realAccounts()
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID),
		Dispute("stripe", "dp_reversed", o.TradeNo, o.Total, DisputeOpen, "needs_response")); err != nil {
		t.Fatal(err)
	}
	if err := w.p.dispute(t.Context(), MerchantID(merchant.ID),
		Dispute("stripe", "dp_reversed", o.TradeNo, o.Total, DisputeWon, "won")); err != nil {
		t.Fatal(err)
	}
	// The row is now reversed. Simulate the pre-fix path that reversed it without suspending.
	w.exec("UPDATE payments.order_disputes SET suspended_at = NULL WHERE provider_id = 'dp_reversed'")
	w.exec(fmt.Sprintf("UPDATE accounts.accounts SET status = 'active' WHERE id = '%s'", w.account))
	if err := w.p.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.accountStatus(); got != "disabled" {
		t.Fatalf("the poll did not repair the reversed dispute: %s", got)
	}
}
