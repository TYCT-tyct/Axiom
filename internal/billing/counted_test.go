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
		h, err = b.billing.reserveOne(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: text, Counts: counts}, []Quotable{q})
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

// chargeOf is the named item of the call's charge details.
func (b *bank) chargeOf(id uuid.UUID, item string) (ChargeItem, bool) {
	b.t.Helper()
	for _, it := range b.charges(id).Items {
		if it.Item == item {
			return it, true
		}
	}
	return ChargeItem{}, false
}

// wantCharge is what one item of a call's charge details must hold: its count, the unit it is billed in and its unit
// price.
type wantCharge struct {
	count       int64
	unit, price string
}

// checkCharges checks the items are exactly one per wanted name: each count, unit and price, and none estimated. A
// repeated name is a failure too, which the second length catches.
func checkCharges(t *testing.T, items []ChargeItem, want map[string]wantCharge) {
	t.Helper()
	got := make(map[string]ChargeItem, len(items))
	for _, item := range items {
		got[item.Item] = item
	}
	if len(items) != len(want) || len(got) != len(want) {
		t.Fatalf("charge items %+v, want %v", items, want)
	}
	for name, w := range want {
		if g := got[name]; g.Quantity != w.count || g.Unit != w.unit || g.UnitPrice != w.price || g.Estimated {
			t.Errorf("charge item %s = %+v, want %+v", name, g, w)
		}
	}
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

// TestCountedSettlement: a settled call charges each piece, second and character at the locked price, and its charge
// details name the unit and add up to the charge.
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
	checkCharges(t, details.Items, map[string]wantCharge{
		"input": {1000, "token", "0.000000001"}, "output": {10, "token", "0.000000001"},
		"image_out_piece": {2, "piece", "0.5"}, "audio_in_second": {30, "second", "0.002"},
		"character_in": {500, "character", "0.0001"},
	})
	if details.Subtotal != "1.11000101" || details.Charged != "1.11000101" || details.Rounding != "0" {
		t.Errorf("details subtotal %q rounding %q charged %q", details.Subtotal, details.Rounding, details.Charged)
	}
	b.balanced()
}

// TestCountedRelease: a call that fails before commit releases the whole reservation, pieces and all.
func TestCountedRelease(t *testing.T) {
	t.Parallel()
	b := newBank(t, 10_000_000_000, 0, Config{})
	h, err := b.tryCountedHold(counted(t), 1000, countedDemand().Counts)
	if err != nil {
		t.Fatal(err)
	}
	f := b.settleWith(h, outcome(FailedBeforeCommit, FinishError, reported(t, map[catalog.Item]int64{
		catalog.ItemImageOutPiece: 2, catalog.ItemAudioInSecond: 30, catalog.ItemCharacterIn: 500,
	})))
	if f.Charged != (platform.Exact{}) || !f.Waived {
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
	if second, ok := b.chargeOf(h.id, "audio_in_second"); !ok || second.Quantity != 30 || second.Unit != "second" ||
		second.UnitPrice != "0.004" || !second.Estimated {
		t.Errorf("the audio charge = %+v, %v; want 30 seconds at 0.004 estimated", second, ok)
	}
	b.balanced()
}

// TestCountedExplicitZeroPrice: a unit priced at an explicit zero is priced, so an item of that unit the schedule leaves
// out is charged at that zero instead of at the token price. Highest's missing cannot tell this: with only the standard
// tier in the schedule, it is true even when the unit has a zero price.
func TestCountedExplicitZeroPrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, schedule string
		text           int64
		usage          map[catalog.Item]int64
		want           platform.Exact
	}{
		{
			"a zero in the base prices",
			`{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001","audio_out_second":"0"}}`,
			10,
			map[catalog.Item]int64{catalog.ItemOutput: 10, catalog.ItemAudioInSecond: 30},
			platform.AmountExact(10),
		},
		{
			"a zero only in a later band",
			`{"v":1,"currency":"USD","rates":{"input":"0.001","output":"0.001"},"context_tiers":{"basis":["input"],
				"method":"whole","tiers":[{"above":1000,"rates":{"audio_in_second":"0"}}]}}`,
			10,
			map[catalog.Item]int64{catalog.ItemInput: 10, catalog.ItemAudioInSecond: 30},
			platform.AmountExact(10_000_000),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			settleZeroPricedUnit(t, tc.schedule, tc.text, tc.usage, tc.want)
		})
	}
}

