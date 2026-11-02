package billing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestReserveQuotesTheSegmentsTheRoutePlannedWith: routing marks entries from one read of the live policy; if the
// policy reloads before quoting, billing must use the same values, not a fresh read that no longer matches the flags.
func TestReserveQuotesTheSegmentsTheRoutePlannedWith(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e9, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour,
		Segments: NewSegmentPolicy(Segments{Output: 1000, Overshoot: 2 * time.Second})})
	frozen := b.billing.Segments().Load()
	segmented := b.exact
	segmented.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"}}`)
	segmented.Model = Model{ContextLength: 1e6, OutputSpeed: 100}
	segmented.MaxOutput, segmented.Segmentable = 2000, true
	want, err := Quote(Demand{TextBytes: 10}, []Quotable{segmented}, frozen, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A hot reload between planning and quoting must not turn the 2-second allowance into a 600-second one.
	changed := Segments{Output: 1000, Overshoot: 10 * time.Minute}
	b.billing.Segments().v.Store(&changed)
	changedQuote, err := Quote(Demand{TextBytes: 10}, []Quotable{segmented}, changed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedQuote.Amount.Cmp(want.Amount) <= 0 {
		t.Fatalf("the changed policy did not make the quote dearer: frozen %v changed %v", want.Amount, changedQuote.Amount)
	}
	var h *Hold
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		holds, _, errs, err := b.billing.ReserveMany(t.Context(), tx, []ReserveRequest{{
			Admitted: b.admitted, ID: uuid.NewV7(), Demand: Demand{TextBytes: 10}, Quotables: []Quotable{segmented}, Segments: frozen,
		}})
		if err == nil {
			h, err = holds[0], errs[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if h.Amount.Cmp(want.Amount) != 0 {
		t.Fatalf("reserved %v, want the route's frozen %v (the reloaded policy would be %v)", h.Amount, want.Amount, changedQuote.Amount)
	}
}
