package billing

import (
	"fmt"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/telemetry/telemetrytest"
)

func TestChargeReadFailureIsIsolatedAndReported(t *testing.T) {
	t.Parallel()

	for _, mutation := range []struct{ name, sql string }{
		{"future schedule", `UPDATE billing.reservations SET prices[1]=(prices[1]::jsonb || '{"future_condition":true}'::jsonb)::text WHERE request_id='%s'`},
		{"future usage", `UPDATE billing.reservations SET final_usage='[{"future_usage":true}]'::jsonb WHERE request_id='%s'`},
		{"price mismatch", `UPDATE billing.reservations SET price=price+1 WHERE request_id='%s'`},
	} {
		t.Run(mutation.name, func(t *testing.T) { checkChargeReadFailure(t, mutation.sql) })
	}
}

func checkChargeReadFailure(t *testing.T, mutation string) {
	t.Helper()
	b := newBank(t, 10000, 0, Config{})
	good, bad := b.hold(100), b.hold(100)
	for _, h := range []*Hold{good, bad} {
		b.settleWith(h, outcome(Committed, FinishStop, used(t, 3, 2, 0)))
	}
	b.sql(fmt.Sprintf(mutation, bad.id.String()))
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tel.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	logs := new(telemetrytest.Buffer)
	reader := NewChargeReader(logs.Logger(), tel.Metrics.Meter("billing/test"))
	var got map[uuid.UUID]ChargeDetails
	err = b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		got, err = reader.ReadCharges(t.Context(), tx, []uuid.UUID{bad.id, good.id})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[good.id]; !ok || len(got) != 1 {
		t.Fatalf("healthy details lost or bad details returned: %+v", got)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), bad.id.String()) {
		t.Errorf("failure not logged by request ID: %s", logs.String())
	}
	if got := telemetrytest.Metric(t, tel, "axiom_billing_charge_details_unavailable_total"); got != "1" {
		t.Errorf("failed reads: %s", got)
	}
}

// TestSettledFinalsReadsWhatSettlementStored: the call log completes a record written before its settlement from what
// the settlement stored, in the platform scope: the charge, the waiver, the price version and the usage, as Settle
// returned them. A call not settled is absent, and so is one whose stored usage this build cannot read, which is
// logged and counted rather than failing the read: the call log's writer retries a failed read forever.
func TestSettledFinalsReadsWhatSettlementStored(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10000, 0, Config{})
	good, unreadable, unsettled := b.hold(100), b.hold(100), b.hold(100)
	want := b.settleWith(good, outcome(Committed, FinishStop, used(t, 3, 2, 0)))
	b.settleWith(unreadable, outcome(Committed, FinishStop, used(t, 3, 2, 0)))
	b.sql(fmt.Sprintf(`UPDATE billing.reservations SET final_usage='[{"future_usage":true}]'::jsonb WHERE request_id='%s'`, unreadable.id))
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tel.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	logs := new(telemetrytest.Buffer)
	reader := NewChargeReader(logs.Logger(), tel.Metrics.Meter("billing/test"))
	var got map[uuid.UUID]Final
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
		got, err = reader.SettledFinals(t.Context(), tx, []uuid.UUID{good.id, unreadable.id, unsettled.id})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f, ok := got[good.id]
	if !ok || len(got) != 1 {
		t.Fatalf("settled finals %+v, want only %s", got, good.id)
	}
	if !sameFinal(f, want) {
		t.Errorf("settled final %+v, want what Settle returned, %+v", f, want)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), unreadable.id.String()) {
		t.Errorf("unreadable usage not logged by request ID: %s", logs.String())
	}
	if got := telemetrytest.Metric(t, tel, "axiom_billing_charge_details_unavailable_total"); got != "1" {
		t.Errorf("failed reads: %s", got)
	}
}

// sameFinal compares what the call log keeps of a Final: the charge, the waiver, the price version and the usage.
func sameFinal(a, b Final) bool {
	if a.Charged.Cmp(b.Charged) != 0 || a.Waived != b.Waived || (a.PriceVersion == nil) != (b.PriceVersion == nil) ||
		a.PriceVersion != nil && *a.PriceVersion != *b.PriceVersion || len(a.Segments) != len(b.Segments) {
		return false
	}
	for i := range a.Segments {
		for it := range catalog.NumItems {
			if a.Segments[i].Usage.Count(it) != b.Segments[i].Usage.Count(it) {
				return false
			}
		}
	}
	return true
}
