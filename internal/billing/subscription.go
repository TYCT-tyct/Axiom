package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

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
	n := term / window
	if term%window != 0 {
		n++ // ceiling without term+window-1, which overflows at the values a bigint can hold
	}
	if n > maxPlanWindows {
		return 0, &PlanError{fmt.Sprintf("term holds %d windows, more than %d", n, maxPlanWindows)}
	}
	return int32(n), nil //nolint:gosec // G115: at most maxPlanWindows
}

const (
	minWindowSeconds = 3600
	maxPlanWindows   = 400
	// maxTermSeconds is the longest term or window a time.Duration can hold. Saving a longer one would wrap the
	// nanosecond conversion and sell a term of fractions of a second.
	maxTermSeconds = int64(math.MaxInt64) / int64(time.Second)
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
	CreatedAt, UpdatedAt              time.Time
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
	if _, err := unitScale(p.Currency); err != nil {
		return &PlanError{err.Error()}
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
	if p.TermSeconds > maxTermSeconds {
		return &PlanError{fmt.Sprintf("needs a term of at most %d seconds", maxTermSeconds)}
	}
	if p.WindowSeconds > maxTermSeconds {
		return &PlanError{fmt.Sprintf("needs a window of at most %d seconds", maxTermSeconds)}
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
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
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