// settleZeroPricedUnit settles one call whose schedule prices a unit at zero, and checks the unpriced item of that unit
// is charged at that zero and estimated.
func settleZeroPricedUnit(t *testing.T, schedule string, text int64, usage map[catalog.Item]int64, want platform.Exact) {
	t.Helper()
	b := newBank(t, 10_000_000_000, 0, Config{})
	q := Quotable{Price: mustSchedule(t, schedule), Model: Model{ContextLength: 4096}, MaxOutput: 100, Endpoint: uuid.NewV7()}
	h, err := b.tryCountedHold(q, text, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := b.settleWith(h, outcome(Committed, FinishNone, reported(t, usage)))
	if f.Charged != want || !f.Estimated {
		t.Fatalf("Settle = %+v, want %v estimated", f, want)
	}
	if second, ok := b.chargeOf(h.id, "audio_in_second"); !ok || second.Quantity != 30 || second.Unit != "second" ||
		second.UnitPrice != "0" || second.Amount != "0" || !second.Estimated {
		t.Errorf("the audio charge = %+v, %v; want 30 seconds at 0 estimated", second, ok)
	}
	b.balanced()
}

// countedOnly is one rate a speech or transcription model charges for: its schedule as the dataset parser writes it, the
// items it may report, and what a request stating a count of the priced item reserves, settles and shows.
type countedOnly struct {
	name, schedule, unit string
	reports              catalog.ItemSet
	key                  catalog.Item
	count                int64
	want                 platform.Exact
	subtotal             string
}

// TestCountedOnlyQuote: the schedules the dataset parser gives speech and transcription models price a counted unit and
// no tokens (catalog.TestParseLiteLLM pins their exact text). A request that declares only that unit quotes and settles
// without token prices.
func TestCountedOnlyQuote(t *testing.T) {
	t.Parallel()
	for _, tc := range []countedOnly{
		{
			"a character read aloud", `{"currency":"USD","rates":{"character_in":"0.000015"},"v":1}`, "character",
			1 << catalog.ItemCharacterIn, catalog.ItemCharacterIn, 500, platform.AmountExact(7_500_000), "0.0075",
		},
		{
			"a second transcribed", `{"currency":"USD","rates":{"audio_in_second":"0.0001"},"v":1}`, "second",
			1 << catalog.ItemAudioInSecond, catalog.ItemAudioInSecond, 30, platform.AmountExact(3_000_000), "0.003",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			quoteAndSettleCountedOnly(t, tc)
		})
	}
}

// quoteAndSettleCountedOnly quotes and settles a request stating the rate's count on a model that may report only that
// item, and checks the reservation, the charge and its one item.
func quoteAndSettleCountedOnly(t *testing.T, tc countedOnly) {
	t.Helper()
	q := Quotable{Price: mustSchedule(t, tc.schedule), Model: Model{ContextLength: 4096, Reports: tc.reports}, Endpoint: uuid.NewV7()}
	counts := map[catalog.Item]int64{tc.key: tc.count}
	res, err := Quote(Demand{Counts: counts}, []Quotable{q}, Segments{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Amount != tc.want {
		t.Errorf("reserved %v, want %v", res.Amount, tc.want)
	}
	b := newBank(t, 10_000_000_000, 0, Config{})
	h, err := b.tryCountedHold(q, 0, counts)
	if err != nil {
		t.Fatal(err)
	}
	if f := b.settleWith(h, outcome(Committed, FinishNone, reported(t, counts))); f.Charged != tc.want ||
		f.Estimated || f.Waived {
		t.Fatalf("Settle = %+v, want %v not estimated, not waived", f, tc.want)
	}
	details := b.charges(h.id)
	if len(details.Items) != 1 || details.Subtotal != tc.subtotal {
		t.Fatalf("charge details %+v, subtotal %q, want one item and %s", details.Items, details.Subtotal, tc.subtotal)
	}
	if item := details.Items[0]; item.Quantity != tc.count || item.Unit != tc.unit || item.Estimated {
		t.Errorf("charge item %+v, want unit %s and not estimated", item, tc.unit)
	}
	b.balanced()
}

// TestCountedOnlyNeedsTokenPrices: a model that may report any token item is refused without its token prices, however
// its counted units are priced.
func TestCountedOnlyNeedsTokenPrices(t *testing.T) {
	t.Parallel()
	text := Quotable{
		Price:    mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.001"}}`),
		Model:    Model{ContextLength: 4096, Reports: 1<<catalog.ItemInput | 1<<catalog.ItemOutput},
		Endpoint: uuid.NewV7(), MaxOutput: 10,
	}
	if _, err := Quote(Demand{TextBytes: 10}, []Quotable{text}, Segments{}, nil); !errors.Is(err, ErrUnpriced) {
		t.Errorf("a text model without an output price: %v, want ErrUnpriced", err)
	}
}

// tryDemand reserves a call of q with the demand as given.
func (b *bank) tryDemand(q Quotable, d Demand) (*Hold, error) {
	b.t.Helper()
	var h *Hold
	err := b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var err error
		h, err = b.billing.reserveOne(b.t.Context(), tx, b.admitted, uuid.NewV7(), d, []Quotable{q})
		return err
	})
	return h, err
}

