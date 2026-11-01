package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"
)

// dispute books a verified chargeback. The channel's event is already authentic; the order, the amounts and whether
// the money may move are this module's decisions, never the channel's. Every case that cannot be booked automatically
// becomes a discrepancy and still acknowledges the event, so a channel does not retry a decision only a person can
// make.
//
//nolint:cyclop,gocognit // the event-to-payment lookup and the decision table are one transaction
func (p *Payments) dispute(ctx context.Context, merchant MerchantID, n Notice) error {
	d := n.Dispute
	if d == nil {
		d = &DisputeNotice{}
	}
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := boundOrderTx(ctx, tx); err != nil {
			return err
		}
		if err := queries.HoldMerchantIdentity(ctx, tx, merchantKey(merchant)); err != nil {
			return err
		}
		if kind := invalidDispute(d); kind != "" {
			return p.disputeDifference(ctx, tx, merchant, nil, d, kind)
		}
		payment, err := queries.DisputeOrder(ctx, tx, DisputeOrderParams{MerchantID: uuid.UUID(merchant), TradeNo: d.PaymentIntent})
		if errors.Is(err, pg.ErrNoRows) {
			return p.disputeDifference(ctx, tx, merchant, nil, d, "dispute_unmatched")
		}
		if err != nil {
			return fmt.Errorf("payments: find the disputed payment: %w", err)
		}
		o, err := lockForChange(ctx, tx, OrderID(payment.OrderID))
		if err != nil {
			return fmt.Errorf("payments: lock the disputed order: %w", err)
		}
		if o.MerchantID != uuid.UUID(merchant) {
			return p.disputeDifference(ctx, tx, merchant, nil, d, "dispute_unmatched")
		}
		if d.Order != "" && d.Order != OrderID(o.ID).TypeID() {
			return p.disputeDifference(ctx, tx, merchant, &o, d, "dispute_order")
		}
		created, err := queries.InsertDispute(ctx, tx, InsertDisputeParams{
			ID: uuid.NewV7(), SiteID: o.SiteID, AccountID: o.AccountID, OrderID: o.ID, MerchantID: o.MerchantID,
			Provider: d.Provider, ProviderID: d.ID, PaymentIntent: d.PaymentIntent,
			Amount: d.Amount.Minor, Currency: d.Amount.Currency.String(), Reason: cutRunes(d.Reason, 64),
			Status: cutRunes(d.Status, 64),
		})
		if err != nil {
			return fmt.Errorf("payments: record the dispute: %w", err)
		}
		row, err := queries.DisputeByProvider(ctx, tx, DisputeByProviderParams{
			MerchantID: o.MerchantID, Provider: d.Provider, ProviderID: d.ID,
		})
		if err != nil {
			return fmt.Errorf("payments: lock the dispute: %w", err)
		}
		if row.OrderID != o.ID || row.PaymentIntent != d.PaymentIntent {
			return p.disputeDifference(ctx, tx, merchant, &o, d, "dispute_order")
		}
		return p.applyDispute(ctx, tx, o, row, d, created > 0, n.Raw)
	})
}

