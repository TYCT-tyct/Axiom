package routing_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alloctest"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func key(t *testing.T, r *routing.Router, a routing.AccountID, model, body string) routing.SessionKey {
	t.Helper()
	req := input().Req
	req.Model = model
	return r.SessionKey(a, req, []byte(body), "")
}

// TestSessionKey: the client's own session value wins, the same value from two accounts is two sessions, and without
// one the key follows the account, the model and the start of the conversation.
func TestSessionKey(t *testing.T) {
	t.Parallel()
	r, err := routing.New(routing.Config{SessionPrefix: 2048})
	if err != nil {
		t.Fatal(err)
	}
	other := routing.AccountID(id(88))
	conv := func(first string) string {
		return `{"model":"m","messages":[{"role":"user","content":"` + first + `"}]}`
	}
	same := []struct{ name, a, b string }{
		{"session_id", `{"session_id":"s1","messages":[1]}`, `{"model":"x","session_id":"s1","messages":[2]}`},
		{"prompt_cache_key", `{"prompt_cache_key":"k1","input":"a"}`, `{"prompt_cache_key":"k1","input":"b"}`},
		{"claude code's user_id", `{"metadata":{"user_id":"u_session_1"},"messages":[1]}`, `{"messages":[2],"metadata":{"x":1,"user_id":"u_session_1"}}`},
		{"the conversation's start", conv("hello there"), conv("hello there")},
		{"the start only", conv("hello there") + strings.Repeat(" ", 100), `{"model":"m","messages":[{"role":"user","content":"hello there"},{"role":"assistant","content":"more"}]}`[:100]},
	}
	for _, c := range same {
		if key(t, r, acct, "m", c.a) != key(t, r, acct, "m", c.b) {
			t.Errorf("%s: two bodies of one session have two keys", c.name)
		}
	}
	differ := []struct {
		name string
		a, b routing.SessionKey
	}{
		{"two accounts, one session value", key(t, r, acct, "m", `{"session_id":"default"}`), key(t, r, other, "m", `{"session_id":"default"}`)},
		{"two session values", key(t, r, acct, "m", `{"session_id":"s1"}`), key(t, r, acct, "m", `{"session_id":"s2"}`)},
		{"two accounts, one conversation", key(t, r, acct, "m", conv("hi")), key(t, r, other, "m", conv("hi"))},
		{"two conversations", key(t, r, acct, "m", conv("hi")), key(t, r, acct, "m", conv("hello"))},
		{"two models", key(t, r, acct, "m", conv("hi")), key(t, r, acct, "n", conv("hi"))},
		{"a session value and none", key(t, r, acct, "m", conv("hi")), key(t, r, acct, "m", `{"session_id":"hi","messages":[]}`)},
	}
	for _, c := range differ {
		if c.a == c.b {
			t.Errorf("%s: one key", c.name)
		}
	}
}

// TestSessionKeyReadsABoundedPrefix: what lies beyond the prefix does not change the key, so a large body costs no more
// to key than a small one; a body cut in the middle of a token still keys.
func TestSessionKeyReadsABoundedPrefix(t *testing.T) {
	t.Parallel()
	r, err := routing.New(routing.Config{SessionPrefix: 512})
	if err != nil {
		t.Fatal(err)
	}
	head := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("a", 600) + `"}`
	a := key(t, r, acct, "m", head+strings.Repeat(`,{"role":"user","content":"tail one"}`, 200)+`]}`)
	b := key(t, r, acct, "m", head+strings.Repeat(`,{"role":"user","content":"tail two"}`, 300)+`]}`)
	if a != b {
		t.Error("the key depends on what lies past the prefix")
	}
	if c := key(t, r, acct, "m", head[:200]); c == (routing.SessionKey{}) {
		t.Error("a cut body gave the zero key")
	}
	// A session value past the prefix is not seen: the key then rests on the start of the conversation.
	late := head + strings.Repeat(" ", 600) + `],"session_id":"late"}`
	if key(t, r, acct, "m", late) != key(t, r, acct, "m", head+strings.Repeat(" ", 600)+`],"session_id":"other"}`) {
		t.Error("a session value past the prefix was read")
	}
	if key(t, r, acct, "m", "") != key(t, r, acct, "m", "not json") {
		t.Error("an unreadable body is not keyed by account and model alone")
	}
}

