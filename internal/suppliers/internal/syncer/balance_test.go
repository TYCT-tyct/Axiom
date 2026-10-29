package syncer

import (
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

func TestBalanceObservationDatesTheStartOfTheRead(t *testing.T) {
	t.Parallel()
	clock := &clock{t: t0}
	store := newStore()
	for task := TaskCatalog; task < numTasks; task++ {
		store.state.Tasks[task].Due = t0.Add(time.Hour)
	}
	manager := &supplier{account: func() (adapter.Account, error) {
		clock.advance(time.Minute)
		return adapter.Account{Balance: "10", Funding: adapter.FundingWallet}, nil
	}}
	if err := New(store, manager, Defaults(), clock.now).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.state.BalanceAt != t0 {
		t.Fatalf("balance observed at completion %v, want read start %v", store.state.BalanceAt, t0)
	}
}
