package suppliers

import (
	"context"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
)

// TestRoutableIsWhatListRoutableHolds: the endpoints a routing snapshot is built from are the ones the call side reads,
// with the name a call sends upstream, the formats a call may use, the schedule in force and what the supplier does with
// a stream. An endpoint a person switched off, and one whose cost is not known, are in neither of the two.
func TestRoutableIsWhatListRoutableHolds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	// A second endpoint of the group, which a person switched off, and a third whose cost we do not know.
	off, unknown := uuid.NewV7(), uuid.NewV7()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		if err := w.addEndpoint(ctx, tx, off, "gpt-off", true); err != nil {
			return err
		}
		return w.addEndpoint(ctx, tx, unknown, "gpt-untouched", false)
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}

	var listed []store.ListRoutableRow
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		var err error
		listed, err = store.New().ListRoutable(ctx, tx)
		return err
	})

	got := w.svc.Routable()
	if len(got) != 1 || len(listed) != 1 {
		t.Fatalf("Routable has %d endpoints and ListRoutable %d, want one of each", len(got), len(listed))
	}
	sameRoutable(t, got[0], listed[0], w.endpoint)
	for _, id := range []uuid.UUID{off, unknown} {
		if slices.ContainsFunc(got, func(r Routable) bool { return r.ID == id }) {
			t.Errorf("Routable holds endpoint %s, which no call may be routed to", id)
		}
	}
}

// TestOnlyAFundedSupplierIsRoutable: a supplier whose balance was never read, or is zero or overdrawn, is out of the
// routing snapshot; one above zero is in.
func TestOnlyAFundedSupplierIsRoutable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	for _, c := range []struct {
		balance string
		routes  bool
	}{{"NULL", false}, {"0", false}, {"-13.5", false}, {"0.000001", true}, {"100", true}} {
		w.tx(func(ctx context.Context, tx pg.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET balance_native = "+c.balance+" WHERE id = $1", w.supplier)
			return err
		})
		if err := w.svc.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
		if routes := len(w.svc.Routable()) == 1; routes != c.routes {
			t.Errorf("balance %s: routable %v, want %v", c.balance, routes, c.routes)
		}
	}
}

// sameRoutable compares what the service hands a routing snapshot with the row the call side reads, field by field: the
// two are one endpoint or the snapshot would route to something the call side cannot send.
func sameRoutable(t *testing.T, r Routable, row store.ListRoutableRow, endpoint uuid.UUID) {
	t.Helper()
	if r.ID != row.EndpointID || r.ID != endpoint {
		t.Errorf("Routable names endpoint %s and ListRoutable %s, want the world's %s", r.ID, row.EndpointID, endpoint)
	}
	if r.Supplier != row.SupplierID || r.Model != row.ModelID {
		t.Errorf("Routable's supplier and model %s/%s, ListRoutable's %s/%s", r.Supplier, r.Model, row.SupplierID, row.ModelID)
	}
	if r.Upstream != row.UpstreamName || r.Upstream != "gpt" {
		t.Errorf("the upstream name is %q, and %q in the query, want gpt", r.Upstream, row.UpstreamName)
	}
	if want := []protocol.Format{protocol.Chat}; !slices.Equal(r.Formats, want) {
		t.Errorf("the formats are %v, want %v", r.Formats, want)
	}
	if got := scheduleText(r.Schedule); got != scheduleJSON {
		t.Errorf("the schedule is %s, want %s", got, scheduleJSON)
	}
	if !r.Streams || !r.Cancels {
		t.Errorf("streams %v, cancels %v, want both: that is what the adapter says of this supplier", r.Streams, r.Cancels)
	}
}

// scheduleText is a schedule's canonical JSON, for an error message.
func scheduleText(s catalog.Schedule) string {
	b, err := s.MarshalJSON()
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// addEndpoint gives the world's group another endpoint, named upstream. A switched-off one still has the cost schedule it
// was priced by; the other has none, so its cost is unknown. Neither may be routed to.
func (w *world) addEndpoint(ctx context.Context, tx pg.Tx, id uuid.UUID, upstream string, switchedOff bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO suppliers.endpoints
		(id, supplier_id, supplier_group_id, model_id, upstream_name, formats, enabled)
		VALUES ($1, $2, $3, $4, $5, ARRAY['chat'], $6)`,
		id, w.supplier, w.group, uuid.NewV7(), upstream, !switchedOff)
	if err != nil || !switchedOff {
		return err
	}
	sc, err := store.New().InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
		SupplierID: w.supplier, EndpointID: id, NativeUnit: "quota", Schedule: []byte(scheduleJSON), Basis: "published",
		Fingerprint: upstream,
	})
	if err != nil {
		return err
	}
	return store.New().SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: id, CostScheduleID: &sc})
}
