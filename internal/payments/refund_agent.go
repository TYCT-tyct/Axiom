package payments

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

// AgentRefund is a channel refund of part of a paid order that the operations agent asks for
// (docs/domains/operations.md「运营接口」): Agent is the credential that asks, Key the operation's ID, so an operation
// makes one refund at most, and Approver the person who approved the operation, who approves the refund too.
type AgentRefund struct {
	Order   OrderID
	Credits int64
	Reason  string
	Agent   uuid.UUID
	Key     string
	// Approver and ApprovalReason are the operation's approval; QuoteAgentRefundTx needs neither.
	Approver       uuid.UUID
	ApprovalReason string
}

// QuoteAgentRefundTx is what RequestAgentRefundTx would freeze and return for r in tx now, with the same refusals,
// and leaves nothing: it runs the request to a savepoint it then rolls back to, the row locks it took included.
func (p *Payments) QuoteAgentRefundTx(ctx context.Context, tx pg.Tx, r AgentRefund) (Refund, error) {
	if _, err := tx.Exec(ctx, "SAVEPOINT agent_refund_quote"); err != nil {
		return Refund{}, fmt.Errorf("payments: quote a refund: %w", err)
	}
	refund, err := p.requestAgentRefund(ctx, tx, r)
	if _, undo := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT agent_refund_quote"); undo != nil {
		return Refund{}, fmt.Errorf("payments: undo a refund's quote: %w", undo)
	}
	return refund, err
}

// RequestAgentRefundTx freezes r's credits and quotes its cash in tx, the transaction that approves the agent's
// operation, as an administrator's channel refund: the order must be paid, the credits unused, the merchant must
// refund through its channel and no other refund of the order may be in flight (ErrRefundAmount, ErrRefundForbidden,
// ErrRefundInFlight). The credential is its requester and the approver approves it, so the second-person rule holds as
// for any refund: within the site's limit the refund is approved and the refund poller sends it after tx commits; above
// it the approver is its first approver and a different administrator approves it again in the refund review. The
// order's lock and statement bounds (boundOrderTx) hold for the rest of tx. The key's refund, when r's request made it
// already, is returned as it is.
func (p *Payments) RequestAgentRefundTx(ctx context.Context, tx pg.Tx, r AgentRefund) (Refund, error) {
	refund, err := p.requestAgentRefund(ctx, tx, r)
	if err != nil || refund.Approved || refund.FirstApprovedBy != nil {
		return refund, err
	}
	return ApproveRefundTx(ctx, tx, refund.ID, r.Approver, r.ApprovalReason)
}

func (p *Payments) requestAgentRefund(ctx context.Context, tx pg.Tx, r AgentRefund) (Refund, error) {
	req := RefundRequest{Order: r.Order, Credits: r.Credits, Key: r.Key, Reason: r.Reason, Via: "channel", User: r.Agent, Agent: true}
	if err := req.check(); err != nil {
		return Refund{}, err
	}
	o, err := loadOrder(ctx, tx, r.Order)
	if err != nil {
		return Refund{}, err
	}
	if o.State != Paid && o.State != PartiallyRefunded { // as Refund: no paid batch to freeze
		return Refund{}, ErrRefundAmount
	}
	if err := boundOrderTx(ctx, tx); err != nil {
		return Refund{}, err
	}
	if err := queries.HoldMerchantIdentity(ctx, tx, merchantKey(o.MerchantID)); err != nil {
		return Refund{}, err
	}
	locked, err := lockForChange(ctx, tx, r.Order)
	if err != nil {
		return Refund{}, err
	}
	previous, err := queries.RefundByKey(ctx, tx, RefundByKeyParams{OrderID: locked.ID, IdemKey: r.Key})
	if err != nil {
		return Refund{}, err
	}
	if len(previous) > 0 {
		return refundReplay(previous[0], req, o)
	}
	row, err := p.freezeRefund(ctx, tx, locked, req, billing.Freeze{Account: o.AccountID, Batch: uuid.UUID(o.ID), Amount: r.Credits}, true)
	return refundOf(row, o.Total.Currency), err
}

// AgentRefundOf reads the refund operation key made of order, found false when there is none: the approval that
// would have made it rolled back, or it was never approved.
func (p *Payments) AgentRefundOf(ctx context.Context, order OrderID, key string) (Refund, bool, error) {
	var refund Refund
	var found bool
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		rows, err := queries.RefundByKey(ctx, tx, RefundByKeyParams{OrderID: uuid.UUID(order), IdemKey: key})
		if err != nil || len(rows) == 0 || !rows[0].RequestedByAgent {
			return err
		}
		o, err := loadOrder(ctx, tx, order)
		if err != nil {
			return err
		}
		refund, found = refundOf(rows[0], o.Total.Currency), true
		return nil
	})
	return refund, found, err
}

// OrderOwner is the site and account an order belongs to, which never change.
type OrderOwner struct{ Site, Account uuid.UUID }

// OrderOwnerTx reads whose order id is, in tx's scope; an order the scope does not see is ErrNotFound.
func OrderOwnerTx(ctx context.Context, tx pg.Tx, id OrderID) (OrderOwner, error) {
	o, err := loadOrder(ctx, tx, id)
	return OrderOwner{Site: o.SiteID, Account: o.AccountID}, err
}
