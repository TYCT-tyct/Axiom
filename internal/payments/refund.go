package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

var (
	// ErrRefundInFlight means the preceding refund must reach a definitive outcome first.
	ErrRefundInFlight = errors.New("payments: a refund is still in flight")
	// ErrRefundForbidden means this scope or merchant does not permit the requested refund operation.
	ErrRefundForbidden = errors.New("payments: refund operation is not permitted")
	// ErrSecondPerson requires an approval from a different authenticated person.
	ErrSecondPerson = errors.New("payments: a different person must approve")
	// ErrRefundAmount means no positive cash refund can be made for the requested credits.
	ErrRefundAmount = errors.New("payments: no refundable amount")
)

// RefundRequest identifies the original payment and unused credits. User comes from authenticated authorization,
// never from a request body; Agent says it is an operations credential's ID rather than a person's. An account may
// only request a channel refund when its merchant enables user_refund.
type RefundRequest struct {
	Order            OrderID
	Credits          int64
	Key, Reason, Via string
	User             uuid.UUID
	Agent            bool
}

// Refund is the durable state, including the actual frozen credits and original-payment cash quote. Approved says
// whether the refund may be sent; SecondRequired is the approval rule snapshotted when it was frozen, and
// FirstApprovedBy is the first of the two operators on a refund above the limit. RequestedBy never approves; with
// ByAgent it is the operations credential that asked for it, not a user.
type Refund struct {
	ID              RefundID
	Order           OrderID
	SiteID          uuid.UUID
	AccountID       uuid.UUID
	State, Via      string
	Credits         int64
	Amount          Money
	Reason          string
	RequestedBy     uuid.UUID
	ByAgent         bool
	ApprovedBy      *uuid.UUID
	FirstApprovedBy *uuid.UUID
	SecondRequired  bool
	ProviderRef     string
	Approved        bool
	CreatedAt       time.Time
	FinishedAt      *time.Time
}

func (r RefundRequest) check() error {
	if r.User == (uuid.UUID{}) || r.Credits <= 0 || r.Key == "" || len(r.Key) > 128 || invalidPaymentText(r.Key) {
		return &FieldError{"/refund", "user, positive credits and an idempotency key are required"}
	}
	if !validReason(r.Reason, 1000) || r.Via != "channel" && r.Via != "offline" {
		return &FieldError{"/refund", "a reason and channel or offline route are required"}
	}
	return nil
}

// requester is who asked for the refund, as the audit names them.
func (r RefundRequest) requester() audit.Actor {
	if r.Agent {
		return audit.OperationsAgent(r.User)
	}
	return audit.User(r.User)
}

func validReason(s string, maxLen int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maxLen && !invalidPaymentText(s)
}

// paymentAccess checks the caller's scope before using the platform scope required by billing and audit. Tenant IDs
// of an order never change. A caller must supply the scope and user obtained from accounts authorization.
func (p *Payments) paymentAccess(ctx context.Context, s pg.Scope, id OrderID, requireAdmin bool) (Order, error) {
	var o Order
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) error {
		var err error
		o, err = loadOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if requireAdmin {
			ok, err := queries.PaymentAdministrator(ctx, tx, o.SiteID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrRefundForbidden
			}
		}
		return nil
	})
	return o, err
}

// Refund freezes and quotes atomically. It returns frozen for review: one operator releases a refund within the site's
// limit, and two distinct operators are required above it. The rule is snapshotted with the refund, so replays and
// later limit changes do not refreeze or change an existing request.
func (p *Payments) Refund(ctx context.Context, s pg.Scope, r RefundRequest) (Refund, error) {
	if err := r.check(); err != nil {
		return Refund{}, err
	}
	var o Order
	var isAdmin bool
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		o, err = loadOrder(ctx, tx, r.Order)
		if err != nil {
			return err
		}
		isAdmin, err = queries.PaymentAdministrator(ctx, tx, o.SiteID)
		return err
	})
	if err != nil {
		return Refund{}, err
	}
	// Check replays before preparing billing: a successful refund may have exhausted the batch already.
	var existing []PaymentsRefund
	err = p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		existing, err = queries.RefundByKey(ctx, tx, RefundByKeyParams{OrderID: uuid.UUID(r.Order), IdemKey: r.Key})
		return err
	})
	if err != nil {
		return Refund{}, err
	}
	if len(existing) > 0 {
		return refundReplay(existing[0], r, o)
	}
	// Only a paid order has a paid-credit batch to freeze. checkRefundTerms rejects this too, but it runs after the
	// freeze; answering here keeps a request that can never work out of the billing layer's internal errors.
	if o.State != Paid && o.State != PartiallyRefunded {
		return Refund{}, ErrRefundAmount
	}
	f := billing.Freeze{Account: o.AccountID, Batch: uuid.UUID(o.ID), Amount: r.Credits}
	if err = billing.PrepareRefund(ctx, p.db, p.platform, f); err != nil {
		return Refund{}, err
	}

	row, err := p.reserveRefund(ctx, o, r, f, isAdmin)
	return refundOf(row, o.Total.Currency), err
}

