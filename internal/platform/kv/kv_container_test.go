package kv_test

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// countScript is the count script of a rate counter, written here as a module writes it: the shared state holds no rule
// of its own (internal/platform/kv/README.md「接口」).
const countScript = `
local counts = {}
for i = 1, #KEYS, 2 do
  local current = redis.call('INCR', KEYS[i])
  if current == 1 then redis.call('PEXPIRE', KEYS[i], ARGV[1]) end
  local previous = redis.call('GET', KEYS[i + 1]) or 0
  counts[#counts + 1] = current
  counts[#counts + 1] = tonumber(previous)
end
return counts`

// site is one site's ID for the keys under test.
var site = uuid.MustParse("6f1b2f14-3a4c-4bd0-9d1e-2f5a7c8b9d10")

// window is one window's two keys, as a module builds them: this window's and the one before.
func window(c *kv.Client, index int) []string {
	return []string{
		c.Key(kv.RateSpace, site, "key", "subject", strconv.Itoa(index)),
		c.Key(kv.RateSpace, site, "key", "subject", strconv.Itoa(index-1)),
	}
}

// count runs the script once and returns its pair of counts.
func count(t *testing.T, client *kv.Client, script kv.Script, index int) ([]int64, error) {
	t.Helper()
	values, err := client.Do(t.Context(), kv.Precheck, script.Cmd(window(client, index), "120000"))
	if err != nil {
		return nil, err
	}
	return values[0].Ints()
}

// TestCountRuns: the batch runs as one round trip and its reply is the pair of counts, this window's and the previous
// one's, which is read and not written.
func TestCountRuns(t *testing.T) {
	t.Parallel()
	client := kvtest.New(t).Open(t, kv.Deps{})
	script := kv.NewScript(countScript)
	for i, step := range []struct {
		window int
		want   [2]int64
	}{{100, [2]int64{1, 0}}, {100, [2]int64{2, 0}}, {99, [2]int64{1, 0}}, {100, [2]int64{3, 1}}} {
		counts, err := count(t, client, script, step.window)
		if err != nil || len(counts) != 2 || counts[0] != step.want[0] || counts[1] != step.want[1] {
			t.Fatalf("step %d, window %d: counts = %v, %v, want %v", i, step.window, counts, err, step.want)
		}
	}
}

// TestKeysExpire: the key a count wrote carries the relative life the script was given, so nothing outlives the two
// windows it is read in, and the life does not depend on any clock (README「不变量」).
func TestKeysExpire(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	client := server.Open(t, kv.Deps{})
	script := kv.NewScript(countScript)
	const ttl = 120000 // milliseconds, as the script's argument
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
	key := window(client, 100)[0]
	ms, err := server.Do(t, t.Context(), server.Admin.B().Pttl().Key(key).Build())[0].ToInt64()
	if err != nil {
		t.Fatalf("read %q's life: %v", key, err)
	}
	// The second key is only read, so it has no life of its own; the first was written with the argument's.
	if ms <= 0 || ms > ttl {
		t.Fatalf("%q has %d ms to live, want up to %d", key, ms, ttl)
	}
}

// TestClockTooFarRefusesTheStart: a node whose clock is a second or more from the server's is a deployment mistake, and
// its windows would be computed at the wrong moment (README「配置与启动」「时钟」).
func TestClockTooFarRefusesTheStart(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	_, err := kv.Open(t.Context(), server.Config(kvtest.Namespace()),
		kvtest.Deps(kv.Deps{Now: func() time.Time { return time.Now().Add(time.Hour) }}))
	if err == nil {
		t.Fatal("a node an hour ahead of the server started")
	}
	for _, want := range []string{kv.URLVar, "away from the server's"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

// TestASmallRealSkewStarts: a node whose clock runs a little behind a real server's starts and counts. What the offset is,
// and what is refused, is decided without a real server's moving clock in TestTheOffsetToTheServerIsMeasuredAndKept; this
// is the real server accepting what was measured, and asserts nothing that depends on how long the test took.
func TestASmallRealSkewStarts(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	client := server.Open(t, kv.Deps{Now: func() time.Time { return time.Now().Add(-300 * time.Millisecond) }})
	if _, err := count(t, client, kv.NewScript(countScript), 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
}

// TestOneBatchPerCall: a precheck is one access, whatever the batch holds, because the first token's path may afford
// one round trip and no more (README「不变量」).
func TestOneBatchPerCall(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	reader := sdkmetric.NewManualReader()
	client := server.Open(t, kv.Deps{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("axiom/kv/test")})
	script := kv.NewScript(countScript)
	before := accesses(t, reader)
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
	if got := accesses(t, reader) - before; got != 1 {
		t.Fatalf("one command counted as %d accesses", got)
	}
	keys := window(client, 100)
	if _, err := client.Do(t.Context(), kv.Precheck, script.Cmd(keys, "120000"), script.Cmd(keys, "120000")); err != nil {
		t.Fatalf("run two commands: %v", err)
	}
	if got := accesses(t, reader) - before; got != 2 {
		t.Fatalf("two batches counted as %d accesses", got)
	}
}

// TestScriptFlushCostsNoSecondRoundTrip: the server forgetting its scripts (a restart, a failover, SCRIPT FLUSH) does not
// make the next precheck two round trips, because every access sends the script itself and never asks for it by its hash
// (README「不变量」).
func TestScriptFlushCostsNoSecondRoundTrip(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	seen := newReadings()
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	script := kv.NewScript(countScript)
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count: %v", err)
	}
	server.Do(t, t.Context(), server.Admin.B().ScriptFlush().Build())
	before := accesses(t, seen.reader)
	if _, err := count(t, client, script, 100); err != nil {
		t.Fatalf("run the count after the flush: %v", err)
	}
	if got := accesses(t, seen.reader) - before; got != 1 {
		t.Fatalf("the precheck after a script flush counted as %d accesses, want 1", got)
	}
}

// accesses is what axiom.kv.calls counted so far for the precheck, over every result: the client's own readings of the
// server are counted apart.
func accesses(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatalf("collect the metrics: %v", err)
	}
	var total int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "axiom.kv.calls" {
				continue
			}
			for _, point := range sum.DataPoints {
				if use, _ := point.Attributes.Value("use"); use.AsString() == string(kv.Precheck) {
					total += point.Value
				}
			}
		}
	}
	return total
}
