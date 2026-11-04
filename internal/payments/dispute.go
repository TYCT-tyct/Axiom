package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // sqlc's timestamp parameters; the value itself comes from the billing ledger
)

// dispute books a verified chargeback. The channel's event is already authentic; the order, the amounts and whether
// the money may move are this module's decisions, never the channel's. Every case that cannot be booked automatically
// becomes a discrepancy and still acknowledges the event, so a channel does not retry a decision only a person can
// make.
func (p *Payments) dispute(ctx context.Context, merchant MerchantID, n Notice) error {
	d := n.Dispute
	if d == nil {
		d = &DisputeNotice{}
	}
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		return p.disputeNotice(ctx, tx, merchant, d, n.Raw)
	})
}

// disputeNotice finds the disputed payment and its already recorded dispute inside the callback transaction.
func (p *Payments) disputeNotice(ctx context.Context, tx pg.Tx, merchant MerchantID, d *DisputeNotice, raw string) error {
	if err := boundOrderTx(ctx, tx); err != nil {
		return err
	}
	if err := queries.HoldMerchantIdentity(ctx, tx, merchantKey(merchant)); err != nil {
		return err
	}
	if kind := invalidDispute(d); kind != "" {
		return p.disputeDifference(ctx, tx, merchant, nil, d, kind)
	}
	o, err := p.disputedOrder(ctx, tx, merchant, d)
	if err != nil || o == nil {
		return err
	}
	return p.bookDispute(ctx, tx, merchant, *o, d, raw)
}

// disputedOrder returns the locked order the notice names, or nil after recording why the notice matches no order.
func (p *Payments) disputedOrder(ctx context.Context, tx pg.Tx, merchant MerchantID, d *DisputeNotice) (*LockOrderRow, error) {
	payment, err := queries.DisputeOrder(ctx, tx, DisputeOrderParams{MerchantID: uuid.UUID(merchant), TradeNo: d.PaymentIntent})
	if errors.Is(err, pg.ErrNoRows) {
		return nil, p.disputeDifference(ctx, tx, merchant, nil, d, "dispute_unmatched")
	}
	if err != nil {
		return nil, fmt.Errorf("payments: find the disputed payment: %w", err)
	}
	o, err := lockForChange(ctx, tx, OrderID(payment.OrderID))
	if err != nil {
		return nil, fmt.Errorf("payments: lock the disputed order: %w", err)
	}
	if o.MerchantID != uuid.UUID(merchant) {
		return nil, p.disputeDifference(ctx, tx, merchant, nil, d, "dispute_unmatched")
	}
	if d.Order != "" && d.Order != OrderID(o.ID).TypeID() {
		return nil, p.disputeDifference(ctx, tx, merchant, &o, d, "dispute_order")
	}
	return &o, nil
}

// bookDispute records the locked order's dispute row and applies the event.
func (p *Payments) bookDispute(ctx context.Context, tx pg.Tx, merchant MerchantID, o LockOrderRow, d *DisputeNotice, raw string) error {
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
	return p.applyDispute(ctx, tx, o, row, d, created > 0, raw)
}

// applyDispute moves the row and the money according to the action. The row is locked, so two events for one dispute
// serialize here: a later "money taken" cannot overwrite a won dispute.
func (p *Payments) applyDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, fresh bool, raw string,
) error {
	status, reason := cutRunes(d.Status, 64), cutRunes(d.Reason, 64)
	switch d.Action {
	case DisputeWarning:
		return noteDispute(ctx, tx, o, row, d, status, reason, "dispute warning", "dispute_warning", fresh, raw)
	case DisputeWon:
		if row.State == "applied" {
			return p.reverseDispute(ctx, tx, o, row, d, status, raw)
		}
		return noteDispute(ctx, tx, o, row, d, status, reason, "won dispute", "dispute_won", fresh, raw)
	case DisputeOpen, DisputeLost:
		return p.applyOpenDispute(ctx, tx, o, row, d, status, reason, raw)
	}
	return fmt.Errorf("payments: unknown dispute action %d", d.Action)
}

// noteDispute records a status the channel reported without moving money, and writes an order event when the status
// changed or the event's own row is new. A repeated event changes nothing and writes nothing.
func noteDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason, what, kind string, fresh bool, raw string,
) error {
	changed, err := queries.TouchDispute(ctx, tx, TouchDisputeParams{Status: status, Reason: reason, ID: row.ID})
	if err != nil {
		return fmt.Errorf("payments: record the %s: %w", what, err)
	}
	if fresh || changed > 0 {
		return disputeEvent(ctx, tx, o, d, kind, State(o.State), raw)
	}
	return nil
}

