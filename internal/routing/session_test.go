package routing_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/alloctest"
	"axiom/internal/routing"
)

func key(t *testing.T, r *routing.Router, a routing.AccountID, model, body string) routing.SessionKey {
	t.Helper()
	req := input().Req
	req.Model = model
	return r.SessionKey(a, req, []byte(body))
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

// TestUpstreamSessionIsOpaque: the identifier passed on is derived with a secret, is stable for a session and differs
// between sessions and between secrets, and shows nothing of the client's value.
func TestUpstreamSessionIsOpaque(t *testing.T) {
	t.Parallel()
	r := mustRouter(t)
	k1, k2 := key(t, r, acct, "m", `{"session_id":"default"}`), key(t, r, routing.AccountID(id(88)), "m", `{"session_id":"default"}`)
	s1, s2 := k1.Upstream([]byte("secret")), k2.Upstream([]byte("secret"))
	if s1 != k1.Upstream([]byte("secret")) || s1 == s2 || s1 == k1.Upstream([]byte("other")) || strings.Contains(s1, "default") || len(s1) != 32 {
		t.Errorf("%q %q", s1, s2)
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
	in.Override.Order = []routing.SupplierID{sup(1), sup(2)}
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
	in.Override.Order = []routing.SupplierID{sup(2), sup(1)}
	if p = h.plan(in); first(p) != ep(2) {
		t.Errorf("an exhausted supplier as the first entry: %v", p.Entries)
	}
}

// TestDecide is the table of what to do after a failure that nothing has been forwarded for.
func TestDecide(t *testing.T) {
	t.Parallel()
	for scope, want := range map[routing.Scope]routing.Action{
		routing.ScopeRequest:    routing.ReturnToClient,
		routing.ScopeEgress:     routing.OtherCredential,
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
// sticks to the third: what BenchmarkPlan and TestPlanScales plan over.
func candidates(tb testing.TB, n int) (*harness, routing.Input) {
	tb.Helper()
	var endpoints []routing.Endpoint
	for i := 1; i <= n; i++ {
		endpoints = append(endpoints, endpoint(tb, i, func(e *routing.Endpoint) {
			e.Price.Schedule = schedule(tb, "0.000001", "0.000002", "0.0000001")
			if i%7 == 0 {
				e.BreakerOpen = true
			}
		}))
	}
	in := input()
	in.Sticky = ep(3)
	return newHarness(tb, routing.Config{}, openPreset(), endpoints...), in
}

// BenchmarkPlan is the route for a request with fifty candidates, the size docs/performance.md expects ("通常几十个").
// It reports time and allocations; just bench-compare fails on more allocations. The 1ms p99 budget is a wall-clock
// figure, so it is checked by the load tests of docs/performance.md「怎么验证」, not here, where a busy machine would
// fail it at random (#107); TestPlanScales catches the regressions a budget would.
func BenchmarkPlan(b *testing.B) {
	h, in := candidates(b, 50)
	b.ReportAllocs()
	for b.Loop() {
		if _, wait, err := h.r.Plan(in); wait != nil || err != nil {
			b.Fatal(wait, err)
		}
	}
}

// TestPlanScales: planning over thirty-two times the candidates costs about thirty-two times as much, not a thousand,
// so a change that makes Plan quadratic fails here whatever the machine's load. Each size is timed over the same
// number of plans, at least 5ms at the small size, as some clocks tick too coarsely to time one plan; the fastest of
// several such samples counts, interleaved between the sizes: load only ever adds time, so the minimum is the closest
// to the true cost, and both sizes see the same machine. A linear Plan with its n log n sort measures about 30 to 40;
// the bar of 96 leaves room for noise, and even a quadratic pass as cheap as comparing IDs (the mutation in the PR for
// #107) measures over 100. Allocations must not grow with the candidates beyond a few: Plan's slices are sized once.
func TestPlanScales(t *testing.T) {
	t.Run("time", func(t *testing.T) {
		const small, large, samples = 25, 800, 5
		hs, ins := candidates(t, small)
		hl, inl := candidates(t, large)
		reps := 1 // doubled until even the fastest of a few runs at the small size is long enough to time; the first runs
		// also warm both sizes up
		for min(timed(hs, ins, reps), timed(hs, ins, reps), timed(hs, ins, reps)) < 5*time.Millisecond {
			timed(hl, inl, 1)
			reps *= 2
		}
		bestSmall, bestLarge := time.Duration(math.MaxInt64), time.Duration(math.MaxInt64)
		for range samples {
			bestSmall, bestLarge = min(bestSmall, timed(hs, ins, reps)), min(bestLarge, timed(hl, inl, reps))
		}
		if ratio := float64(bestLarge) / float64(bestSmall); ratio > 96 {
			t.Errorf("planning %d candidates takes %.1f times as long as %d (%v against %v for %d plans), want about %d",
				large, ratio, small, bestLarge, bestSmall, reps, large/small)
		}
	})
	t.Run("allocs", func(t *testing.T) {
		const small, large = 25, 800
		hs, ins := candidates(t, small)
		hl, inl := candidates(t, large)
		// The measurement runs in a child process, so this binary's other goroutines are not counted as Plan's.
		got := alloctest.Measure(t,
			alloctest.Function{Name: "small", F: func() { hs.plan(ins) }},
			alloctest.Function{Name: "large", F: func() { hl.plan(inl) }},
		)
		if got["large"].Count > 3*got["small"].Count {
			t.Errorf("planning %d candidates allocates %v times, %d candidates %v: want about as many",
				large, got["large"].Count, small, got["small"].Count)
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
