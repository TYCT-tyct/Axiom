package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// The plan side of the money paths (docs/domains/billing.md「订阅套餐」): a window is settled or released where the
// reservation froze it, and a charge that a window's remaining quota no longer covers falls back to the wallet.

// settleWindow moves a plan settlement into its frozen window: the hold goes back and the charge is used.
func moveSettledWindow(ctx context.Context, tx pg.Tx, subscription uuid.UUID, seq int32, w windowSettle) error {
	n, err := queries.SettleWindow(ctx, tx, SettleWindowParams{SubscriptionID: subscription, Seq: seq, Held: w.held, Charged: w.charged})
	if err != nil {
		return fmt.Errorf("billing: settle the subscription window: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("billing: the window %s/%d did not take a charge of %d", subscription, seq, w.charged)
	}
	return nil
}

// settlePlanWindows settles every plan window a batch's reservation ends moved, in one statement.
func settlePlanWindows(ctx context.Context, tx pg.Tx, ends []*reservationEnd) error {
	var params SettleWindowsParams
	type windowKey struct {
		subscription uuid.UUID
		seq          int32
	}
	windows := make(map[windowKey]struct{})
	for _, e := range ends {
		if e.window == nil {
			continue
		}
		windows[windowKey{e.c.row.SubscriptionID, e.c.row.WindowSeq}] = struct{}{}
		params.Subscriptions = append(params.Subscriptions, e.c.row.SubscriptionID)
		params.Seqs = append(params.Seqs, e.c.row.WindowSeq)
		params.Helds = append(params.Helds, e.window.held)
		params.Chargeds = append(params.Chargeds, e.window.charged)
	}
	if len(params.Subscriptions) == 0 {
		return nil
	}
	n, err := queries.SettleWindows(ctx, tx, params)
	if err != nil {
		return fmt.Errorf("billing: settle the subscription windows: %w", err)
	}
	if n != int64(len(windows)) {
		return fmt.Errorf("billing: settle the subscription windows wrote %d of %d rows", n, len(windows))
	}
	return nil
}

// planRoom is what a subscription window can still charge: its quota less what it used and what its other holds
// reserve, with the subscription it belongs to. The caller holds the balance row lock.
func planRoom(ctx context.Context, tx pg.Tx, subscription uuid.UUID, seq int32) (int64, Subscription, error) {
	w, err := queries.Window(ctx, tx, WindowParams{SubscriptionID: subscription, Seq: seq})
	if err != nil {
		return 0, Subscription{}, fmt.Errorf("billing: read the subscription window: %w", err)
	}
	s, err := queries.Subscription(ctx, tx, subscription)
	if err != nil {
		return 0, Subscription{}, fmt.Errorf("billing: read the subscription: %w", err)
	}
	win := Window{Seq: w.Seq, StartsAt: w.StartsAt, EndsAt: w.EndsAt, Quota: w.Quota, Used: w.Used, Reserved: w.Reserved}
	return win.Remaining(), Subscription{ID: s.ID, State: s.State, ClosedAt: s.ClosedAt}, nil
}

// markLateWallets records the source that actually paid a late settlement: cleanup released the window's hold and its
// credit may have lapsed, so the row becomes a wallet reservation before a correction can read it as a plan charge.
func markLateWallets(ctx context.Context, tx pg.Tx, ends []*reservationEnd) error {
	var ids []uuid.UUID
	for _, e := range ends {
		if e.w.wallet {
			ids = append(ids, e.c.id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := queries.SetReservationsWallet(ctx, tx, ids); err != nil {
		return fmt.Errorf("billing: record the wallet as the late settlement's source: %w", err)
	}
	return nil
}
