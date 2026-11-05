package operations

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// CapPolicy is what the owner allows in a scope (Site, nil for the platform) and category, in 10⁻⁹ of Currency. The
// autonomous limits decide whether a proposal waits for the owner; the Hard ones refuse it. Paused is when the
// reconciliation found its counters drifted (ReconcileCaps), nil when it is not: every proposal then waits for the owner.
type CapPolicy struct {
	Site                               *uuid.UUID
	Category                           Category
	Currency                           platform.Currency
	Single, Daily, Weekly, ObjectDaily int64
	DailyCount                         int32
	HardSingle, HardDaily, HardWeekly  int64
	Rev                                int64
	Updated                            time.Time
	Paused                             *time.Time
}

// CapPolicies reads every policy, the platform's first in each category.
func CapPolicies(ctx context.Context, tx pg.Tx) ([]CapPolicy, error) {
	rows, err := New().CapPolicies(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the cap policies: %w", err)
	}
	policies := make([]CapPolicy, len(rows))
	for i, r := range rows {
		if policies[i], err = policyOf(r); err != nil {
			return nil, err
		}
	}
	return policies, nil
}

// SetCapPolicy makes or changes p's policy as actor and records it. rev is the revision the caller read, 0 for a
// policy not made yet; another is ErrPolicyChanged. The currency of a policy that exists cannot change.
func SetCapPolicy(ctx context.Context, tx pg.Tx, actor audit.Actor, p CapPolicy, rev int64, reason string) (CapPolicy, error) {
	if err := p.check(); err != nil {
		return CapPolicy{}, err
	}
	found, err := New().LockCapPolicyForUpdate(ctx, tx, LockCapPolicyForUpdateParams{Category: string(p.Category), SiteID: p.Site})
	if err != nil {
		return CapPolicy{}, fmt.Errorf("lock the cap policy: %w", err)
	}
	switch {
	case len(found) == 0 && rev != 0, len(found) > 0 && found[0].Rev != rev:
		return CapPolicy{}, ErrPolicyChanged
	case len(found) > 0 && found[0].Currency != p.Currency.String():
		return CapPolicy{}, ErrCurrencyFixed
	}
	var before map[string]any
	if len(found) > 0 {
		old, err := policyOf(found[0])
		if err != nil {
			return CapPolicy{}, err
		}
		before = old.detail()
	}
	set, err := writePolicy(ctx, tx, p, len(found) == 0)
	if err != nil {
		return CapPolicy{}, err
	}
	return set, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "cap_policy.set", Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "cap_policy", ID: scopeName(p.Site) + ":" + string(p.Category)},
		Detail: map[string]any{"before": before, "after": set.detail()},
	})
}

// check refuses a negative limit and a hard limit below its autonomous one, as the table does.
func (p CapPolicy) check() error {
	if min(p.Single, p.Daily, p.Weekly, p.ObjectDaily, int64(p.DailyCount)) < 0 ||
		p.HardSingle < p.Single || p.HardDaily < p.Daily || p.HardWeekly < p.Weekly {
		return ErrBadPolicy
	}
	return nil
}

// writePolicy makes p's policy at revision 1, or replaces the limits of the one there and moves its revision.
func writePolicy(ctx context.Context, tx pg.Tx, p CapPolicy, create bool) (CapPolicy, error) {
	q := New()
	var row OperationsCapPolicy
	var err error
	if create {
		row, err = q.InsertCapPolicy(ctx, tx, InsertCapPolicyParams{
			SiteID: p.Site, Category: string(p.Category), Currency: p.Currency.String(), SingleMax: p.Single,
			DailyMax: p.Daily, WeeklyMax: p.Weekly, ObjectDailyMax: p.ObjectDaily, DailyCountMax: p.DailyCount,
			HardSingleMax: p.HardSingle, HardDailyMax: p.HardDaily, HardWeeklyMax: p.HardWeekly,
		})
	} else {
		row, err = q.UpdateCapPolicy(ctx, tx, UpdateCapPolicyParams{
			SiteID: p.Site, Category: string(p.Category), SingleMax: p.Single, DailyMax: p.Daily, WeeklyMax: p.Weekly,
			ObjectDailyMax: p.ObjectDaily, DailyCountMax: p.DailyCount, HardSingleMax: p.HardSingle,
			HardDailyMax: p.HardDaily, HardWeeklyMax: p.HardWeekly,
		})
	}
	if err != nil {
		return CapPolicy{}, fmt.Errorf("write the cap policy: %w", err)
	}
	return policyOf(row)
}

