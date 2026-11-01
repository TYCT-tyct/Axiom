package payments

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func (p *Payments) pollRefunds(ctx context.Context) error {
	var ids []uuid.UUID
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) { ids, err = queries.DueRefunds(ctx, tx); return err })
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !refundHasBudget(ctx) {
			return ctx.Err()
		}
		if err := p.driveRefund(ctx, id, false); err != nil {
			p.logger.Warn("payments: refund recovery failed", "refund", RefundID(id).TypeID(), "error", err)
		}
	}
	return nil
}

func (p *Payments) driveRefund(ctx context.Context, id uuid.UUID, initial bool) error {
	row, o, err := p.refundAccess(ctx, p.platform, RefundID(id), false)
	if err != nil {
		return err
	}
	if !row.automatic() {
		return nil
	}
	ch, err := p.channelOf(ctx, o.MerchantID)
	if err != nil {
		return err
	}
	if row.State == "frozen" && !ch.Caps().Has(CapRefund) {
		return ErrRefundForbidden
	}
	if !refundHasBudget(ctx) {
		return ctx.Err()
	}
	// The absolute deadline starts before sending commits, independently of the request or poll that discovered it.
	callCtx, cancel := context.WithTimeoutCause(context.WithoutCancel(ctx), refundCallTimeout, errQueryDeadline)
	defer cancel()
	row, first, claimed, err := p.claimRefund(ctx, row, initial)
	if err != nil || !claimed {
		return err
	}
	canQuery := ch.Caps().Has(CapQueryRefund)
	canResend := row.RefundKey && ch.Caps().Has(CapRefundKey|CapRefund)
	result, callErr := p.callRefund(callCtx, ch, row, o, first, canQuery, canResend)
	outcome := refundOutcome(result, callErr, first)
	return p.saveRefundOutcome(ctx, row, outcome, result.ProviderRef, !canQuery && !canResend)
}

// Stripe keeps an idempotency key for 24 hours. Leave a minute of margin on the safe side of that boundary.
const refundKeyWindow = 24*time.Hour - time.Minute

func refundKeyOpenAt(r PaymentsRefund, now time.Time) bool {
	return r.SentAt.Valid && now.Before(r.SentAt.Time.Add(refundKeyWindow))
}

func (p *Payments) refundKeyWindowOpen(ctx context.Context, r PaymentsRefund) bool {
	var now time.Time
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		now, err = queries.RefundClock(ctx, tx)
		return err
	})
	return err == nil && refundKeyOpenAt(r, now)
}

func refundHasBudget(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	deadline, limited := ctx.Deadline()
	return !limited || time.Until(deadline) >= refundCallTimeout
}

func refundOutcome(result RefundState, err error, first bool) string {
	if err != nil {
		refused, ok := errors.AsType[*Rejected](err)
		if first && ok && refused != nil {
			return "failed"
		}
		return "pending"
	}
	switch result.State {
	case RefundSucceeded:
		return "succeeded"
	case RefundFailed:
		return "failed"
	case RefundPending:
	}
	return "pending"
}

func (p *Payments) claimRefund(ctx context.Context, row PaymentsRefund, initial bool) (PaymentsRefund, bool, bool, error) {
	var first, claimed bool
	err := p.changeRefund(ctx, OrderID(row.OrderID), row.ID, func(tx pg.Tx, _ LockOrderRow, locked PaymentsRefund) error {
		now, err := queries.RefundClock(ctx, tx)
		if err != nil {
			return err
		}
		first = locked.State == "frozen"
		if !locked.automatic() || locked.NextQueryAt.After(now) && (!initial || !first) {
			return nil
		}
		row, err = queries.ClaimRefund(ctx, tx, row.ID)
		claimed = err == nil
		return err
	})
	return row, first, claimed, err
}

func (r PaymentsRefund) automatic() bool {
	return r.Via == "channel" && r.ApprovedBy != nil && !r.ManualOnly && !refundTerminal(r.State)
}

func refundTerminal(state string) bool { return state == "succeeded" || state == "failed" }

func (p *Payments) callRefund(ctx context.Context, ch Channel, r PaymentsRefund, o Order, first, canQuery, canResend bool) (RefundState, error) {
	if err := ctx.Err(); err != nil {
		return RefundState{}, err
	}
	call := RefundCall{
		Refund: RefundID(r.ID), Order: o.ID, TradeNo: o.TradeNo, ProviderRef: r.ProviderRef,
		Amount: Money{Currency: o.Total.Currency, Minor: r.Amount}, Reason: r.Reason,
	}
	if first || !canQuery && canResend {
		if !ch.Caps().Has(CapRefund) {
			return RefundState{}, errors.New("payments: refund capability was disabled")
		}
		return ch.Refund(ctx, call)
	}
	if !canQuery {
		return RefundState{}, ErrUnconfirmed
	}
	token, err := p.acquireQuery(ctx, o.MerchantID, false)
	if err != nil {
		return RefundState{}, err
	}
	result, err := ch.QueryRefund(ctx, call)
	finishErr := p.finishQuery(ctx, o.MerchantID, token, queryHealthOutcome(ctx, err, false))
	if finishErr == nil && errors.Is(err, ErrRefundNotFound) && canResend && p.refundKeyWindowOpen(ctx, r) {
		// The first request may have died before it reached the channel. Within the key's window, sending the same
		// RefundCall again is the only safe way to finish; after the window a missing refund proves nothing.
		if !ch.Caps().Has(CapRefund) {
			return result, err
		}
		return ch.Refund(ctx, call)
	}
	return result, errors.Join(err, finishErr)
}