// TestRetryBudget: a supplier may be retried onto as many times as a share of its requests plus a small floor; the
// window ages out; Plan leaves a later entry at an exhausted supplier out, never the first.
func TestRetryBudget(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{RetryRatio: 0.5, RetryFloor: 1, RetryWindow: time.Minute}, openPreset(), endpoint(t, 1), endpoint(t, 2))
	for range 4 {
		h.r.Sent(sup(2))
	}
	// 4 requests and a floor of 1: 0.5×4 + 1 = 3 retries.
	for i := range 3 {
		if !h.r.TakeRetry(sup(2)) {
			t.Fatalf("retry %d refused", i+1)
		}
	}
	if h.r.TakeRetry(sup(2)) {
		t.Error("a fourth retry fit")
	}
	if !h.r.TakeRetry(sup(1)) || h.r.TakeRetry(sup(1)) {
		t.Error("another supplier shares the budget, or has none of its floor")
	}
	h.now = h.now.Add(2 * time.Minute) // the window has passed
	if !h.r.TakeRetry(sup(2)) {
		t.Error("the window did not age out")
	}
}

// TestRetryBudgetInPlan: an exhausted supplier is left out of later entries and the leaving out is recorded; a first
// entry is not a retry, so it stays.
func TestRetryBudgetInPlan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{RetryRatio: 0.5, RetryFloor: 1}, openPreset(), endpoint(t, 1), endpoint(t, 2))
	for h.r.TakeRetry(sup(2)) { // use it all
	}
	in := input()
	in.Override.OrderNames = []string{"g1", "g2"}
	p := h.plan(in)
	if len(p.Entries) != 1 || first(p) != ep(1) {
		t.Errorf("route %v", p.Entries)
	}
	var recorded bool
	for _, e := range p.Record.Excluded {
		recorded = recorded || e == (routing.Excluded{Endpoint: ep(2), Reason: routing.RetryBudget})
	}
	if !recorded {
		t.Errorf("not recorded: %+v", p.Record.Excluded)
	}
	in.Override.OrderNames = []string{"g2", "g1"}
	if p = h.plan(in); first(p) != ep(2) {
		t.Errorf("an exhausted supplier as the first entry: %v", p.Entries)
	}
}

// TestDecide is the table of what to do after a failure that nothing has been forwarded for.
func TestDecide(t *testing.T) {
	t.Parallel()
	for scope, want := range map[routing.Scope]routing.Action{
		routing.ScopeRequest:    routing.ReturnToClient,
		routing.ScopeEgress:     routing.NextEntry,
		routing.ScopeCredential: routing.OtherCredential,
		routing.ScopeEndpoint:   routing.NextEntry,
		routing.ScopeGroup:      routing.NextEntry,
		routing.ScopeSupplier:   routing.NextEntry,
	} {
		if got := routing.Decide(routing.Failure{Scope: scope}); got != want {
			t.Errorf("scope %d: %d, want %d", scope, got, want)
		}
	}
}