// reverseDispute returns the money of a dispute this transaction already booked. The order stays charged back: the
// dispute did happen and the order must not become refundable again. A win does not lift a suspension, and an older
// node that booked the chargeback may have missed it, so the suspension is recorded now when this dispute has not
// suspended the account yet.
func (p *Payments) reverseDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, raw string,
) error {
	if err := billing.ReverseChargeBack(ctx, tx, billing.ChargebackAmounts{
		Batch: row.OrderID, Credits: row.Credits, Revision: row.ChargebackRevision,
		RefundAmounts: billing.RefundAmounts{Returned: row.Cash, Fee: row.Fee}, //nolint:modernize // the embedded type is required in a keyed literal
	}); err != nil {
		return fmt.Errorf("payments: reverse the dispute: %w", err)
	}
	reversedAt, err := billing.TransactionCreatedAt(ctx, tx, row.OrderID, "chargeback_reverse", row.ChargebackRevision)
	if err != nil {
		return fmt.Errorf("payments: read the reversal time: %w", err)
	}
	n, err := queries.ReverseDispute(ctx, tx, ReverseDisputeParams{
		Status: status, ReversedAt: pgtype.Timestamptz{Time: reversedAt, Valid: true}, ID: row.ID,
	})
	if err != nil {
		return fmt.Errorf("payments: record the won dispute: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("payments: dispute %s was reversed by someone else", row.ID)
	}
	if err := p.suspendForDispute(ctx, tx, row, o.AccountID, d.Provider, d.ID); err != nil {
		return err
	}
	return disputeEvent(ctx, tx, o, d, "dispute_won", State(o.State), raw)
}

// applyOpenDispute books a "money taken" event and then suspends the account. The money moves before the suspension and
// the suspension before any discrepancy signal, so this transaction takes the balance lock, then the account row and
// only then the global alert lock; a billing correction takes the balance and alert locks in that order, and an
// operator's own suspension takes the account row and then the alert lock. The dispute records that it suspended the
// account, so a repeated event cannot suspend an account an operator has already lifted.
func (p *Payments) applyOpenDispute(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason, raw string,
) error {
	difference, record, err := p.openDisputeMoney(ctx, tx, o, row, d, status, reason, raw)
	if err != nil {
		return err
	}
	// A win that arrived before its open already refused the chargeback; a stale open must not suspend the account.
	if row.Status != "won" {
		if err := p.suspendForDispute(ctx, tx, row, o.AccountID, d.Provider, d.ID); err != nil {
			return err
		}
	}
	if record {
		return persistPending(ctx, tx, difference)
	}
	return nil
}

// openDisputeMoney applies the money side of an open or lost dispute. It returns the discrepancy it recorded when the
// chargeback cannot be booked automatically; its signal waits for the caller's suspension, for the lock order above.
func (p *Payments) openDisputeMoney(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason, raw string,
) (RecordDiscrepancyParams, bool, error) {
	// A won event may arrive before the open event that it settles. Its row is still "recorded" because there was
	// nothing to reverse, but a late "money taken" must not book the chargeback the win already refused.
	if row.State == "reversed" || row.State == "applied" || row.Status == "won" {
		return RecordDiscrepancyParams{}, false, p.touchDispute(ctx, tx, o, row, d, status, reason)
	}
	kind, err := disputeRefusal(ctx, tx, o, row, d)
	if err != nil {
		return RecordDiscrepancyParams{}, false, err
	}
	if kind != "" {
		return p.openDisputeDifference(ctx, tx, o, row, d, status, reason, kind)
	}
	amounts, err := billing.ChargeBack(ctx, tx, billing.Chargeback{Batch: o.ID, Cash: d.Amount.Minor})
	if errors.Is(err, billing.ErrRefundInFlight) {
		return p.openDisputeDifference(ctx, tx, o, row, d, status, reason, "dispute_refund_in_flight")
	}
	if errors.Is(err, billing.ErrChargebackCash) {
		return p.openDisputeDifference(ctx, tx, o, row, d, status, reason, "dispute_amount")
	}
	if err != nil {
		return RecordDiscrepancyParams{}, false, fmt.Errorf("payments: book the dispute: %w", err)
	}
	return RecordDiscrepancyParams{}, false, applyChargeback(ctx, tx, o, row, d, status, reason, amounts, raw)
}

// disputeRefusal names the automatic-chargeback rule that keeps this event from moving money, in the order the rules
// are checked; an empty kind means the chargeback may be booked.
func disputeRefusal(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice,
) (string, error) {
	other, err := queries.OtherDispute(ctx, tx, OtherDisputeParams{OrderID: o.ID, ID: row.ID})
	if err != nil {
		return "", fmt.Errorf("payments: read the other disputes of the order: %w", err)
	}
	if other > 0 {
		return "dispute_second", nil
	}
	if !chargeable(o) {
		return "dispute_unpaid", nil
	}
	if d.Amount.Currency.String() != o.PayCurrency || d.Amount.Minor <= 0 {
		return "dispute_amount", nil
	}
	terms, err := queries.RefundTerms(ctx, tx, o.ID)
	if err != nil {
		return "", fmt.Errorf("payments: read the refunds of the disputed order: %w", err)
	}
	if terms.Inflight {
		return "dispute_refund_in_flight", nil
	}
	return "", nil
}

// openDisputeDifference records the unbookable chargeback and updates the dispute row; its signal is left to the
// caller so a suspension can take the alert lock first.
func (p *Payments) openDisputeDifference(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason, kind string,
) (RecordDiscrepancyParams, bool, error) {
	recorded, inserted, err := p.recordDisputeDifference(ctx, tx, MerchantID(o.MerchantID), &o, d, kind)
	if err != nil {
		return RecordDiscrepancyParams{}, false, err
	}
	if err := p.touchDispute(ctx, tx, o, row, d, status, reason); err != nil {
		return RecordDiscrepancyParams{}, false, err
	}
	return recorded, inserted, nil
}

// applyChargeback records the booked chargeback, moves the order and writes its event.
func applyChargeback(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsOrderDispute,
	d *DisputeNotice, status, reason string, amounts billing.ChargebackAmounts, raw string,
) error {
	appliedAt, err := billing.TransactionCreatedAt(ctx, tx, o.ID, "chargeback", amounts.Revision)
	if err != nil {
		return fmt.Errorf("payments: read the chargeback time: %w", err)
	}
	n, err := queries.ApplyDispute(ctx, tx, ApplyDisputeParams{
		Status: status, Reason: reason, Credits: amounts.Credits, Cash: amounts.Returned, Fee: amounts.Fee,
		Revision: amounts.Revision, AppliedAt: pgtype.Timestamptz{Time: appliedAt, Valid: true}, ID: row.ID,
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

// suspendForDispute suspends the account and marks the dispute once. The mark keeps a repeated callback from
// suspending an account an operator has already lifted; a dispute an older node applied carries no mark and is
// repaired here or by the poll.
func (p *Payments) suspendForDispute(ctx context.Context, tx pg.Tx, row PaymentsOrderDispute, account uuid.UUID,
	provider, providerID string,
) error {
	if row.SuspendedAt.Valid {
		return nil
	}
	if p.accounts == nil {
		return errors.New("payments: account suspension is not configured")
	}
	if err := p.accounts.SetAccountSuspended(ctx, tx, audit.System("payments.dispute"), accounts.AccountID(account), true,
		"chargeback "+provider+"/"+providerID); err != nil {
		return fmt.Errorf("payments: suspend the disputed account: %w", err)
	}
	if _, err := queries.MarkDisputeSuspended(ctx, tx, row.ID); err != nil {
		return fmt.Errorf("payments: record the dispute suspension: %w", err)
	}
	return nil
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

// disputeDifference records and signals a chargeback that cannot be booked automatically.
func (p *Payments) disputeDifference(ctx context.Context, tx pg.Tx, merchant MerchantID, o *LockOrderRow,
	d *DisputeNotice, kind string,
) error {
	r, inserted, err := p.recordDisputeDifference(ctx, tx, merchant, o, d, kind)
	if err != nil || !inserted {
		return err
	}
	return persistPending(ctx, tx, r)
}

// recordDisputeDifference inserts a chargeback that cannot be booked automatically, leaving its signal to the caller.
// The record names the dispute, so a repeated event does not make another row; a changed amount or status is a new fact
// and gets its own row.
func (p *Payments) recordDisputeDifference(ctx context.Context, tx pg.Tx, merchant MerchantID, o *LockOrderRow,
	d *DisputeNotice, kind string,
) (RecordDiscrepancyParams, bool, error) {
	var zero RecordDiscrepancyParams
	detail := map[string]any{
		"dispute": d.ID, "provider": d.Provider, "payment_intent": d.PaymentIntent, "claimed_order": d.Order,
		"status": d.Status, "reason": d.Reason, "action": d.Action.String(), "kind": kind,
		"amount": d.Amount.Minor, "currency": d.Amount.Currency.String(),
	}
	key, err := idem.Fingerprint(d.Provider, d.ID)
	if err != nil {
		return zero, false, err
	}
	r, err := pendingDifference(uuid.UUID(merchant), kind, fmt.Sprintf("%s/%x", d.Provider, key), detail)
	if err != nil {
		return zero, false, err
	}
	if o != nil {
		r.OrderID, r.AccountID = &o.ID, &o.AccountID
	}
	inserted, err := insertPending(ctx, tx, r)
	if err != nil {
		return zero, false, fmt.Errorf("payments: record the dispute discrepancy: %w", err)
	}
	if !inserted {
		return zero, false, nil
	}
	p.logger.Warn("payments: dispute needs attention", "merchant", merchant.TypeID(), "kind", kind,
		"dispute", d.ID, "payment_intent", d.PaymentIntent, "discrepancy", r.ID)
	return r, true, nil
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
