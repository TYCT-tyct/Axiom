package syncer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// clock is a time the test moves.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// memStore keeps one supplier's state and everything committed to it, and fences like the real one.
type memStore struct {
	mu       sync.Mutex
	state    State
	events   []Event
	plans    []Plan
	charges  map[adapter.ChargeRef]adapter.Charge
	commits  int
	failAt   int // the commit with this number fails once, before it is applied (0: none)
	onCommit func()
}

func newStore() *memStore { return &memStore{charges: map[adapter.ChargeRef]adapter.Charge{}} }

func (s *memStore) Load(context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.clone(), nil
}

func (s *memStore) Commit(_ context.Context, expect int64, c Commit) error {
	if s.onCommit != nil {
		s.onCommit()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	if s.commits == s.failAt {
		return errors.New("store: connection lost")
	}
	if expect != s.state.Version {
		return ErrFenced
	}
	s.state = c.State.clone()
	s.events, s.plans = append(s.events, c.Events...), append(s.plans, c.Plans...)
	for _, ch := range c.Charges {
		s.charges[ch.Ref] = ch
	}
	return nil
}

func (s *memStore) kinds() (out []Kind) {
	for _, e := range s.events {
		out = append(out, e.Kind)
	}
	return out
}

func (s *memStore) count(k Kind) (n int) {
	for _, e := range s.events {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// supplier is a scripted Manager. Each method counts its calls and returns what its field says.
type supplier struct {
	calls   [numTasks]int
	account func() (adapter.Account, error)
	offer   func() (adapter.Offer, error)
	keys    func() ([]adapter.KeyState, error)
	charges func(scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error)
}

func (m *supplier) Account(context.Context) (adapter.Account, error) {
	m.calls[TaskBalance]++
	return m.account()
}

func (m *supplier) Offer(context.Context) (adapter.Offer, error) {
	m.calls[TaskCatalog]++
	return m.offer()
}

func (m *supplier) Keys(context.Context) ([]adapter.KeyState, error) {
	m.calls[TaskKeys]++
	return m.keys()
}

func (*supplier) KeySecret(context.Context, adapter.KeyRef) (secret.Secret, error) {
	return secret.Secret{}, errors.New("not used")
}

func (m *supplier) Charges(_ context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	m.calls[TaskCharges]++
	return m.charges(scan)
}

// healthy is a supplier whose answers never change.
func healthy() *supplier {
	return &supplier{
		account: func() (adapter.Account, error) {
			return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
		},
		offer: func() (adapter.Offer, error) {
			return adapter.Offer{Groups: []adapter.Group{{
				Ref: "1", Name: "g", Ratio: "1.5", Facts: adapter.GroupFacts(`{}`),
				Models: []adapter.Model{{UpstreamName: "m1"}, {UpstreamName: "m2"}},
			}}}, nil
		},
		keys: func() ([]adapter.KeyState, error) {
			return []adapter.KeyState{{Ref: "7", GroupRef: "1", Status: adapter.KeyActive}}, nil
		},
		charges: func(scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
			return adapter.ChargePage{Done: true}, scan, nil
		},
	}
}

func engine(m adapter.Manager) (*Engine, *memStore, *clock) {
	st, clk := newStore(), &clock{t: t0}
	return New(st, m, Defaults(), clk.now), st, clk
}

func round(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestTasksRunWhenDue: every task runs in the first round, none again before its interval, and each again after it.
func TestTasksRunWhenDue(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, _, clk := engine(m)
	round(t, e)
	if m.calls != [numTasks]int{1, 1, 1, 1} {
		t.Fatalf("first round calls = %v", m.calls)
	}
	clk.advance(30 * time.Second)
	round(t, e)
	if m.calls != [numTasks]int{1, 1, 1, 1} {
		t.Errorf("30 s later calls = %v, want none", m.calls)
	}
	clk.advance(time.Minute) // 1m30s: balance (1m) and charges (2m not yet)
	round(t, e)
	if m.calls != [numTasks]int{2, 1, 1, 1} {
		t.Errorf("after 1m30s calls = %v, want only the balance again", m.calls)
	}
	clk.advance(4 * time.Minute) // 5m30s: all due
	round(t, e)
	if m.calls != [numTasks]int{3, 2, 2, 2} {
		t.Errorf("after 5m30s calls = %v", m.calls)
	}
}

// TestFailuresBackOffAndRecover: failures double the wait from a minute up to half an hour with the jitter applied; the
// task is not called inside the wait, others go on, and one success clears it.
func TestFailuresBackOffAndRecover(t *testing.T) {
	t.Parallel()
	m := healthy()
	fail := true
	m.account = func() (adapter.Account, error) {
		if fail {
			return adapter.Account{}, errors.New("timeout")
		}
		return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
	}
	e, st, clk := engine(m)
	e.set.Jitter = func(d time.Duration) time.Duration { return d / 2 }
	var waits []time.Duration
	for range 8 {
		before := clk.now()
		round(t, e)
		ts := st.state.Tasks[TaskBalance]
		waits = append(waits, ts.BackoffUntil.Sub(before))
		inside := m.calls[TaskBalance]
		clk.advance(ts.BackoffUntil.Sub(before) - time.Second)
		round(t, e)
		if m.calls[TaskBalance] != inside {
			t.Fatal("called inside the backoff")
		}
		clk.advance(time.Second)
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	if !slices.Equal(waits, want) {
		t.Errorf("backoff waits = %v, want %v (halved by the jitter, capped at 30m)", waits, want)
	}
	if m.calls[TaskCatalog] < 2 {
		t.Error("a failing balance stopped the other tasks")
	}
	fail = false
	round(t, e)
	if ts := st.state.Tasks[TaskBalance]; ts.Failures != 0 || !ts.BackoffUntil.IsZero() {
		t.Errorf("after a success: %+v", ts)
	}
}

// TestAuthRejectionStopsEverything: once the supplier rejects the account no task is called, in this round or later,
// and one event says why. This is what keeps the engine from trying a rejected account again and again.
func TestAuthRejectionStopsEverything(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.offer = func() (adapter.Offer, error) {
		return adapter.Offer{}, fmt.Errorf("login: %w", adapter.ErrAuthRejected)
	}
	e, st, clk := engine(m)
	round(t, e)
	if st.state.Status != NeedsAttention || m.calls[TaskKeys] != 0 || m.calls[TaskCharges] != 0 || st.count(NeedsAttentionEvent) != 1 {
		t.Fatalf("status %v, calls %v, events %v", st.state.Status, m.calls, st.kinds())
	}
	before := m.calls
	for range 3 {
		clk.advance(time.Hour)
		round(t, e)
	}
	if m.calls != before {
		t.Errorf("polled a rejected supplier: %v -> %v", before, m.calls)
	}
}

// TestUnrecognisedAnswerHaltsOnlyItsTask: after an upgrade at the supplier one task stops, writes nothing and says so;
// the others keep going.
func TestUnrecognisedAnswerHaltsOnlyItsTask(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.offer = func() (adapter.Offer, error) { return adapter.Offer{}, adapter.ErrUnrecognised }
	e, st, clk := engine(m)
	round(t, e)
	clk.advance(10 * time.Minute)
	round(t, e)
	if m.calls[TaskCatalog] != 1 || m.calls[TaskKeys] != 2 || st.count(TaskHalted) != 1 || len(st.state.Groups) != 0 {
		t.Errorf("calls %v, events %v, groups %d", m.calls, st.kinds(), len(st.state.Groups))
	}
}

// TestBlockedBucketsAreNotFailures: a request that the supplier's limits refused is tried again when they allow, with
// no failure counted.
func TestBlockedBucketsAreNotFailures(t *testing.T) {
	t.Parallel()
	m := healthy()
	until := t0.Add(7 * time.Minute)
	m.keys = func() ([]adapter.KeyState, error) { return nil, adapter.Blocked{Until: until} }
	e, st, _ := engine(m)
	round(t, e)
	if ts := st.state.Tasks[TaskKeys]; ts.Failures != 0 || !ts.Due.Equal(until) {
		t.Errorf("keys task = %+v, want due at %v with no failure", ts, until)
	}
}

// TestSameAnswerTwiceChangesNothing and TestChangedRatioMakesOneEventAndOnePlan are the sync's idempotence: a repeat
// is silent; a change is one event and one new plan version.
func TestSameAnswerTwiceChangesNothing(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, st, clk := engine(m)
	st.state.Confirmed = map[string]string{"m1": confirmedBase}
	round(t, e)
	events, plans := len(st.events), len(st.plans)
	if plans != 1 || st.count(GroupAdded) != 1 || st.count(ModelAdded) != 2 {
		t.Fatalf("first read: events %v, %d plans", st.kinds(), plans)
	}
	clk.advance(10 * time.Minute)
	round(t, e)
	if len(st.events) != events || len(st.plans) != plans {
		t.Errorf("the same answer again made events %v and %d plans", st.kinds()[events:], len(st.plans)-plans)
	}
}

const confirmedBase = `{"currency":"USD","v":1,"rates":{"input":"0.000003","output":"0.000015"}}`

func TestChangedRatioMakesOneEventAndOnePlan(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, st, clk := engine(m)
	st.state.Confirmed = map[string]string{"m1": confirmedBase}
	round(t, e)
	first := st.state.Plans["1/m1"]
	m.offer = func() (adapter.Offer, error) {
		return adapter.Offer{Groups: []adapter.Group{{
			Ref: "1", Name: "g", Ratio: "2", Facts: adapter.GroupFacts(`{}`),
			Models: []adapter.Model{{UpstreamName: "m1"}, {UpstreamName: "m2"}},
		}}}, nil
	}
	clk.advance(10 * time.Minute)
	events, plans := len(st.events), len(st.plans)
	round(t, e)
	got := st.kinds()[events:]
	if !slices.Equal(got, []Kind{RatioChanged, PlanChanged}) || len(st.plans)-plans != 1 {
		t.Fatalf("ratio change: events %v, %d new plans", got, len(st.plans)-plans)
	}
	if now := st.state.Plans["1/m1"]; now == first || now == "" {
		t.Errorf("plan fingerprint %q -> %q", first, now)
	}
	if _, ok := st.state.Plans["1/m2"]; ok {
		t.Error("a model without a confirmed base got a plan")
	}
	// The plan is the base scaled by the new ratio, in the supplier's own unit.
	last := st.plans[len(st.plans)-1]
	s, err := catalog.ParseSchedule([]byte(last.Schedule))
	if err != nil || last.Basis != "confirmed" {
		t.Fatalf("plan %+v: %v", last, err)
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 1000000}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cost, _, err := s.Cost(u); err != nil || cost.Rat().FloatString(6) != "6.000000" {
		t.Errorf("a million input tokens cost %v (%v), want 6 = 0.000003 × 2 × 1e6", cost.Rat().FloatString(6), err)
	}
}

// TestUnreadGroupKeepsItsModels: a group whose models could not be read keeps the ones it had, and the ratio still moves.
func TestUnreadGroupKeepsItsModels(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, st, clk := engine(m)
	round(t, e)
	m.offer = func() (adapter.Offer, error) {
		return adapter.Offer{Groups: []adapter.Group{{Ref: "1", Name: "g", Ratio: "3", Facts: adapter.GroupFacts(`{}`), ModelsUnread: true}}}, nil
	}
	clk.advance(10 * time.Minute)
	round(t, e)
	g := st.state.Groups[0]
	if len(g.Models) != 2 || g.Ratio != "3" || st.count(ModelRemoved) != 0 {
		t.Errorf("group after an unread round: %+v, events %v", g, st.kinds())
	}
}

// page returns a scripted charge page.
func record(n int) adapter.Charge {
	return adapter.Charge{Ref: adapter.ChargeRef(fmt.Sprintf("client:r%d", n)), Amount: "1", Funding: adapter.FundingWallet}
}

// pagedSupplier serves a pass of four pages, each repeating the last record of the page before, and refuses the read of
// page blockAt once. The pages it answered are recorded.
func pagedSupplier(t *testing.T, blockAt int) (*supplier, *[]int) {
	m, served := healthy(), new([]int)
	m.charges = func(scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		var pos struct {
			Page int `json:"page"`
		}
		if len(scan) > 0 { // the first call has no position
			if err := jsonx.Unmarshal(scan, &pos); err != nil {
				t.Fatal(err)
			}
		}
		if pos.Page == blockAt {
			blockAt = -1
			return adapter.ChargePage{}, nil, adapter.Blocked{Until: t0.Add(time.Minute)}
		}
		*served = append(*served, pos.Page)
		pos.Page++
		next, err := jsonx.MarshalCanonical(pos)
		if err != nil {
			t.Fatal(err)
		}
		return adapter.ChargePage{Charges: []adapter.Charge{record(pos.Page - 1), record(pos.Page)}, Done: pos.Page == 4}, next, nil
	}
	return m, served
}

// TestChargePassIsCommittedPageByPageAndResumes: the first call has no position; every page is stored with the position
// after it; a limit in the middle ends the round with the progress kept, and the next round goes on from the next page,
// not the first; a record read twice is one.
func TestChargePassIsCommittedPageByPageAndResumes(t *testing.T) {
	t.Parallel()
	m, served := pagedSupplier(t, 2)
	e, st, clk := engine(m)
	round(t, e)
	if !st.state.ScanOpen || len(st.state.Scan) == 0 || len(st.charges) != 3 {
		t.Fatalf("after the limit: scan open %v, position %q, %d records", st.state.ScanOpen, st.state.Scan, len(st.charges))
	}
	clk.advance(time.Minute)
	round(t, e)
	if !slices.Equal(*served, []int{0, 1, 2, 3}) {
		t.Errorf("pages read %v, want 0 to 3 once each: the pass must resume where it stopped", *served)
	}
	if st.state.ScanOpen || len(st.charges) != 5 {
		t.Errorf("after resuming: scan open %v, %d records", st.state.ScanOpen, len(st.charges))
	}
}

// TestFundingOtherThanWalletStopsTheSupplier: a charge paid from something we do not model is a cost nobody accounts
// for; the supplier is stopped for a person to look at.
func TestFundingOtherThanWalletStopsTheSupplier(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.charges = func(scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		c := record(1)
		c.Funding = adapter.FundingOther
		return adapter.ChargePage{Charges: []adapter.Charge{c}, Done: true}, scan, nil
	}
	e, st, _ := engine(m)
	round(t, e)
	if st.state.Status != NeedsAttention || st.count(NeedsAttentionEvent) != 1 {
		t.Errorf("status %v, events %v", st.state.Status, st.kinds())
	}
}

// TestFencedStepWritesNothing: another writer got in between the read and the commit; the step's results are dropped.
func TestFencedStepWritesNothing(t *testing.T) {
	t.Parallel()
	m := healthy()
	e, st, _ := engine(m)
	st.onCommit = func() { st.onCommit = nil; st.mu.Lock(); st.state.Version += 10; st.mu.Unlock() }
	if err := e.Round(t.Context()); !errors.Is(err, ErrFenced) {
		t.Fatalf("Round = %v, want ErrFenced", err)
	}
	if len(st.events) != 0 || len(st.state.Groups) != 0 || st.state.Balance != "" {
		t.Error("a fenced step left results behind")
	}
}

func TestBalanceLowIsSentOnceUntilItRecovers(t *testing.T) {
	t.Parallel()
	m := healthy()
	balance := adapter.Amount("3")
	m.account = func() (adapter.Account, error) {
		return adapter.Account{Balance: balance, Funding: adapter.FundingWallet}, nil
	}
	e, st, clk := engine(m)
	for range 3 {
		round(t, e)
		clk.advance(2 * time.Minute)
	}
	if st.count(BalanceLow) != 1 {
		t.Fatalf("BalanceLow sent %d times while low", st.count(BalanceLow))
	}
	balance = "50"
	round(t, e)
	clk.advance(2 * time.Minute)
	balance = "2"
	round(t, e)
	if st.count(BalanceLow) != 2 {
		t.Errorf("BalanceLow sent %d times after a recovery and a new drop, want 2", st.count(BalanceLow))
	}
}

// TestEndpointIsRoutableOnlyWhenEveryConditionHolds flips each condition in turn.
func TestEndpointIsRoutableOnlyWhenEveryConditionHolds(t *testing.T) {
	t.Parallel()
	ok := EndpointFacts{Normal: true, Wallet: true, Balance: "10", Reserve: "1", GroupSelected: true, Mapped: true, CostKnown: true, UsableCredentials: 1}
	if r := ok.Check(); r != Routable {
		t.Fatalf("all conditions hold but %q", r)
	}
	for want, mutate := range map[Reason]func(*EndpointFacts){
		SupplierStopped:  func(f *EndpointFacts) { f.Normal = false },
		NotWallet:        func(f *EndpointFacts) { f.Wallet = false },
		NoBalance:        func(f *EndpointFacts) { f.Balance = "1" },
		GroupNotSelected: func(f *EndpointFacts) { f.GroupSelected = false },
		Unmapped:         func(f *EndpointFacts) { f.Mapped = false },
		CostUnknown:      func(f *EndpointFacts) { f.CostKnown = false },
		NoUsableKey:      func(f *EndpointFacts) { f.UsableCredentials = 0 },
	} {
		f := ok
		mutate(&f)
		if got := f.Check(); got != want {
			t.Errorf("with %q failing, Check = %q", want, got)
		}
	}
	negative := ok
	negative.Balance = "-13"
	if negative.Check() != NoBalance {
		t.Error("an overdrawn supplier is routable")
	}
}
