package gateway

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// offsetClock is the real clock moved by an amount a test sets after the client has measured its offset to the server, so
// that a test puts "now" where it wants it without waiting, and without the start refusing a clock that far off
// (internal/platform/kv/README.md「时钟」).
type offsetClock struct{ ahead atomic.Int64 }

func (c *offsetClock) Now() time.Time { return time.Now().Add(time.Duration(c.ahead.Load())) }

// midWindow moves the clock to the middle of a window ahead of the real one, so that the milliseconds a test takes stay
// inside one window: the windows are the epoch's, and a count that crossed a boundary would be a different count.
func (c *offsetClock) midWindow(window time.Duration) {
	now := time.Now()
	start := (now.UnixMilli()/window.Milliseconds() + 1) * window.Milliseconds()
	c.ahead.Store(int64(time.UnixMilli(start).Add(window / 2).Sub(now)))
}

// node is the precheck's side of a core node: its own in-process counter, and a client of the shared cache in the
// namespace that makes two nodes one deployment. Its in-process counter reads the client's clock, as the shared count does.
func node(t *testing.T, server *kvtest.Server, namespace string, clock *offsetClock) *Handler {
	t.Helper()
	limits, err := New(DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := server.OpenIn(t, namespace, kv.Deps{Now: clock.Now})
	limits.Rate.now = cache.Now
	return &Handler{Limits: limits, KV: cache}
}

var (
	rateSite   = accounts.SiteID(uuid.MustParse("6f1b2f14-3a4c-4bd0-9d1e-2f5a7c8b9d10"))
	rateSecond = accounts.SiteID(uuid.MustParse("0b3c1d6e-7a58-4c1f-8e34-52d1a9f0c7b2"))
)

// TestSharedCountIsOneAcrossNodes: two nodes on one Valkey count one key's calls together, so the limit is the platform's
// and does not grow with the nodes; another key and another site are counted apart (internal/platform/kv/README.md
// 「多节点」, the acceptance of #315).
func TestSharedCountIsOneAcrossNodes(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace := kvtest.Namespace()
	clock := &offsetClock{}
	a, b := node(t, server, namespace, clock), node(t, server, namespace, clock)
	clock.midWindow(a.Limits.cfg.RateWindow)
	ctx := t.Context()
	for i := 1; i <= 10; i++ {
		h := a
		if i%2 == 0 {
			h = b
		}
		if got := h.countRate(ctx, rateSite, callDimension, "local", "one"); got != i {
			t.Fatalf("call %d, counted on node %d: %d", i, i%2, got)
		}
	}
	for name, count := range map[string]int{
		"another key":       a.countRate(ctx, rateSite, callDimension, "local", "two"),
		"another site":      b.countRate(ctx, rateSecond, callDimension, "local", "one"),
		"another dimension": b.countRate(ctx, rateSite, countDimension, "local", "one"),
	} {
		if count != 1 {
			t.Errorf("%s was counted with the first: %d, want 1", name, count)
		}
	}
}

// TestSharedCountMatchesTheProcessCount: for any sequence of calls and moments, the count script and RateCounter give the
// same number, because the limits they are judged against are one formula (internal/platform/kv/README.md「测试」).
// The moments are in milliseconds and jump by less than a window, by one, by two and by more, over three keys.
func TestSharedCountMatchesTheProcessCount(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	steps := []time.Duration{
		0, time.Millisecond, 7 * time.Millisecond, 999 * time.Millisecond, 17 * time.Second,
		time.Minute, 61 * time.Second, 90 * time.Second, 3 * time.Minute, 7*time.Minute + 13*time.Millisecond,
	}
	for seed := range uint64(6) {
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			clock := &offsetClock{}
			h := node(t, server, kvtest.Namespace(), clock)
			window := h.Limits.cfg.RateWindow
			clock.midWindow(window)
			reference := NewRateCounter(window)
			index, _ := reference.slot(h.KV.Now())
			reference.now, reference.start = h.KV.Now, time.UnixMilli(index*window.Milliseconds())
			random := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // a seeded sequence of inputs, repeatable
			// The windows keep their keys for two windows of the real clock, which this test does not take.
			for op := range 120 {
				clock.ahead.Add(int64(steps[random.IntN(len(steps))]))
				key := strconv.Itoa(random.IntN(3))
				want := reference.Count(key)
				if got := h.countRate(t.Context(), rateSite, callDimension, key, key); got != want {
					t.Fatalf("seed %d, call %d on key %s: shared %d, process %d", seed, op, key, got, want)
				}
			}
		})
	}
}

