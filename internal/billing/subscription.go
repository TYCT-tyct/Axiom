package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // sqlc's row types spell a nullable column as pgtype.Text

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Subscription plans as a funding source (docs/domains/billing.md「订阅套餐」). A plan is the administrator's catalog:
// what one window holds, how long a window and the term are, and the price it is listed at. An account's subscription
// is a snapshot of those terms: its windows are written once, and a later change to the plan affects only what is sold
// after it.

// planWindows is how many windows a term of term seconds holds at window seconds each, rounded up, and refused when it
// is more than a term may have. The count is what a grant writes, so it is bounded where the plan is saved.
func planWindows(term, window int64) (int32, error) {
	if window < minWindowSeconds || term < window {
		return 0, &PlanError{"window or term is too short"}
	}
	n := (term + window - 1) / window
	if n > maxPlanWindows {
		return 0, &PlanError{fmt.Sprintf("term holds %d windows, more than %d", n, maxPlanWindows)}
	}
	return int32(n), nil //nolint:gosec // G115: at most maxPlanWindows
}

const (
	minWindowSeconds = 3600
	maxPlanWindows   = 400
)

// PlanError is a plan or a subscription grant that breaks a rule of the catalog.
type PlanError struct{ Reason string }

func (e *PlanError) Error() string { return "billing: subscription plan " + e.Reason }

// ErrPlanConflict is a plan or subscription that cannot be sold to this account: another plan is in force, or the plan
// is not the site's or not active.
var ErrPlanConflict = errors.New("billing: another subscription plan is in force or the plan cannot be sold")

// Plan is one row of the catalog.
type Plan struct {
	ID                                uuid.UUID
	Site                              uuid.UUID
	Name                              string
	Currency                          platform.Currency
	Price                             int64 // minor units of Currency
	Quota, WindowSeconds, TermSeconds int64
	Active                            bool
	Rev                               int32
}

// PlanInput is what an administrator sets: the plan's terms and price.
type PlanInput struct {
	Name          string
	Reason        string // goes to the audit log
	Currency      platform.Currency
	Price         int64
	Quota         int64
	WindowSeconds int64
	TermSeconds   int64
	Active        bool
}

// check validates a plan before it is written, so a saved plan always has an executable term.
func (p PlanInput) check() error {
	if p.Name == "" || len(p.Name) > 200 {
		return &PlanError{"needs a name of 1 to 200 bytes"}
	}
	if _, err := minorScale(p.Currency); err != nil {
		return err
	}
	if p.Price < 0 {
		return &PlanError{"has a negative price"}
	}
	if p.Quota <= 0 {
		return &PlanError{"needs a quota above zero"}
	}
	if p.WindowSeconds < minWindowSeconds {
		return &PlanError{"needs a window of at least an hour"}
	}
	if _, err := planWindows(p.TermSeconds, p.WindowSeconds); err != nil {
		return err
	}
	if _, err := faceValue(p.Quota, p.TermSeconds, p.WindowSeconds); err != nil {
		return err
	}
	return nil
}

// faceValue is the credit a plan's whole term holds: every window's quota, the most its liability can ever be.
func faceValue(quota, term, window int64) (int64, error) {
	n, err := planWindows(term, window)
	if err != nil {
		return 0, err
	}
	if quota > math.MaxInt64/int64(n) {
		return 0, &PlanError{"credits more than the ledger holds"}
	}
	return quota * int64(n), nil
}

// minorScale is billionths of one unit of c in its minor unit, and refuses a currency the ledger cannot take.
func minorScale(c platform.Currency) (int64, error) {
	scale, err := unitScale(c)
	if err != nil {
		return 0, &PlanError{err.Error()}
	}
	return scale, nil
}

