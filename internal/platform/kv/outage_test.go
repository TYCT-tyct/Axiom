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

// memory is axiom.kv.memory_used_bytes as last written.
func (r readings) memory(t *testing.T) int64 {
	t.Helper()
	for _, m := range r.collect(t) {
		if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == "axiom.kv.memory_used_bytes" && len(gauge.DataPoints) > 0 {
			return gauge.DataPoints[len(gauge.DataPoints)-1].Value
		}
	}
	return 0
}

// total is a counter's sum over its points.
func (r readings) total(t *testing.T, name string) int64 {
	t.Helper()
	var total int64
	for _, m := range r.collect(t) {
		if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == name {
			for _, point := range sum.DataPoints {
				total += point.Value
			}
		}
	}
	return total
}

// pauseWrites makes the server hold every command that may write, as a server stalled on its disk does, until the test
// lets them go with the function it returns (and at the latest when the test ends). Reads and the client's own readings
// keep answering, so the connection stays up while the scripts wait in it. Unlike a pause for a length of time, nothing
// here depends on how long the test takes.
func pauseWrites(t *testing.T, server *kvtest.Server) (resume func()) {
	t.Helper()
	server.Do(t, t.Context(), server.Admin.B().ClientPause().Timeout(time.Hour.Milliseconds()).Write().Build())
	var once sync.Once
	resume = func() {
		once.Do(func() {
			if err := server.Admin.Do(context.WithoutCancel(t.Context()), server.Admin.B().ClientUnpause().Build()).Error(); err != nil {
				t.Errorf("resume the server: %v", err)
			}
		})
	}
	t.Cleanup(resume)
	return resume
}

// untilCounts runs the count until it succeeds, with a jump over the breaker's open period before each: what the test
// waits for is the breaker's probe finding the server back.
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

// TestCallerGivingUpDoesNotOpenTheBreaker: accesses the caller abandons, by its own deadline or its own cancel, are
// skipped and say nothing about the server, or a client that sends requests and hangs up could open the breaker for
// everyone (README「熔断」).
func TestCallerGivingUpDoesNotOpenTheBreaker(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	seen := newReadings()
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	script := kv.NewScript(countScript)
	pauseWrites(t, server)
	for range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond) // earlier than the client's deadline
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

// TestEveryAccessReturns: far more concurrent accesses than a connection's ring has slots, against a server whose scripts
// wait, must each return, and no more of them may reach the server than the client lets be out: the ring waits for a slot
// without looking at any context, so what keeps a call from waiting there is that the client never fills it. Calls are
// bounded by caller deadlines while writes are paused (README「客户端与连接」).
func TestEveryAccessReturns(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	client := server.Open(t, kv.Deps{})
	script := kv.NewScript(countScript)
	keys := window(client, 100)
	resume := pauseWrites(t, server)
	const concurrent = 5000
	var group sync.WaitGroup
	group.Add(concurrent)
	for range concurrent {
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(t.Context(), kv.Timeout)
			defer cancel()
			if _, err := client.Do(ctx, kv.Precheck, script.Cmd(keys, "120000")); !errors.Is(err, kv.ErrUnavailable) {
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
	// The scripts that were out run when the server resumes; the key counts them.
	resume()
	got := untilStable(t, server, keys[0])
	if got < 1 || got > kv.MaxOutstanding {
		t.Fatalf("%d of %d accesses reached the server, want between 1 and the %d the client lets be out", got, concurrent, kv.MaxOutstanding)
	}
}

// untilStable is the value of the counter key once it has stopped moving: the scripts the paused server held run in the
// background after the resume, and a count taken in the middle of them would only be smaller.
func untilStable(t *testing.T, server *kvtest.Server, key string) int {
	t.Helper()
	read := func() int {
		got, err := server.Admin.Do(t.Context(), server.Admin.B().Get().Key(key).Build()).AsInt64()
		if err != nil {
			return 0 // no script has run yet
		}
		return int(got)
	}
	deadline := time.Now().Add(time.Minute)
	for last, same := read(), 0; ; {
		time.Sleep(50 * time.Millisecond)
		now := read()
		if now != last || now == 0 {
			last, same = now, 0
		} else if same++; same == 4 {
			return now
		}
		if time.Now().After(deadline) {
			t.Fatalf("the counter never settled: %d", now)
		}
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
		kvtest.Deps(kv.Deps{Now: clock.Now, Logger: slog.New(slog.NewTextHandler(logs, nil))}))
	if err != nil {
		t.Fatalf("a node whose server is stopped refused to start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(t, client) })
	client.StopChecks()
	server.Start(t)
	// The failed initial dial is not cached by valkey-go. Start waits for readiness, and stopping the background checks
	// leaves the half-open probe to this measurement; neither retries nor elapsed time decide when to check the policy.
	clock.jump(pastOpen)
	if err := client.Measure(t.Context()); err != nil {
		t.Fatalf("measure the restarted server: %v", err)
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
	_, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()), kvtest.Deps(kv.Deps{}))
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
	// The server's answer is what is under test, not how soon it comes: kvtest gives the client a wide deadline, so a busy
	// machine cannot turn the answer into a timeout.
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	script := kv.NewScript(countScript)
	kept := window(client, 100)
	if _, err := client.Do(t.Context(), kv.Precheck, script.Cmd(kept, "600000")); err != nil {
		t.Fatalf("write the key that must survive: %v", err)
	}
	fillUp(t, server)
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
	if got := seen.total(t, "axiom.kv.rejected_oom"); got != refused {
		t.Errorf("rejected_oom = %d, want the %d refusals", got, refused)
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

// usedMemory is the memory the server reports as used, from INFO memory.
func usedMemory(t *testing.T, server *kvtest.Server) int64 {
	t.Helper()
	info, err := server.Do(t, t.Context(), server.Admin.B().Info().Section("memory").Build())[0].ToString()
	if err != nil {
		t.Fatalf("read the server's memory: %v", err)
	}
	for line := range strings.SplitSeq(info, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "used_memory:"); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				t.Fatalf("read used_memory %q: %v", value, err)
			}
			return n
		}
	}
	t.Fatal("INFO memory has no used_memory")
	return 0
}

// fillUp puts the server's data over its memory limit and checks that it is. A write is refused when the server's memory,
// counting the buffer the write arrived in, is over the limit, so filling with writes alone ends with the data hovering at
// the limit, where one write is refused and the next fits again. So it fills to two thirds of the limit with writes, and
// then makes a value larger than the whole limit with one small command, SETRANGE at a large offset, which the server
// takes because it is under the limit when the command starts. The data is over the limit then, and stays over: a small
// write must be refused, or the test stops.
func fillUp(t *testing.T, server *kvtest.Server) {
	t.Helper()
	filler := strings.Repeat("x", 1<<10)
	for i := 0; usedMemory(t, server) < 2<<20; i++ { // 2 MiB of the 3 MiB the test gives the server
		if i == 1<<14 {
			t.Fatal("16 MiB of writes did not fill a server limited to 3 MiB")
		}
		server.Do(t, t.Context(), server.Admin.B().Set().Key("ax:fill:"+strconv.Itoa(i)).Value(filler).Build())
	}
	server.Do(t, t.Context(), server.Admin.B().Setrange().Key("ax:fill:big").Offset(8<<20).Value("x").Build())
	err := server.Admin.Do(t.Context(), server.Admin.B().Set().Key("ax:fill:probe").Value("x").Build()).Error()
	if err == nil || !strings.HasPrefix(err.Error(), "OOM") {
		t.Fatalf("the server did not stay over its limit: a small write got %v, with %d bytes used", err, usedMemory(t, server))
	}
}