// TestRateKeysExpireWhateverTheClock: every key the count writes has a life, and the same life when the node's clock is
// an hour away from the server's, because the expiry is a length and not a moment (internal/platform/kv/README.md
// 「时钟」「不变量」).
func TestRateKeysExpireWhateverTheClock(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	clock := &offsetClock{}
	h := node(t, server, kvtest.Namespace(), clock)
	for _, ahead := range []time.Duration{0, time.Hour, -time.Hour} {
		clock.ahead.Store(int64(ahead))
		h.countRate(t.Context(), rateSite, callDimension, "local", strconv.FormatInt(int64(ahead), 10))
	}
	keys, err := server.Do(t, t.Context(), server.Admin.B().Keys().Pattern("ax:*").Build())[0].AsStrSlice()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("%d keys written, want one per call: %v", len(keys), keys)
	}
	window := h.Limits.cfg.RateWindow
	for _, key := range keys {
		if strings.Contains(key, "@") {
			t.Errorf("%q carries an address", key)
		}
		ttl, err := server.Do(t, t.Context(), server.Admin.B().Pttl().Key(key).Build())[0].ToInt64()
		if err != nil || ttl <= 0 || ttl > (2*window).Milliseconds() {
			t.Errorf("%q has %d ms to live (%v), want up to two windows", key, ttl, err)
		}
	}
}

// TestRateFallsBackToTheProcessCount: with the server gone the precheck keeps counting in the process and judges against
// the same full limit, so Valkey being away never refuses a call earlier than a single node would
// (internal/platform/kv/README.md「不变量」).
func TestRateFallsBackToTheProcessCount(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	clock := &offsetClock{}
	h := node(t, server, kvtest.Namespace(), clock)
	clock.midWindow(h.Limits.cfg.RateWindow)
	if got := h.countRate(t.Context(), rateSite, callDimension, "local", "one"); got != 1 {
		t.Fatalf("first call = %d", got)
	}
	server.Stop(t)
	// The first call after the server is gone counted in the process too, and those that follow count on from it.
	for want := 2; want <= 30; want++ {
		if got := h.countRate(t.Context(), rateSite, callDimension, "local", "one"); got != want {
			t.Fatalf("call %d with the server gone = %d", want, got)
		}
	}
}

// BenchmarkPrecheck is the shared count of a precheck against a real Valkey, with the breaker closed: one access, the
// allocations of the client and the script's keys included. The rule (docs/performance.md「调用路径上的规则」): the
// count is held, and bench-compare fails a pull request that raises it. AllocsPerRun cannot hold it here, as it counts the
// client's own goroutines too.
func BenchmarkPrecheck(b *testing.B) {
	server := kvtest.New(b)
	limits, err := New(DefaultConfig(), nil)
	if err != nil {
		b.Fatal(err)
	}
	h := &Handler{Limits: limits, KV: server.Open(b, kv.Deps{})}
	ctx := b.Context()
	h.countRate(ctx, rateSite, callDimension, "local", "bench") // the connection, and the key
	b.ReportAllocs()
	for b.Loop() {
		h.countRate(ctx, rateSite, callDimension, "local", "bench")
	}
}

// TestKeyRateIsJudgedOnTheSharedCount: calls another node counted are in the count a request is judged against, so a key's
// allowance is the platform's and not each node's (the acceptance of #315).
func TestKeyRateIsJudgedOnTheSharedCount(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace, clock := kvtest.Namespace(), &offsetClock{}
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) { c.KeyRate = 2 }
		o.cache = server.OpenIn(t, namespace, kv.Deps{Now: clock.Now})
	})
	other := node(t, server, namespace, clock) // opened before the clock moves: the start refuses a clock that far off
	clock.midWindow(r.cfg.RateWindow)
	a := r.newAccount(100_000_000)
	for range 2 {
		other.countRate(t.Context(), accounts.SiteID(siteID), callDimension, "other", kv.Hex(uuid.UUID(a.keyID)))
	}
	if resp, _ := r.message(a, "acme/beta"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a call after the other node's two: status %d, want 429", resp.StatusCode)
	}
	wantRefused(t, r.refused(), map[string]int64{"key_rate": 1})
}

// TestKeyRateHoldsWhileTheCacheIsDown: with Valkey gone the precheck counts in the process and the limit is the same full
// number, so the outage refuses nothing a single node would have let in (internal/platform/kv/README.md「不变量」).
func TestKeyRateHoldsWhileTheCacheIsDown(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) { c.KeyRate = 3 }
		o.cache = server.Open(t, kv.Deps{})
	})
	a := r.newAccount(100_000_000)
	server.Stop(t)
	var codes []int
	for range 4 {
		resp, _ := r.message(a, "acme/beta")
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 200 || codes[3] != 429 {
		t.Errorf("statuses %v with the cache down and a limit of 3", codes)
	}
}
