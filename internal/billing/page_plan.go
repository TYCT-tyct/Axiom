package billing

import (
	"context"
	"fmt"

	"axiom/internal/platform/pg"
)

// planLedgerPage preserves early stopping even for the correlated account/seq-NULL history and cached parameter
// estimates. This setting lasts until the caller's transaction ends, including queries after replay.
// Scope InitPlans fix per-row parsing and RLS estimates, not these estimates of the query's own conditions.
func planLedgerPage(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort = off"); err != nil {
		return fmt.Errorf("billing: plan the ledger page in index order: %w", err)
	}
	return nil
}
