package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// BudgetUsage is the same spend and in-flight reservation a key or workspace budget gates on.
type BudgetUsage struct {
	Spent, Reserved int64
	ResetsAt        time.Time
}

// ReadBudgetUsage reads one budget's current UTC window and reset using the database clock.
func ReadBudgetUsage(ctx context.Context, tx pg.Tx, subject uuid.UUID, reset Reset) (BudgetUsage, error) {
	row, err := queries.ReadBudgetUsage(ctx, tx, ReadBudgetUsageParams{Subject: subject, Reset: string(reset)})
	if err != nil {
		return BudgetUsage{}, fmt.Errorf("read budget usage: %w", err)
	}
	return BudgetUsage{Spent: row.Spent, Reserved: row.Reserved, ResetsAt: row.ResetsAt.UTC()}, nil
}
