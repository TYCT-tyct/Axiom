package billing

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// inWorkspace is the bank's account's call by another key of the same workspace, so the two share its budget.
func (b *bank) inWorkspace(budget *Limit) (first, second Admitted) {
	first = b.admitted
	first.WorkspaceBudget = budget
	second = first
	second.Key = uuid.NewV7()
	return first, second
}

// workspaceUse is the bank's workspace's spent and in-flight amounts over the window reset covers.
func (b *bank) workspaceUse(reset Reset) BudgetUsage {
	b.t.Helper()
	var use BudgetUsage
	if err := b.app.ReadTx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		use, err = ReadBudgetUsage(b.t.Context(), tx, b.admitted.Workspace, reset)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return use
}

// TestWorkspaceBudgetPool: two keys of a workspace reserving at once never take it past its budget, which they share,
// while the balance has room for far more; each refusal is the workspace's, for an amount above what was left after
// every reservation that succeeded.
func TestWorkspaceBudgetPool(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{})
	first, second := b.inWorkspace(&Limit{Amount: 1500, Reset: ResetDaily})
	total, refused := b.storm(SourceWorkspace, first, second)
	if total > 1500 || len(refused) == 0 {
		t.Fatalf("reserved %d of the budget's 1500 with %d refusals", total, len(refused))
	}
	for _, n := range refused {
		if n <= 1500-total {
			t.Errorf("refused %d with %d left", n, 1500-total)
		}
	}
	if use := b.workspaceUse(ResetDaily); use.Reserved != total || use.Spent != 0 {
		t.Errorf("the workspace holds %+v, want %d in flight", use, total)
	}
	b.consistent()
}

// TestWorkspaceBudgetSettlement: what a key's call is charged counts against the budget its workspace's other keys
// share, once however often settlement is retried; a call that did not run gives its hold back; a budget lowered below
// what is used refuses the next call but not the charge of one already admitted.
func TestWorkspaceBudgetSettlement(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{})
	budget := &Limit{Amount: 1000, Reset: ResetNever}
	first, second := b.inWorkspace(budget)
	refusedByWorkspace := func(a Admitted, amount int64) {
		t.Helper()
		var short *Insufficient
		if _, err := b.reserve(a, amount); !errors.As(err, &short) || short.Source != SourceWorkspace || short.Transient {
			t.Errorf("reserving %d: %v, want the workspace budget short for good", amount, err)
		}
	}
	held := b.mustReserve(first, 600)
	refusedByWorkspace(second, 500)
	for range 2 { // a retried settlement charges once
		if f := b.settleWith(held, outcome(Committed, FinishStop, used(t, 300, 0, 0))); f.Charged.CeilAmount() != 300 {
			t.Fatalf("charged %v, want 300", f.Charged)
		}
	}
	if use := b.workspaceUse(ResetNever); use.Spent != 300 || use.Reserved != 0 {
		t.Errorf("after the charge the workspace holds %+v, want 300 spent", use)
	}
	late := b.mustReserve(second, 700)
	budget.Amount = 500
	refusedByWorkspace(first, 1)
	b.settleWith(late, outcome(Committed, FinishStop, used(t, 700, 0, 0)))
	if use := b.workspaceUse(ResetNever); use.Spent != 1000 || use.Reserved != 0 {
		t.Errorf("an admitted call charged past the lowered budget: %+v, want 1000 spent", use)
	}
	budget.Amount = 2000
	unrun := b.mustReserve(first, 900)
	b.settleWith(unrun, outcome(NotExecuted, FinishNone))
	b.mustReserve(second, 1000)
	if use := b.workspaceUse(ResetNever); use.Spent != 1000 || use.Reserved != 1000 {
		t.Errorf("after a call that did not run: %+v, want 1000 spent and 1000 in flight", use)
	}
	b.balanced()
}

func (b *bank) mustReserve(a Admitted, amount int64) *Hold {
	b.t.Helper()
	h, err := b.reserve(a, amount)
	if err != nil {
		b.t.Fatalf("reserving %d: %v", amount, err)
	}
	return h
}
