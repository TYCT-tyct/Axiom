package billing

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/suppliers"
)

// TestCorrectionRePricesAnEstimatedCharge: an estimated settlement is reversed and settled again from the supplier's
// complete usage, at the schedule and tier the request locked, and the account gets the difference back.
// usageOf is a standard-tier usage of the given counts, valid by construction.
func usageOf(t *testing.T, counts map[catalog.Item]int64) catalog.Usage {
	t.Helper()
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCorrectionRePricesAnEstimatedCharge(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in, catalog.ItemOutput, webSearch)))
	if !f.Estimated || f.Charged != platform.AmountExact(100) {
		t.Fatalf("estimate = %+v; want estimated 100", f)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1<<in | 1<<catalog.ItemOutput | 1<<webSearch,
	}
	c := &corrector{db: b.app, scope: b.platform}
	borne, err := c.correct(t.Context(), ev)
	if err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	assertCorrectedCharge(t, b, h.id)
	if _, err := c.correct(t.Context(), ev); !errors.Is(err, errNoCorrection) {
		t.Fatalf("a second delivery = %v; want no correction", err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 950 {
		t.Errorf("balance %d after a repeated event, want 950", got)
	}
	b.balanced()
	b.replayed(b.admitted.Account)
}

// assertCorrectedCharge checks what the correction left on the reservation, the ledger and the balance.
func assertCorrectedCharge(t *testing.T, b *bank, request uuid.UUID) {
	t.Helper()
	for _, c := range []struct {
		query string
		want  int64
	}{
		{"SELECT charged FROM billing.reservations WHERE request_id = '" + request.String() + "'", 50},
		{"SELECT price FROM billing.reservations WHERE request_id = '" + request.String() + "'", 50},
		{"SELECT estimated::int FROM billing.reservations WHERE request_id = '" + request.String() + "'", 0},
		{"SELECT count(*) FROM billing.transactions WHERE request_id = '" + request.String() + "' AND revision = 1 AND kind IN ('reverse', 'settle')", 2},
	} {
		if got := b.count(c.query); got != c.want {
			t.Errorf("%s = %d, want %d", c.query, got, c.want)
		}
	}
	if got := b.balanceOf(b.admitted.Account); got != 950 {
		t.Errorf("balance %d after the correction, want 950", got)
	}
}

// TestCorrectionRefusesAMissingItem: a supplier record that leaves out an item we had to estimate is not a complete
// count. Pricing the missing item as zero would refund it instead of correcting it, so the estimate stays.
func TestCorrectionRefusesAMissingItem(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 20, 0, in, catalog.ItemOutput)))
	if !f.Estimated {
		t.Fatalf("estimate = %+v; want estimated", f)
	}
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1 << in}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || !errors.Is(err, errNoCorrection) {
		t.Fatalf("retry %v, err %v; want no correction", retry, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 100 {
		t.Errorf("charged %d, want the original 100", got)
	}
}

// TestCorrectionRefusesUnknownUsage: a charge with an item the upstream did not name cannot be replaced from a
// supplier record that only names items; the old estimate stays.
func TestCorrectionRefusesUnknownUsage(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: 100}, catalog.ServiceTierStandard,
		[]catalog.UnknownUsage{{Field: "usage.new_thing_tokens", Value: "7"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, Segment{Usage: u, Reported: 1 << in})); err != nil {
		t.Fatal(err)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(),
		Revision: 1, Usage: u, Reported: 1 << in,
	}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || !errors.Is(err, errNoCorrection) {
		t.Fatalf("retry %v, err %v; want no correction", retry, err)
	}
}

// TestCorrectionWaitsForTheReservation: an event read before its settlement is committed is kept, not dropped, and is
// corrected once the reservation is there.
func TestCorrectionWaitsForTheReservation(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1 << in}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); !retry || err == nil {
		t.Fatalf("before settlement: retry %v, err %v; want retry", retry, err)
	}
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in))); err != nil {
		t.Fatal(err)
	}
	if retry, err := c.one(t.Context(), ev); retry || err != nil {
		t.Fatalf("after settlement: retry %v, err %v; want corrected", retry, err)
	}
}

// TestCorrectionRefusesAPartlyReportedCharge: a settlement with a reported item is already actual for it; correcting it
// from the supplier's counts would charge that item twice, so it is left alone.
func TestCorrectionRefusesAPartlyReportedCharge(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 100, 0, 0, catalog.ItemOutput, webSearch))); err != nil {
		t.Fatal(err)
	}
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1 << in}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || !errors.Is(err, errNoCorrection) {
		t.Fatalf("retry %v, err %v; want no correction", retry, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 100 {
		t.Errorf("charged %d, want the original 100", got)
	}
}

// TestCorrectionTrustsTheRecordNotTheAttemptsCostFlag: cost_flag describes how completely the attempt's own estimate
// covered its cost; it is no_usage when the stream broke before any usage was reported. The supplier's record can
// still be complete, and then it is exactly the record that must replace the estimate.
func TestCorrectionTrustsTheRecordNotTheAttemptsCostFlag(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in)))
	if !f.Estimated {
		t.Fatalf("estimate = %+v; want estimated", f)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostNoUsage.String(), Revision: 1,
		Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1 << in,
	}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || err != nil {
		t.Fatalf("retry %v, err %v; want corrected despite cost_flag=no_usage", retry, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 50 {
		t.Errorf("charged %d after the correction, want 50", got)
	}
}

// TestTestEventDoesNotWaitForAReservation: a probe has no tenant reservation, and must be discarded before the
// reservation lookup, not retained in the retry table forever.
func TestTestEventDoesNotWaitForAReservation(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: uuid.NewV7(), AttemptSeq: 1, Test: true, Revision: 1}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || !errors.Is(err, errNoCorrection) {
		t.Fatalf("retry %v, err %v; want discarded as a test event", retry, err)
	}
	if got := b.count("SELECT count(*) FROM billing.charge_corrections"); got != 0 {
		t.Errorf("%d retry rows for a test event, want none", got)
	}
}
