package kv_test

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// offsetClock is the real clock moved by an amount a test sets after the client has measured its offset to the server:
// the breaker's open period is then outwaited by a jump and not by sleeping, and the start does not refuse a clock that
// is far off (internal/platform/kv/README.md「熔断」, CONTRIBUTING.md「时序」).
type offsetClock struct{ ahead atomic.Int64 }

func (c *offsetClock) Now() time.Time { return time.Now().Add(time.Duration(c.ahead.Load())) }

// jump moves the clock forward.
func (c *offsetClock) jump(d time.Duration) { c.ahead.Add(int64(d)) }

// pastOpen is a jump over the breaker's open period, which is five seconds.
const pastOpen = 10 * time.Second

// readings is a meter whose instruments a test reads.
type readings struct {
	reader *sdkmetric.ManualReader
	meter  metric.Meter
}

func newReadings() readings {
	reader := sdkmetric.NewManualReader()
	return readings{reader: reader, meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("axiom/kv/test")}
}

func (r readings) collect(t *testing.T) []metricdata.Metrics {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := r.reader.Collect(t.Context(), &data); err != nil {
		t.Fatalf("collect the metrics: %v", err)
	}
	var all []metricdata.Metrics
	for _, scope := range data.ScopeMetrics {
		all = append(all, scope.Metrics...)
	}
	return all
}

// breakerOpen is axiom.kv.breaker_open: 1 while the breaker refuses every access.
func (r readings) breakerOpen(t *testing.T) int64 {
	t.Helper()
	for _, m := range r.collect(t) {
		if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == "axiom.kv.breaker_open" && len(gauge.DataPoints) > 0 {
			return gauge.DataPoints[len(gauge.DataPoints)-1].Value
		}
	}
	return 0
}

// calls is what axiom.kv.calls counted with this result.
func (r readings) calls(t *testing.T, result string) int64 {
	t.Helper()
	var total int64
	for _, m := range r.collect(t) {
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok || m.Name != "axiom.kv.calls" {
			continue
		}
		for _, point := range sum.DataPoints {
			if got, _ := point.Attributes.Value("result"); got.AsString() == result {
				total += point.Value
			}
		}
	}
	return total
}

// pause stops the server answering anyone for d, as a stall of its host would.
func pause(t *testing.T, server *kvtest.Server, d time.Duration) {
	t.Helper()
	server.Do(t, t.Context(), server.Admin.B().ClientPause().Timeout(d.Milliseconds()).All().Build())
}

// untilCounts runs the count until it succeeds, with a jump over the breaker's open period before each: the pause or the
// restart is the server's, in real time, and what the test waits for is the breaker's probe finding it back.
func untilCounts(t *testing.T, clock *offsetClock, client *kv.Client, script kv.Script) []int64 {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		clock.jump(pastOpen)
		counts, err := count(t, client, script, 100)
		if err == nil {
			return counts
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client never reached the server again: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTimeoutReturnsAndTheBreakerRecovers: a paused server does not answer, so every access returns unavailable at the
// deadline, the breaker opens, and once the server answers the half-open probe closes it. The test does not time the
// deadline: an access that waited for the server instead would succeed after the pause, which the test refuses
// (internal/platform/kv/README.md「测试」).
func TestTimeoutReturnsAndTheBreakerRecovers(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	clock, seen := &offsetClock{}, newReadings()
	client := server.Open(t, kv.Deps{Now: clock.Now, Meter: seen.meter})
	script := kv.NewScript(countScript)
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
	pause(t, server, 3*time.Second)
	for i := range 32 {
		if _, err := count(t, client, script, 100); !errors.Is(err, kv.ErrUnavailable) {
			t.Fatalf("access %d against a paused server: %v", i, err)
		}
	}
	if got := seen.breakerOpen(t); got != 1 {
		t.Fatalf("breaker_open = %d after 32 accesses that timed out", got)
	}
	if counts := untilCounts(t, clock, client, script); len(counts) != 2 {
		t.Fatalf("count after the pause = %v", counts)
	}
	if got := seen.breakerOpen(t); got != 0 {
		t.Fatalf("breaker_open = %d after the probe succeeded", got)
	}
}

// TestCallerGivingUpDoesNotOpenTheBreaker: accesses the caller abandons, by its own deadline or its own cancel, are
// skipped and say nothing about the server, or a client that sends requests and hangs up could open the breaker for
// everyone (README「熔断」).
func TestCallerGivingUpDoesNotOpenTheBreaker(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	seen := newReadings()
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	script := kv.NewScript(countScript)
	pause(t, server, 3*time.Second)
	for range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond) // earlier than kv.Timeout, so the caller's
		_, err := client.Do(ctx, kv.Precheck, script.Cmd(window(client, 100), "120000"))
		cancel()
		if !errors.Is(err, kv.ErrUnavailable) {
			t.Fatalf("an access the caller gave up on: %v", err)
		}
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := client.Do(cancelled, kv.Precheck, script.Cmd(window(client, 100), "120000")); !errors.Is(err, kv.ErrUnavailable) {
			t.Fatalf("an access after the caller cancelled: %v", err)
		}
	}
	if got := seen.calls(t, "skipped"); got != 40 {
		t.Errorf("%d accesses counted as skipped, want 40", got)
	}
	if got := seen.breakerOpen(t); got != 0 {
		t.Fatalf("callers giving up opened the breaker: breaker_open = %d", got)
	}
}