// CreatePlan writes p to the catalog in tx, which must be in the platform scope, with its audit entry. A name already
// used on the site is a conflict of the unique index.
func CreatePlan(ctx context.Context, tx pg.Tx, site uuid.UUID, p PlanInput, actor audit.Actor) (Plan, error) {
	if err := p.check(); err != nil {
		return Plan{}, err
	}
	id := uuid.NewV7()
	plan := Plan{
		ID: id, Site: site, Name: p.Name, Currency: p.Currency, Price: p.Price, Quota: p.Quota,
		WindowSeconds: p.WindowSeconds, TermSeconds: p.TermSeconds, Active: p.Active, Rev: 1,
	}
	err := queries.InsertPlan(ctx, tx, InsertPlanParams{
		ID: id, SiteID: site, Name: p.Name, Currency: p.Currency.String(), Price: p.Price, Quota: p.Quota,
		WindowSeconds: p.WindowSeconds, TermSeconds: p.TermSeconds,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("billing: create the subscription plan: %w", err)
	}
	if !p.Active {
		if _, err := queries.UpdatePlan(ctx, tx, UpdatePlanParams{
			ID: id, Rev: 1, Name: p.Name, Price: p.Price,
			Quota: p.Quota, WindowSeconds: p.WindowSeconds, TermSeconds: p.TermSeconds, Active: false,
		}); err != nil {
			return Plan{}, fmt.Errorf("billing: disable the subscription plan: %w", err)
		}
		plan.Rev = 2
	}
	return plan, auditPlan(ctx, tx, actor, "subscription_plan.create", plan, p.Reason)
}

// UpdatePlan saves p over the plan id the administrator read at rev: 0 rows means the plan changed since, and the
// caller answers with the current row instead of overwriting another administrator's save.
func UpdatePlan(ctx context.Context, tx pg.Tx, id uuid.UUID, rev int32, p PlanInput, actor audit.Actor) (Plan, error) {
	if err := p.check(); err != nil {
		return Plan{}, err
	}
	n, err := queries.UpdatePlan(ctx, tx, UpdatePlanParams{
		ID: id, Rev: rev, Name: p.Name, Price: p.Price, Quota: p.Quota,
		WindowSeconds: p.WindowSeconds, TermSeconds: p.TermSeconds, Active: p.Active,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("billing: update the subscription plan: %w", err)
	}
	if n != 1 {
		return Plan{}, fmt.Errorf("%w: plan %s at revision %d", ErrPlanConflict, id, rev)
	}
	plan, err := ReadPlan(ctx, tx, id)
	if err != nil {
		return Plan{}, err
	}
	return plan, auditPlan(ctx, tx, actor, "subscription_plan.update", plan, p.Reason)
}

func auditPlan(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, p Plan, reason string) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "billing", Kind: "subscription_plan", ID: p.ID.String()},
		Detail: map[string]any{
			"name": p.Name, "currency": p.Currency.String(), "price": p.Price, "quota": p.Quota,
			"window_seconds": p.WindowSeconds, "term_seconds": p.TermSeconds, "active": p.Active,
		},
	})
}

// ReadPlan reads one plan.
func ReadPlan(ctx context.Context, tx pg.Tx, id uuid.UUID) (Plan, error) {
	p, err := queries.ReadPlan(ctx, tx, id)
	if err != nil {
		return Plan{}, fmt.Errorf("billing: read the subscription plan: %w", err)
	}
	return planOf(p), nil
}

// Plans lists the site's catalog, inactive plans included when asked.
func Plans(ctx context.Context, tx pg.Tx, site uuid.UUID, includeInactive bool) ([]Plan, error) {
	rows, err := queries.ListPlans(ctx, tx, ListPlansParams{SiteID: site, IncludeInactive: includeInactive})
	if err != nil {
		return nil, fmt.Errorf("billing: read the subscription plans: %w", err)
	}
	plans := make([]Plan, len(rows))
	for i, r := range rows {
		plans[i] = planOf(r)
	}
	return plans, nil
}

func planOf(p BillingSubscriptionPlan) Plan {
	currency, err := platform.ParseCurrency(p.Currency)
	if err != nil {
		return Plan{}
	}
	return Plan{
		ID: p.ID, Site: p.SiteID, Name: p.Name, Currency: currency, Price: p.Price,
		Quota: p.Quota, WindowSeconds: p.WindowSeconds, TermSeconds: p.TermSeconds, Active: p.Active, Rev: p.Rev,
	}
}

