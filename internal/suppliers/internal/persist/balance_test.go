package persist_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/syncer"
)

func TestBalanceRequestPreservesBackoffAndFencesInflightStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		halted, stopped bool
	}{
		{"normal", false, false}, {"halted", true, false}, {"stopped", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBalanceRequest(t, tc.halted, tc.stopped)
		})
	}
}

func checkBalanceRequest(t *testing.T, halted, stopped bool) {
	t.Helper()
	c := newCluster(t)
	s := c.acquire(c.node("sync"))
	st := stateAt(1)
	st.Tasks[syncer.TaskBalance] = syncer.TaskState{
		Due: t0.Add(time.Hour), BackoffUntil: t0.Add(time.Minute),
		LastOK: t0, Failures: 3, Halted: halted,
	}
	st.Scan = []byte(`{"page":7}`)
	if stopped {
		st.Status = syncer.NeedsAttention
		st.Reason = "login rejected"
	}
	if err := s.Store().Commit(t.Context(), 0, syncer.Commit{State: st}); err != nil {
		t.Fatal(err)
	}
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		return persist.RequestBalance(t.Context(), tx, c.supplier, t0)
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := st
	if !halted && !stopped {
		want.Version++
		want.Tasks[syncer.TaskBalance].Due = t0
	}
	if diff := cmp.Diff(want, after, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("balance request changed state (-want +got): %s", diff)
	}
	if halted || stopped {
		return
	}
	err = s.Store().Commit(t.Context(), 1, syncer.Commit{State: stateAt(2)})
	if !errors.Is(err, syncer.ErrFenced) {
		t.Fatalf("old step committed over balance request: %v", err)
	}
	// The current lease is retained, so another node cannot start alongside an in-flight upstream request.
	if _, err := c.node("other").Acquire(t.Context(), c.supplier); !errors.Is(err, persist.ErrLeaseHeld) {
		t.Fatalf("request released another node's lease: %v", err)
	}
}
