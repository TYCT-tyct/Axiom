package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// The lifecycle of a subscription (docs/domains/billing.md「订阅套餐」): passing windows, expiring a term, writing off
// what is left once the last hold ends, and reading the window admission and settlement use.

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
		next, err := queries.NextWindow(ctx, tx, NextWindowParams{SubscriptionID: w.subscription, Seq: w.seq})
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
