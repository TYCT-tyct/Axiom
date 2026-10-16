package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// supplies is the Suppliers double: it keeps what billing records and serves a journal the test writes.
type supplies struct {
	mu       sync.Mutex
	attempts map[uuid.UUID][]AttemptRecord
	journal  map[uuid.UUID]JournalAttempt
	lost     map[uuid.UUID]bool // true while lost, false once resolved
	ended    map[uuid.UUID]int  // how many times each request was told to have ended
}

func newSupplies() *supplies {
	return &supplies{
		attempts: map[uuid.UUID][]AttemptRecord{}, journal: map[uuid.UUID]JournalAttempt{}, lost: map[uuid.UUID]bool{},
		ended: map[uuid.UUID]int{},
	}
}

func (s *supplies) RecordAttempts(_ context.Context, _ pg.Tx, id uuid.UUID, attempts []AttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[id] = append(s.attempts[id], attempts...)
	return nil
}

func (s *supplies) CommittedAttempt(_ context.Context, _ pg.Tx, id uuid.UUID) (JournalAttempt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.journal[id]
	return j, ok, nil
}

func (s *supplies) RecordLostRequest(_ context.Context, _ pg.Tx, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost[id] = true
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

func (s *supplies) ResolveLostRequest(_ context.Context, _ pg.Tx, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost[id] = false
	return nil
}

// metered is a route entry priced in billionths: a token of input or output 1, a web search 10; its output limit 100,
// so 1000 bytes of text reserve 1100.
func metered(tb testing.TB) Quotable {
	tb.Helper()
	return Quotable{
		Price: mustSchedule(tb, `{"v":1,"currency":"USD",
			"rates":{"input":"0.000000001","output":"0.000000001","web_search":"0.00000001"}}`),
		Model: Model{ContextLength: 1e12}, MaxOutput: 100,
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

func outcome(result Result, finish FinishReason, cancelled bool, segments ...Segment) Outcome {
	return Outcome{Cancelled: cancelled, Attempts: []AttemptResult{{Seq: 1, Cost: 7, Costed: true, Segments: segments, Result: result, Finish: finish}}}
}

// TestSettleWaivers pins the table of docs/domains/billing.md「免单规则」 at 50 input, 20 output and one search (80):
// what is charged, and when the daily cap stops a waiver.
func TestSettleWaivers(t *testing.T) {
	full, zero := used(t, 50, 20, 1), used(t, 50, 0, 1)
	for _, tc := range []struct {
		name string
		o    Outcome
		cap  int64
		want int64
	}{
		{"delivered and finished", outcome(Committed, FinishStop, false, full), 1000, 80},
		{"aggregated", outcome(Aggregated, FinishStop, false, full), 1000, 80},
		{"delivered, no output, no finish: only the search", outcome(Committed, FinishNone, false, zero), 1000, 10},
		{"delivered, no output, an error finish", outcome(Committed, FinishError, false, zero), 1000, 10},
		{"delivered, no output, a stop finish is not waived", outcome(Committed, FinishStop, false, zero), 1000, 60},
		{"cancelled is never waived", outcome(Committed, FinishNone, true, zero), 1000, 60},
		{"cancelled before any content", outcome(Unknown, FinishNone, true, full), 1000, 80},
		{"unknown: model usage free, the search charged", outcome(Unknown, FinishNone, false, full), 1000, 10},
		{"failed before commit", outcome(FailedBeforeCommit, FinishError, false, full), 1000, 10},
		{"not executed", outcome(NotExecuted, FinishNone, false), 1000, 0},
		{"unknown past the daily cap", outcome(Unknown, FinishNone, false, full), 6, 80},
		{"no reported usage past the cap is not a zero output", outcome(Unknown, FinishNone, false, used(t, 50, 0, 1, in, catalog.ItemOutput, webSearch)), 6, 60},
		{"input reported, output not: not a zero output", outcome(Unknown, FinishNone, false, used(t, 50, 0, 1, catalog.ItemOutput)), 6, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, WaiverCap: tc.cap})
			f, err := b.billing.Settle(t.Context(), b.hold(1000), tc.o)
			if err != nil || f.Charged != platform.AmountExact(tc.want) {
				t.Fatalf("Settle = %+v, %v; want %d charged", f, err, tc.want)
			}
			b.balanced()
		})
	}
}

