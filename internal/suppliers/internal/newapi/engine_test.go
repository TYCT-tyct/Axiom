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

// TestScanProtocol: the module's sync starts a scan with no position and stores what comes back.
func TestScanProtocol(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	adaptertest.RunScan(t, (&Adapter{now: clk.now}).managerOf(t, newLogServer(clk, globalSpecs())))
}

// TestTheEngineDrivesTheAdapter: the real adapter runs under the real engine. The first round only marks the start;
// later the records of the window are stored once each.
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
}