// TestRecordJSON: the stored form names the reasons and carries the route and the attempts.
func TestRecordJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1), endpoint(t, 2, func(e *routing.Endpoint) { e.BreakerOpen = true }))
	p := h.plan(input())
	p.Record.Attempts = append(p.Record.Attempts, routing.Attempt{Endpoint: ep(1), Egress: "direct", Failed: true, Scope: routing.ScopeEndpoint, Action: routing.NextEntry})
	got, err := p.Record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"reason":"breaker_open"`, `"endpoint":"10000000-0000-0000-0000-000000000002"`, `"route":["10000000-0000-0000-0000-000000000001"]`, `"egress":"direct"`, `"failed":true`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("%s lacks %s", got, want)
		}
	}
}

// candidates is a harness with n endpoints of one model, every seventh with its breaker open, and a request that
// sticks to the third: what BenchmarkPlan and TestPlanScales plan over. cfg is the router's configuration.
func candidates(tb testing.TB, n int, cfg routing.Config) (*harness, routing.Input) {
	tb.Helper()
	var endpoints []routing.Endpoint
	for i := 1; i <= n; i++ {
		endpoints = append(endpoints, endpoint(tb, i, func(e *routing.Endpoint) {
			e.Price.Schedule = schedule(tb, "0.000001", "0.000002", "0.0000001")
			e.Cost = e.Price.Schedule
			e.Funds = &suppliers.Funds{Balance: platform.AmountExact(1_000_000_000_000)}
			if i%7 == 0 {
				e.BreakerOpen = true
			}
		}))
	}
	in := input()
	in.Sticky = ep(3)
	return newHarness(tb, cfg, openPreset(), endpoints...), in
}

// BenchmarkPlan is the route for a request with fifty candidates, the size docs/performance.md expects ("通常几十个").
// It reports time and allocations; just bench-compare fails on more allocations. The 1ms p99 budget is a wall-clock
// figure, so it is checked by the load tests of docs/performance.md「怎么验证」, not here, where a busy machine would
// fail it at random (#107); TestPlanScales catches the regressions a budget would.
func BenchmarkPlan(b *testing.B) {
	h, in := candidates(b, 50, routing.Config{})
	b.ReportAllocs()
	for b.Loop() {
		if _, wait, err := h.r.Plan(in); wait != nil || err != nil {
			b.Fatal(wait, err)
		}
	}
}

// TestPlanScales: planning over a hundred and twenty-eight times the candidates costs about that many times as much, not
// sixteen thousand, so a change that makes Plan quadratic fails here whatever the machine's load. Time is the only
// measure that sees a quadratic pass as cheap as comparing IDs (the mutation in the PR for #107), which neither draws
// numbers nor allocates, so it is read with a wide margin: linear Plan with its n log n sort measures about 130 to 160,
// the bar is 1000 and a quadratic pass measures over 10000. Each size is timed over the same number of plans, enough to
// be at least 2ms at the small size, as some clocks tick too coarsely to time one plan; the fastest of several samples
// counts, interleaved between the sizes: load only ever adds time, so the minimum is the closest to the true cost.
// Besides, the scoring is counted: the Balanced strategy draws one random number for each candidate it scores, so every
// candidate must be either left out of the record or drawn for exactly once, at both sizes. What Plan allocates grows
// with the candidates by no more than they do, and not beyond a few allocations: Plan's slices are sized once.
func TestPlanScales(t *testing.T) {
	const small, large = 25, 3200
	t.Run("time", func(t *testing.T) { planTimeRatio(t, small, large) })
	t.Run("scoring", func(t *testing.T) {
		for _, n := range []int{small, large} {
			draws := 0
			h, in := candidates(t, n, routing.Config{Rand: func() float64 { draws++; return 0.5 }})
			p := h.plan(in)
			if excluded := len(p.Record.Excluded); excluded == 0 || draws+excluded != n {
				t.Errorf("%d candidates: %d scored and %d left out, want each one or the other, and some of each", n, draws, excluded)
			}
		}
	})
	t.Run("allocs", func(t *testing.T) {
		hs, ins := candidates(t, small, routing.Config{})
		hl, inl := candidates(t, large, routing.Config{})
		// The measurement runs in a child process, so this binary's other goroutines are not counted as Plan's.
		got := alloctest.Measure(t,
			alloctest.Function{Name: "small", F: func() { hs.plan(ins) }},
			alloctest.Function{Name: "large", F: func() { hl.plan(inl) }},
		)
		if got["large"].Count > 3*got["small"].Count {
			t.Errorf("planning %d candidates allocates %v times, %d candidates %v: want about as many",
				large, got["large"].Count, small, got["small"].Count)
		}
		if got["large"].Bytes > 2*large/small*got["small"].Bytes {
			t.Errorf("planning %d candidates allocates %v bytes, %d candidates %v: want about %d times as many",
				large, got["large"].Bytes, small, got["small"].Bytes, large/small)
		}
	})
}

// timed is how long reps plans take.
func timed(h *harness, in routing.Input, reps int) time.Duration {
	start := time.Now()
	for range reps {
		h.plan(in)
	}
	return time.Since(start)
}

// planTimeRatio fails when planning over large candidates takes more than a thousand times as long as over small.
func planTimeRatio(t *testing.T, small, large int) {
	hs, ins := candidates(t, small, routing.Config{})
	hl, inl := candidates(t, large, routing.Config{})
	checkPlanRatio(t, "candidates", small, large, 1000, hs, ins, hl, inl)
}

// checkPlanRatio fails when planning large takes more than limit times as long as planning small.
func checkPlanRatio(t *testing.T, what string, small, large int, limit float64, hs *harness, ins routing.Input, hl *harness, inl routing.Input) {
	const samples = 3
	reps := 1 // doubled until even the fastest of a few runs at the small size is long enough to time; the first runs
	// also warm both sizes up
	for min(timed(hs, ins, reps), timed(hs, ins, reps), timed(hs, ins, reps)) < 2*time.Millisecond {
		timed(hl, inl, 1)
		reps *= 2
	}
	bestSmall, bestLarge := time.Duration(math.MaxInt64), time.Duration(math.MaxInt64)
	for range samples {
		bestSmall, bestLarge = min(bestSmall, timed(hs, ins, reps)), min(bestLarge, timed(hl, inl, reps))
	}
	if ratio := float64(bestLarge) / float64(bestSmall); ratio > limit {
		t.Errorf("planning %d %s takes %.0f times as long as %d (%v against %v for %d plans), want about %d",
			large, what, ratio, small, bestLarge, bestSmall, reps, large/small)
	}
}

// modelCandidates is a harness with n models, one endpoint each and all free, and a request for openrouter/auto or
// openrouter/free: what TestPlanScalesAcrossModels plans over. Only the last model is allowed, so the model scan is what
// the plan costs and its one candidate leaves nothing for the global sort to hide it behind.
func modelCandidates(tb testing.TB, n int, auto, free bool) (*harness, routing.Input) {
	tb.Helper()
	endpoints := make([]routing.Endpoint, 0, n)
	for i := 1; i <= n; i++ {
		m := catalog.ModelID(id(i))
		endpoints = append(endpoints, endpoint(tb, i, func(e *routing.Endpoint) {
			e.Model = m
			e.Free = true
		}))
	}
	in := input()
	in.Auto, in.Free = auto, free
	in.Models = []catalog.ModelID{model} // auto and free ignore the request's own list
	in.Admission.Allowed = map[catalog.ModelID]struct{}{catalog.ModelID(id(n)): {}}
	return newHarness(tb, routing.Config{}, openPreset(), endpoints...), in
}

// TestPlanScalesAcrossModels: openrouter/auto and openrouter/free rank the snapshot's own model lists, which are unique
// already. Planning over 64 times the models costs about 64 times as much, not 4096: #537 found a dedup of those lists
// that compared every model with every earlier one. Only one model is allowed, so the scan is the cost and the global
// sort cannot hide it; only a model count sees the quadratic, since candidates adds endpoints of one model.
func TestPlanScalesAcrossModels(t *testing.T) {
	const small, large = 100, 6400
	for _, c := range []struct {
		name string
		auto bool
		free bool
	}{
		{"auto", true, false},
		{"free", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			hs, ins := modelCandidates(t, small, c.auto, c.free)
			hl, inl := modelCandidates(t, large, c.auto, c.free)
			checkPlanRatio(t, "models", small, large, 300, hs, ins, hl, inl)
		})
	}
}