// TestWaiverCapCountsCosts: the daily cap adds up the waived calls' costs, so the call that would take it past the cap
// is charged by the zero-output waiver alone; an attempt whose cost is not all known counts at what its usage sells
// for (80 here), which the 保本线 keeps at or above its cost.
func TestWaiverCapCountsCosts(t *testing.T) {
	full := used(t, 50, 20, 1)
	waived := func(cost int64, costed bool) Outcome {
		return Outcome{Attempts: []AttemptResult{{Seq: 1, Cost: cost, Costed: costed, Segments: []Segment{full}, Result: Unknown}}}
	}
	for _, tc := range []struct {
		name  string
		cap   int64
		calls []Outcome
		want  []int64
	}{
		{"costs under the cap are waived", 14, []Outcome{waived(7, true), waived(7, true)}, []int64{10, 10}},
		{"the call past the cap is charged", 13, []Outcome{waived(7, true), waived(7, true)}, []int64{10, 80}},
		{"a cost not known counts its sale price", 79, []Outcome{waived(7, false)}, []int64{80}},
		{"a sale price within the cap is waived", 80, []Outcome{waived(7, false)}, []int64{10}},
		{"a known part above the sale price counts as itself", 99, []Outcome{waived(100, false)}, []int64{80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, WaiverCap: tc.cap})
			for i, o := range tc.calls {
				f, err := b.billing.Settle(t.Context(), b.hold(1000), o)
				if err != nil || f.Charged != platform.AmountExact(tc.want[i]) {
					t.Fatalf("call %d: Settle = %+v, %v; want %d charged", i, f, err, tc.want[i])
				}
			}
			b.balanced()
		})
	}
}

// balanced runs the reconciliation and the floor check.
func (b *bank) balanced() {
	b.t.Helper()
	problems, err := b.billing.Reconcile(b.t.Context())
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
	o := outcome(Committed, FinishStop, false, used(t, 50, 20, 1))
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
	f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, false, used(t, 5000, 0, 0)))
	if err != nil || f.Charged != platform.AmountExact(1500) {
		t.Fatalf("Settle = %+v, %v; want 2000 + 100 - 600 = 1500 charged", f, err)
	}
	if n := b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'platform_borne'"); n != 3500 {
		t.Errorf("the platform bore %d, want 3500", n)
	}
	if _, err := b.billing.Settle(t.Context(), other, outcome(Committed, FinishStop, false, used(t, 500, 100, 0))); err != nil {
		t.Fatal(err)
	}
	b.balanced()
}

// TestCorrect: an estimate is replaced once per revision, in any order of delivery, the makeup capped by what the
// reservation still holds; after the window only the cost follows the record.
func TestCorrect(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	h := b.hold(1000)
	f, err := b.billing.Settle(t.Context(), h, outcome(Unknown, FinishNone, true, used(t, 300, 20, 0, in)))
	if err != nil || !f.Estimated || f.Charged != platform.AmountExact(320) {
		t.Fatalf("Settle = %+v, %v; want 320 estimated", f, err)
	}
	usage := func(input int64) []catalog.Usage { return []catalog.Usage{used(t, input, 20, 0).Usage} }
	for _, c := range []struct {
		revision      int64
		input, charge int64
	}{
		{2, 900, 920},
		{1, 100, 920},   // older: ignored
		{2, 100, 920},   // the same again: ignored
		{3, 5000, 1100}, // past the hold of 1100: capped, the platform bears 3920
	} {
		if err := b.billing.Correct(t.Context(), Correction{Request: h.id, Seq: 1, Revision: c.revision, Usage: usage(c.input), Cost: 9}); err != nil {
			t.Fatal(err)
		}
		if n := b.count("SELECT charged FROM billing.reservations"); n != c.charge {
			t.Errorf("revision %d: charged %d, want %d", c.revision, n, c.charge)
		}
		b.balanced()
	}
	if s := b.state(); s != "estimated" {
		t.Errorf("the corrected reservation is %s, want estimated until its window ends", s)
	}
}

// TestCleanup: a call no Settle ended is a lost request unless the journal shows a committed attempt with a key;
// a late Settle then charges by the full outcome against what cleanup did.
func TestCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		journal bool
		correct bool
		late    int64 // the late Settle's usage
		state   string
		charged int64
		lost    bool
	}{
		{"lost, settled late", false, false, 300, "settled", 300, false},
		{"lost, settled late past the floor", false, false, 30_000, "settled", 10_000, false},
		{"journal, corrected, settled late for more", true, true, 700, "settled", 700, false},
		{"journal, never corrected", true, false, 0, "cleanup_released", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, CorrectionWindow: 500 * time.Millisecond})
			h := b.hold(1000)
			b.takenOver(h, tc.journal, tc.correct)
			if tc.late > 0 {
				if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, false, used(t, tc.late, 0, 0))); err != nil {
					t.Fatal(err)
				}
			}
			state := b.state()
			if charged := b.count("SELECT charged FROM billing.reservations"); state != tc.state || charged != tc.charged || b.supplies.lost[h.id] != tc.lost {
				t.Errorf("%s charged %d, lost %v; want %s, %d, %v", state, charged, b.supplies.lost[h.id], tc.state, tc.charged, tc.lost)
			}
			b.balanced()
		})
	}
}

