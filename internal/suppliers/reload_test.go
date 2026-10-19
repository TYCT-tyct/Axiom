package suppliers

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// routable is the snapshot's endpoint ids, failing the test on an error.
func (w *world) routable() []Routable {
	w.t.Helper()
	got, err := w.svc.Routable()
	if err != nil {
		w.t.Fatalf("Routable: %v", err)
	}
	return got
}

// addSupplier seeds a second supplier, with its own group, credential and endpoint, beside the world's, and returns its
// supplier and credential ids. It is not in the snapshot until the next Reload.
func (w *world) addSupplier() (supplier, credential uuid.UUID) {
	w.t.Helper()
	first := *w
	w.second, w.credential = true, uuid.NewV7()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return w.seed(ctx, tx, 1) })
	supplier, credential = w.supplier, w.credential
	w.supplier, w.group, w.credential, w.endpoint, w.sc, w.second = first.supplier, first.group, first.credential, first.endpoint, first.sc, false
	return supplier, credential
}

// failedReload is a Reload that cannot read the database, as one cut off from it is.
func (w *world) failedReload() {
	w.t.Helper()
	ctx, cancel := context.WithCancel(w.t.Context())
	cancel()
	if err := w.svc.Reload(ctx); err == nil {
		w.t.Fatal("a Reload with its context ended succeeded")
	}
}

// TestAnUnloadableSupplierIsLeftOutAndTheRestStillRoute: a credential whose key does not open (after a key rotation, a
// key version this node does not hold) takes only its own supplier out of the snapshot, and the supplier is set to
// needs_attention with a reason, here and for every other node; the Reload that found it still succeeds.
func TestAnUnloadableSupplierIsLeftOutAndTheRestStillRoute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	bad, credential := w.addSupplier()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET key_version = 2 WHERE id = $1", credential)
		return err
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatalf("Reload = %v, want the other supplier loaded and no error", err)
	}
	got := w.routable()
	if len(got) != 1 || got[0].ID != w.endpoint || got[0].Supplier != w.supplier {
		t.Errorf("Routable = %v, want only the first supplier's endpoint %s", got, w.endpoint)
	}
	same(t, "the supplier left out is flagged", 1,
		w.count("SELECT count(*) FROM suppliers.suppliers WHERE id = $1 AND status = 'needs_attention' AND status_reason = $2 AND config_gen = 1", bad, unloadableReason))
	same(t, "the other supplier is untouched", 1,
		w.count("SELECT count(*) FROM suppliers.suppliers WHERE id = $1 AND status = 'active' AND config_gen = 0", w.supplier))
	if err := w.svc.Reload(t.Context()); err != nil || len(w.routable()) != 1 {
		t.Errorf("the next Reload = %v with %d endpoints, want it to succeed with the one", err, len(w.routable()))
	}
}

// TestASnapshotNoReloadConfirmsGoesStale: while Reload keeps failing the snapshot is used for MaxStale and then refused,
// and a Reload that succeeds again is believed at once.
func TestASnapshotNoReloadConfirmsGoesStale(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.clock.advance(w.svc.cfg.MaxStale)
	w.failedReload()
	if got := w.routable(); len(got) != 1 {
		t.Fatalf("%d endpoints at the limit, want the snapshot's 1", len(got))
	}
	w.clock.advance(time.Nanosecond)
	w.failedReload()
	if _, err := w.svc.Routable(); !errors.Is(err, ErrStale) {
		t.Fatalf("Routable = %v after the limit, want ErrStale", err)
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.routable(); len(got) != 1 {
		t.Errorf("%d endpoints after a Reload, want 1", len(got))
	}
}

// TestASupplierSwitchedOffWhileReloadFailsIsNotRoutedForLong: the Reload that would drop it keeps failing for another
// reason, so the node routes to it for at most MaxStale and then not at all, rather than for as long as the failure lasts.
func TestASupplierSwitchedOffWhileReloadFailsIsNotRoutedForLong(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET status = 'paused' WHERE id = $1", w.supplier)
		return err
	})
	w.failedReload()
	w.clock.advance(w.svc.cfg.MaxStale + time.Second)
	w.failedReload()
	if got, err := w.svc.Routable(); !errors.Is(err, ErrStale) || len(got) != 0 {
		t.Errorf("Routable = %v, %v, want no endpoint and ErrStale", got, err)
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.routable(); len(got) != 0 {
		t.Errorf("%d endpoints once Reload works, want the paused supplier's gone", len(got))
	}
}