func (p *Payments) saveRefundOutcome(ctx context.Context, row PaymentsRefund, outcome, provider string, noRecovery bool) error {
	// Channel facts outlive the HTTP request and the poll's discovery budget; retain a bounded completion window.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundFinishTimeout)
	defer cancel()
	if invalidPaymentText(provider) || len(provider) > 1024 {
		provider = ""
	}
	if outcome != "pending" {
		if err := billing.PrepareRefund(ctx, p.db, p.platform, refundFreeze(row)); err != nil {
			return err
		}
	}
	return p.changeRefund(ctx, OrderID(row.OrderID), row.ID, func(tx pg.Tx, o LockOrderRow, locked PaymentsRefund) error {
		// Attempts fence a late response from an expired sender, including a response after manual resolution.
		if refundTerminal(locked.State) || locked.Attempts != row.Attempts {
			return nil
		}
		if provider == "" {
			provider = locked.ProviderRef
		}
		if err := finishRefundTx(ctx, tx, o, locked, outcome, provider, "job"); err != nil {
			return err
		}
		if outcome != "pending" {
			return nil
		}
		return recordUnknownRefund(ctx, tx, o, locked, noRecovery)
	})
}

func recordUnknownRefund(ctx context.Context, tx pg.Tx, o LockOrderRow, row PaymentsRefund, noRecovery bool) error {
	now, err := queries.RefundClock(ctx, tx)
	if err != nil {
		return err
	}
	if !noRecovery && row.SentAt.Time.Add(24*time.Hour).After(now) {
		return nil
	}
	d, err := pendingDifference(o.MerchantID, "refund_unknown", RefundID(row.ID).TypeID(), map[string]any{
		"refund": RefundID(row.ID).TypeID(), "amount": fmt.Sprint(row.Amount), "currency": o.PayCurrency,
	})
	if err != nil {
		return err
	}
	d.OrderID, d.AccountID = &o.ID, &o.AccountID
	if _, err = recordPending(ctx, tx, d); err != nil {
		return err
	}
	if noRecovery {
		return queries.ParkRefund(ctx, tx, row.ID)
	}
	return nil
}

func finishRefundTx(ctx context.Context, tx pg.Tx, o LockOrderRow, r PaymentsRefund, outcome, provider, source string) error {
	if refundTerminal(r.State) {
		return nil
	}
	f := refundFreeze(r)
	at, err := queries.RefundClock(ctx, tx)
	if err != nil {
		return err
	}
	switch outcome {
	case "succeeded":
		at, err = settleRefund(ctx, tx, r, f)
	case "failed":
		err = billing.ReleaseRefund(ctx, tx, f)
	}
	if err != nil {
		return err
	}
	if err := queries.FinishRefund(ctx, tx, FinishRefundParams{ID: r.ID, State: outcome, ProviderRef: provider, FinishedAt: at}); err != nil {
		return err
	}
	if outcome != "succeeded" {
		return nil
	}
	return refundOrder(ctx, tx, o, r, source)
}

func refundOrder(ctx context.Context, tx pg.Tx, o LockOrderRow, r PaymentsRefund, source string) error {
	total, err := queries.SuccessfulRefundCredits(ctx, tx, r.OrderID)
	if err != nil {
		return err
	}
	terms, err := queries.RefundTerms(ctx, tx, o.ID)
	if err != nil {
		return err
	}
	next := PartiallyRefunded
	if total == *terms.Credited {
		next = Refunded
	}
	if err = queries.RefundOrderPolicy(ctx, tx, RefundOrderPolicyParams{ID: o.ID, WithFee: &r.WithFee}); err != nil {
		return err
	}
	return move(ctx, tx, o, next, event{source: source, kind: "refund", amount: &r.Amount, tradeNo: o.TradeNo})
}

func settleRefund(ctx context.Context, tx pg.Tx, r PaymentsRefund, f billing.Freeze) (time.Time, error) {
	refund := billing.Refund{Freeze: f, WithFee: r.WithFee}
	quote, err := billing.QuoteRefund(ctx, tx, refund)
	if err != nil {
		return time.Time{}, err
	}
	if quote.Returned != r.Amount {
		return time.Time{}, errors.New("payments: frozen refund quote changed")
	}
	if err = billing.SettleRefund(ctx, tx, refund); err != nil {
		return time.Time{}, err
	}
	return billing.RefundRecordedAt(ctx, tx, r.OrderID)
}
