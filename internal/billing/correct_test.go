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
	assertCorrection(t, b, h.id, 50)
	if _, err := c.correct(t.Context(), ev); !errors.Is(err, errNoCorrection) {
		t.Fatalf("a second delivery = %v; want no correction", err)
	}
	if got := b.balanceOf(b.admitted.Account); got != 950 {
		t.Errorf("balance %d after a repeated event, want 950", got)
	}
	b.balanced()
	b.replayed(b.admitted.Account)
}

// assertCorrection checks what a correction left on the reservation, the ledger and the account's balance, which starts
// at 1000.
func assertCorrection(t *testing.T, b *bank, request uuid.UUID, charged int64) {
	t.Helper()
	for _, c := range []struct {
		query string
		want  int64
	}{
		{"SELECT charged FROM billing.reservations WHERE request_id = '" + request.String() + "'", charged},
		{"SELECT price FROM billing.reservations WHERE request_id = '" + request.String() + "'", charged},
		{"SELECT estimated::int FROM billing.reservations WHERE request_id = '" + request.String() + "'", 0},
		{"SELECT count(*) FROM billing.transactions WHERE request_id = '" + request.String() + "' AND revision = 1 AND kind IN ('reverse', 'settle')", 2},
	} {
		if got := b.count(c.query); got != c.want {
			t.Errorf("%s = %d, want %d", c.query, got, c.want)
		}
	}
	if got, want := b.balanceOf(b.admitted.Account), 1000-charged; got != want {
		t.Errorf("balance %d after the correction, want %d", got, want)
	}
}

// TestCorrectionFromANewAPIRecord: new-api's consumption log states prompt_tokens, completion_tokens and an "other" JSON
// with cache_tokens and cache_creation_tokens, under the request id it returned in X-Oneapi-Request-Id (new-api
// model/log.go:57-79 and common/constants.go:194; our reader internal/suppliers/internal/newapi/charges.go:225, whose
// reported at :251 declares the input only when no cache token is named, and never the output because
// completion_tokens includes reasoning the log does not split). A settlement whose only charged item is the estimated
// input is therefore the one a new-api record corrects, and it replaces the estimate exactly.
func TestCorrectionFromANewAPIRecord(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in)))
	if !f.Estimated || f.Charged != platform.AmountExact(100) {
		t.Fatalf("estimate = %+v; want estimated 100", f)
	}
	// The record: prompt_tokens=50, completion_tokens=0, other={"billing_source":"wallet","cache_tokens":0,
	// "cache_creation_tokens":0}; read as the adapter does, it declares only the input item.
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: usageOf(t, map[catalog.Item]int64{in: 50}), Reported: 1 << in,
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), ev); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	assertCorrection(t, b, h.id, 50)
	b.balanced()
	b.replayed(b.admitted.Account)
}

// TestCorrectionFromASub2APIRecord: sub2api's /api/v1/usage record states input_tokens, output_tokens, cache_read_tokens,
// cache_creation_tokens and the 5m/1h writes, under the "client:"-prefixed id it echoes in X-Client-Request-Id
// (sub2api service/usage_service.go:20-40 and handler/usage_handler.go; our reader
// internal/suppliers/internal/sub2api/charges.go:191 and quirks.go:41). It declares every count field present, so a
// record corrects an estimated input and output together.
func TestCorrectionFromASub2APIRecord(t *testing.T) {
	b := newBank(t, 1000, 0, fallbackBankConfig)
	h := b.holdOn(metered(t), 100)
	f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 20, 0, in, catalog.ItemOutput)))
	if !f.Estimated || f.Charged != platform.AmountExact(120) {
		t.Fatalf("estimate = %+v; want estimated 120", f)
	}
	// The record: input_tokens=50, output_tokens=10, cache_read_tokens=0, cache_creation_tokens=0; every present field
	// is declared, so both charged items are corrected together.
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage:    usageOf(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 10}),
		Reported: 1<<in | 1<<catalog.ItemOutput,
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), ev); err != nil || borne != 0 {
		t.Fatalf("correct = %d borne, %v", borne, err)
	}
	assertCorrection(t, b, h.id, 60)
	b.balanced()
	b.replayed(b.admitted.Account)
}

// TestCorrectionIsBorneAtTheFloor: a record that prices above the estimate is charged up to the account's floor and not
// past it; the rest is the platform's, recorded on the books and alerted, so a correction cannot push the balance under
// its floor.
func TestCorrectionIsBorneAtTheFloor(t *testing.T) {
	b := newBank(t, 100, -50, fallbackBankConfig)
	h := b.holdOn(b.exact, 100)
	if f := b.settleWith(h, outcome(Committed, FinishStop, used(t, 100, 0, 0, in))); !f.Estimated || f.Charged != platform.AmountExact(100) {
		t.Fatalf("estimate = %+v; want estimated 100", f)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: usageOf(t, map[catalog.Item]int64{in: 400}), Reported: 1 << in,
	}
	c := &corrector{db: b.app, scope: b.platform}
	if borne, err := c.correct(t.Context(), ev); err != nil || borne != 250 {
		t.Fatalf("correct = %d borne, %v; want 250 borne", borne, err)
	}
	if got := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); got != 150 {
		t.Errorf("charged %d, want 150 up to the floor", got)
	}
	if got := b.balanceOf(b.admitted.Account); got != -50 {
		t.Errorf("balance %d, want the floor -50", got)
	}
	if got := b.borne(); got != 250 {
		t.Errorf("the platform bore %d, want 250", got)
	}
	if got := b.count("SELECT count(*) FROM platform.alert_signals WHERE body->>'kind' = 'billing.charge_correction_borne' AND body->'fields'->>'request' = '" + h.id.String() + "' AND body->'fields'->>'borne' = '250'"); got != 1 {
		t.Errorf("%d borne alerts, want 1", got)
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
