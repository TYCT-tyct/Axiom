package billing

import (
	"context"
	"errors"
	"fmt"
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

func (s *supplies) EndRequests(ctx context.Context, tx pg.Tx, ends []suppliers.EndedRequest) error {
	for _, e := range ends {
		if err := s.EndRequest(ctx, tx, e.Request, e.Site, e.Account, e.At); err != nil {
			return err
		}
	}
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
		h, _, err = b.billing.reserveOne(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: text}, []Quotable{q})
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	first, dropped, last := metered(t), metered(t), metered(t)
	dropped.MaxOutput = 1e9 // a quote far beyond the balance
	var h *Hold
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.reserveOne(t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 1000}, []Quotable{first, dropped, last})
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
	t.Parallel()

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
