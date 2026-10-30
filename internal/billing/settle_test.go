package billing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// supplies is the Suppliers double: it keeps what billing records of the attempts, the requests it recorded as lost
// with their routes, and how often it was told a request ended.
type supplies struct {
	mu       sync.Mutex
	attempts map[uuid.UUID][]suppliers.AttemptRecord
	lost     map[uuid.UUID][]uuid.UUID // the route of each request recorded lost
	resolved map[uuid.UUID]bool        // the requests a late settlement resolved
	ended    map[uuid.UUID]int         // how many times each request was told to have ended
	stuck    chan struct{}             // when set, recording a lost request waits for it to close or the context to end
	failLost map[uuid.UUID]error       // the requests whose recording as lost fails, with the error
}

func newSupplies() *supplies {
	return &supplies{
		attempts: map[uuid.UUID][]suppliers.AttemptRecord{}, lost: map[uuid.UUID][]uuid.UUID{},
		resolved: map[uuid.UUID]bool{}, ended: map[uuid.UUID]int{},
	}
}

func (s *supplies) RecordAttempts(_ context.Context, _ pg.Tx, attempts []suppliers.AttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range attempts {
		s.attempts[uuid.UUID(a.Request)] = append(s.attempts[uuid.UUID(a.Request)], a)
	}
	return nil
}

func (s *supplies) RecordLostRequest(ctx context.Context, _ pg.Tx, id, _, _ uuid.UUID, route []uuid.UUID) error {
	if s.stuck != nil {
		select {
		case <-s.stuck:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failLost[id]; err != nil {
		return err
	}
	s.lost[id] = route
	return nil
}

func (s *supplies) ResolveLostRequest(_ context.Context, _ pg.Tx, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, lost := s.lost[id]; !lost {
		// The real one fails on a request that was never recorded as lost (suppliers' ResolveLostRequest writes no row
		// and reports it), so the double fails too, rather than hiding the bug.
		return fmt.Errorf("no lost request %s to resolve", id)
	}
	s.resolved[id] = true
	return nil
}

func (s *supplies) EndRequest(_ context.Context, _ pg.Tx, id, _, _ uuid.UUID, at time.Time) error {
	if at.IsZero() {
		return errors.New("no time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended[id]++
	return nil
}

// lostNow is whether the request is recorded lost and no late settlement has resolved it.
func (s *supplies) lostNow(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, lost := s.lost[id]
	return lost && !s.resolved[id]
}

// metered is a route entry priced in billionths: a token of input or output 1, a web search 10; its output limit 100,
// so 1000 bytes of text reserve 1100.
func metered(tb testing.TB) Quotable {
	tb.Helper()
	return Quotable{
		Price: mustSchedule(tb, `{"v":1,"currency":"USD",
			"rates":{"input":"0.000000001","output":"0.000000001","web_search":"0.00000001"}}`),
		Model: Model{ContextLength: 1e12}, MaxOutput: 100, Endpoint: uuid.NewV7(),
	}
}

func (b *bank) hold(text int64) *Hold {
	b.t.Helper()
	h, err := b.tryHold(text)
	if err != nil {
		b.t.Fatal(err)
	}
	return h
}

func (b *bank) tryHold(text int64) (*Hold, error) { return b.tryHoldOn(metered(b.t), text) }

func (b *bank) holdOn(q Quotable, text int64) *Hold {
	b.t.Helper()
	h, err := b.tryHoldOn(q, text)
	if err != nil {
		b.t.Fatal(err)
	}
	return h
}

func (b *bank) tryHoldOn(q Quotable, text int64) (h *Hold, err error) {
	err = b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: text}, []Quotable{q})
		return err
	})
	return h, err
}

// used is one sampling of input, output and searches, all reported unless estimated is set.
func used(tb testing.TB, input, output, searches int64, estimated ...catalog.Item) Segment {
	tb.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: input, catalog.ItemOutput: output, webSearch: searches}, catalog.ServiceTierStandard, nil)
	if err != nil {
		tb.Fatal(err)
	}
	reported := catalog.ItemSet(1<<in | 1<<catalog.ItemOutput | 1<<webSearch)
	for _, it := range estimated {
		reported &^= 1 << it
	}
	return Segment{Usage: u, Reported: reported}
}

func outcome(result Result, finish FinishReason, segments ...Segment) Outcome {
	return Outcome{Attempts: []AttemptResult{{Seq: 1, Cost: 7, Segments: segments, Result: result, Finish: finish}}}
}

