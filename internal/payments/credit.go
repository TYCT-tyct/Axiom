package payments

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// confirmed is shared by callbacks, user queries and the poll. No network call occurs in its transaction.
type confirmed struct {
	merchant MerchantID
	order    string
	payment  Payment
	source   string
	reason   string // a notice that disagrees with the query must never credit automatically
	raw      string // only the plugin's sanitized description, never the request's form or query
}

func (p *Payments) receive(ctx context.Context, c confirmed) error {
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := boundOrderTx(ctx, tx); err != nil {
			return err
		}
		if err := queries.HoldMerchantIdentity(ctx, tx, merchantKey(c.merchant)); err != nil {
			return err
		}
		id, parseErr := orderPrefix.Decode(c.order)
		rows, err := queries.PaymentOrder(ctx, tx, PaymentOrderParams{ID: id, MerchantID: uuid.UUID(c.merchant)})
		if err != nil {
			return err
		}
		if parseErr != nil || len(rows) == 0 {
			if c.payment.State != PayPaid {
				return nil
			}
			return p.discrepancy(ctx, tx, c, nil, "unmatched_payment")
		}
		o, err := lockForChange(ctx, tx, OrderID(id))
		if err != nil {
			return err
		}
		return p.receiveOrder(ctx, tx, o, c)
	})
	if err != nil {
		return fmt.Errorf("payments: record confirmed payment: %w", err)
	}
	return nil
}

func (p *Payments) receiveOrder(ctx context.Context, tx pg.Tx, o LockOrderRow, c confirmed) error {
	if c.payment.State != PayPaid {
		return paymentEvent(ctx, tx, o, c, State(o.State))
	}
	if c.payment.Paid.Minor <= 0 || c.payment.Paid.Currency == (platform.Currency{}) || c.payment.TradeNo == "" || len(c.payment.TradeNo) > 1024 {
		return p.reviewPayment(ctx, tx, o, c, "invalid_payment")
	}
	if err := queries.RecordPayment(ctx, tx, RecordPaymentParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID,
		MerchantID: uuid.UUID(c.merchant), TradeNo: c.payment.TradeNo, Amount: c.payment.Paid.Minor,
		Currency: c.payment.Paid.Currency.String(), Source: c.source,
	}); err != nil {
		return fmt.Errorf("payments: insert payment: %w", err)
	}
	previous, err := queries.RecordedPayment(ctx, tx, RecordedPaymentParams{MerchantID: uuid.UUID(c.merchant), TradeNo: c.payment.TradeNo})
	if err != nil {
		return err
	}
	if previous[0].OrderID != o.ID || previous[0].Amount != c.payment.Paid.Minor || previous[0].Currency != c.payment.Paid.Currency.String() {
		return p.reviewPayment(ctx, tx, o, c, "trade_no_conflict")
	}
	n, err := queries.PaymentCount(ctx, tx, o.ID)
	if err != nil {
		return err
	}
	if n > 1 {
		return p.reviewPayment(ctx, tx, o, c, "multiple_payment")
	}
	if c.reason != "" {
		return p.reviewPayment(ctx, tx, o, c, c.reason)
	}
	if o.Total != c.payment.Paid.Minor || o.PayCurrency != c.payment.Paid.Currency.String() {
		return p.reviewPayment(ctx, tx, o, c, "amount_mismatch")
	}
	if !State(o.State).canMoveTo(Paid) {
		return paymentEvent(ctx, tx, o, c, State(o.State))
	}
	// A held payment requires a human conclusion; a repeated query cannot clear an earlier discrepancy.
	if State(o.State) == Review {
		return paymentEvent(ctx, tx, o, c, Review)
	}
	return p.creditOrder(ctx, tx, o, c)
}

func (p *Payments) creditOrder(ctx context.Context, tx pg.Tx, o LockOrderRow, c confirmed) error {
	terms, err := queries.PaymentTerms(ctx, tx, o.ID)
	if err != nil {
		return err
	}
	if terms.ObservedAt.After(o.ExpiresAt.Add(time.Duration(terms.AutoCreditSeconds) * time.Second)) {
		return p.reviewPayment(ctx, tx, o, c, "late_payment")
	}
	credit, update, err := arrivalCredit(ctx, tx, o, terms, c.payment)
	if err != nil {
		if errors.Is(err, ErrOutOfRange) {
			return p.reviewPayment(ctx, tx, o, c, "invalid_credit")
		}
		return err
	}
	// Billing's validation can discover a conflicting historical batch. A savepoint keeps its partial writes out of
	// the committed discrepancy while ordinary database failures still roll back the whole receipt for a retry.
	if _, err = tx.Exec(ctx, "SAVEPOINT paid_credit"); err != nil {
		return err
	}
	if err = billing.CreditPaid(ctx, tx, credit); err != nil {
		if !errors.Is(err, billing.ErrCreditConflict) && !isPaidError(err) {
			return err
		}
		if _, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT paid_credit"); err != nil {
			return err
		}
		return p.reviewPayment(ctx, tx, o, c, "credit_conflict")
	}
	if err = restoreReservation(ctx, tx, o); err != nil {
		return err
	}
	changed, err := queries.CreditOrder(ctx, tx, update)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("payments: locked order could not become paid")
	}
	return paymentEvent(ctx, tx, o, c, Paid)
}

func isPaidError(err error) bool {
	_, ok := errors.AsType[*billing.PaidError](err)
	return ok
}

