package syncer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

type hooks struct {
	inFlight int
	deletes  int
	failNext string
}

func (h *hooks) InFlight(context.Context) (int, error) {
	if h.failNext == "inflight" {
		h.failNext = ""
		return 0, errors.New("inflight: unavailable")
	}
	return h.inFlight, nil
}

func (h *hooks) DeleteSecrets(context.Context) error {
	if h.failNext == "delete" {
		h.failNext = ""
		return errors.New("delete: unavailable")
	}
	h.deletes++
	return nil
}

// removeAll calls Remove until it is done, moving the clock past every wait.
func removeAll(t *testing.T, e *Engine, clk *clock, h Hooks) {
	t.Helper()
	for range 60 {
		done, err := e.Remove(t.Context(), h)
		if err == nil && done {
			return
		}
		clk.advance(10 * time.Minute)
	}
	t.Fatal("the removal does not end")
}

// TestRemovalCompletesFromAnyInterruption: whichever commit fails, or whichever hook, calling Remove again finishes the
// removal with each of its effects once: the start event, the end event, the configuration generation raised once; the
// delete may run again after a commit failed, which is why it must be idempotent. Attempts in flight and late records are waited for.
func TestRemovalCompletesFromAnyInterruption(t *testing.T) {
	t.Parallel()
	for failAt := 0; failAt <= 12; failAt++ {
		for _, hookFail := range []string{"", "inflight", "delete"} {
			t.Run(fmt.Sprintf("commit %d hook %q", failAt, hookFail), func(t *testing.T) { removalInterrupted(t, failAt, hookFail) })
		}
	}
}

// removalInterrupted removes a supplier while the failAt-th commit from now fails once and the hook fails once.
func removalInterrupted(t *testing.T, failAt int, hookFail string) {
	m := healthy()
	e, st, clk := engine(m)
	round(t, e)
	if failAt > 0 {
		st.failAt = st.commits + failAt
	}
	h := &hooks{inFlight: 2, failNext: hookFail}
	gen := st.state.ConfigGen
	removeAll(t, e, clk, h)
	if st.state.Status != Removed || st.count(RemovalStarted) != 1 || st.count(RemovalDone) != 1 || h.deletes < 1 || st.state.ConfigGen != gen+1 {
		t.Errorf("status %v, started %d, done %d, deletes %d, generation %d -> %d", st.state.Status,
			st.count(RemovalStarted), st.count(RemovalDone), h.deletes, gen, st.state.ConfigGen)
	}
	if st.state.Removal != StepDone {
		t.Errorf("removal step %v", st.state.Removal)
	}
}

// TestRemovalWaitsForInFlightAndGrace: with attempts in flight the removal waits up to the limit; it waits the grace
// period for late records and reads them; nothing is deleted before.
func TestRemovalWaitsForInFlightAndGrace(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, st, clk := engine(m)
	round(t, e)
	h := &hooks{inFlight: 1}
	expect := func(step RemovalStep, why string) {
		t.Helper()
		if done, err := e.Remove(t.Context(), h); done || err != nil || st.state.Removal != step {
			t.Fatalf("%s: done %v, err %v, step %v, want step %v", why, done, err, st.state.Removal, step)
		}
	}
	expect(StepDraining, "with an attempt in flight")
	clk.advance(6 * time.Minute) // past DrainLimit
	expect(StepGrace, "after the drain limit")
	scans := m.calls[TaskCharges]
	clk.advance(29 * time.Minute)
	expect(StepGrace, "inside the grace period")
	if h.deletes != 0 {
		t.Fatal("deleted inside the grace period")
	}
	clk.advance(2 * time.Minute)
	if done, err := e.Remove(t.Context(), h); !done || err != nil || h.deletes != 1 || m.calls[TaskCharges] <= scans {
		t.Errorf("after the grace period: done %v, err %v, deletes %d, scans %d -> %d", done, err, h.deletes, scans, m.calls[TaskCharges])
	}
	var revoke string
	for _, ev := range st.events {
		if ev.Kind == RemovalDone {
			revoke = ev.Why
		}
	}
	if revoke != "revoke these keys at the supplier: 7" {
		t.Errorf("the end event says %q", revoke)
	}
}

// TestRemovalGoesOnWhenTheAccountIsRejected: a supplier that no longer lets us in has no records to give; the removal
// notes it and continues instead of waiting for ever.
func TestRemovalGoesOnWhenTheAccountIsRejected(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.charges = func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		return adapter.ChargePage{}, nil, adapter.ErrAuthRejected
	}
	e, st, clk := engine(m)
	removeAll(t, e, clk, &hooks{})
	if st.state.Status != Removed {
		t.Errorf("status %v", st.state.Status)
	}
}
