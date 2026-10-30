package billing

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// counted is a route entry priced in every unit: a token of input or output a billionth, a generated image half a
// credit, a second of audio input 0.002 and a character 0.0001.
func counted(tb testing.TB) Quotable {
	tb.Helper()
	return Quotable{
		Price: mustSchedule(tb, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001",
			"image_out_piece":"0.5","audio_in_second":"0.002","audio_out_second":"0.002","character_in":"0.0001"}}`),
		Model: Model{ContextLength: 1e12}, MaxOutput: 100, Endpoint: uuid.NewV7(),
	}
}

// countedDemand is 1000 bytes of prompt, two generated images, 30 seconds of audio and 500 characters.
func countedDemand() Demand {
	return Demand{TextBytes: 1000, Counts: map[catalog.Item]int64{
		catalog.ItemImageOutPiece: 2, catalog.ItemAudioInSecond: 30, catalog.ItemCharacterIn: 500,
	}}
}

// reported is one sampling of counts, each one reported, in the standard tier.
func reported(tb testing.TB, counts map[catalog.Item]int64) Segment {
	tb.Helper()
	var items catalog.ItemSet
	for it := range counts {
		items.Add(it)
	}
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return Segment{Usage: u, Reported: items}
}

// tryCountedHold reserves a call of q with the given counted demand in a transaction of its own.
func (b *bank) tryCountedHold(q Quotable, text int64, counts map[catalog.Item]int64) (h *Hold, err error) {
	err = b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: text, Counts: counts}, []Quotable{q})
		return err
	})
	return h, err
}

// charges reads one call's itemized charge.
func (b *bank) charges(id uuid.UUID) ChargeDetails {
	b.t.Helper()
	var details ChargeDetails
	err := b.app.ReadTx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		all, err := NewChargeReader(nil, nil).ReadCharges(b.t.Context(), tx, []uuid.UUID{id})
		details = all[id]
		return err
	})
	if err != nil {
		b.t.Fatal(err)
	}
	return details
}

// TestCountedQuote: every piece, second and character the request states is reserved at its price on top of the token
// bounds, a count of a token item is left to those bounds, and a counted item the schedule does not price refuses the
// entry before the upstream is called.
func TestCountedQuote(t *testing.T) {
	t.Parallel()
	d := countedDemand()
	res, err := Quote(d, []Quotable{counted(t)}, Segments{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 input and 100 output tokens at a billionth each, 2 × 0.5 + 30 × 0.002 + 500 × 0.0001.
	if got := res.Amount.CeilAmount(); got != 1_110_001_100 {
		t.Errorf("reserved %d, want 1110001100", got)
	}
	tokenCount := Demand{TextBytes: 1000, Counts: map[catalog.Item]int64{catalog.ItemInput: 1e9}}
	if res, err := Quote(tokenCount, []Quotable{counted(t)}, Segments{}, nil); err != nil || res.Amount.CeilAmount() != 1_100 {
		t.Errorf("a token count quoted %v, %v; want 1100", res.Amount.CeilAmount(), err)
	}
	unpriced := counted(t)
	unpriced.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"}}`)
	if _, err := Quote(d, []Quotable{unpriced}, Segments{}, nil); !errors.Is(err, ErrUnpriced) {
		t.Errorf("a counted item without a price: %v, want ErrUnpriced", err)
	}
}

