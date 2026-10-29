package billing

import (
	"context"
	"fmt"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// ConsumeBalanceNotices drains at most 1000 movements atomically with the recipient's effects.
// Per-account movement values freeze crossings even when workers consume out of order.
func ConsumeBalanceNotices(ctx context.Context, tx pg.Tx, consume func([]accounts.BalanceNotice) error) error {
	rows, err := tx.Query(ctx, `DELETE FROM billing.balance_notices WHERE id IN (
 SELECT id FROM billing.balance_notices ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1000)
 RETURNING id,site_id,account_id,previous,balance,top_up`)
	if err != nil {
		return fmt.Errorf("billing: read balance notices: %w", err)
	}
	var notices []accounts.BalanceNotice
	for rows.Next() {
		var n accounts.BalanceNotice
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
	if len(notices) == 0 {
		return nil
	}
	return consume(notices)
}