// applyDispute moves the row and the money according to the action. The row is locked, so two events for one dispute
// serialize here: a later "money taken" cannot overwrite a won dispute.
//
//nolint:cyclop,gocognit,funlen // every action is one branch of the dispute state machine, in one transaction
func (p *Payments) applyDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, fresh bool, raw string,
) error {
	status, reason := cutRunes(d.Status, 64), cutRunes(d.Reason, 64)
	switch d.Action {
	case DisputeWarning:
		changed, err := queries.TouchDispute(ctx, tx, TouchDisputeParams{Status: status, Reason: reason, ID: row.ID})
		if err != nil {
			return fmt.Errorf("payments: record the dispute warning: %w", err)
		}
		if fresh || changed > 0 {
			return disputeEvent(ctx, tx, o, d, "dispute_warning", State(o.State), raw)
		}
		return nil

	case DisputeWon:
		if row.State == "applied" {
			if err := billing.ReverseChargeBack(ctx, tx, billing.ChargebackAmounts{
				Batch: row.OrderID, Credits: row.Credits, Revision: row.ChargebackRevision,
				RefundAmounts: billing.RefundAmounts{Returned: row.Cash, Fee: row.Fee}, //nolint:modernize // the embedded type is required in a keyed literal
			}); err != nil {
				return fmt.Errorf("payments: reverse the dispute: %w", err)
			}
			n, err := queries.ReverseDispute(ctx, tx, ReverseDisputeParams{Status: status, ID: row.ID})
			if err != nil {
				return fmt.Errorf("payments: record the won dispute: %w", err)
			}
			if n != 1 {
				return fmt.Errorf("payments: dispute %s was reversed by someone else", row.ID)
			}
			// The order stays charged back: the dispute did happen and the order must not become refundable again.
			return disputeEvent(ctx, tx, o, d, "dispute_won", State(o.State), raw)
		}
		changed, err := queries.TouchDispute(ctx, tx, TouchDisputeParams{Status: status, Reason: reason, ID: row.ID})
		if err != nil {
			return fmt.Errorf("payments: record the won dispute: %w", err)
		}
		if fresh || changed > 0 {
			return disputeEvent(ctx, tx, o, d, "dispute_won", State(o.State), raw)
		}
		return nil

	case DisputeOpen, DisputeLost:
		if row.State == "reversed" {
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		if row.State == "applied" {
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		// A won event may arrive before the open event that it settles. Its row is still "recorded" because there was
		// nothing to reverse, but a late "money taken" must not book the chargeback the win already refused.
		if row.Status == "won" {
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		other, err := queries.OtherDispute(ctx, tx, OtherDisputeParams{OrderID: o.ID, ID: row.ID})
		if err != nil {
			return fmt.Errorf("payments: read the other disputes of the order: %w", err)
		}
		if other > 0 {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_second"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		if !chargeable(o) {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_unpaid"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		if d.Amount.Currency.String() != o.PayCurrency || d.Amount.Minor <= 0 {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_amount"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		terms, err := queries.RefundTerms(ctx, tx, o.ID)
		if err != nil {
			return fmt.Errorf("payments: read the refunds of the disputed order: %w", err)
		}
		if terms.Inflight {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_refund_in_flight"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		amounts, err := billing.ChargeBack(ctx, tx, billing.Chargeback{Batch: o.ID, Cash: d.Amount.Minor})
		if errors.Is(err, billing.ErrRefundInFlight) {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_refund_in_flight"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		if errors.Is(err, billing.ErrChargebackCash) {
			if err := p.disputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, "dispute_amount"); err != nil {
				return err
			}
			return p.touchDispute(ctx, tx, o, row, d, status, reason)
		}
		if err != nil {
			return fmt.Errorf("payments: book the dispute: %w", err)
		}
		n, err := queries.ApplyDispute(ctx, tx, ApplyDisputeParams{
			Status: status, Reason: reason, Credits: amounts.Credits, Cash: amounts.Returned, Fee: amounts.Fee,
			Revision: amounts.Revision, ID: row.ID,
		})
		if err != nil {
			return fmt.Errorf("payments: apply the dispute: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("payments: dispute %s was applied by someone else", row.ID)
		}
		moved, err := queries.MoveOrder(ctx, tx, MoveOrderParams{
			State: string(ChargedBack), TradeNo: "", ID: o.ID, FromStates: predecessors(ChargedBack),
		})
		if err != nil {
			return fmt.Errorf("payments: charge back the order: %w", err)
		}
		if moved != 1 {
			return fmt.Errorf("payments: order %s cannot be charged back from %s", OrderID(o.ID).TypeID(), o.State)
		}
		return disputeEvent(ctx, tx, o, d, "dispute", ChargedBack, raw)
	}
	return fmt.Errorf("payments: unknown dispute action %d", d.Action)
}

func (*Payments) touchDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason string,
) error {
	changed, err := queries.TouchDispute(ctx, tx, TouchDisputeParams{Status: status, Reason: reason, ID: row.ID})
	if err != nil {
		return fmt.Errorf("payments: update the dispute: %w", err)
	}
	if changed > 0 {
		return disputeEvent(ctx, tx, o, d, "dispute_status", State(o.State), "")
	}
	return nil
}

// disputeDifference records a chargeback that cannot be booked automatically. The record names the dispute, so a
// repeated event does not make another row; a changed amount or status is a new fact and gets its own row.
func (p *Payments) disputeDifference(ctx context.Context, tx pg.Tx, merchant MerchantID, o *LockOrderRow,
	d *DisputeNotice, kind string,
) error {
	detail := map[string]any{
		"dispute": d.ID, "provider": d.Provider, "payment_intent": d.PaymentIntent, "claimed_order": d.Order,
		"status": d.Status, "reason": d.Reason, "action": d.Action.String(), "kind": kind,
		"amount": d.Amount.Minor, "currency": d.Amount.Currency.String(),
	}
	key, err := idem.Fingerprint(d.Provider, d.ID)
	if err != nil {
		return err
	}
	r, err := pendingDifference(uuid.UUID(merchant), kind, fmt.Sprintf("%s/%x", d.Provider, key), detail)
	if err != nil {
		return err
	}
	if o != nil {
		r.OrderID, r.AccountID = &o.ID, &o.AccountID
	}
	inserted, err := recordPending(ctx, tx, r)
	if err != nil {
		return fmt.Errorf("payments: record the dispute discrepancy: %w", err)
	}
	if inserted {
		p.logger.Warn("payments: dispute needs attention", "merchant", merchant.TypeID(), "kind", kind,
			"dispute", d.ID, "payment_intent", d.PaymentIntent, "discrepancy", r.ID)
	}
	return nil
}

func disputeEvent(ctx context.Context, tx pg.Tx, o LockOrderRow, d *DisputeNotice, kind string, to State, raw string) error {
	amount := d.Amount.Minor
	return queries.OrderEvent(ctx, tx, OrderEventParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID, Source: "notify", Kind: kind,
		FromState: o.State, ToState: string(to), TradeNo: d.PaymentIntent, Amount: &amount, Raw: cut(raw),
	})
}

//nolint:cyclop // every field of the notice is a separate boundary check
func invalidDispute(d *DisputeNotice) string {
	if d == nil || invalidPaymentText(d.Provider) || invalidPaymentText(d.ID) ||
		invalidPaymentText(d.PaymentIntent) || d.Provider == "" || d.ID == "" || len(d.ID) > 255 ||
		len(d.Provider) > 32 || len(d.PaymentIntent) == 0 || len(d.PaymentIntent) > 1024 ||
		d.Amount.Minor <= 0 || len(d.Amount.Currency.String()) != 3 || d.Action > DisputeWon ||
		utf8.RuneCountInString(d.Status) > 64 || utf8.RuneCountInString(d.Reason) > 64 {
		return "dispute_invalid"
	}
	return ""
}

func chargeable(o LockOrderRow) bool {
	switch State(o.State) { //nolint:exhaustive // only these states can hold paid credit
	case Paid, PartiallyRefunded, Refunded:
		return true
	}
	return false
}

func cutRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return strings.ToValidUTF8(s, "")
	}
	r := []rune(strings.ToValidUTF8(s, ""))
	return string(r[:max])
}

func (a DisputeAction) String() string {
	switch a {
	case DisputeWarning:
		return "warning"
	case DisputeOpen:
		return "open"
	case DisputeLost:
		return "lost"
	case DisputeWon:
		return "won"
	}
	return "unknown"
}
