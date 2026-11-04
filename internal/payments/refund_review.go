package payments

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// ApproveRefund records an independent administrator and then starts the channel flow. Within the limit one approval
// releases it; above the limit the first approval is recorded and a different administrator must approve again.
// Repeating approval never causes another send. Offline refunds remain frozen until an evidenced, separately approved
// manual conclusion.
func (p *Payments) ApproveRefund(ctx context.Context, s pg.Scope, id RefundID, user uuid.UUID, reason string) (Refund, error) {
	var refund Refund
	err := p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		refund, err = ApproveRefundTx(ctx, tx, id, user, reason)
		return err
	})
	if err != nil {
		return Refund{}, err
	}
	if refund.Via == "channel" {
		if err = p.SendRefund(ctx, s, id, true); err != nil {
			return Refund{}, err
		}
	}
	return p.GetRefund(ctx, s, id)
}

// ApproveRefundTx records an independent administrator and the audit entry in tx, without sending anything to the
// channel. Above the limit the first call records only the first approver, so ApprovedBy stays nil and the poller does
// not send; a different operator must call again. The caller must commit tx before SendRefund: the local decision and
// its audit are the idempotent effect, and the channel call is recoverable by the refund poller if the process dies
// between the two.
func ApproveRefundTx(ctx context.Context, tx pg.Tx, id RefundID, user uuid.UUID, reason string) (Refund, error) {
	if user == (uuid.UUID{}) || !validReason(reason, 1000) {
		return Refund{}, &FieldError{"/reason", "a reason of 1 to 1000 characters is required"}
	}
	row, o, err := refundAccessTx(ctx, tx, id, true)
	if err != nil {
		return Refund{}, err
	}
	if user == row.RequestedBy {
		return Refund{}, ErrSecondPerson
	}
	updated := row
	err = changeRefundTx(ctx, tx, OrderID(row.OrderID), row.ID, func(tx pg.Tx, _ LockOrderRow, locked PaymentsRefund) error {
		var err error
		updated, err = approveRefundRow(ctx, tx, row.ID, locked, user, reason)
		return err
	})
	if err != nil {
		return Refund{}, err
	}
	return refundOf(updated, o.Total.Currency), nil
}

// approveRefundRow applies one approval decision to a locked refund. The database row is changed only after the
// second-approval rule has been checked, so a second-required refund never gets approved_by without a first approver.
func approveRefundRow(ctx context.Context, tx pg.Tx, id uuid.UUID, locked PaymentsRefund, user uuid.UUID, reason string) (PaymentsRefund, error) {
	if refundTerminal(locked.State) {
		return PaymentsRefund{}, ErrRefundForbidden
	}
	if locked.ApprovedBy != nil {
		return locked, nil
	}
	if locked.SecondRequired && locked.FirstApprovedBy == nil {
		if err := queries.FirstApproveRefund(ctx, tx, FirstApproveRefundParams{ID: id, FirstApprovedBy: &user}); err != nil {
			return PaymentsRefund{}, err
		}
		if err := refundAudit(ctx, tx, locked, user, "refund.approve", reason, "first"); err != nil {
			return PaymentsRefund{}, err
		}
		locked.FirstApprovedBy = &user
		return locked, nil
	}
	if locked.SecondRequired && *locked.FirstApprovedBy == user {
		return locked, nil
	}
	if err := queries.ApproveRefund(ctx, tx, ApproveRefundParams{ID: id, ApprovedBy: &user}); err != nil {
		return PaymentsRefund{}, err
	}
	if err := refundAudit(ctx, tx, locked, user, "refund.approve", reason, "final"); err != nil {
		return PaymentsRefund{}, err
	}
	locked.ApprovedBy = &user
	return locked, nil
}

// SendRefund starts or resumes a channel refund that is already approved. It is separate from the local decision so the
// decision and its idempotency answer can commit together; a failure here is retried by the poller. initial marks the
// approval that may send for the first time.
func (p *Payments) SendRefund(ctx context.Context, s pg.Scope, id RefundID, initial bool) error {
	if _, _, err := p.refundAccess(ctx, s, id, true); err != nil {
		return err
	}
	return p.driveRefund(ctx, uuid.UUID(id), initial)
}

func refundAudit(ctx context.Context, tx pg.Tx, r PaymentsRefund, user uuid.UUID, action, reason, stage string) error {
	detail := map[string]any{"credits": r.Credits, "amount": r.Amount, "with_fee": r.WithFee, "via": r.Via}
	if stage != "" {
		detail["stage"] = stage
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(user), Action: action, Reason: reason,
		Target: audit.Target{Module: "payments", Kind: "refund", ID: RefundID(r.ID).TypeID(), Site: r.SiteID, Account: r.AccountID},
		Detail: detail,
	})
}

