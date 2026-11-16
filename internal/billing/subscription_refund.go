package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Refunds of a paid subscription grant (docs/domains/billing.md「订阅套餐」): an operator returns part or all of what
// was paid, and the term that money bought is taken back from its end. The request's ID is the idempotency key.

// ErrNotRefundable is a grant that cannot be refunded as asked: a gift, an expired or foreign grant, or an amount above
// what is left.
var ErrNotRefundable = errors.New("billing: the subscription grant cannot be refunded")

// ErrRefundHeld is a refund whose windows still hold a reservation; the refund waits for it to settle.
var ErrRefundHeld = errors.New("billing: the subscription window still holds a reservation")

// SubscriptionRefundRequest is one operator refund: Credits are site credits, at most what the grant has left. Reason is
// required and goes to the audit log.
type SubscriptionRefundRequest struct {
	ID, Site, Account, Grant uuid.UUID
	SiteCurrency             platform.Currency
	Credits                  int64
	Reason                   string
	Actor                    audit.Actor
}

// SubscriptionRefund is one stored refund.
type SubscriptionRefund struct {
	ID           uuid.UUID
	Grant        uuid.UUID
	Subscription uuid.UUID
	Credits      int64 // site credits returned
	Paid, Fee    int64 // payment currency minor units
	Currency     platform.Currency
	Windows      int32 // windows voided by this refund
	Unused       int64 // plan liability taken back
	Revoked      bool
	CreatedAt    time.Time
}

// applySubscriptionRefund refunds a paid grant in tx, which must be in the platform scope. Only an approved request
// reaches it; there is no direct API path that writes a refund. It takes back the last
// whole windows of the grant's block, in proportion to the credits refunded, and refuses to touch a window that still
// holds a reservation. A refund of everything left revokes the subscription; a partial one only removes future
// windows, so a term in its current window cannot be partly refunded. The ledger reverses the unused liability, the
// sale's discount or premium, and the revenue of what was used; the payment's cash and fee are returned in their own
// currency.
func applySubscriptionRefund(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest) (SubscriptionRefund, error) {
	if err := checkRefundRequest(req); err != nil {
		return SubscriptionRefund{}, err
	}
	if old, ok, err := refundByID(ctx, tx, req.ID); err != nil {
		return SubscriptionRefund{}, err
	} else if ok {
		return sameSubscriptionRefund(old, req)
	}
	if _, err := lockAccount(ctx, tx, req.Account); err != nil {
		return SubscriptionRefund{}, err
	}
	// The account lock serializes refunds of this account, so a concurrent request with the same ID is visible now.
	if old, ok, err := refundByID(ctx, tx, req.ID); err != nil {
		return SubscriptionRefund{}, err
	} else if ok {
		return sameSubscriptionRefund(old, req)
	}
	rg, err := readRefundGrant(ctx, tx, req)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	plan, err := planRefund(ctx, tx, req, rg)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	if err := applyRefund(ctx, tx, req, rg, plan); err != nil {
		return SubscriptionRefund{}, err
	}
	stored, _, err := refundByID(ctx, tx, req.ID)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	return stored, nil
}

func checkRefundRequest(req SubscriptionRefundRequest) error {
	switch {
	case req.ID == (uuid.UUID{}) || req.Grant == (uuid.UUID{}) || req.Site == (uuid.UUID{}) || req.Account == (uuid.UUID{}):
		return &PlanError{"refund names no ID, grant, site or account"}
	case req.Credits <= 0:
		return &PlanError{"refund needs credits above zero"}
	case req.SiteCurrency == (platform.Currency{}):
		return &PlanError{"refund names no site currency"}
	case req.Reason == "":
		return &PlanError{"refund needs a reason"}
	}
	return nil
}

// refundCase is the paid grant being refunded and the subscription it belongs to, with the values the sale recorded.
type refundCase struct {
	grant     ReadGrantRow
	sub       BillingSubscription
	currency  platform.Currency
	rate      *BillingExchangeRate
	value     int64 // site credits the grant bought, before refunds
	remaining int64 // site credits left to refund
	full      bool  // the request takes everything that is left
}