// takenOver lets cleanup take h over, with a committed attempt in the journal or none; a supplier record then
// corrects it, or its correction window runs out.
func (b *bank) takenOver(h *Hold, journal, correct bool) {
	b.t.Helper()
	if journal {
		b.supplies.journal[h.id] = JournalAttempt{Seq: 1, Key: "client:abc"}
	}
	b.cleanup()
	if want := map[bool]string{true: "cleanup_pending", false: "cleanup_released"}[journal]; b.state() != want {
		b.t.Fatalf("after cleanup: %s, want %s", b.state(), want)
	}
	switch {
	case correct:
		if err := b.billing.Correct(b.t.Context(), Correction{Request: h.id, Seq: 1, Revision: 1, Usage: []catalog.Usage{used(b.t, 400, 0, 0).Usage}, Cost: 9}); err != nil {
			b.t.Fatal(err)
		}
		if n := b.count("SELECT charged FROM billing.reservations WHERE state = 'cleanup_settled'"); n != 400 {
			b.t.Fatalf("the supplier record charged %d, want 400", n)
		}
	case journal:
		time.Sleep(600 * time.Millisecond) // past the correction window
		b.cleanup()
	}
}

func (b *bank) cleanup() {
	b.t.Helper()
	time.Sleep(5 * time.Millisecond) // past the reservation's deadline
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

// TestLedgerProperty drives random sequences of credits, reservations, settlements with retries, crashes left to
// cleanup, and corrections in any order, and checks after every step that every transaction balances, the balance
// equals its book, the counters equal the pending holds and the floor holds.
func TestLedgerProperty(t *testing.T) {
	for seed := range uint64(3) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, ^seed)) //nolint:gosec // G404: test data
			b := newBank(t, 5000, -500, Config{
				HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: 20 * time.Millisecond,
				CorrectionWindow: 20 * time.Millisecond, WaiverCap: 50,
			})
			var holds []*Hold
			for range 40 {
				b.step(r, &holds)
				b.balanced()
			}
		})
	}
}

func (b *bank) step(r *rand.Rand, holds *[]*Hold) {
	b.t.Helper()
	ctx := b.t.Context()
	pick := func() *Hold { return (*holds)[r.IntN(len(*holds))] }
	switch n := r.IntN(6); {
	case n == 0:
		b.credit(r.Int64N(2000) + 1)
	case n == 1 || len(*holds) == 0:
		if h, err := b.tryHold(r.Int64N(1500)); err == nil {
			*holds = append(*holds, h)
		} else if !errors.As(err, new(*Insufficient)) {
			b.t.Fatal(err)
		}
	case n == 2: // settle, or settle again
		b.settleRandom(r, pick())
	case n == 3:
		if err := b.billing.Correct(ctx, Correction{
			Request: pick().id, Seq: 1, Revision: r.Int64N(4) + 1,
			Usage: []catalog.Usage{used(b.t, r.Int64N(3000), r.Int64N(200), 0).Usage}, Cost: r.Int64N(50),
		}); err != nil {
			b.t.Fatal(err)
		}
	default: // a crash: what was left pending goes to cleanup, a journal sometimes showing a committed attempt,
		// sometimes after a correction window has run out
		if r.IntN(2) == 0 {
			b.supplies.journal[pick().id] = JournalAttempt{Seq: 1, Key: "client:k"}
		}
		time.Sleep(time.Duration(r.IntN(2)) * 25 * time.Millisecond)
		b.cleanup()
	}
}

func (b *bank) settleRandom(r *rand.Rand, h *Hold) {
	b.t.Helper()
	results := []Result{Committed, Aggregated, FailedBeforeCommit, Unknown, NotExecuted}
	o := outcome(results[r.IntN(len(results))], FinishReason(r.IntN(9)), r.IntN(4) == 0, //nolint:gosec // G115: below 9
		used(b.t, r.Int64N(2000), r.Int64N(200), r.Int64N(3), []catalog.Item{in}[:r.IntN(2)]...))
	if _, err := b.billing.Settle(b.t.Context(), h, o); err != nil {
		b.t.Fatal(err)
	}
}

// TestLedgerScope: a settlement writes its revenue and cost entries in its account's scope, and no other account's
// scope reads them.
func TestLedgerScope(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	if _, err := b.billing.Settle(t.Context(), b.hold(100), outcome(Committed, FinishStop, false, used(t, 50, 20, 0))); err != nil {
		t.Fatal(err)
	}
	var n int64
	other := pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})
	if err := b.app.Tx(t.Context(), other, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM billing.entries").Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("another account reads %d entries, %v", n, err)
	}
}

// TestSettleBalanceExtremes: the floor cap is computed exactly, so a balance at the top of int64 above a negative
// floor charges in full instead of wrapping to nothing.
func TestSettleBalanceExtremes(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	h := b.hold(100)
	b.sql(fmt.Sprintf("UPDATE billing.balances SET balance = %d, floor = -1", int64(math.MaxInt64)))
	if f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, false, used(t, 50, 20, 0))); err != nil || f.Charged != platform.AmountExact(70) {
		t.Errorf("Settle = %+v, %v; want 70 charged", f, err)
	}
}