func (p *Payments) reserveRefund(ctx context.Context, o Order, r RefundRequest, f billing.Freeze, isAdmin bool) (PaymentsRefund, error) {
	var row PaymentsRefund
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := boundOrderTx(ctx, tx); err != nil {
			return err
		}
		// Identity saves take this lock before examining in-flight refunds. Hold it before the order lock too.
		if err := queries.HoldMerchantIdentity(ctx, tx, merchantKey(o.MerchantID)); err != nil {
			return err
		}
		locked, err := lockForChange(ctx, tx, r.Order)
		if err != nil {
			return err
		}
		previous, err := queries.RefundByKey(ctx, tx, RefundByKeyParams{OrderID: locked.ID, IdemKey: r.Key})
		if err != nil {
			return err
		}
		if len(previous) > 0 {
			row = previous[0]
			_, err = refundReplay(row, r, o)
			return err
		}
		row, err = p.freezeRefund(ctx, tx, locked, r, f, isAdmin)
		return err
	})
	return row, err
}

func refundReplay(row PaymentsRefund, r RefundRequest, o Order) (Refund, error) {
	if row.RequestedCredits != r.Credits || row.Via != r.Via || row.Reason != r.Reason || row.RequestedBy != r.User || row.RequestedByAgent != r.Agent {
		return Refund{}, ErrKeyReused
	}
	return refundOf(row, o.Total.Currency), nil
}

func (p *Payments) freezeRefund(ctx context.Context, tx pg.Tx, o LockOrderRow, r RefundRequest, f billing.Freeze, isAdmin bool) (PaymentsRefund, error) {
	terms, err := queries.RefundTerms(ctx, tx, o.ID)
	if err != nil {
		return PaymentsRefund{}, err
	}
	if err = checkRefundTerms(terms, o, r, isAdmin); err != nil {
		return PaymentsRefund{}, err
	}
	caps, err := p.refundCaps(ctx, tx, MerchantID(o.MerchantID), r.Via)
	if err != nil {
		return PaymentsRefund{}, err
	}
	f.Amount, err = billing.FreezeRefund(ctx, tx, f)
	if err != nil {
		return PaymentsRefund{}, err
	}
	if f.Amount == 0 {
		return PaymentsRefund{}, ErrRefundAmount
	}
	withFee := terms.SiteWithFee != nil && *terms.SiteWithFee
	if terms.RefundWithFee != nil {
		withFee = *terms.RefundWithFee
	}
	limits, err := readSettings(ctx, tx, o.SiteID)
	if err != nil {
		return PaymentsRefund{}, err
	}
	amount, err := billing.QuoteRefund(ctx, tx, billing.Refund{Freeze: f, WithFee: withFee})
	if err != nil {
		return PaymentsRefund{}, err
	}
	if amount.Returned <= 0 {
		return PaymentsRefund{}, ErrRefundAmount
	}
	row, err := queries.InsertRefund(ctx, tx, InsertRefundParams{
		ID: uuid.NewV7(), OrderID: o.ID, SiteID: o.SiteID, AccountID: o.AccountID,
		Via: r.Via, Credits: f.Amount, RequestedCredits: r.Credits, Amount: amount.Returned, WithFee: withFee, RefundKey: caps.Has(CapRefundKey),
		Reason: r.Reason, RequestedBy: r.User, RequestedByAgent: r.Agent, IdemKey: r.Key, SecondRequired: limits.needsSecondApproval(f.Amount),
	})
	if err != nil {
		return row, err
	}
	return row, refundAuditAs(ctx, tx, row, r.requester(), "refund.request", r.Reason, "")
}