func readRefundGrant(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest) (refundCase, error) {
	g, currency, rate, value, remaining, err := readGrantValue(ctx, tx, req)
	if err != nil {
		return refundCase{}, err
	}
	full := req.Credits == remaining
	sub, err := readRefundSubscription(ctx, tx, req, g)
	if err != nil {
		return refundCase{}, err
	}
	return refundCase{grant: g, sub: sub, currency: currency, rate: rate, value: value, remaining: remaining, full: full}, nil
}

func readGrantRow(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest) (ReadGrantRow, error) {
	g, err := queries.ReadGrant(ctx, tx, req.Grant)
	if err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ReadGrantRow{}, fmt.Errorf("%w: grant %s does not exist", ErrNotRefundable, req.Grant)
		}
		return ReadGrantRow{}, fmt.Errorf("billing: read the subscription grant: %w", err)
	}
	switch {
	case g.SiteID != req.Site || g.AccountID != req.Account:
		return ReadGrantRow{}, fmt.Errorf("%w: grant %s is not this site's and account's", ErrNotRefundable, req.Grant)
	case g.Source != "paid" || !g.Currency.Valid || g.Paid == nil || g.Fee == nil:
		return ReadGrantRow{}, fmt.Errorf("%w: only a paid grant returns money", ErrNotRefundable)
	}
	if g.FirstWindow == nil {
		// A node of the previous release wrote this grant before first_window existed. Grants append their windows, so
		// the earlier grants' windows say where this one starts.
		first, err := queries.GrantFirstWindow(ctx, tx, GrantFirstWindowParams{
			SubscriptionID: g.SubscriptionID, CreatedAt: g.CreatedAt, ID: g.ID,
		})
		if err != nil {
			return ReadGrantRow{}, fmt.Errorf("billing: read the grant's first window: %w", err)
		}
		g.FirstWindow = &first
	}
	if *g.FirstWindow < 1 {
		return ReadGrantRow{}, fmt.Errorf("%w: the grant does not know its first window", ErrNotRefundable)
	}
	return g, nil
}

func readGrantValue(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest) (ReadGrantRow, platform.Currency, *BillingExchangeRate, int64, int64, error) {
	g, err := readGrantRow(ctx, tx, req)
	if err != nil {
		return ReadGrantRow{}, platform.Currency{}, nil, 0, 0, err
	}
	rate, err := readRate(ctx, tx, orElse(g.RateID, uuid.UUID{}))
	if err != nil {
		return ReadGrantRow{}, platform.Currency{}, nil, 0, 0, err
	}
	currency, err := platform.ParseCurrency(g.Currency.String)
	if err != nil {
		return ReadGrantRow{}, platform.Currency{}, nil, 0, 0, &PlanError{err.Error()}
	}
	value, err := CreditsFor(Payment{Currency: currency, Paid: *g.Paid, Fee: *g.Fee}, req.SiteCurrency, rate)
	if err != nil {
		return ReadGrantRow{}, platform.Currency{}, nil, 0, 0, err
	}
	remaining, err := refundableValue(g, value, req.Credits)
	if err != nil {
		return ReadGrantRow{}, platform.Currency{}, nil, 0, 0, err
	}
	return g, currency, rate, value, remaining, nil
}

func refundableValue(g ReadGrantRow, value, credits int64) (int64, error) {
	remaining := value - g.RefundedValue
	switch {
	case value <= 0:
		return 0, fmt.Errorf("%w: the grant bought no credit", ErrNotRefundable)
	case remaining <= 0:
		return 0, fmt.Errorf("%w: the grant is fully refunded", ErrNotRefundable)
	case credits > remaining:
		return 0, fmt.Errorf("%w: %d credits are more than the %d left", ErrNotRefundable, credits, remaining)
	}
	return remaining, nil
}

