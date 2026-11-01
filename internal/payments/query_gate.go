package payments

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// ErrQueryPaused means the merchant's two slots are occupied or its circuit is open. No order backoff is consumed.
var ErrQueryPaused = errors.New("payments: merchant queries are temporarily paused")

// ErrQueryBusy is transient contention, rather than an open circuit. Background jobs spend a retry, with jitter.
var ErrQueryBusy = fmt.Errorf("%w: query slots are occupied", ErrQueryPaused)

func (p *Payments) acquireQuery(ctx context.Context, merchant MerchantID, background bool) (uuid.UUID, error) {
	token := uuid.NewV7()
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		if err := queries.InitQueryHealth(ctx, tx, uuid.UUID(merchant)); err != nil {
			return err
		}
		n, err := queries.AcquireQuery(ctx, tx, AcquireQueryParams{Token: token, MerchantID: uuid.UUID(merchant), Background: background})
		if err == nil && n == 0 {
			paused, err := queries.QueryIsPaused(ctx, tx, uuid.UUID(merchant))
			if err != nil {
				return err
			}
			if paused {
				return ErrQueryPaused
			}
			return ErrQueryBusy
		}
		return err
	})
	return token, err
}

type queryOutcome uint8

const (
	queryUnused queryOutcome = iota
	queryKnown
	queryUnknown
)

// queryHealthOutcome separates caller cancellation from the core gateway deadline and definite channel replies.
func queryHealthOutcome(ctx context.Context, err error, incomplete bool) queryOutcome {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil && !errors.Is(context.Cause(ctx), errQueryDeadline) {
		return queryUnused
	}
	var refused *Rejected
	if errors.Is(err, ErrRefundNotFound) {
		return queryKnown
	}
	if err != nil && !errors.As(err, &refused) || incomplete {
		return queryUnknown
	}
	return queryKnown
}

// finishQuery fences an expired worker with its token. A completion already in flight cannot reopen a paused circuit.
func (p *Payments) finishQuery(ctx context.Context, merchant MerchantID, token uuid.UUID, outcome queryOutcome) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	return p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		h, err := queries.LockQueryHealth(ctx, tx, uuid.UUID(merchant))
		if err != nil {
			return err
		}
		if (h.FirstToken == nil || *h.FirstToken != token) && (h.SecondToken == nil || *h.SecondToken != token) {
			return nil
		}
		changed, resolved := advanceQueryHealth(&h, outcome)
		if err = queries.FinishQuery(ctx, tx, FinishQueryParams{MerchantID: uuid.UUID(merchant), Token: token, Failures: h.Failures, PausedUntil: h.PausedUntil}); err != nil {
			return err
		}
		if changed {
			return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "payments.query_paused", Key: merchant.TypeID(), Severity: "warning", Fields: map[string]string{"value": "1"}, Resolved: resolved})
		}
		return nil
	})
}

func advanceQueryHealth(h *LockQueryHealthRow, outcome queryOutcome) (changed, resolved bool) {
	if outcome == queryUnused || h.PausedUntil.After(h.ObservedAt) {
		return false, false
	}
	if outcome == queryKnown {
		h.Failures = 0
		changed = h.PausedUntil.After(time.Unix(0, 0))
		h.PausedUntil = time.Unix(0, 0)
		return changed, true
	}
	h.Failures++
	if h.Failures < 5 {
		return false, false
	}
	h.Failures, h.PausedUntil = 0, h.ObservedAt.Add(5*time.Minute)
	return true, false
}
