package billing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/suppliers"
)

// segmentTokens is the output tokens a segment of the property bank's reservations covers.
const segmentTokens = 50

// correctionStats counts the corrections a property run applied and how their charged amount moved.
type correctionStats struct {
	applied, refunds, extras int
}

// TestLedgerProperty drives random sequences of credits, gifts and payments in the site's currency and another, some of
// them expiring, reservations, settlements with retries
// and crashes left to cleanup, some with usage only a fallback price covers, write-offs of expired credit, and
// corrections of estimated charges from a supplier's record, some of them refunds and some extra charges. After every
// step it checks that every transaction balances, the balance equals its book, the
// counters equal the pending holds, the floor holds, and the credit batches fund exactly the balance without any being
// over-spent.
func TestLedgerProperty(t *testing.T) {
	t.Parallel()

	const seeds = 3
	var corrected correctionStats
	var ran int
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			// Counted here, not from t.Run's result: a filtered subtest returns success without calling this function.
			ran++
			r := rand.New(rand.NewPCG(seed, ^seed)) //nolint:gosec // G404: test data
			b := fallbackBank(t, 5000, -500, Config{
				HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: 20 * time.Millisecond,
				Segments: NewSegmentPolicy(Segments{Output: segmentTokens, Overshoot: time.Second}),
			})
			var holds []*Hold
			for range 40 {
				b.step(r, &holds)
				b.balanced()
				b.replayed(b.admitted.Account)
			}
			t.Logf("corrections %+v", b.corrected)
			corrected.applied += b.corrected.applied
			corrected.refunds += b.corrected.refunds
			corrected.extras += b.corrected.extras
		})
	}
	// The cross-seed check only means something when every seed ran; a single filtered seed must still be runnable on its
	// own to reproduce a ledger failure. A run that corrected nothing, or only one way, has not shown that a
	// correction's refund and its extra charge both leave the books and the batches as they were.
	if ran == seeds && (corrected.refunds == 0 || corrected.extras == 0) {
		t.Errorf("corrections %+v, want applied corrections with at least one refund and one extra charge", corrected)
	}
}

func (b *bank) step(r *rand.Rand, holds *[]*Hold) {
	b.t.Helper()
	switch n := r.IntN(10); n {
	case 0:
		b.randomCredit(r, r.Int64N(2000)+1, []*time.Time{nil, longAgo, farAhead}[r.IntN(3)])
	case 6, 7:
		b.expire()
	case 1:
		b.tryRandomHold(r, holds)
	case 2, 3: // settle, or a retry of a settlement already made
		if len(*holds) == 0 { // nothing reserved yet to settle
			b.tryRandomHold(r, holds)
		} else {
			b.settleRandom(r, (*holds)[r.IntN(len(*holds))])
		}
	case 8: // reservations and settlements of one account at once, as its concurrent calls make them
		b.concurrent(r)
	case 9: // a reservation made in segments, extended once, then settled where a stream cut short stops
		b.truncated(r)
	default: // a crash: what was left pending goes to cleanup, which records the request as lost
		b.cleanup()
	}
}

// tryRandomHold reserves one more call at a random prompt and keeps it for a later step to settle. A balance too small is
// a state the property allows and later credits may lift; any other failure is not.
func (b *bank) tryRandomHold(r *rand.Rand, holds *[]*Hold) {
	b.t.Helper()
	if h, err := b.tryHold(r.Int64N(1500)); err == nil {
		*holds = append(*holds, h)
	} else if !errors.As(err, new(*Insufficient)) {
		b.t.Fatal(err)
	}
}

