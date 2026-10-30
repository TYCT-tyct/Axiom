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
// never from a request body. An account may only request a channel refund when its merchant enables user_refund.
type RefundRequest struct {
	Order            OrderID
	Credits          int64
	Key, Reason, Via string
	User             uuid.UUID
}

// Refund is the durable state, including the actual frozen credits and original-payment cash quote. Approved says
// whether an administrator has released it to the channel, which the console shows as review versus processing.
// RequestedBy and ApprovedBy are the people on its two sides: the requester never approves, and the operator's list
// shows a frozen refund without an approver as the one waiting for review.
type Refund struct {
	ID          RefundID
	Order       OrderID
	SiteID      uuid.UUID
	AccountID   uuid.UUID
	State, Via  string
	Credits     int64
	Amount      Money
	Reason      string
	RequestedBy uuid.UUID
	ApprovedBy  *uuid.UUID
	ProviderRef string
	Approved    bool
	CreatedAt   time.Time
	FinishedAt  *time.Time
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

// Refund freezes and quotes atomically. It returns frozen for review: until the operations approval policy is wired
// in, every refund requires a separate administrator's approval. Replays do not refreeze or depend on today's policy.
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
	if row.RequestedCredits != r.Credits || row.Via != r.Via || row.Reason != r.Reason || row.RequestedBy != r.User {
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
		Reason: r.Reason, RequestedBy: r.User, IdemKey: r.Key,
	})
	if err != nil {
		return row, err
	}
	return row, refundAudit(ctx, tx, row, r.User, "refund.request", r.Reason)
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
		RequestedBy: row.RequestedBy, ApprovedBy: row.ApprovedBy, ProviderRef: row.ProviderRef,
		Approved: row.ApprovedBy != nil, CreatedAt: row.CreatedAt, FinishedAt: finished,
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

// ApproveRefund records an independent administrator and then starts the channel flow. Repeating approval never
// causes another send. Offline refunds remain frozen until an evidenced, separately approved manual conclusion.
func (p *Payments) ApproveRefund(ctx context.Context, s pg.Scope, id RefundID, user uuid.UUID, reason string) (Refund, error) {
	var refund Refund
	err := p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		refund, err = p.ApproveRefundTx(ctx, tx, id, user, reason)
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
// channel. The caller must commit tx before SendRefund: the local decision and its audit are the idempotent effect, and
// the channel call is recoverable by the refund poller if the process dies between the two.
func (p *Payments) ApproveRefundTx(ctx context.Context, tx pg.Tx, id RefundID, user uuid.UUID, reason string) (Refund, error) {
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
		updated = locked
		if refundTerminal(locked.State) {
			return ErrRefundForbidden
		}
		if locked.ApprovedBy != nil {
			return nil
		}
		if err := queries.ApproveRefund(ctx, tx, ApproveRefundParams{ID: row.ID, ApprovedBy: &user}); err != nil {
			return err
		}
		if err := refundAudit(ctx, tx, locked, user, "refund.approve", reason); err != nil {
			return err
		}
		updated.ApprovedBy = &user
		return nil
	})
	if err != nil {
		return Refund{}, err
	}
	return refundOf(updated, o.Total.Currency), nil
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

func refundAudit(ctx context.Context, tx pg.Tx, r PaymentsRefund, user uuid.UUID, action, reason string) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(user), Action: action, Reason: reason,
		Target: audit.Target{Module: "payments", Kind: "refund", ID: RefundID(r.ID).TypeID(), Site: r.SiteID, Account: r.AccountID},
		Detail: map[string]any{"credits": r.Credits, "amount": r.Amount, "with_fee": r.WithFee, "via": r.Via},
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
	var refund Refund
	err := p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		refund, err = p.RejectRefundTx(ctx, tx, id, user, reason)
		return err
	})
	return refund, err
}

// RejectRefundTx returns the frozen credits and records the audit entry in tx, without committing it. A decision that
// already failed is answered as it is; an approved or terminal one is refused before billing unfreezes credits that a
// channel refund may already have taken.
func (p *Payments) RejectRefundTx(ctx context.Context, tx pg.Tx, id RefundID, user uuid.UUID, reason string) (Refund, error) {
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
	if row.State != "frozen" || row.ApprovedBy != nil {
		return Refund{}, ErrRefundForbidden
	}
	err = changeRefundTx(ctx, tx, OrderID(row.OrderID), row.ID, func(tx pg.Tx, o LockOrderRow, locked PaymentsRefund) error {
		if locked.State == "failed" {
			return nil
		}
		if locked.State != "frozen" || locked.ApprovedBy != nil {
			return ErrRefundForbidden
		}
		// The refund lock keeps a concurrent approval from taking the credits between this check and the unfreeze.
		if err := billing.PrepareRefund(ctx, p.db, p.platform, refundFreeze(locked)); err != nil {
			return err
		}
		if err := finishRefundTx(ctx, tx, o, locked, "failed", "", "admin"); err != nil {
			return err
		}
		return refundAudit(ctx, tx, locked, user, "refund.reject", reason)
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
