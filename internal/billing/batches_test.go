package billing

import (
	"testing"

	"axiom/internal/platform/pg"
)

// A charge committed after the last advance is newer than the balance read with the batches; the batches must be left
// out rather than describe a moment before that charge, and a later advance must bring them to the same snapshot.
func TestReadCreditBatchesLeavesOutAReplayBehindTheSnapshot(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{})
	if ready, err := AdvanceCreditBatches(t.Context(), b.app, b.admitted.Scope, b.admitted.Account, 8); err != nil || !ready {
		t.Fatalf("AdvanceCreditBatches = %v, %v; want ready", ready, err)
	}
	b.charge(400_000)

	var ready bool
	var batches []CreditBatchView
	err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var err error
		batches, ready, err = ReadCreditBatches(t.Context(), tx, b.admitted.Account, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatalf("a replay behind the snapshot reported ready with %d batches", len(batches))
	}

	if ready, err = AdvanceCreditBatches(t.Context(), b.app, b.admitted.Scope, b.admitted.Account, 8); err != nil || !ready {
		t.Fatalf("AdvanceCreditBatches = %v, %v; want ready", ready, err)
	}
	err = b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var err error
		batches, ready, err = ReadCreditBatches(t.Context(), tx, b.admitted.Account, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ready || len(batches) != 1 {
		t.Fatalf("ReadCreditBatches = %d batches, ready %v; want one", len(batches), ready)
	}
	if batches[0].Remaining != 600_000 {
		t.Errorf("remaining = %d; want 600000", batches[0].Remaining)
	}
}

// A spent batch stays in the replay for a correction, but it is not credit the console lists.
func TestReadCreditBatchesLeavesOutSpentBatches(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{})
	b.charge(1e6)
	if ready, err := AdvanceCreditBatches(t.Context(), b.app, b.admitted.Scope, b.admitted.Account, 8); err != nil || !ready {
		t.Fatalf("AdvanceCreditBatches = %v, %v; want ready", ready, err)
	}
	var ready bool
	var batches []CreditBatchView
	err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var err error
		batches, ready, err = ReadCreditBatches(t.Context(), tx, b.admitted.Account, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ready || len(batches) != 0 {
		t.Fatalf("ReadCreditBatches = %d batches, ready %v; want none", len(batches), ready)
	}
}