func (b *bank) settleRandom(r *rand.Rand, h *Hold) {
	b.t.Helper()
	// Every charged item is estimated, as a call that reported no usage of its own leaves them, so that a complete
	// supplier's record can replace the whole charge; partial records are covered by the refusal tests.
	results := []Result{Committed, Aggregated, FailedBeforeCommit, Unknown, NotExecuted}
	o := outcome(results[r.IntN(len(results))], FinishReason(r.IntN(9)), //nolint:gosec // G115: below 9
		maybeUnknown(b.t, r, used(b.t, r.Int64N(2000), r.Int64N(200), r.Int64N(3), in, catalog.ItemOutput, webSearch)))
	f, err := b.billing.Settle(b.t.Context(), h, o)
	if err != nil {
		b.t.Fatal(err)
	}
	if f.Estimated {
		b.correctRandom(r, h, o)
	}
}

// correctRandom corrects an estimated settlement from a complete supplier's record, as the correction job does. Every
// other applied correction states more tokens than the estimate and every other fewer, so a run covers both a refund
// and an extra charge; the floor that caps an extra charge has its own test.
func (b *bank) correctRandom(r *rand.Rand, h *Hold, o Outcome) {
	b.t.Helper()
	old := o.Attempts[0].Segments[0].Usage
	counts := map[catalog.Item]int64{in: old.Count(in), catalog.ItemOutput: old.Count(catalog.ItemOutput), webSearch: old.Count(webSearch)}
	for _, it := range []catalog.Item{in, catalog.ItemOutput, webSearch} {
		if b.corrected.applied%2 == 0 {
			counts[it] = max(counts[it]-r.Int64N(50)-1, 0)
		} else {
			counts[it] += r.Int64N(50) + 1
		}
	}
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	ev := suppliers.ChargeEvent{
		ID: uuid.NewV7(), Request: h.id, AttemptSeq: 1, CostFlag: suppliers.CostComplete.String(), Revision: 1,
		Usage: u, Reported: 1<<in | 1<<catalog.ItemOutput | 1<<webSearch,
	}
	before := b.chargedOf(h.id)
	c := &corrector{db: b.app, scope: b.platform}
	if _, err := c.correct(b.t.Context(), ev); err != nil {
		if !errors.Is(err, errNoCorrection) {
			b.t.Fatal(err)
		}
		return
	}
	b.corrected.applied++
	switch after := b.chargedOf(h.id); {
	case after < before:
		b.corrected.refunds++
	case after > before:
		b.corrected.extras++
	}
}

// chargedOf is what the reservation's current settlement actually charged, before or after a correction. The price is
// not enough: the floor can leave the charge unchanged while the price rises.
func (b *bank) chargedOf(request uuid.UUID) int64 {
	b.t.Helper()
	return b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + request.String() + "'")
}

