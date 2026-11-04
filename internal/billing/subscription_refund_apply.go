package billing

import (
	"context"
	"fmt"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func applyRefund(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest, rg refundCase, plan refundPlan) error {
	g := rg.grant
	lines, err := refundLines(req, rg, plan)
	if err != nil {
		return err
	}
	for _, w := range plan.windows {
		if n, err := queries.VoidWindow(ctx, tx, VoidWindowParams{SubscriptionID: g.SubscriptionID, Seq: w.Seq}); err != nil {
			return fmt.Errorf("billing: void the subscription window: %w", err)
		} else if n != 1 {
			return fmt.Errorf("billing: window %d was voided under the lock", w.Seq)
		}
	}
	if n, err := queries.RefundGrant(ctx, tx, RefundGrantParams{
		ID: g.ID, Credits: req.Credits, Paid: plan.paid, Fee: plan.fee, Windows: refundCount(plan.remove),
	}); err != nil {
		return fmt.Errorf("billing: record the grant's refund: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: grant %s changed under the lock", ErrNotRefundable, g.ID)
	}
	return finishRefund(ctx, tx, req, rg, plan, lines)
}

// finishRefund writes the applied refund, posts its ledger entries, takes the term back and audits the result. The
// refund entry must exist before the liability is read by takeBackTerm, so it is written before the term changes.
func finishRefund(ctx context.Context, tx pg.Tx, req SubscriptionRefundRequest, rg refundCase, plan refundPlan, lines []line) error {
	g := rg.grant
	if n, err := queries.InsertSubscriptionRefund(ctx, tx, InsertSubscriptionRefundParams{
		ID: req.ID, GrantID: g.ID, SiteID: g.SiteID, AccountID: g.AccountID, SubscriptionID: g.SubscriptionID,
		Credits: req.Credits, Paid: plan.paid, Fee: plan.fee, Currency: rg.currency.String(),
		Windows: refundCount(plan.remove), Unused: plan.unused, Revoked: false, Actor: req.Actor.String(), Reason: req.Reason,
	}); err != nil {
		return fmt.Errorf("billing: write the subscription refund: %w", err)
	} else if n != 1 {
		return fmt.Errorf("%w: refund %s was written under the lock", ErrNotRefundable, req.ID)
	}
	if _, err := post(ctx, tx, event{
		site: g.SiteID, account: g.AccountID, kind: "plan_refund", request: req.ID, revision: 1,
	}, lines...); err != nil {
		return err
	}
	// Post the refund before taking the term back: writeOffSubscription reads the liability, and it must see the
	// refund's credit before it lapses what an older, already-ended window still owes.
	revoked, err := takeBackTerm(ctx, tx, rg.sub, plan.now)
	if err != nil {
		return err
	}
	if n, err := queries.SetSubscriptionRefundRevoked(ctx, tx, SetSubscriptionRefundRevokedParams{ID: req.ID, Revoked: revoked}); err != nil {
		return fmt.Errorf("billing: record whether the refund revoked the subscription: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: refund %s was written under the lock", req.ID)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: req.Actor, Action: "subscription.refund", Reason: req.Reason,
		Target: audit.Target{Module: "billing", Kind: "subscription", ID: g.SubscriptionID.String()},
		Detail: map[string]any{
			"refund": req.ID.String(), "grant": g.ID.String(), "credits": req.Credits, "paid": plan.paid,
			"fee": plan.fee, "currency": rg.currency.String(), "windows": plan.remove, "unused": plan.unused, "revoked": revoked,
		},
	})
}

func refundLines(req SubscriptionRefundRequest, rg refundCase, plan refundPlan) ([]line, error) {
	scale, err := unitScale(rg.currency)
	if err != nil {
		return nil, err
	}
	paidB, feeB := plan.paid*scale, plan.fee*scale
	adjust := mulDivSigned(plan.removedFace, rg.grant.Face-rg.value, rg.grant.Face)
	// The balancing line is the revenue of the used part: what the refund is worth in site credits, less the unused
	// liability taken back, plus the discount or premium of the removed windows (a discount raises it, a premium
	// lowers it). The sale's discount is reversed with a credit and its premium with a debit.
	revenue := req.Credits - plan.unused + adjust
	lines := []line{{bookPlanLiability, plan.unused, siteCredits}, {bookRevenue, revenue, siteCredits}}
	if adjust > 0 {
		lines = append(lines, line{bookPlanDiscount, -adjust, siteCredits})
	} else if adjust < 0 {
		lines = append(lines, line{bookRevenue, -adjust, siteCredits})
	}
	if rg.rate == nil {
		return append(lines,
			line{bookCash, -paidB, siteCredits}, line{bookFeeIncome, feeB, siteCredits},
			line{bookExchange, paidB - feeB - req.Credits, siteCredits}), nil
	}
	in := rg.currency.String()
	return append(lines,
		line{bookCash, -paidB, in}, line{bookFeeIncome, feeB, in},
		line{bookExchange, paidB - feeB, in}, line{bookExchange, -req.Credits, siteCredits}), nil
}