// TestCountedSettlement: a settled call charges each piece, second and character at the locked price, its charge details
// name the unit and add up to the charge, and a call that fails before commit releases the whole reservation.
func TestCountedSettlement(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000_000_000, 0, Config{})
	h, err := b.tryCountedHold(counted(t), 1000, countedDemand().Counts)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Amount.CeilAmount(); got != 1_110_001_100 {
		t.Fatalf("reserved %d, want 1110001100", got)
	}
	f := b.settleWith(h, outcome(Committed, FinishStop, reported(t, map[catalog.Item]int64{
		in: 1000, catalog.ItemOutput: 10, catalog.ItemImageOutPiece: 2,
		catalog.ItemAudioInSecond: 30, catalog.ItemCharacterIn: 500,
	})))
	if want := platform.AmountExact(1_110_001_010); f.Charged != want || f.Estimated || f.Waived {
		t.Fatalf("Settle = %+v, want %v, not estimated, not waived", f, want)
	}
	details := b.charges(h.id)
	want := map[string]struct {
		count int64
		unit  string
		price string
	}{
		"input": {1000, "token", "0.000000001"}, "output": {10, "token", "0.000000001"},
		"image_out_piece": {2, "piece", "0.5"}, "audio_in_second": {30, "second", "0.002"},
		"character_in": {500, "character", "0.0001"},
	}
	if len(details.Items) != len(want) {
		t.Fatalf("charge items %+v, want %d", details.Items, len(want))
	}
	for _, item := range details.Items {
		w, ok := want[item.Item]
		if !ok || item.Quantity != w.count || item.Unit != w.unit || item.UnitPrice != w.price || item.Estimated {
			t.Errorf("charge item %+v, want %+v", item, w)
			continue
		}
		delete(want, item.Item)
	}
	if len(want) != 0 {
		t.Errorf("missing charge items %v", want)
	}
	if details.Subtotal != "1.11000101" || details.Charged != "1.11000101" || details.Rounding != "0" {
		t.Errorf("details subtotal %q rounding %q charged %q", details.Subtotal, details.Rounding, details.Charged)
	}
	b.balanced()

	// Nothing reached the client: the reservation is released, pieces and all.
	failed, err := b.tryCountedHold(counted(t), 1000, countedDemand().Counts)
	if err != nil {
		t.Fatal(err)
	}
	if f := b.settleWith(failed, outcome(FailedBeforeCommit, FinishError, reported(t, map[catalog.Item]int64{
		catalog.ItemImageOutPiece: 2, catalog.ItemAudioInSecond: 30, catalog.ItemCharacterIn: 500,
	}))); f.Charged != (platform.Exact{}) || !f.Waived {
		t.Errorf("a failed call = %+v, want nothing charged and waived", f)
	}
	b.balanced()
}

// TestCountedZeroOutput: a delivered reply whose pieces and seconds are reported is not a zero output, so it is charged
// even though its text output is zero; a reply that reported no output and delivered nothing is waived, pieces and all.
func TestCountedZeroOutput(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000_000_000, 0, Config{})
	counts := map[catalog.Item]int64{catalog.ItemImageOutPiece: 2, catalog.ItemAudioOutSecond: 12}
	h, err := b.tryCountedHold(counted(t), 0, counts)
	if err != nil {
		t.Fatal(err)
	}
	// The text output was reported as zero, but two images and twelve seconds of audio are output: not the zero-output
	// waiver, and 2 × 0.5 + 12 × 0.002 is charged.
	f := b.settleWith(h, outcome(Committed, FinishNone, reported(t, map[catalog.Item]int64{
		catalog.ItemOutput: 0, catalog.ItemImageOutPiece: 2, catalog.ItemAudioOutSecond: 12,
	})))
	if want := platform.AmountExact(1_024_000_000); f.Charged != want || f.Waived {
		t.Errorf("two images and twelve seconds = %+v, want %v charged and not waived", f, want)
	}
	h, err = b.tryCountedHold(counted(t), 0, counts)
	if err != nil {
		t.Fatal(err)
	}
	if f := b.settleWith(h, outcome(Committed, FinishNone, reported(t, map[catalog.Item]int64{
		catalog.ItemOutput: 0, catalog.ItemImageOutPiece: 0, catalog.ItemAudioOutSecond: 0,
	}))); f.Charged != (platform.Exact{}) || !f.Waived {
		t.Errorf("a reply with no output = %+v, want nothing charged and waived", f)
	}
	b.balanced()
}

// TestCountedMissingPrice: a second an upstream reports that the request did not state is charged at the dearest price
// the schedule has for a second, never at a token price, and the call is estimated.
func TestCountedMissingPrice(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000_000_000, 0, Config{})
	q := counted(t)
	q.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001",
		"audio_out_second":"0.004"}}`)
	h, err := b.tryCountedHold(q, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := b.settleWith(h, outcome(Committed, FinishStop, reported(t, map[catalog.Item]int64{
		in: 1000, catalog.ItemAudioInSecond: 30,
	})))
	// 30 × 0.004 + 1000 × 0.000000001
	if want := platform.AmountExact(120_001_000); f.Charged != want || !f.Estimated {
		t.Errorf("Settle = %+v, want %v estimated", f, want)
	}
	var second *ChargeItem
	details := b.charges(h.id)
	for i := range details.Items {
		if details.Items[i].Item == "audio_in_second" {
			second = &details.Items[i]
		}
	}
	if second == nil || second.Quantity != 30 || second.Unit != "second" || second.UnitPrice != "0.004" || !second.Estimated {
		t.Errorf("charge items %+v, want 30 seconds at 0.004 estimated", details.Items)
	}
	b.balanced()
}
