package payments

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Resolution proposes a manual conclusion. Exactly one of Refund and Payment is set; Outcome is succeeded/failed
// for a refund or credit for a held payment. Evidence must identify the channel record proving that conclusion.
// User is the authenticated proposing administrator, supplied by the caller after authorization.
type Resolution struct {
	Order                 OrderID
	Refund                RefundID
	Outcome               string
	Payment               Payment
	Key, Evidence, Reason string
	User                  uuid.UUID
}

func (r Resolution) check() error {
	if !validResolutionRequest(r) {
		return &FieldError{"/resolution", "user, key, channel evidence and reason are required"}
	}
	switch r.Outcome {
	case "credit":
		if r.Refund == (RefundID{}) && r.Payment.State == PayPaid && validPayment(r.Payment) && !invalidPaymentText(r.Payment.TradeNo) {
			return nil
		}
	case "succeeded", "failed":
		if r.Refund != (RefundID{}) && r.Payment == (Payment{}) {
			return nil
		}
	}
	return &FieldError{"/resolution", "a refund outcome or a confirmed payment is required"}
}

func validResolutionRequest(r Resolution) bool {
	return r.User != (uuid.UUID{}) && r.Key != "" && len(r.Key) <= 128 && !invalidPaymentText(r.Key) && validReason(r.Evidence, 8192) && validReason(r.Reason, 1000)
}

// Resolve persists the proposal; it moves no money. A second administrator must call ApproveResolution. The immutable
// contents, including evidence and target, are compared on every replay instead of trusting two supplied user IDs.
func (p *Payments) Resolve(ctx context.Context, s pg.Scope, r Resolution) error {
	if err := r.check(); err != nil {
		return err
	}
	order, err := p.paymentAccess(ctx, s, r.Order, true)
	if err != nil {
		return err
	}
	if r.Outcome == "credit" && r.Payment.Paid.Currency != order.Total.Currency {
		return ErrRefundForbidden
	}
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := boundOrderTx(ctx, tx); err != nil {
			return err
		}
		o, err := lockForChange(ctx, tx, r.Order)
		if err != nil {
			return err
		}
		return proposeResolution(ctx, tx, o, r)
	})
}

func proposeResolution(ctx context.Context, tx pg.Tx, o LockOrderRow, r Resolution) error {
	previous, err := queries.ReadResolution(ctx, tx, ReadResolutionParams{OrderID: o.ID, IdemKey: r.Key})
	if err != nil {
		return err
	}
	if len(previous) > 0 {
		return matchResolution(previous[0], r)
	}
	if err = resolutionTarget(ctx, tx, o, r); err != nil {
		return err
	}
	var refund *uuid.UUID
	if r.Refund != (RefundID{}) {
		id := uuid.UUID(r.Refund)
		refund = &id
	}
	err = queries.InsertResolution(ctx, tx, InsertResolutionParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID,
		RefundID: refund, Outcome: r.Outcome, TradeNo: r.Payment.TradeNo, Amount: r.Payment.Paid.Minor, Evidence: r.Evidence, Reason: r.Reason, RequestedBy: r.User, IdemKey: r.Key,
	})
	if err != nil {
		return err
	}
	return resolutionAudit(ctx, tx, o, r.User, "resolution.propose", r.Key, r.Reason)
}

func matchResolution(row PaymentsResolution, r Resolution) error {
	id := uuid.UUID{}
	if row.RefundID != nil {
		id = *row.RefundID
	}
	if id != uuid.UUID(r.Refund) || row.Outcome != r.Outcome || row.TradeNo != r.Payment.TradeNo || row.Amount != r.Payment.Paid.Minor ||
		row.Evidence != r.Evidence || row.Reason != r.Reason || row.RequestedBy != r.User {
		return ErrKeyReused
	}
	return nil
}

func resolutionTarget(ctx context.Context, tx pg.Tx, o LockOrderRow, r Resolution) error {
	if r.Outcome == "credit" {
		if State(o.State) != Review {
			return ErrRefundForbidden
		}
		return nil
	}
	row, err := queries.ReadRefund(ctx, tx, uuid.UUID(r.Refund))
	if err != nil {
		return err
	}
	if len(row) == 0 || row[0].OrderID != o.ID {
		return ErrNotFound
	}
	return canResolveRefund(row[0])
}

func canResolveRefund(r PaymentsRefund) error {
	if r.ApprovedBy == nil || r.State != "pending" && (r.Via != "offline" || r.State != "frozen") {
		return ErrRefundForbidden
	}
	return nil
}