// TestSettleWaivers pins the table of docs/domains/billing.md「免单规则」 at 50 input, 20 output and one search (80):
// what is charged. Only the three rules decide, with no limit on how many calls an account has waived.
func TestSettleWaivers(t *testing.T) {
	full, zero := used(t, 50, 20, 1), used(t, 50, 0, 1)
	for _, tc := range []struct {
		name string
		o    Outcome
		want int64
	}{
		{"delivered and finished", outcome(Committed, FinishStop, full), 80},
		{"aggregated", outcome(Aggregated, FinishStop, full), 80},
		{"delivered, no output, no finish: only the search", outcome(Committed, FinishNone, zero), 10},
		{"delivered, no output, an error finish", outcome(Committed, FinishError, zero), 10},
		{"delivered, no output, a stop finish is not waived", outcome(Committed, FinishStop, zero), 60},
		{"cancelled is never waived", outcome(Committed, FinishCancelled, zero), 60},
		{"cancelled before any content", outcome(Unknown, FinishCancelled, full), 80},
		{"unknown: model usage free, the search charged", outcome(Unknown, FinishNone, full), 10},
		{"failed before commit", outcome(FailedBeforeCommit, FinishError, full), 10},
		// The client leaving after the failure is not in the outcome: the charged attempt failed, so it is waived.
		{"failed with nothing delivered, the client left after", outcome(Unknown, FinishError, full), 10},
		{"not executed", outcome(NotExecuted, FinishNone), 0},
		{"no reported usage is not a zero output", outcome(Committed, FinishNone, used(t, 50, 0, 1, in, catalog.ItemOutput, webSearch)), 60},
		{"input reported, output not: not a zero output", outcome(Committed, FinishNone, used(t, 50, 0, 1, catalog.ItemOutput)), 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
			f, err := b.billing.Settle(t.Context(), b.hold(1000), tc.o)
			if err != nil || f.Charged != platform.AmountExact(tc.want) {
				t.Fatalf("Settle = %+v, %v; want %d charged", f, err, tc.want)
			}
			b.balanced()
		})
	}
}

// TestWaiversHaveNoCap: a hundred zero-output calls in a row are all free, and nothing counts them: the daily cap is
// gone, so nothing is kept of them but the books' own entries (docs/domains/billing.md「免单规则」).
func TestWaiversHaveNoCap(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	for i := range 100 {
		f, err := b.billing.Settle(t.Context(), b.hold(1000), outcome(Committed, FinishNone, used(t, 50, 0, 0)))
		if err != nil || f.Charged != (platform.Exact{}) {
			t.Fatalf("call %d: Settle = %+v, %v; want nothing charged", i, f, err)
		}
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 10_000 {
		t.Errorf("balance %d after a hundred free calls, want 10000", n)
	}
	b.balanced()
}

// TestACancelledCallStillLeavesTheSuppliersRow: cancelling is not free for the platform, so a cancelled call's attempt is
// written to suppliers with the cost billing booked for it, whatever the user is charged (docs/domains/billing.md「客户端断开」).
func TestACancelledCallStillLeavesTheSuppliersRow(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	h := b.hold(1000)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishCancelled, used(t, 50, 20, 0))); err != nil {
		t.Fatal(err)
	}
	if rows := b.supplies.attempts[h.id]; len(rows) != 1 || rows[0].Seq != 1 {
		t.Errorf("suppliers got %+v for the cancelled call, want its one attempt", rows)
	}
	if n := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'supplier_cost'"); n != 7 {
		t.Errorf("the books hold the attempt at %d, want 7: its cost whether or not the user is charged", n)
	}
	b.balanced()
}

// balanced runs the reconciliation and the floor check.
func (b *bank) balanced() {
	b.t.Helper()
	problems, err := Reconcile(b.t.Context(), b.app, b.platform)
	if err != nil || len(problems) != 0 {
		b.t.Errorf("Reconcile = %v, %v", problems, err)
	}
	b.consistent()
	if n := b.count("SELECT count(*) FROM billing.transactions t JOIN billing.entries e ON e.transaction_id = t.id " +
		"WHERE e.book = 'platform_borne' AND t.kind NOT IN ('settle', 'reverse')"); n != 0 {
		b.t.Errorf("%d platform-borne entries outside charges", n)
	}
}

// TestSettleOnce: a retried Settle returns what the first stored and charges nothing more; the counters move once.
func TestSettleOnce(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	h := b.hold(1000)
	o := outcome(Committed, FinishStop, used(t, 50, 20, 1))
	for range 3 {
		if f, err := b.billing.Settle(t.Context(), h, o); err != nil || f.Charged != platform.AmountExact(80) {
			t.Fatalf("Settle = %+v, %v", f, err)
		}
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 10_000-80 {
		t.Errorf("balance %d, want %d", n, 10_000-80)
	}
	if n := b.count("SELECT spent FROM billing.spend WHERE subject = '" + b.admitted.Key.String() + "'"); n != 80 {
		t.Errorf("the key spent %d, want 80", n)
	}
	b.balanced()
}

// TestSettleFloor: usage past what was reserved, as when the model outran its fastest speed, is charged down to the
// floor and no further, other reservations kept covered; the platform bears the rest, on the books.
func TestSettleFloor(t *testing.T) {
	b := newBank(t, 2000, -100, Config{})
	other, h := b.hold(500), b.hold(1000) // 600 and 1100 reserved; 2100 may be spent
	f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, 5000, 0, 0)))
	if err != nil || f.Charged != platform.AmountExact(1500) {
		t.Fatalf("Settle = %+v, %v; want 2000 + 100 - 600 = 1500 charged", f, err)
	}
	if n := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'platform_borne'"); n != 3500 {
		t.Errorf("the platform bore %d, want 3500", n)
	}
	if _, err := b.billing.Settle(t.Context(), other, outcome(Committed, FinishStop, used(t, 500, 100, 0))); err != nil {
		t.Fatal(err)
	}
	b.balanced()
}