// Subscription is what the console and the administrator read of an account's subscription: its terms as sold, its
// state, and its current window.
type Subscription struct {
	ID         uuid.UUID
	Site       uuid.UUID
	Account    uuid.UUID
	PlanID     uuid.UUID
	PlanName   string
	StartsAt   time.Time
	EndsAt     time.Time
	State      string
	CurrentSeq int32
	ClosedAt   *time.Time
	CreatedAt  time.Time
	Window     Window
}

// Window is one slice of a subscription's term.
type Window struct {
	Seq      int32
	StartsAt time.Time
	EndsAt   time.Time
	Quota    int64
	Used     int64
	Reserved int64
}

// Remaining is what the window can still fund: its quota less what it has used and holds.
func (w Window) Remaining() int64 { return w.Quota - w.Used - w.Reserved }

// SubscriptionGrant is one sale or gift of a plan to an account, or a renewal of the plan in force. ID is the
// caller's idempotency key: the same ID with the same terms writes nothing.
type SubscriptionGrant struct {
	ID, Site, Account, Plan uuid.UUID
	SiteCurrency            platform.Currency // the currency of the site's credit, for a paid grant
	Source                  string            // "gift" or "paid"
	Payment                 Payment           // paid only
	Channel                 Channel           // paid only
	RateID                  uuid.UUID         // paid only, zero in the site's currency
	Actor                   audit.Actor
	Reason                  string
}

// CreditSubscription grants or renews g in tx, which must be in the platform scope: the subscription's windows, the
// grant, the ledger's liability and the audit entry, all or none. The same ID with the same terms changes nothing;
// with others it is ErrCreditConflict.
func CreditSubscription(ctx context.Context, tx pg.Tx, g SubscriptionGrant) error {
	if err := g.check(); err != nil {
		return err
	}
	if _, err := lockAccount(ctx, tx, g.Account); err != nil {
		return err
	}
	switch found, err := g.same(ctx, tx); {
	case err != nil:
		return err
	case found:
		return nil
	}
	plan, err := ReadPlan(ctx, tx, g.Plan)
	if err != nil {
		return err
	}
	if plan.Site != g.Site || !plan.Active {
		return fmt.Errorf("%w: plan %s is not an active plan of the site", ErrPlanConflict, plan.ID)
	}
	face, err := faceValue(plan.Quota, plan.TermSeconds, plan.WindowSeconds)
	if err != nil {
		return err
	}
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return fmt.Errorf("billing: read the clock: %w", err)
	}
	sub, first, err := extendSubscription(ctx, tx, g, plan, now)
	if err != nil {
		return err
	}
	if n, err := queries.InsertGrant(ctx, tx, g.params(sub.ID, plan, face)); err != nil {
		return fmt.Errorf("billing: write the subscription grant: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: grant %s", ErrCreditConflict, g.ID)
	}
	lines, err := g.entries(ctx, tx, face)
	if err != nil {
		return err
	}
	if _, err := post(ctx, tx, event{
		site: g.Site, account: g.Account, kind: "plan_credit", request: sub.ID,
		revision: int64(first),
	}, lines...); err != nil {
		return err
	}
	detail := map[string]any{
		"grant": g.ID.String(), "plan": plan.ID.String(), "plan_name": plan.Name,
		"source": g.Source, "windows": planWindowsOf(plan), "face": face, "quota": plan.Quota,
		"window_seconds": plan.WindowSeconds, "term_seconds": plan.TermSeconds,
	}
	if g.Source == "paid" {
		detail["currency"], detail["paid"], detail["fee"] = g.Payment.Currency.String(), g.Payment.Paid, g.Payment.Fee
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: g.Actor, Action: "subscription.grant", Target: audit.Target{Module: "billing", Kind: "subscription", ID: sub.ID.String()},
		Reason: g.Reason, Detail: detail,
	})
}

func planWindowsOf(p Plan) int32 {
	// The plan was checked when it was saved, so its term divides into windows here.
	n, err := planWindows(p.TermSeconds, p.WindowSeconds)
	if err != nil {
		panic("billing: a saved plan window: " + err.Error())
	}
	return n
}

