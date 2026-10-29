package billing

import (
	"errors"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/suppliers"
	"uuid"
)

// TestCorrectionRePricesAnEstimatedCharge: an estimated settlement is reversed and settled again from the supplier's
// complete usage, at the schedule and tier the request locked, and the account gets the difference back.
func TestCorrectionRePricesAnEstimatedCharge(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in, catalog.ItemOutput, webSearch)))
	if !f.Estimated || f.Charged != platform.AmountExact(100) {
		t.Fatalf("estimate = %+v; want estimated 100", f)
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: 50}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: u, Reported: 1<<in | 1<<catalog.ItemOutput | 1<<webSearch,
	}
	c := &corrector{db: b.app, scope: b.platform}
	borne, err := c.correct(t.Context(), ev)
	if err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 50 {
		t.Errorf("charged %d after the correction, want 50", got)
	}
	if got := b.count("SELECT price FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 50 {
		t.Errorf("price %d after the correction, want 50", got)
	}
	if got := b.count("SELECT estimated::int FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 0 {
		t.Errorf("estimated %d after the correction, want 0", got)
	}
	if got := b.count("SELECT count(*) FROM billing.transactions WHERE request_id = '" + h.id.String() + "' AND revision = 1 AND kind IN ('reverse', 'settle')"); got != 2 {
		t.Errorf("%d correction transactions, want reverse and settle", got)
	}
	if got := b.balanceOf(b.admitted.Account); got != 950 {
		t.Errorf("balance %d after the correction, want 950", got)
	}
	if _, err := c.correct(t.Context(), ev); !errors.Is(err, errNoCorrection) {
		t.Fatalf("a second delivery = %v; want no correction", err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 950 {
		t.Errorf("balance %d after a repeated event, want 950", got)
	}
	b.balanced()
	b.replayed(b.admitted.Account)
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
	u, _ := catalog.NewUsage(map[catalog.Item]int64{in: 50}, catalog.ServiceTierStandard, nil)
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: u, Reported: 1 << in}
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
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(),
		Revision: 1, Usage: u, Reported: 1 << in}
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
	u, _ := catalog.NewUsage(map[catalog.Item]int64{in: 50}, catalog.ServiceTierStandard, nil)
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: u, Reported: 1 << in}
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
	u, _ := catalog.NewUsage(map[catalog.Item]int64{in: 50}, catalog.ServiceTierStandard, nil)
	ev := suppliers.ChargeEvent{ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1, Usage: u, Reported: 1 << in}
	c := &corrector{db: b.app, scope: b.platform}
	if retry, err := c.one(t.Context(), ev); retry || !errors.Is(err, errNoCorrection) {
		t.Fatalf("retry %v, err %v; want no correction", retry, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 100 {
		t.Errorf("charged %d, want the original 100", got)
	}
}