// concurrent reserves and settles four calls of the account at once: each is a transaction of its own on the one
// balance row, and whatever the interleaving, the books must balance and the balance stay above its floor.
func (b *bank) concurrent(r *rand.Rand) {
	b.t.Helper()
	type call struct{ text, output int64 }
	calls := make([]call, 4)
	for i := range calls {
		calls[i] = call{text: r.Int64N(300), output: r.Int64N(100)}
	}
	errs := make(chan error, len(calls))
	var wg sync.WaitGroup
	for _, c := range calls {
		wg.Go(func() {
			h, err := b.tryHold(c.text)
			if err != nil {
				if !errors.As(err, new(*Insufficient)) {
					errs <- err
				}
				return
			}
			if _, err := b.billing.Settle(context.Background(), h, outcome(Committed, FinishStop, used(b.t, 0, c.output, 0))); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		b.t.Error(err)
	}
}

// truncated reserves by segment, then either approves one extension or has it refused for funds, as a call near its
// balance sees it, and settles where a stream cut short stops: inside what the approved segments cover, so the charge
// stays within what was reserved for it. The segment is 50 tokens and the model's speed 10 a second, so its second of
// overshoot holds 10 more: a first reservation of 60 and an extension to 110, which is what tells an extension that was
// written from one that was not, and leaves an output limit a real router would segment (billing's output limit here is
// the 200 tokens the route's entry would carry).
func (b *bank) truncated(r *rand.Rand) {
	b.t.Helper()
	q := metered(b.t)
	q.Segmentable, q.Model, q.MaxOutput = true, Model{ContextLength: 1e12, OutputSpeed: 10}, 200
	h := b.holdOn(q, 0)
	reserved := func() platform.Exact {
		return platform.AmountExact(b.count("SELECT amount FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"))
	}
	first := reserved()
	m := h.Bind(0)
	step := m.Observe(Progress{ContentBytes: 31}) // past the latest extension point of a segment of 50
	if !step.Extend || step.Exhausted {
		b.t.Fatalf("at 31 tokens of a segment of 50: %+v", step)
	}
	covered, want := int64(2*segmentTokens), step.Request.Target
	if r.IntN(3) == 0 {
		// One unit past what the balance leaves: the extension is refused, nothing is written, the meter keeps the first
		// segment, and the cut comes inside it. The target is the test's own because a balance that admitted the hold can
		// never refuse the meter's: an extension adds a segment to what is covered, less than the segment plus its
		// allowance the hold already reserved. That refusal by a short balance is TestExtend's.
		avail := b.count("SELECT balance - reserved - frozen - floor FROM billing.balances")
		err := b.billing.Extend(b.t.Context(), h, ExtendRequest{Seq: step.Request.Seq, Target: platform.AmountExact(first.CeilAmount() + avail + 1)})
		if !errors.As(err, new(*Insufficient)) {
			b.t.Fatalf("an extension of %d past the %d the balance leaves: %v", avail+1, avail, err)
		}
		m.Extended(step.Request.Seq, err)
		covered, want = segmentTokens, first
	} else {
		if err := b.billing.Extend(b.t.Context(), h, step.Request); err != nil {
			b.t.Fatal(err)
		}
		m.Extended(step.Request.Seq, nil)
	}
	if got := reserved(); got.Cmp(want) != 0 {
		b.t.Fatalf("the reservation is %v, want %v (first %v, target %v)", got, want, first, step.Request.Target)
	}
	// An approved extension moved the reservation past the first segment's; a refused one left it there, so settling
	// beyond the first reservation is only covered by the former, and settling inside it is the latter's whole bound.
	allowance, _ := (Segments{Output: segmentTokens, Overshoot: time.Second}).Allowance(10) // the bank's settings and the model's speed
	output := 1 + r.Int64N(segmentTokens)                                                   // inside the first segment
	if want.Cmp(first) > 0 {
		output = segmentTokens + allowance + 1 + r.Int64N(covered-segmentTokens-allowance) // past the first reservation
	}
	f, err := b.billing.Settle(b.t.Context(), h, outcome(Committed, FinishTruncated, used(b.t, 0, output, 0)))
	if err != nil {
		b.t.Fatal(err)
	}
	if want.Cmp(first) > 0 && f.Charged.Cmp(first) <= 0 {
		b.t.Errorf("a cut stream of %d tokens was charged %v of the %v its extension was needed for", output, f.Charged, first)
	}
	if f.Charged.Cmp(want) > 0 {
		b.t.Errorf("a cut stream was charged %v of %v reserved", f.Charged, want)
	}
}

// maybeUnknown adds, one time in three, usage protocol could not map: tokens or uses, to be priced by fallback.
func maybeUnknown(tb testing.TB, r *rand.Rand, s Segment) Segment {
	tb.Helper()
	if r.IntN(3) != 0 {
		return s
	}
	counts := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		counts[it] = s.Usage.Count(it)
	}
	field := []string{"usage.new_thing_tokens", "usage.server_tool_use.web_fetch_requests"}[r.IntN(2)]
	u, err := catalog.NewUsage(counts, s.Usage.ServiceTier(), []catalog.UnknownUsage{{Field: field, Value: fmt.Sprint(1 + r.IntN(40))}})
	if err != nil {
		tb.Fatal(err)
	}
	return Segment{Usage: u, Reported: s.Reported}
}