func (g SubscriptionGrant) check() error {
	switch g.Source {
	case "gift":
		if g.Payment != (Payment{}) || g.Channel != (Channel{}) || g.RateID != (uuid.UUID{}) {
			return &PlanError{"gift carries a payment"}
		}
	case "paid":
		if (g.Channel.Merchant == uuid.UUID{}) == (g.Channel.Offline == "") {
			return &PaidError{"names no channel, or both a merchant account and an offline channel"}
		}
		if _, _, err := g.Payment.billionths(); err != nil {
			return err
		}
	default:
		return &PlanError{"has source " + g.Source}
	}
	if g.Site == (uuid.UUID{}) || g.Account == (uuid.UUID{}) || g.Plan == (uuid.UUID{}) {
		return &PlanError{"names no site, account or plan"}
	}
	return nil
}

// same answers g's ID granted before: nothing to do if the grant is g's, ErrCreditConflict if it is anything else.
func (g SubscriptionGrant) same(ctx context.Context, tx pg.Tx) (bool, error) {
	old, err := queries.ReadGrant(ctx, tx, g.ID)
	if errors.Is(err, pg.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("billing: read the subscription grant: %w", err)
	}
	if old.SiteID == g.Site && old.AccountID == g.Account && old.PlanID == g.Plan && old.Source == g.Source &&
		old.Currency == currencyOf(g) && eqInt(old.Paid, g.Payment.Paid) && eqInt(old.Fee, g.Payment.Fee) &&
		eqUUID(old.MerchantID, g.Channel.Merchant) && old.OfflineChannel == offlineOf(g) && eqUUID(old.RateID, g.RateID) {
		return true, nil
	}
	return false, fmt.Errorf("%w: grant %s", ErrCreditConflict, g.ID)
}

// currencyOf is the payment's currency for the grant's row, which the database keeps NULL for a gift.
func currencyOf(g SubscriptionGrant) pgtype.Text {
	if g.Source != "paid" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: g.Payment.Currency.String(), Valid: true}
}

func offlineOf(g SubscriptionGrant) pgtype.Text {
	if g.Channel.Offline == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: g.Channel.Offline, Valid: true}
}

// eqInt compares a stored optional amount with one of a grant: a gift stores none, and its payment amounts are zero.
func eqInt(p *int64, n int64) bool { return (p == nil && n == 0) || (p != nil && *p == n) }

// eqUUID compares a stored optional ID the same way: a gift names no merchant account, channel rate or offline channel.
func eqUUID(p *uuid.UUID, id uuid.UUID) bool {
	return (p == nil) == (id == uuid.UUID{}) && (p == nil || *p == id)
}

// entries is the ledger of the sale: the liability is credited the whole term's credit, and what was received pays
// for it. A gift is an expense; a payment is cash less the fee, with the difference to the face value as the plan's
// discount, or as revenue when the payment is worth more than the credit it buys.
func (g SubscriptionGrant) entries(ctx context.Context, tx pg.Tx, face int64) ([]line, error) {
	if g.Source == "gift" {
		return []line{{bookGiftExpense, face, siteCredits}, {bookPlanLiability, -face, siteCredits}}, nil
	}
	rate, err := readRate(ctx, tx, g.RateID)
	if err != nil {
		return nil, err
	}
	value, err := CreditsFor(g.Payment, g.SiteCurrency, rate)
	if err != nil {
		return nil, err
	}
	paid, fee, err := g.Payment.billionths()
	if err != nil {
		return nil, err
	}
	lines := []line{{bookPlanLiability, -face, siteCredits}}
	if face > value { // sold below its face value: the difference is a discount, not an exchange loss
		lines = append(lines, line{bookPlanDiscount, face - value, siteCredits})
	} else if value > face {
		lines = append(lines, line{bookRevenue, -(value - face), siteCredits})
	}
	if rate == nil {
		return append(lines,
			line{bookCash, paid, siteCredits}, line{bookFeeIncome, -fee, siteCredits},
			line{bookExchange, value - (paid - fee), siteCredits}), nil
	}
	in := g.Payment.Currency.String()
	return append(lines,
		line{bookCash, paid, in}, line{bookFeeIncome, -fee, in}, line{bookExchange, fee - paid, in},
		line{bookExchange, value, siteCredits}), nil
}