func takeBackTerm(ctx context.Context, tx pg.Tx, sub BillingSubscription, now time.Time) (bool, error) {
	lastLive, err := queries.LastLiveWindow(ctx, tx, sub.ID)
	if err != nil {
		return false, fmt.Errorf("billing: read the last window left: %w", err)
	}
	if lastLive == 0 {
		return true, revokeRefundedTerm(ctx, tx, sub, now)
	}
	lastWindow, err := queries.Window(ctx, tx, WindowParams{SubscriptionID: sub.ID, Seq: lastLive})
	if err != nil {
		return false, fmt.Errorf("billing: read the last window left: %w", err)
	}
	if !now.Before(lastWindow.EndsAt) {
		return true, expireRefundedTerm(ctx, tx, sub, lastWindow)
	}
	if err := pointAtLiveWindow(ctx, tx, sub, lastLive); err != nil {
		return false, err
	}
	if n, err := queries.ShortenSubscription(ctx, tx, ShortenSubscriptionParams{ID: sub.ID, EndsAt: lastWindow.EndsAt}); err != nil {
		return false, fmt.Errorf("billing: shorten the subscription: %w", err)
	} else if n != 1 {
		return false, fmt.Errorf("billing: the subscription %s changed under the lock", sub.ID)
	}
	return false, nil
}

func revokeRefundedTerm(ctx context.Context, tx pg.Tx, sub BillingSubscription, now time.Time) error {
	ends := now
	if !ends.After(sub.StartsAt) {
		ends = sub.StartsAt.Add(time.Second)
	}
	if n, err := queries.RevokeSubscription(ctx, tx, RevokeSubscriptionParams{ID: sub.ID, EndsAt: ends, CurrentSeq: 1}); err != nil {
		return fmt.Errorf("billing: revoke the subscription: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the subscription %s changed under the lock", sub.ID)
	}
	return writeOffSubscription(ctx, tx, expiredSubscription(sub))
}

// expireRefundedTerm marks a term whose remaining live windows already ended as expired. writeOffSubscription waits
// for any hold on those windows and then lapses what is left.
func expireRefundedTerm(ctx context.Context, tx pg.Tx, sub BillingSubscription, last WindowRow) error {
	if n, err := queries.ExpireSubscriptionAt(ctx, tx, ExpireSubscriptionAtParams{ID: sub.ID, EndsAt: last.EndsAt}); err != nil {
		return fmt.Errorf("billing: expire the subscription: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the subscription %s changed under the lock", sub.ID)
	}
	return writeOffSubscription(ctx, tx, expiredSubscription(sub))
}

// pointAtLiveWindow repairs current_seq when a full refund voided it while an older window remains live.
func pointAtLiveWindow(ctx context.Context, tx pg.Tx, sub BillingSubscription, lastLive int32) error {
	voided, err := queries.WindowVoided(ctx, tx, WindowVoidedParams{SubscriptionID: sub.ID, Seq: sub.CurrentSeq})
	if err != nil {
		return fmt.Errorf("billing: read the current window: %w", err)
	}
	if !voided {
		return nil
	}
	if n, err := queries.SetCurrentWindow(ctx, tx, SetCurrentWindowParams{ID: sub.ID, Seq: lastLive}); err != nil {
		return fmt.Errorf("billing: point the subscription at its last live window: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the subscription %s changed under the lock", sub.ID)
	}
	return nil
}

func expiredSubscription(sub BillingSubscription) Subscription {
	return Subscription{ID: sub.ID, Site: sub.SiteID, Account: sub.AccountID, State: "expired"}
}

// refundCount narrows a window count to the integer column; a plan has at most 400 windows.
func refundCount(n int64) int32 {
	return int32(n) //nolint:gosec // a plan has at most 400 windows, so n fits
}

// readSubscriptionRefund reads a refund by its idempotency key.