// TestRequestPrice: a schedule that prices the per-request item charges one request whatever its size, and its charge
// details name the use. A request count of zero, as a probe's estimate, charges no request.
func TestRequestPrice(t *testing.T) {
	t.Parallel()
	q := Quotable{
		Price: mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0","request":"1.5"}}`),
		Model: Model{ContextLength: 1000}, MaxOutput: 10, Endpoint: uuid.NewV7(),
	}
	if res, err := Quote(Demand{TextBytes: 10, Requests: 0}, []Quotable{q}, Segments{}, nil); err != nil || res.Amount.CeilAmount() != 0 {
		t.Errorf("no request: %v, %v; want nothing reserved", res.Amount.CeilAmount(), err)
	}
	b := newBank(t, 10_000_000_000, 0, Config{})
	h, err := b.tryDemand(q, Demand{TextBytes: 10, Requests: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Amount.CeilAmount(); got != 1_500_000_000 {
		t.Fatalf("reserved %d, want 1500000000", got)
	}
	f := b.settleWith(h, outcome(Committed, FinishStop, reported(t, map[catalog.Item]int64{catalog.ItemOutput: 10})))
	if want := platform.AmountExact(1_500_000_000); f.Charged != want || f.Estimated || f.Waived {
		t.Fatalf("Settle = %+v, want %v, not estimated, not waived", f, want)
	}
	checkCharges(t, b.charges(h.id).Items, map[string]wantCharge{
		"request": {1, "use", "1.5"}, "output": {10, "token", "0"},
	})
	b.balanced()
}

// TestRequestPriceOfAnotherServiceTier: a request price on a tier the request may not be served at is still what the
// request pays, in the quote and in settlement, rather than the price of another per-use item.
func TestRequestPriceOfAnotherServiceTier(t *testing.T) {
	t.Parallel()
	// A request price only on standard, with complete token prices on priority, must still be admitted for a priority
	// request. Admission reserves the same highest request price settlement charges, instead of rejecting a price table
	// the listing gate accepted.
	mixed := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","request":"1"},
		"service_tiers":{"priority":{"rates":{"input":"0.000002","output":"0.000004"}}}}`)
	res, err := Quote(Demand{TextBytes: 10, Requests: 1, ServiceTiers: 1 << catalog.ServiceTierPriority},
		[]Quotable{{Price: mixed, Model: Model{ContextLength: 1000}, MaxOutput: 10, Endpoint: uuid.NewV7()}}, Segments{}, nil)
	if err != nil {
		t.Fatalf("a priority request with a standard-only request price: %v", err)
	}
	if got := res.Amount.CeilAmount(); got < 1_000_000_000 {
		t.Errorf("reserved %d, want at least the request price", got)
	}

	// The served tier has no request price, but the schedule has one on another tier. The request is charged at the
	// highest request price, not at the price of another per-use item.
	only := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0","request":"2","web_search":"0.01"}}`)
	seg := chargeTestSegment(t, map[catalog.Item]int64{catalog.ItemOutput: 10}, catalog.ServiceTierPriority, nil, nil)
	priced, err := priceSegments(only, fallbacks{perUse: map[catalog.Item]platform.UnitPrice{
		catalog.ItemWebSearch: mustUnitPrice("0.01"), catalog.ItemCodeExecution: mustUnitPrice("0.02"),
	}}, []Segment{seg}, false, 1<<catalog.ServiceTierStandard)
	if err != nil {
		t.Fatal(err)
	}
	if priced.price != 2_000_000_000 || !priced.estimated {
		t.Errorf("an undeclared tier's request = %d, estimated %v; want 2000000000, estimated", priced.price, priced.estimated)
	}
}