// params is the grant's row, with the plan's terms as they were at the grant.
func (g SubscriptionGrant) params(subscription uuid.UUID, plan Plan, face int64) InsertGrantParams {
	n := planWindowsOf(plan)
	p := InsertGrantParams{
		ID: g.ID, SiteID: g.Site, AccountID: g.Account, SubscriptionID: subscription, PlanID: plan.ID, PlanName: plan.Name,
		Source: g.Source, Quota: plan.Quota, WindowSeconds: plan.WindowSeconds, TermSeconds: plan.TermSeconds,
		Windows: n, Face: face, Currency: currencyOf(g),
	}
	if g.Source == "paid" {
		p.Paid, p.Fee = &g.Payment.Paid, &g.Payment.Fee
		if g.Channel.Offline != "" {
			p.OfflineChannel = offlineOf(g)
		} else {
			p.MerchantID = &g.Channel.Merchant
		}
		if g.RateID != (uuid.UUID{}) {
			p.RateID = &g.RateID
		}
	}
	return p
}

// extendSubscription finds the subscription the grant extends, creates one if the account has none in force, appends
// the new term's windows, and returns the subscription with the first window of the added block. A subscription whose
// term ended is expired first, so a sale after it starts a new one.
func extendSubscription(ctx context.Context, tx pg.Tx, g SubscriptionGrant, plan Plan, now time.Time) (Subscription, int32, error) {
	cur, err := readActiveSubscription(ctx, tx, g.Account)
	switch {
	case errors.Is(err, pg.ErrNoRows):
		return newSubscription(ctx, tx, g, plan, now)
	case err != nil:
		return Subscription{}, 0, err
	}
	if !now.Before(cur.EndsAt) {
		if err := expireTerm(ctx, tx, cur); err != nil {
			return Subscription{}, 0, err
		}
		return newSubscription(ctx, tx, g, plan, now)
	}
	if cur.PlanID != plan.ID {
		return Subscription{}, 0, fmt.Errorf("%w: %s is in force", ErrPlanConflict, cur.PlanName)
	}
	last, err := queries.LastWindowSeq(ctx, tx, cur.ID)
	if err != nil {
		return Subscription{}, 0, fmt.Errorf("billing: read the last window: %w", err)
	}
	n := planWindowsOf(plan)
	first := last + 1
	if err := insertWindows(ctx, tx, windowBlock{
		subscription: cur.ID, site: g.Site, account: g.Account, first: first,
		start: cur.EndsAt, end: cur.EndsAt.Add(time.Duration(plan.TermSeconds) * time.Second), quota: plan.Quota,
		window: plan.WindowSeconds, count: n,
	}); err != nil {
		return Subscription{}, 0, err
	}
	ends := cur.EndsAt.Add(time.Duration(plan.TermSeconds) * time.Second)
	if n, err := queries.UpdateSubscriptionTerm(ctx, tx, UpdateSubscriptionTermParams{ID: cur.ID, EndsAt: ends, CurrentSeq: cur.CurrentSeq}); err != nil {
		return Subscription{}, 0, fmt.Errorf("billing: extend the subscription: %w", err)
	} else if n != 1 {
		return Subscription{}, 0, fmt.Errorf("billing: the subscription %s changed under the lock", cur.ID)
	}
	cur.EndsAt = ends
	return cur, first, nil
}

func newSubscription(ctx context.Context, tx pg.Tx, g SubscriptionGrant, plan Plan, now time.Time) (Subscription, int32, error) {
	id := uuid.NewV7()
	ends := now.Add(time.Duration(plan.TermSeconds) * time.Second)
	sub := Subscription{ID: id, PlanID: plan.ID, PlanName: plan.Name, StartsAt: now, EndsAt: ends, State: "active"}
	if err := queries.InsertSubscription(ctx, tx, InsertSubscriptionParams{
		ID: id, SiteID: g.Site, AccountID: g.Account, PlanID: plan.ID, PlanName: plan.Name, StartsAt: now, EndsAt: ends,
	}); err != nil {
		return Subscription{}, 0, fmt.Errorf("billing: create the subscription: %w", err)
	}
	if err := insertWindows(ctx, tx, windowBlock{
		subscription: id, site: g.Site, account: g.Account, first: 1,
		start: now, end: ends, quota: plan.Quota, window: plan.WindowSeconds, count: planWindowsOf(plan),
	}); err != nil {
		return Subscription{}, 0, err
	}
	return sub, 1, nil
}