func checkRefundTerms(t RefundTermsRow, o LockOrderRow, r RefundRequest, isAdmin bool) error {
	if t.Inflight {
		return ErrRefundInFlight
	}
	if State(o.State) != Paid && State(o.State) != PartiallyRefunded || t.Credited == nil {
		return ErrRefundAmount
	}
	if r.Via == "channel" && !t.RefundEnabled {
		return ErrRefundForbidden
	}
	if !isAdmin && (!t.UserRefund || r.Via != "channel") {
		return ErrRefundForbidden
	}
	if r.Credits > *t.Credited-t.Used {
		return ErrRefundAmount
	}
	return nil
}

func (p *Payments) refundCaps(ctx context.Context, tx pg.Tx, id MerchantID, via string) (Caps, error) {
	if via == "offline" {
		return 0, nil
	}
	ch, err := p.channel(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	caps := ch.Caps()
	if !caps.Has(CapRefund) {
		return 0, ErrRefundForbidden
	}
	return caps, nil
}

func refundOf(row PaymentsRefund, currency platform.Currency) Refund {
	var finished *time.Time
	if row.FinishedAt.Valid {
		at := row.FinishedAt.Time
		finished = &at
	}
	return Refund{
		ID: RefundID(row.ID), Order: OrderID(row.OrderID), SiteID: row.SiteID, AccountID: row.AccountID, State: row.State, Via: row.Via,
		Credits: row.Credits, Amount: Money{Currency: currency, Minor: row.Amount}, Reason: row.Reason,
		RequestedBy: row.RequestedBy, ByAgent: row.RequestedByAgent, ApprovedBy: row.ApprovedBy, FirstApprovedBy: row.FirstApprovedBy,
		SecondRequired: row.SecondRequired, ProviderRef: row.ProviderRef, Approved: row.ApprovedBy != nil,
		CreatedAt: row.CreatedAt, FinishedAt: finished,
	}
}

// ListRefunds reads the refunds of several orders in one statement, oldest first. The scope is the access control, as
// for every read here: another account's rows are not visible.
func (p *Payments) ListRefunds(ctx context.Context, s pg.Scope, orders []OrderID) (map[OrderID][]Refund, error) {
	if len(orders) == 0 {
		return map[OrderID][]Refund{}, nil
	}
	ids := make([]uuid.UUID, len(orders))
	for i, id := range orders {
		ids[i] = uuid.UUID(id)
	}
	refunds := make(map[OrderID][]Refund, len(orders))
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) error {
		rows, err := queries.ListRefunds(ctx, tx, ids)
		if err != nil {
			return fmt.Errorf("payments: list refunds: %w", err)
		}
		for _, r := range rows {
			currency, err := platform.ParseCurrency(r.PayCurrency)
			if err != nil {
				return fmt.Errorf("payments: refund %s is paid in %q: %w", RefundID(r.PaymentsRefund.ID).TypeID(), r.PayCurrency, err)
			}
			order := OrderID(r.PaymentsRefund.OrderID)
			refunds[order] = append(refunds[order], refundOf(r.PaymentsRefund, currency))
		}
		return nil
	})
	return refunds, err
}

// GetRefund reads a refund visible in s; another account's ID is indistinguishable from an absent ID.
func (p *Payments) GetRefund(ctx context.Context, s pg.Scope, id RefundID) (Refund, error) {
	row, o, err := p.refundAccess(ctx, s, id, false)
	return refundOf(row, o.Total.Currency), err
}

func (p *Payments) refundAccess(ctx context.Context, s pg.Scope, id RefundID, admin bool) (PaymentsRefund, Order, error) {
	var row PaymentsRefund
	var o Order
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		row, o, err = refundAccessTx(ctx, tx, id, admin)
		return err
	})
	return row, o, err
}

// refundAccessTx is refundAccess in a transaction the caller owns, whose scope is the access control: the refund and
// its order must both be visible, and admin also requires the caller to administer their site.
func refundAccessTx(ctx context.Context, tx pg.Tx, id RefundID, admin bool) (PaymentsRefund, Order, error) {
	rows, err := queries.ReadRefund(ctx, tx, uuid.UUID(id))
	if err != nil {
		return PaymentsRefund{}, Order{}, err
	}
	if len(rows) == 0 {
		return PaymentsRefund{}, Order{}, ErrNotFound
	}
	o, err := loadOrder(ctx, tx, OrderID(rows[0].OrderID))
	if err != nil {
		return PaymentsRefund{}, Order{}, err
	}
	if admin {
		ok, err := queries.PaymentAdministrator(ctx, tx, o.SiteID)
		if err != nil {
			return PaymentsRefund{}, Order{}, err
		}
		if !ok {
			return PaymentsRefund{}, Order{}, ErrRefundForbidden
		}
	}
	return rows[0], o, nil
}