func arrivalCredit(ctx context.Context, tx pg.Tx, o LockOrderRow, t PaymentTermsRow, payment Payment) (billing.PaidCredit, CreditOrderParams, error) {
	site, err := platform.ParseCurrency(o.SiteCurrency)
	if err != nil {
		return billing.PaidCredit{}, CreditOrderParams{}, err
	}
	c := billing.PaidCredit{
		ID: o.ID, Site: o.SiteID, Account: o.AccountID, SiteCurrency: site, Credited: o.Credits,
		Payment: billing.Payment{Currency: payment.Paid.Currency, Paid: payment.Paid.Minor, Fee: o.Fee},
		Channel: billing.Channel{Merchant: o.MerchantID}, Actor: audit.System("payments"), Reason: "confirmed payment",
	}
	if t.RateID != nil {
		c.RateID = *t.RateID
	}
	u := CreditOrderParams{ID: o.ID, TradeNo: payment.TradeNo, PaidTotal: &payment.Paid.Minor, FromStates: predecessors(Paid)}
	if site != payment.Paid.Currency && t.ObservedAt.After(o.ExpiresAt.Add(time.Duration(t.GraceSeconds)*time.Second)) {
		rate, err := billing.TopUpRate(ctx, tx, o.SiteID, site, payment.Paid.Currency)
		if err != nil {
			return c, u, err
		}
		c.Credited, c.Payment.Fee, err = creditAt(payment.Paid, Fee{PPM: t.FeePpm, Fixed: t.FeeFixed}, rate.Applied)
		if err != nil {
			return c, u, err
		}
		c.RateID = rate.Reference.ID
		ref, _ := rate.Reference.Between(site, payment.Paid.Currency) // TopUpRate already requires this pair.
		u.ArrivalRateID = &c.RateID
		refNum, refDen := ref.Num().String(), ref.Denom().String()
		num, den := rate.Applied.Num().String(), rate.Applied.Denom().String()
		u.ArrivalRefNum, u.ArrivalRefDen = &refNum, &refDen
		u.ArrivalNum, u.ArrivalDen = &num, &den
	}
	u.Credited, u.PaidFee = &c.Credited, &c.Payment.Fee
	return c, u, nil
}

// creditAt is the inverse of quote, rounded down once. The locked quote stays untouched when an arrival uses it.
func creditAt(paid Money, fee Fee, applied *big.Rat) (credits, chargedFee int64, err error) {
	net := new(big.Rat).Mul(new(big.Rat).SetInt64(paid.Minor), big.NewRat(ppmScale-int64(fee.PPM), ppmScale))
	net.Sub(net, new(big.Rat).SetInt64(fee.Fixed))
	if net.Sign() <= 0 {
		return 0, 0, ErrOutOfRange
	}
	net.Mul(net, new(big.Rat).SetFrac(big.NewInt(creditScale), minorScale(paid.Currency)))
	net.Quo(net, applied)
	n := new(big.Int).Quo(net.Num(), net.Denom())
	if !n.IsInt64() || n.Sign() <= 0 {
		return 0, 0, ErrOutOfRange
	}
	q, err := quote(n.Int64(), applied, paid.Currency, Fee{})
	if err != nil {
		return 0, 0, err
	}
	return n.Int64(), paid.Minor - q.Total.Minor, nil
}

func restoreReservation(ctx context.Context, tx pg.Tx, o LockOrderRow) error {
	if State(o.State) != Expired && State(o.State) != Cancelled && State(o.State) != Failed {
		return nil
	}
	n, err := queries.RestoreUsage(ctx, tx, o.ID)
	if err != nil {
		return fmt.Errorf("payments: restore late payment usage: %w", err)
	}
	if n != 1 {
		return errors.New("payments: late payment has no usage row")
	}
	return nil
}

func (p *Payments) reviewPayment(ctx context.Context, tx pg.Tx, o LockOrderRow, c confirmed, kind string) error {
	if err := p.discrepancy(ctx, tx, c, &o, kind); err != nil {
		return err
	}
	if State(o.State).canMoveTo(Review) {
		if err := restoreReservation(ctx, tx, o); err != nil {
			return err
		}
		return move(ctx, tx, o, Review, event{source: c.source, kind: kind, raw: c.raw})
	}
	return paymentEvent(ctx, tx, o, c, State(o.State))
}

func (p *Payments) discrepancy(ctx context.Context, tx pg.Tx, c confirmed, o *LockOrderRow, kind string) error {
	detail, err := jsonx.MarshalCanonical(map[string]any{
		"order": c.order, "trade_no": c.payment.TradeNo, "amount": c.payment.Paid.Minor, "currency": c.payment.Paid.Currency.String(),
	})
	if err != nil {
		return err
	}
	r := RecordDiscrepancyParams{ID: uuid.NewV7(), MerchantID: uuid.UUID(c.merchant), Kind: kind,
		Dedupe: c.order + ":" + c.payment.TradeNo, Detail: detail}
	if o != nil {
		r.OrderID, r.AccountID = &o.ID, &o.AccountID
	}
	n, err := queries.RecordDiscrepancy(ctx, tx, r)
	if err != nil {
		return fmt.Errorf("payments: record discrepancy: %w", err)
	}
	if n > 0 {
		p.logger.Warn("payments: confirmed payment needs attention", "merchant", c.merchant.TypeID(), "kind", kind)
	}
	return nil
}

func paymentEvent(ctx context.Context, tx pg.Tx, o LockOrderRow, c confirmed, next State) error {
	var amount *int64
	if c.payment.Paid.Minor > 0 {
		amount = &c.payment.Paid.Minor
	}
	return queries.OrderEvent(ctx, tx, OrderEventParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID, Source: c.source, Kind: "payment",
		FromState: o.State, ToState: string(next), TradeNo: c.payment.TradeNo, Amount: amount, Raw: cut(c.raw),
	})
}
