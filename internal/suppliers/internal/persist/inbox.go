package persist

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/store"
)

// The kinds of the suppliers' to-do queue that the tables can show (docs/domains/suppliers.md「待处理队列」).
// unlisted_model_called is not among them: the refused calls are in the call log, which this module does not read.
var (
	// InboxEverything is every kind an observation by the sync brings in line.
	InboxEverything = []string{"low_balance", "ratio_changed", "cost_mismatch", "group_added", "key_invalid", "unknown_model"}
	// InboxKeys is the kind a change of a credential's status starts or ends.
	InboxKeys = []string{"key_invalid"}
	// InboxGroups is the kind a person's choice of a group ends.
	InboxGroups = []string{"group_added"}
)

// The windows of an observation: a ratio change is shown for a week after it, a low balance is divided by the last day's
// spend, and a day's cost is judged for the last seven UTC days, today included.
const (
	ratioWindow  = 7 * 24 * time.Hour
	spendWindow  = 24 * time.Hour
	costDays     = 7
	inboxPercent = 6 // digits of the tolerance as a decimal
)

// ObserveInbox brings the supplier's queue in line with its tables for the kinds named. sighting is an observation by
// the sync, which shows an ignored item again; a person's change that starts or ends a condition is not one.
func ObserveInbox(ctx context.Context, tx pg.Tx, supplier uuid.UUID, now time.Time, kinds []string, sighting bool) error {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if err := store.New().ObserveInbox(ctx, tx, store.ObserveInboxParams{
		SupplierID: supplier, Kinds: kinds, RatioSince: now.Add(-ratioWindow), SpendSince: now.Add(-spendWindow),
		ChargesSince: today.AddDate(0, 0, 1-costDays), Relative: reconcile.DefaultConfig().Relative.FloatString(inboxPercent),
		Now: now, Sighting: sighting,
	}); err != nil {
		return fmt.Errorf("persist: observe the queue of supplier %s: %w", supplier, err)
	}
	return nil
}