// windowBlock is the span a grant adds: count windows of window seconds from start, the last one clipped at end.
type windowBlock struct {
	subscription, site, account uuid.UUID
	first                       int32
	start, end                  time.Time
	quota, window               int64
	count                       int32
}

func insertWindows(ctx context.Context, tx pg.Tx, b windowBlock) error {
	statements := make([]pg.Statement, b.count)
	for i := range int(b.count) {
		from := b.start.Add(time.Duration(int64(i)*b.window) * time.Second)
		to := from.Add(time.Duration(b.window) * time.Second)
		if to.After(b.end) {
			to = b.end
		}
		statements[i] = pg.Statement{SQL: insertWindow, Args: []any{b.subscription, b.first + int32(i), b.site, b.account, from, to, b.quota}}
	}
	if err := tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
		for range statements {
			if _, err := batch.Exec(); err != nil {
				return fmt.Errorf("billing: write the subscription windows: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// activeSubscription reads the account's subscription in force (pg.ErrNoRows when it has none).
func readActiveSubscription(ctx context.Context, tx pg.Tx, account uuid.UUID) (Subscription, error) {
	r, err := queries.ActiveSubscription(ctx, tx, account)
	if err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return Subscription{}, err
		}
		return Subscription{}, fmt.Errorf("billing: read the active subscription: %w", err)
	}
	return Subscription{
		ID: r.ID, Site: r.SiteID, Account: r.AccountID, PlanID: r.PlanID, PlanName: r.PlanName,
		StartsAt: r.StartsAt, EndsAt: r.EndsAt, State: r.State, CurrentSeq: r.CurrentSeq, ClosedAt: r.ClosedAt,
	}, nil
}

// expireSubscription marks a subscription whose term ended as expired, so no window of it funds a new reservation.
func expireTerm(ctx context.Context, tx pg.Tx, sub Subscription) error {
	if sub.State != "active" {
		return nil
	}
	if _, err := queries.ExpireSubscription(ctx, tx, sub.ID); err != nil {
		return fmt.Errorf("billing: expire the subscription: %w", err)
	}
	return nil
}

// writeOffSubscription closes an expired subscription whose pending holds have all ended: what is left of its
// liability lapses (docs/domains/billing.md「订阅套餐」). A later correction of a plan charge draws on that write-off.
// It is idempotent: the expiry event is keyed by the subscription, and closed_at is set once.
func writeOffSubscription(ctx context.Context, tx pg.Tx, sub Subscription) error {
	if sub.State != "expired" || sub.ClosedAt != nil {
		return nil
	}
	held, err := queries.PendingPlanHolds(ctx, tx, &sub.ID)
	if err != nil {
		return fmt.Errorf("billing: read the subscription's holds: %w", err)
	}
	if held > 0 {
		return nil // a late settlement may still charge a window; the next run closes it
	}
	credited, err := queries.Liability(ctx, tx, sub.ID)
	if err != nil {
		return fmt.Errorf("billing: read the subscription's liability: %w", err)
	}
	if remaining := -credited; remaining > 0 {
		if _, err := post(ctx, tx, event{site: sub.Site, account: sub.Account, kind: "plan_expire", request: sub.ID},
			line{bookPlanLiability, remaining, siteCredits}, line{bookCreditExpired, -remaining, siteCredits}); err != nil {
			return err
		}
	}
	if n, err := queries.CloseSubscription(ctx, tx, sub.ID); err != nil {
		return fmt.Errorf("billing: close the subscription: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the subscription %s closed under the lock", sub.ID)
	}
	return nil
}

// planWindow is the account's subscription as admission and settlement use it: the window that funds new requests, or
// the window a reservation froze, with its remaining credit.
type planWindow struct {
	subscription, site, account uuid.UUID
	seq                         int32
	termEnds                    time.Time
	starts, ends                time.Time
	quota, used, reserved       int64
}

// remaining is what the window can still fund: its quota less what it has used and holds.
func (w *planWindow) remaining() int64 { return w.quota - w.used - w.reserved }

// due reports whether the window or the term ended by now, so a reservation may not fund from it before it advances.
func (w *planWindow) due(now time.Time) bool { return !now.Before(w.ends) || !now.Before(w.termEnds) }

func planWindowOf(r AvailablePlanRow) *planWindow {
	return &planWindow{
		subscription: r.SubscriptionID, site: r.SiteID, account: r.AccountID, seq: r.Seq,
		termEnds: r.TermEnds, starts: r.WindowStarts, ends: r.WindowEnds, quota: r.Quota, used: r.Used, reserved: r.Reserved,
	}
}

// readPlanWindow reads the current window as it is, without advancing it.
func readPlanWindow(ctx context.Context, tx pg.Tx, account uuid.UUID) (*planWindow, error) {
	r, err := queries.AvailablePlan(ctx, tx, account)
	if errors.Is(err, pg.ErrNoRows) {
		return nil, nil //nolint:nilnil // no subscription in force is not an error
	}
	if err != nil {
		return nil, fmt.Errorf("billing: read the subscription window: %w", err)
	}
	return planWindowOf(r), nil
}

// planWindowNow is what admission uses: the account's current window after any due window has been passed and a
// finished term has been expired, or nil when no plan can fund the request. The caller holds the balance row lock.
func planWindowNow(ctx context.Context, tx pg.Tx, account uuid.UUID) (*planWindow, error) {
	w, err := readPlanWindow(ctx, tx, account)
	if w == nil || err != nil {
		return w, err
	}
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("billing: read the clock: %w", err)
	}
	for {
		if !now.Before(w.termEnds) {
			return nil, expireTerm(ctx, tx, Subscription{ID: w.subscription, State: "active"})
		}
		if now.Before(w.ends) {
			return w, nil // the current window is the one that holds now
		}
		next, err := queries.Window(ctx, tx, WindowParams{SubscriptionID: w.subscription, Seq: w.seq + 1})
		if errors.Is(err, pg.ErrNoRows) {
			return nil, expireTerm(ctx, tx, Subscription{ID: w.subscription, State: "active"})
		}
		if err != nil {
			return nil, fmt.Errorf("billing: read the next window: %w", err)
		}
		if n, err := queries.SetCurrentWindow(ctx, tx, SetCurrentWindowParams{ID: w.subscription, Seq: next.Seq}); err != nil {
			return nil, fmt.Errorf("billing: advance the subscription window: %w", err)
		} else if n != 1 {
			return nil, fmt.Errorf("billing: the subscription %s changed under the lock", w.subscription)
		}
		w.seq, w.starts, w.ends, w.quota, w.used, w.reserved = next.Seq, next.StartsAt, next.EndsAt, next.Quota, next.Used, next.Reserved
	}
}

// Subscriptions reads an account's subscriptions for the console and the administrator, newest first.
func Subscriptions(ctx context.Context, tx pg.Tx, account uuid.UUID, max int32) ([]Subscription, error) {
	rows, err := queries.SubscriptionsOfAccount(ctx, tx, SubscriptionsOfAccountParams{AccountID: account, Max: max})
	if err != nil {
		return nil, fmt.Errorf("billing: read the subscriptions: %w", err)
	}
	subs := make([]Subscription, len(rows))
	for i, r := range rows {
		subs[i] = Subscription{
			ID: r.ID, Site: r.SiteID, Account: r.AccountID, PlanID: r.PlanID, PlanName: r.PlanName,
			StartsAt: r.StartsAt, EndsAt: r.EndsAt, State: r.State, CurrentSeq: r.CurrentSeq, ClosedAt: r.ClosedAt, CreatedAt: r.CreatedAt,
			Window: Window{
				Seq: r.WindowSeq, StartsAt: r.WindowStart, EndsAt: r.WindowEnd, Quota: r.Quota, Used: r.Used,
				Reserved: r.Reserved,
			},
		}
	}
	return subs, nil
}
