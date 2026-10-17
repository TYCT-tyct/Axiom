package newapi

import (
	"context"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/syncer"
	"axiom/internal/suppliers/internal/syncer/syncertest"
)

// chargesOnly lets the engine's other tasks succeed beside the charges, which are the real adapter's.
type chargesOnly struct{ adapter.Manager }

func (chargesOnly) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
}

func (chargesOnly) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{Done: true}, nil
}

func (chargesOnly) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return adapter.Page[adapter.KeyState]{Done: true}, nil
}

func (c chargesOnly) Rescan(since time.Time) adapter.ScanState {
	return c.Manager.(adapter.Rescanner).Rescan(since)
}

type noHooks struct{}

func (noHooks) InFlight(context.Context) (int, error) { return 0, nil }
func (noHooks) DeleteSecrets(context.Context) error   { return nil }

// checkedHooks runs check when the credentials are about to be deleted.
type checkedHooks struct {
	noHooks
	check func()
}

func (h checkedHooks) DeleteSecrets(context.Context) error { h.check(); return nil }

// TestScanProtocol: the module's sync starts a scan with no position and stores what comes back.
func TestScanProtocol(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	adaptertest.RunScan(t, (&Adapter{now: clk.now}).managerOf(t, newLogServer(clk, globalSpecs())))
}

// TestTheEngineDrivesTheAdapter: the real adapter runs under the real engine. The first round only marks the start;
// later the records of the window are stored once each; and a removal reads again from before the grace period, so a
// record that was created before the watermark but appeared after the pass is still stored.
func TestTheEngineDrivesTheAdapter(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	srv := newLogServer(clk, globalSpecs())
	m := (&Adapter{now: clk.now}).managerOf(t, srv)
	store := syncertest.New()
	e := syncer.New(store, chargesOnly{m}, syncer.Defaults(), clk.now)

	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv.fill(250, t0, t0.Add(30*time.Minute))
	clk.set(t0.Add(time.Hour))
	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.Charges) != 250 || store.State().ScanOpen {
		t.Fatalf("after the pass: %d charges (want 250), scan open %v", len(store.Charges), store.State().ScanOpen)
	}
	srv.add(t0.Add(40*time.Minute), "late") // created inside the pass's window, but shown only now
	if done, err := e.Remove(t.Context(), noHooks{}); done || err != nil {
		t.Fatalf("Remove = %v, %v; want it to wait out the grace period", done, err)
	}
	clk.set(t0.Add(time.Hour + 31*time.Minute))
	if done, err := e.Remove(t.Context(), noHooks{}); !done || err != nil {
		t.Fatalf("Remove = %v, %v", done, err)
	}
	if _, ok := store.Charges["late"]; !ok {
		t.Errorf("the record that appeared late was not read: %d charges", len(store.Charges))
	}
}

// TestRemovalReadsABacklogAcrossWindowsBeforeTheGracePeriod: new-api's pass covers one adaptive window, so with the
// watermark four hours behind a single pass ends long before the present. The removal goes on, pass after pass, until
// the adapter says the records are read to where it began; a record in the middle of the backlog, that neither the first
// pass nor the rescan from before the grace period covers, is stored before the credentials are deleted.
func TestRemovalReadsABacklogAcrossWindowsBeforeTheGracePeriod(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	srv := newLogServer(clk, globalSpecs())
	m := (&Adapter{now: clk.now}).managerOf(t, srv)
	store := syncertest.New()
	e := syncer.New(store, chargesOnly{m}, syncer.Defaults(), clk.now)
	if err := e.Round(t.Context()); err != nil { // the first scan marks the start: the watermark is t0
		t.Fatal(err)
	}
	srv.add(t0.Add(2*time.Hour), "in-the-gap") // after the first window, before the rescan from removal start less the grace period
	clk.set(t0.Add(4 * time.Hour))
	stored := false
	hooks := checkedHooks{check: func() { _, stored = store.Charges["in-the-gap"] }}
	for range 10 {
		if done, err := e.Remove(t.Context(), hooks); err != nil || done {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		clk.set(clk.now().Add(31 * time.Minute))
	}
	if !stored {
		t.Errorf("the credentials were deleted before the record in the backlog was read: %d charges", len(store.Charges))
	}
}
