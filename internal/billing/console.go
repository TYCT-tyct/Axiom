package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// BalanceView is an account's money as the console shows it, in billionths of a credit: what a call is admitted by.
// Available is Balance less Reserved; a call is admitted while Available less Floor covers its reservation.
type BalanceView struct {
	Balance, Reserved, Floor int64
	SpentThisMonth           int64
}

// ReadBalance is account's money, read in tx, which must hold the account's scope. workspaces are the account's
// workspaces, whose counters the month's spending is summed from. An account with no balance row has nothing.
func ReadBalance(ctx context.Context, tx pg.Tx, account uuid.UUID, workspaces []uuid.UUID) (BalanceView, error) {
	rows, err := queries.ReadBalance(ctx, tx, account)
	if err != nil {
		return BalanceView{}, fmt.Errorf("billing: read the balance: %w", err)
	}
	var view BalanceView
	if len(rows) > 0 {
		view = BalanceView{Balance: rows[0].Balance, Reserved: rows[0].Reserved, Floor: rows[0].Floor}
	}
	if view.SpentThisMonth, err = queries.SpentThisMonth(ctx, tx, workspaces); err != nil {
		return BalanceView{}, fmt.Errorf("billing: read the month's spending: %w", err)
	}
	return view, nil
}

// KeyWindow is a key with a spend limit and how often the limit starts over.
type KeyWindow struct {
	Key   uuid.UUID
	Reset Reset
}

// KeysUsed is what each key has spent and holds reserved in the window of its own reset that holds today, which a
// limit is checked against at admission, read in tx under the account's scope in one statement.
func KeysUsed(ctx context.Context, tx pg.Tx, keys []KeyWindow) (map[uuid.UUID]int64, error) {
	ids, resets := make([]uuid.UUID, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		ids[i], resets[i] = k.Key, string(k.Reset)
	}
	rows, err := queries.ReadKeysUsed(ctx, tx, ReadKeysUsedParams{Keys: ids, Resets: resets})
	if err != nil {
		return nil, fmt.Errorf("billing: read what keys have used: %w", err)
	}
	used := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		used[r.KeyID] = r.Used
	}
	return used, nil
}