// ApproveResolution approves the stored order/key proposal as another authenticated administrator. Approval, audit,
// settlement or release, and the order change commit together; a failure leaves an unapproved proposal for retry.
func (p *Payments) ApproveResolution(ctx context.Context, s pg.Scope, order OrderID, key string, user uuid.UUID) error {
	proposal, err := p.GetResolution(ctx, s, order, key)
	if err != nil {
		return err
	}
	if user == (uuid.UUID{}) {
		return ErrSecondPerson
	}
	if proposal.Refund != (RefundID{}) {
		row, _, err := p.refundAccess(ctx, s, proposal.Refund, true)
		if err != nil {
			return err
		}
		if err = billing.PrepareRefund(ctx, p.db, p.platform, refundFreeze(row)); err != nil {
			return err
		}
	}
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := boundOrderTx(ctx, tx); err != nil {
			return err
		}
		o, err := lockForChange(ctx, tx, order)
		if err != nil {
			return err
		}
		return p.approveResolution(ctx, tx, o, key, user)
	})
}

func (p *Payments) approveResolution(ctx context.Context, tx pg.Tx, o LockOrderRow, key string, user uuid.UUID) error {
	rows, err := queries.ReadResolution(ctx, tx, ReadResolutionParams{OrderID: o.ID, IdemKey: key})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	r := rows[0]
	if r.RequestedBy == user {
		return ErrSecondPerson
	}
	if r.ApprovedBy != nil {
		return nil
	}
	if r.Outcome == "credit" {
		err = p.resolvePayment(ctx, tx, o, r)
	} else {
		err = resolveRefund(ctx, tx, o, r)
	}
	if err != nil {
		return err
	}
	if err = queries.ApproveResolution(ctx, tx, ApproveResolutionParams{ID: r.ID, ApprovedBy: &user}); err != nil {
		return err
	}
	return resolutionAudit(ctx, tx, o, user, "resolution.approve", key, r.Reason)
}

func resolveRefund(ctx context.Context, tx pg.Tx, o LockOrderRow, r PaymentsResolution) error {
	refund, err := queries.LockRefund(ctx, tx, LockRefundParams{ID: *r.RefundID, OrderID: o.ID})
	if err != nil {
		return err
	}
	if refundTerminal(refund.State) {
		if refund.State == r.Outcome {
			return nil
		}
		return errors.New("payments: manual conclusion conflicts with the settled refund")
	}
	if err = canResolveRefund(refund); err != nil {
		return err
	}
	return finishRefundTx(ctx, tx, o, refund, r.Outcome, refund.ProviderRef, "admin")
}

func (p *Payments) resolvePayment(ctx context.Context, tx pg.Tx, o LockOrderRow, r PaymentsResolution) error {
	if State(o.State) != Review {
		return ErrRefundForbidden
	}
	currency, err := platform.ParseCurrency(o.PayCurrency)
	if err != nil {
		return err
	}
	c := confirmed{
		merchant: MerchantID(o.MerchantID), order: OrderID(o.ID).TypeID(), source: "admin", raw: r.Evidence,
		payment: Payment{State: PayPaid, TradeNo: r.TradeNo, Paid: Money{Currency: currency, Minor: r.Amount}},
	}
	kind, err := recordReceipt(ctx, tx, o, c)
	if err != nil {
		return err
	}
	if kind != "" {
		return errors.New("payments: conflicting payment needs a separate payment record")
	}
	return p.creditResolvedPayment(ctx, tx, o, c)
}

func resolutionAudit(ctx context.Context, tx pg.Tx, o LockOrderRow, user uuid.UUID, action, key, reason string) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(user), Action: action, Reason: reason,
		Target: audit.Target{Module: "payments", Kind: "order", ID: OrderID(o.ID).TypeID(), Site: o.SiteID, Account: o.AccountID},
		Detail: map[string]any{"key": key},
	})
}

// GetResolution returns the stored evidence and decision for an administrator to inspect before approving it.
func (p *Payments) GetResolution(ctx context.Context, s pg.Scope, order OrderID, key string) (Resolution, error) {
	o, err := p.paymentAccess(ctx, s, order, true)
	if err != nil {
		return Resolution{}, err
	}
	var rows []PaymentsResolution
	err = p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		rows, err = queries.ReadResolution(ctx, tx, ReadResolutionParams{OrderID: uuid.UUID(order), IdemKey: key})
		return err
	})
	if err != nil {
		return Resolution{}, err
	}
	if len(rows) == 0 {
		return Resolution{}, ErrNotFound
	}
	row := rows[0]
	r := Resolution{Order: order, Outcome: row.Outcome, Key: row.IdemKey, User: row.RequestedBy, Evidence: row.Evidence, Reason: row.Reason}
	if row.RefundID != nil {
		r.Refund = RefundID(*row.RefundID)
	} else {
		r.Payment = Payment{State: PayPaid, TradeNo: row.TradeNo, Paid: Money{Currency: o.Total.Currency, Minor: row.Amount}}
	}
	return r, nil
}