// one minute between attempts leaves a crashed sender fenced off for far longer than its five-second HTTP deadline.
const (
	refundCallTimeout   = 5 * time.Second
	refundFinishTimeout = 30 * time.Second
)

// refundFreeze uses the held credit, which may be smaller than the original requested credit.
func refundFreeze(row PaymentsRefund) billing.Freeze {
	return billing.Freeze{Account: row.AccountID, Batch: row.OrderID, Amount: row.Credits}
}

// RejectRefund declines a still unapproved, certainly unsent request. Unknown refunds must use Resolve instead.
func (p *Payments) RejectRefund(ctx context.Context, s pg.Scope, id RefundID, user uuid.UUID, reason string) (Refund, error) {
	if !validReason(reason, 1000) {
		return Refund{}, &FieldError{"/reason", "a reason of 1 to 1000 characters is required"}
	}
	if user == (uuid.UUID{}) {
		return Refund{}, ErrRefundForbidden
	}
	if err := p.PrepareRejectRefund(ctx, s, id); err != nil {
		return Refund{}, err
	}
	var refund Refund
	err := p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		refund, err = RejectRefundTx(ctx, tx, id, user, reason)
		return err
	})
	return refund, err
}

// PrepareRejectRefund advances the ledger before the decision transaction opens. It must run outside that transaction:
// a pool with one ordinary connection would otherwise wait on the connection the decision holds. It makes no decision;
// the refund lock in RejectRefundTx still checks the state before unfreezing.
func (p *Payments) PrepareRejectRefund(ctx context.Context, s pg.Scope, id RefundID) error {
	row, _, err := p.refundAccess(ctx, s, id, true)
	if err != nil {
		return err
	}
	if row.State != "frozen" || row.ApprovedBy != nil {
		return nil
	}
	return billing.PrepareRefund(ctx, p.db, p.platform, refundFreeze(row))
}

// RejectRefundTx returns the frozen credits and records the audit entry in tx, without committing it. The caller has
// prepared the ledger outside tx. A decision that already failed is answered as it is; an approved or terminal one is
// refused by the refund lock before billing unfreezes credits that a channel refund may already have taken.
func RejectRefundTx(ctx context.Context, tx pg.Tx, id RefundID, user uuid.UUID, reason string) (Refund, error) {
	if !validReason(reason, 1000) {
		return Refund{}, &FieldError{"/reason", "a reason of 1 to 1000 characters is required"}
	}
	if user == (uuid.UUID{}) {
		return Refund{}, ErrRefundForbidden
	}
	row, o, err := refundAccessTx(ctx, tx, id, true)
	if err != nil {
		return Refund{}, err
	}
	if row.State == "failed" {
		return refundOf(row, o.Total.Currency), nil
	}
	err = changeRefundTx(ctx, tx, OrderID(row.OrderID), row.ID, func(tx pg.Tx, o LockOrderRow, locked PaymentsRefund) error {
		if locked.State == "failed" {
			return nil
		}
		if locked.State != "frozen" || locked.ApprovedBy != nil {
			return ErrRefundForbidden
		}
		if err := finishRefundTx(ctx, tx, o, locked, "failed", "", "admin"); err != nil {
			return err
		}
		return refundAudit(ctx, tx, locked, user, "refund.reject", reason, "")
	})
	if err != nil {
		return Refund{}, err
	}
	row, _, err = refundAccessTx(ctx, tx, id, false)
	if err != nil {
		return Refund{}, err
	}
	return refundOf(row, o.Total.Currency), nil
}

// changeRefund shares bounded order -> refund locks across channel outcomes and refund review.
func (p *Payments) changeRefund(ctx context.Context, order OrderID, id uuid.UUID, change func(pg.Tx, LockOrderRow, PaymentsRefund) error) error {
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		return changeRefundTx(ctx, tx, order, id, change)
	})
}

// changeRefundTx is changeRefund in a transaction the caller owns; the caller's scope is the access control.
func changeRefundTx(ctx context.Context, tx pg.Tx, order OrderID, id uuid.UUID, change func(pg.Tx, LockOrderRow, PaymentsRefund) error) error {
	if err := boundOrderTx(ctx, tx); err != nil {
		return err
	}
	o, err := lockForChange(ctx, tx, order)
	if err != nil {
		return err
	}
	r, err := queries.LockRefund(ctx, tx, LockRefundParams{ID: id, OrderID: uuid.UUID(order)})
	if err != nil {
		return err
	}
	return change(tx, o, r)
}