// TestCleanup: a call no Settle ended is a lost request, with the route it was frozen with, and its reservation goes
// back uncharged: nothing can bring its usage afterwards. A late Settle then charges by the full outcome against what
// cleanup did, and resolves the lost request.
func TestCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		late    int64 // the late Settle's usage
		state   string
		charged int64
		lost    bool
	}{
		{"settled late", 300, "settled", 300, false},
		{"settled late past the floor", 30_000, "settled", 10_000, false},
		{"never settled", 0, "cleanup_released", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
			q := metered(t)
			h := b.holdOn(q, 1000)
			b.takenOver(h)
			if route := b.supplies.lost[h.id]; len(route) != 1 || route[0] != q.Endpoint {
				t.Errorf("the lost request's route %v, want the one endpoint the reservation holds", route)
			}
			if tc.late > 0 {
				if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, used(t, tc.late, 0, 0))); err != nil {
					t.Fatal(err)
				}
			}
			b.wantReservation(h, tc.state, tc.charged, tc.lost)
			b.balanced()
		})
	}
}

// TestALostRequestsRouteLeavesOutTheEntriesAdmissionDropped: the reservation marks an entry it dropped for its quote with
// the zero id, and no charge can come from the endpoint of an entry that was never to be called, so the route suppliers
// is given has only the kept ones, in order.
func TestALostRequestsRouteLeavesOutTheEntriesAdmissionDropped(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	first, dropped, last := metered(t), metered(t), metered(t)
	dropped.MaxOutput = 1e9 // a quote far beyond the balance
	var h *Hold
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 1000}, []Quotable{first, dropped, last})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.takenOver(h)
	if route, want := b.supplies.lost[h.id], []uuid.UUID{first.Endpoint, last.Endpoint}; !slices.Equal(route, want) {
		t.Errorf("the lost request's route is %v, want the kept entries %v", route, want)
	}
}

// wantReservation checks what the reservation of h came to: its state, what it charged, and whether its request is
// still lost.
func (b *bank) wantReservation(h *Hold, state string, charged int64, lost bool) {
	b.t.Helper()
	gotState, gotCharged, gotLost := b.state(), b.count("SELECT charged FROM billing.reservations"), b.supplies.lostNow(h.id)
	if gotState != state || gotCharged != charged || gotLost != lost {
		b.t.Errorf("%s charged %d, lost %v; want %s, %d, %v", gotState, gotCharged, gotLost, state, charged, lost)
	}
}

// takenOver lets cleanup take h over: the reservation goes back uncharged, and the request is recorded as lost.
func (b *bank) takenOver(h *Hold) {
	b.t.Helper()
	b.cleanup()
	if s := b.state(); s != "cleanup_released" {
		b.t.Fatalf("after cleanup: %s, want cleanup_released", s)
	}
	if n := b.count("SELECT charged FROM billing.reservations"); n != 0 {
		b.t.Fatalf("cleanup charged %d, want nothing", n)
	}
	if !b.supplies.lostNow(h.id) {
		b.t.Fatal("cleanup did not record the request as lost")
	}
}

func (b *bank) cleanup() {
	b.t.Helper()
	b.pastDeadline()
	if _, err := b.billing.Cleanup(b.t.Context(), 100); err != nil {
		b.t.Fatal(err)
	}
}

// state is the state of the one reservation.
func (b *bank) state() string {
	b.t.Helper()
	var s string
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return tx.QueryRow(b.t.Context(), "SELECT state FROM billing.reservations").Scan(&s)
	}); err != nil {
		b.t.Fatal(err)
	}
	return s
}

// TestCreditOnce: a credit retried with its ID is posted once; outside the platform scope it is refused whole,
// because its audit entry is.
func TestCreditOnce(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	c := Credit{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, Amount: 500,
		Actor: audit.System("billing.test"), Reason: "a gift",
	}
	for range 2 {
		if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error { return AddCredit(t.Context(), tx, c) }); err != nil {
			t.Fatal(err)
		}
	}
	c.ID = uuid.NewV7()
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error { return AddCredit(t.Context(), tx, c) }); err == nil {
		t.Error("a credit in the account's scope went through")
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 500 {
		t.Errorf("balance %d, want 500", n)
	}
	b.balanced()
}

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
				Segments: Segments{Output: segmentTokens, Overshoot: time.Second},
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
