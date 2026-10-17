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
	scanThrough(m, clk)
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
	scanThrough(m, clk)
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

// catchingUp is a supplier that reads a backlog an hour per pass and says how far it has read.
type catchingUp struct {
	*supplier
	read   time.Time
	passes int
}

// TestRemovalReadsUntilTheAdapterHasCaughtUp: one pass does not end at the present, so the first read of a removal goes
// on pass after pass until the adapter says it has read to where the removal began, and only then the grace period
// starts.
func TestRemovalReadsUntilTheAdapterHasCaughtUp(t *testing.T) {
	t.Parallel()
	c := &catchingUp{supplier: healthy(), read: t0}
	var clock *clock
	c.charges = func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		c.passes++
		c.read = c.read.Add(time.Hour)
		if c.read.After(clock.now()) {
			c.read = clock.now()
		}
		return adapter.ChargePage{Done: true, Through: c.read}, adapter.ScanState(`{"v":1}`), nil
	}
	e, st, clk := engine(c)
	clock = clk
	clk.advance(4 * time.Hour)
	if done, err := e.Remove(t.Context(), &hooks{}); done || err != nil || st.state.Removal != StepGrace {
		t.Fatalf("Remove = %v, %v at step %v; want it waiting in the grace period", done, err, st.state.Removal)
	}
	if c.passes != 4 || st.state.Through.Before(st.state.RemovalAt.Add(-2*time.Minute)) {
		t.Errorf("%d passes, read to %v for a removal at %v; want 4 passes to the start", c.passes, st.state.Through, st.state.RemovalAt)
	}
}

// TestRemovalDoesNotTakeAPageWithoutThroughForCaughtUp: a Done page that does not say how far the records are read is
// not taken for a caught-up one. The removal keeps reading, starts no grace period and deletes nothing, and it goes on
// as soon as the adapter does say. The contract test requires the report, so this is what an adapter that never went
// through it looks like: a supplier that cannot be removed, rather than a backlog quietly taken for read (#109).
func TestRemovalDoesNotTakeAPageWithoutThroughForCaughtUp(t *testing.T) {
	t.Parallel()
	m := healthy() // its Done page leaves Through out
	e, st, clk := engine(m)
	h := &hooks{}
	for range 3 {
		if done, err := e.Remove(t.Context(), h); done || err != nil {
			t.Fatalf("Remove = %v, %v; want it to keep reading", done, err)
		}
		clk.advance(time.Minute)
	}
	if st.state.Removal != StepFinalScan || st.state.ScanOpen || h.deletes != 0 {
		t.Fatalf("step %v, scan open %v, deletes %d; want it still reading the records", st.state.Removal, st.state.ScanOpen, h.deletes)
	}
	scanThrough(m, clk)
	if done, err := e.Remove(t.Context(), h); done || err != nil || st.state.Removal != StepGrace {
		t.Fatalf("Remove = %v, %v at step %v; want it past the read once the adapter says", done, err, st.state.Removal)
	}
}

// TestRemovalAlertsOnceWhenCatchingUpTakesLong: a removal held up by ten hours of records, a minute of them read per
// pass, waits inside the bound without a word and then tells a person once; the removal reads on and finishes when the
// adapter has caught up.
func TestRemovalAlertsOnceWhenCatchingUpTakesLong(t *testing.T) {
	t.Parallel()
	m, read := healthy(), t0.Add(-10*time.Hour)
	m.charges = func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		read = read.Add(time.Minute) // the backlog, one pass and one page at a time
		return adapter.ChargePage{Done: true, Through: read}, adapter.ScanState(`{"v":1}`), nil
	}
	e, st, clk := engine(m)
	h := &hooks{}
	remove := func() (done bool) {
		t.Helper()
		done, err := e.Remove(t.Context(), h)
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
	for range 3 { // twenty minutes of catching up, the last call made at that point
		if remove() {
			t.Fatal("the removal ended with ten hours of records unread")
		}
		clk.advance(10 * time.Minute)
	}
	if st.count(RemovalSlow) != 0 {
		t.Errorf("the removal alerted %v into the read, inside the %v bound", clk.now().Sub(st.state.RemovalAt), e.set.CatchUpAlert)
	}
	for range 40 {
		if remove() {
			break
		}
		clk.advance(10 * time.Minute)
	}
	if st.state.Status != Removed || st.count(RemovalSlow) != 1 || !st.state.CatchUpNotified {
		t.Errorf("status %v, %d alerts, notified %v; want it removed after one alert", st.state.Status, st.count(RemovalSlow), st.state.CatchUpNotified)
	}
}