func readRefundSubscription(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest, g ReadGrantRow) (BillingSubscription, error) {
	sub, err := queries.ReadSubscription(ctx, tx, g.SubscriptionID)
	if err != nil {
		return BillingSubscription{}, fmt.Errorf("billing: read the subscription: %w", err)
	}
	if sub.SiteID != req.Site || sub.AccountID != req.Account {
		return BillingSubscription{}, fmt.Errorf("%w: the subscription is not this site's and account's", ErrNotRefundable)
	}
	if sub.State != "active" || sub.ClosedAt != nil {
		return BillingSubscription{}, fmt.Errorf("%w: the subscription is not in force", ErrNotRefundable)
	}
	last := *g.FirstWindow + g.Windows - 1
	if last < *g.FirstWindow {
		return BillingSubscription{}, fmt.Errorf("%w: the grant's window block is malformed", ErrNotRefundable)
	}
	subLast, err := queries.LastWindowSeq(ctx, tx, g.SubscriptionID)
	if err != nil {
		return BillingSubscription{}, fmt.Errorf("billing: read the subscription's last window: %w", err)
	}
	if subLast != last {
		return BillingSubscription{}, fmt.Errorf("%w: only the newest grant of a subscription can be refunded", ErrNotRefundable)
	}
	return sub, nil
}

// refundPlan is the part of the grant a refund takes back: whole future windows from the block's end, the cash and fee
// that go with them, and whether the subscription is left with no window.
type refundPlan struct {
	windows     []RefundWindowsRow
	remove      int64
	unused      int64
	removedFace int64
	paid, fee   int64
	now         time.Time
}

func planRefund(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest, rg refundCase) (refundPlan, error) {
	windows, remove, unused, removedFace, now, err := refundWindowBlock(ctx, tx, req, rg)
	if err != nil {
		return refundPlan{}, err
	}
	paid, fee, err := refundCash(req, rg)
	if err != nil {
		return refundPlan{}, err
	}
	return refundPlan{windows: windows, remove: remove, unused: unused, removedFace: removedFace, paid: paid, fee: fee, now: now}, nil
}

func refundWindowBlock(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest, rg refundCase) ([]RefundWindowsRow, int64, int64, int64, time.Time, error) {
	g := rg.grant
	last := *g.FirstWindow + g.Windows - 1
	want := min(ceilMulDiv(int64(g.Windows), g.RefundedValue+req.Credits, rg.value), int64(g.Windows))
	remove := want - int64(g.RefundedWindows)
	if remove < 1 {
		return nil, 0, 0, 0, time.Time{}, fmt.Errorf("%w: this refund takes back no window", ErrNotRefundable)
	}
	if !rg.full && want >= int64(g.Windows) {
		return nil, 0, 0, 0, time.Time{}, fmt.Errorf("%w: a partial refund cannot take the whole remaining term", ErrNotRefundable)
	}
	remove = min(remove, int64(g.Windows-g.RefundedWindows))
	windows, err := queries.RefundWindows(ctx, tx, RefundWindowsParams{
		SubscriptionID: g.SubscriptionID, First: *g.FirstWindow, Last: last, Max: refundCount(remove),
	})
	if err != nil {
		return nil, 0, 0, 0, time.Time{}, fmt.Errorf("billing: read the windows to refund: %w", err)
	}
	if len(windows) != int(remove) {
		return nil, 0, 0, 0, time.Time{}, fmt.Errorf("%w: the grant has %d windows left, not %d", ErrNotRefundable, len(windows), remove)
	}
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return nil, 0, 0, 0, time.Time{}, fmt.Errorf("billing: read the clock: %w", err)
	}
	var unused, removedFace int64
	for _, w := range windows {
		if w.Reserved > 0 {
			return nil, 0, 0, 0, time.Time{}, fmt.Errorf("%w: window %d holds %d", ErrRefundHeld, w.Seq, w.Reserved)
		}
		if !rg.full && !now.Before(w.StartsAt) {
			return nil, 0, 0, 0, time.Time{},
				fmt.Errorf("%w: a partial refund only takes future windows, and window %d has started", ErrNotRefundable, w.Seq)
		}
		unused += w.Quota - w.Used
		removedFace += w.Quota
	}
	return windows, remove, unused, removedFace, now, nil
}

