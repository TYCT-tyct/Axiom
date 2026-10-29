package billing

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// BalanceNotice is a committed wallet movement. ID deduplicates downstream effects.
type BalanceNotice struct {
	ID, Site, Account        uuid.UUID
	Previous, Balance, TopUp int64
}

// ConsumeBalanceNotices drains at most 100 movements atomically with the recipient's effects.
// Per-account movement values freeze crossings even when workers consume out of order.
func ConsumeBalanceNotices(ctx context.Context, tx pg.Tx, consume func(BalanceNotice) error) error {
	rows, err := tx.Query(ctx, `SELECT id,site_id,account_id,previous,balance,top_up
 FROM billing.balance_notices ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 100`)
	if err != nil {
		return fmt.Errorf("billing: read balance notices: %w", err)
	}
	var notices []BalanceNotice
	for rows.Next() {
		var n BalanceNotice
		if err = rows.Scan(&n.ID, &n.Site, &n.Account, &n.Previous, &n.Balance, &n.TopUp); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		if err = consume(n); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM billing.balance_notices WHERE id=$1`, n.ID); err != nil {
			return err
		}
	}
	return nil
}
