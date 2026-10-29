package billing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestBudgetUsageMatchesAdmissionWindows(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	today := time.Now().UTC().Truncate(24 * time.Hour)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	first := today.AddDate(0, 0, 1-today.Day())
	seedKeyDays(b, today, monday, first)
	for _, reset := range []Reset{ResetNever, ResetDaily, ResetWeekly, ResetMonthly} {
		if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			return checkBudgetWindow(t, b, tx, reset, today)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.app.ReadTx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), func(tx pg.Tx) error {
		usage, err := ReadBudgetUsage(t.Context(), tx, b.admitted.Key, ResetNever)
		if usage.Spent != 0 || usage.Reserved != 0 {
			t.Errorf("foreign usage leaked: %v", usage)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func checkBudgetWindow(t *testing.T, b *bank, tx pg.Tx, reset Reset, today time.Time) error {
	t.Helper()

	budget, err := ReadBudgetUsage(t.Context(), tx, b.admitted.Key, reset)
	if err != nil {
		return err
	}
	usage, err := ReadKeyUsage(t.Context(), tx, b.admitted.Key, reset)
	if err != nil {
		return err
	}
	if budget.Spent+budget.Reserved != usage.Counted {
		t.Errorf("%s: %v vs counted %d", reset, budget, usage.Counted)
	}
	if (budget.ResetsAt == nil) != (reset == ResetNever) {
		t.Errorf("%s reset: %v", reset, budget.ResetsAt)
	}
	if budget.ResetsAt != nil && !budget.ResetsAt.After(today) {
		t.Errorf("reset: %v", budget.ResetsAt)
	}
	workspace, err := ReadBudgetUsage(t.Context(), tx, b.admitted.Workspace, reset)
	if err != nil {
		return err
	}
	if workspace.Spent != 999999 || workspace.Reserved != 0 {
		t.Errorf("workspace: %v", workspace)
	}
	return nil
}