// TestEveryAccessReturns: far more concurrent accesses than the client's ring has slots, 4096, against a server that
// answers nobody, must each return. A full ring blocks in PutOne, which ignores the context, so what bounds it is the
// breaker and the connection's own write timeout (README「客户端与连接」).
func TestEveryAccessReturns(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	client := server.Open(t, kv.Deps{})
	script := kv.NewScript(countScript)
	keys := window(client, 100)
	pause(t, server, 3*time.Second)
	const concurrent = 5000
	var group sync.WaitGroup
	group.Add(concurrent)
	for range concurrent {
		go func() {
			defer group.Done()
			if _, err := client.Do(t.Context(), kv.Precheck, script.Cmd(keys, "120000")); err != nil && !errors.Is(err, kv.ErrUnavailable) {
				t.Errorf("access: %v", err)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		group.Wait()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute): // only catches an access that never returns; it is no measure of how long one takes
		t.Fatal("an access never returned")
	}
}

// TestRestartRecovers: a server that stops and starts again is an outage the client falls back through, and the first
// access after the breaker's open period reconnects by itself. The dataset is empty, which a deployment's restart is
// (README「不持久化」).
func TestRestartRecovers(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	clock := &offsetClock{}
	client := server.Open(t, kv.Deps{Now: clock.Now})
	script := kv.NewScript(countScript)
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
	server.Stop(t)
	for i := range 4 {
		if _, err := count(t, client, script, 100); !errors.Is(err, kv.ErrUnavailable) {
			t.Fatalf("access %d succeeded against a stopped server: %v", i, err)
		}
	}
	server.Start(t)
	if counts := untilCounts(t, clock, client, script); counts[0] != 1 || counts[1] != 0 {
		t.Fatalf("the restarted server kept the count: %v", counts)
	}
}

// TestDeferredChecksReportOnly: a start that could not reach the server owes the checks that need one; the first answer
// runs them, and a server that would fail them is reported and not refused, because a node may not stop serving over a
// mistake it already survived the start with (README「配置与启动」).
func TestDeferredChecksReportOnly(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t, kvtest.Policy("allkeys-lru"))
	server.Stop(t)
	clock, logs := &offsetClock{}, &lockedBuffer{}
	client, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()),
		kv.Deps{Now: clock.Now, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatalf("a node whose server is stopped refused to start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(t, client) })
	server.Start(t)
	deadline := time.Now().Add(time.Minute)
	for {
		clock.jump(pastOpen)
		if err := client.Measure(t.Context()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the client never reached the server")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(logs.String(), "maxmemory_policy") {
		t.Errorf("the policy the start would have refused was not reported: %s", logs.String())
	}
	if _, err := count(t, client, kv.NewScript(countScript), 100); err != nil {
		t.Fatalf("the node stopped serving over a policy it only reports: %v", err)
	}
}

// lockedBuffer is a log destination that the client's loop and the test both touch.
type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// TestMemoryPolicyIsChecked: a server that may evict a key stops the start, naming the variable it was given
// (README「内存」).
func TestMemoryPolicyIsChecked(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t, kvtest.Policy("allkeys-lru"))
	_, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kv.Deps{})
	if err == nil {
		t.Fatal("a server whose policy would evict a key started")
	}
	for _, want := range []string{kv.URLVar, "noeviction", "allkeys-lru"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

// TestOOMFallsBack: a full server refuses a new write, which the caller reads as unavailable and falls back on, but which
// is the server's answer and not an outage: the breaker stays closed, so reads and the counts that fit keep working. The
// keys that were already there keep their values: noeviction is what makes a count survive a flood of new keys
// (README「内存」「不变量」).
func TestOOMFallsBack(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t, kvtest.Memory("3mb"))
	seen := newReadings()
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	script := kv.NewScript(countScript)
	kept := window(client, 100)
	if _, err := client.Do(t.Context(), kv.Precheck, script.Cmd(kept, "600000")); err != nil {
		t.Fatalf("write the key that must survive: %v", err)
	}
	// Fill the server a kilobyte at a time until it refuses to take more. The last key it accepted took it over its limit,
	// and only then does it refuse a write of any size: a refused 16 KiB value would only have been too big to fit.
	filler, full := strings.Repeat("x", 1<<10), false
	for i := range 1 << 14 {
		result := server.Admin.Do(t.Context(),
			server.Admin.B().Set().Key("ax:fill:"+strconv.Itoa(i)).Value(filler).Build())
		if err := result.Error(); err != nil {
			full = true
			break
		}
	}
	if !full {
		used, err := server.Do(t, t.Context(), server.Admin.B().Info().Section("memory").Build())[0].ToString()
		t.Fatalf("16 MiB went into a server limited to 3 MiB without a refusal (%v): %s", err, used)
	}
	const refused = 10 // more than the breaker's streak of unavailable accesses
	for i := range refused {
		_, err := client.Do(t.Context(), kv.Precheck, script.Cmd(window(client, 101+i), "600000"))
		if !errors.Is(err, kv.ErrUnavailable) || !strings.Contains(err.Error(), "OOM") {
			t.Fatalf("a full server answered a write with %v", err)
		}
	}
	if got := seen.calls(t, "error"); got != refused {
		t.Errorf("%d refused writes counted as errors, want %d", got, refused)
	}
	if got := seen.breakerOpen(t); got != 0 {
		t.Fatalf("a server that answers OOM opened the breaker: breaker_open = %d", got)
	}
	// The key written first is still there, with its count: nothing evicted it to make room.
	got, err := server.Do(t, t.Context(), server.Admin.B().Get().Key(kept[0]).Build())[0].ToString()
	if err != nil || got != "1" {
		t.Fatalf("the key that must survive = %q, %v", got, err)
	}
}
