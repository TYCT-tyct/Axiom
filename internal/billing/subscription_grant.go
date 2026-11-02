package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // sqlc's row types spell a nullable column as pgtype.Text

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Grants and renewals of a subscription plan (docs/domains/billing.md「订阅套餐」): the term is a snapshot of the plan's
// terms, the caller's ID makes the write idempotent, and the ledger's liability is posted with the windows.

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
	plan, face, err := g.sellablePlan(ctx, tx)
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
	return g.credit(ctx, tx, plan, sub, first, face)
}

// sellablePlan reads the plan g names and checks that it is an active plan of the grant's site, with an executable
// face value.
func (g SubscriptionGrant) sellablePlan(ctx context.Context, tx pg.Tx) (Plan, int64, error) {
	plan, err := ReadPlan(ctx, tx, g.Plan)
	if err != nil {
		return Plan{}, 0, err
	}
	if plan.Site != g.Site || !plan.Active {
		return Plan{}, 0, fmt.Errorf("%w: plan %s is not an active plan of the site", ErrPlanConflict, plan.ID)
	}
	face, err := faceValue(plan.Quota, plan.TermSeconds, plan.WindowSeconds)
	if err != nil {
		return Plan{}, 0, err
	}
	return plan, face, nil
}

// credit writes the grant row, its liability and its audit entry into the subscription extendSubscription wrote.
func (g SubscriptionGrant) credit(ctx context.Context, tx pg.Tx, plan Plan, sub Subscription, first int32, face int64) error {
	if n, err := queries.InsertGrant(ctx, tx, g.params(sub.ID, plan, first, face)); err != nil {
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
	if g.matches(old) {
		return true, nil
	}
	return false, fmt.Errorf("%w: grant %s", ErrCreditConflict, g.ID)
}

// matches reports whether old is the grant g would write.
func (g SubscriptionGrant) matches(old ReadGrantRow) bool {
	return g.sameTerms(old) && g.samePayment(old) && g.sameChannel(old)
}

func (g SubscriptionGrant) sameTerms(old ReadGrantRow) bool {
	return old.SiteID == g.Site && old.AccountID == g.Account && old.PlanID == g.Plan && old.Source == g.Source
}

func (g SubscriptionGrant) samePayment(old ReadGrantRow) bool {
	return old.Currency == currencyOf(g) && eqInt(old.Paid, g.Payment.Paid) && eqInt(old.Fee, g.Payment.Fee)
}

func (g SubscriptionGrant) sameChannel(old ReadGrantRow) bool {
	return eqUUID(old.MerchantID, g.Channel.Merchant) && old.OfflineChannel == offlineOf(g) && eqUUID(old.RateID, g.RateID)
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
func (g SubscriptionGrant) params(subscription uuid.UUID, plan Plan, first int32, face int64) InsertGrantParams {
	n := planWindowsOf(plan)
	p := InsertGrantParams{
		ID: g.ID, SiteID: g.Site, AccountID: g.Account, SubscriptionID: subscription, PlanID: plan.ID, PlanName: plan.Name,
		Source: g.Source, Quota: plan.Quota, WindowSeconds: plan.WindowSeconds, TermSeconds: plan.TermSeconds,
		Windows: n, FirstWindow: first, Face: face, Currency: currencyOf(g),
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