func refundCash(req SubscriptionRefundRequest, rg refundCase) (int64, int64, error) {
	remainingPaid := *rg.grant.Paid - rg.grant.RefundedPaid
	remainingFee := *rg.grant.Fee - rg.grant.RefundedFee
	if remainingPaid <= 0 || remainingFee < 0 {
		return 0, 0, fmt.Errorf("%w: the grant's refunds exceed what was paid", ErrNotRefundable)
	}
	if rg.full {
		// The last refund takes the remainder, so cumulative rounding can never leave cash behind.
		return remainingPaid, remainingFee, nil
	}
	// Floor the cumulative share, never round each part up: a one-minor-unit refund must not consume the cash that
	// later windows still represent. A refund whose share is still below the smallest currency unit is refused rather
	// than giving away a window for no money; the account can ask for a larger refund.
	cumulative := rg.grant.RefundedValue + req.Credits
	targetPaid := mulDivFloor(cumulative, *rg.grant.Paid, rg.value)
	paid := targetPaid - rg.grant.RefundedPaid
	if paid <= 0 {
		return 0, 0, fmt.Errorf("%w: these credits are below one unit of the grant's payment currency", ErrNotRefundable)
	}
	if paid > remainingPaid {
		return 0, 0, fmt.Errorf("%w: the grant's refunds exceed what was paid", ErrNotRefundable)
	}
	targetFee := mulDivFloor(targetPaid, *rg.grant.Fee, *rg.grant.Paid)
	fee := targetFee - rg.grant.RefundedFee
	if fee < 0 || fee > paid {
		return 0, 0, fmt.Errorf("%w: the grant's fee refund does not fit the cash returned", ErrNotRefundable)
	}
	return paid, fee, nil
}

func refundByID(ctx context.Context, tx pg.Tx, id uuid.UUID) (SubscriptionRefund, bool, error) {
	row, err := queries.ReadSubscriptionRefund(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		return SubscriptionRefund{}, false, nil
	}
	if err != nil {
		return SubscriptionRefund{}, false, fmt.Errorf("billing: read the subscription refund: %w", err)
	}
	currency, err := platform.ParseCurrency(row.Currency)
	if err != nil {
		return SubscriptionRefund{}, false, err
	}
	return SubscriptionRefund{
		ID: row.ID, Grant: row.GrantID, Subscription: row.SubscriptionID, Credits: row.Credits, Paid: row.Paid,
		Fee: row.Fee, Currency: currency, Windows: row.Windows, Unused: row.Unused, Revoked: row.Revoked,
		CreatedAt: row.CreatedAt,
	}, true, nil
}

// sameSubscriptionRefund answers a retry: the same grant and credits return the stored result, anything else is a
// conflict.
func sameSubscriptionRefund(old SubscriptionRefund, req SubscriptionRefundRequest) (SubscriptionRefund, error) {
	if old.Grant == req.Grant && old.Credits == req.Credits {
		return old, nil
	}
	return SubscriptionRefund{}, fmt.Errorf("%w: refund %s was made with other terms", ErrNotRefundable, req.ID)
}

// ceilMulDiv is ceil(a*b/c) without overflowing on the product; a, b and c are positive.
func ceilMulDiv(a, b, c int64) int64 { return mulDiv(a, b, c, true) }

// mulDivFloor is floor(a*b/c) without overflowing on the product; a, b and c are non-negative.
func mulDivFloor(a, b, c int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	return mulDiv(a, b, c, false)
}

func mulDiv(a, b, c int64, roundUp bool) int64 {
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	if roundUp {
		n.Add(n, big.NewInt(c-1))
	}
	n.Quo(n, big.NewInt(c))
	if !n.IsInt64() {
		return math.MaxInt64
	}
	return n.Int64()
}

// mulDivSigned is a*b/c rounded towards zero, for the discount or premium a refund reverses; b may be negative.
func mulDivSigned(a, b, c int64) int64 {
	sign := int64(1)
	if b < 0 {
		sign, b = -1, -b
	}
	return sign * mulDivFloor(a, b, c)
}