func (p CapPolicy) detail() map[string]any {
	return map[string]any{
		"currency": p.Currency.String(), "single_max": p.Single, "daily_max": p.Daily, "weekly_max": p.Weekly,
		"object_daily_max": p.ObjectDaily, "daily_count_max": p.DailyCount, "hard_single_max": p.HardSingle,
		"hard_daily_max": p.HardDaily, "hard_weekly_max": p.HardWeekly, "rev": p.Rev,
	}
}

func policyOf(r OperationsCapPolicy) (CapPolicy, error) {
	currency, err := platform.ParseCurrency(r.Currency)
	if err != nil {
		return CapPolicy{}, fmt.Errorf("the cap policy of %s: %w", r.Category, err)
	}
	return CapPolicy{
		Site: r.SiteID, Category: Category(r.Category), Currency: currency, Single: r.SingleMax, Daily: r.DailyMax,
		Weekly: r.WeeklyMax, ObjectDaily: r.ObjectDailyMax, DailyCount: r.DailyCountMax, HardSingle: r.HardSingleMax,
		HardDaily: r.HardDailyMax, HardWeekly: r.HardWeeklyMax, Rev: r.Rev, Updated: r.UpdatedAt, Paused: r.PausedAt,
	}, nil
}

// lockPolicy holds the policy of site and category for share until tx ends: a change of it waits for tx.
func lockPolicy(ctx context.Context, tx pg.Tx, category Category, site *uuid.UUID) (CapPolicy, error) {
	rows, err := New().LockCapPolicy(ctx, tx, LockCapPolicyParams{Category: string(category), SiteID: site})
	if err != nil {
		return CapPolicy{}, fmt.Errorf("lock the cap policy: %w", err)
	}
	if len(rows) == 0 {
		return CapPolicy{}, ErrNoCapPolicy
	}
	return policyOf(rows[0])
}

// CapCurrency is the currency of the policy of site (nil: the platform) and category, which the caller turns the
// amount into before proposing it; ErrNoCapPolicy when there is none. A policy keeps its currency, so it cannot change
// between this read and the proposal.
func CapCurrency(ctx context.Context, tx pg.Tx, site *uuid.UUID, category Category) (platform.Currency, error) {
	rows, err := New().CapPolicyOf(ctx, tx, CapPolicyOfParams{Category: string(category), SiteID: site})
	if err != nil {
		return platform.Currency{}, fmt.Errorf("read the cap policy: %w", err)
	}
	if len(rows) == 0 {
		return platform.Currency{}, ErrNoCapPolicy
	}
	p, err := policyOf(rows[0])
	return p.Currency, err
}

// CapUsage is a policy with what its category's counters of the current UTC day and ISO week hold.
type CapUsage struct {
	Policy              CapPolicy
	DayStart, WeekStart time.Time
	Day, Week           WindowUsage
}

// WindowUsage is what a counter holds: reserved and committed amounts, and the proposals made in it.
type WindowUsage struct {
	Reserved, Committed int64
	Count               int32
}

// CapUsages reads every policy with its usage now, the platform's first in each category.
func CapUsages(ctx context.Context, tx pg.Tx) ([]CapUsage, error) {
	rows, err := New().CapUsage(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the cap usage: %w", err)
	}
	usage := make([]CapUsage, len(rows))
	for i, r := range rows {
		p, err := policyOf(r.OperationsCapPolicy)
		if err != nil {
			return nil, err
		}
		usage[i] = CapUsage{
			Policy: p, DayStart: r.DayStart, WeekStart: r.WeekStart,
			Day:  WindowUsage{Reserved: r.DayReserved, Committed: r.DayCommitted, Count: r.DayCount},
			Week: WindowUsage{Reserved: r.WeekReserved, Committed: r.WeekCommitted, Count: r.WeekCount},
		}
	}
	return usage, nil
}
