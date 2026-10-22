package kv_test

import (
	"context"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// The stop is driven by an actual INFO reading, not just the flag the writer observes. The server remains available
// and existing bindings readable; lowering usage below the hysteresis floor permits writes again.
func TestStickyMemoryStopsFromServerReading(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	seen := newReadings()
	client := server.Open(t, kv.Deps{Meter: seen.meter})
	key := client.Key(kv.StickySpace, site, "memory")
	if _, err := client.Do(t.Context(), kv.Precheck, kv.Set(key, "original", time.Hour)); err != nil {
		t.Fatal(err)
	}
	used := usedMemory(t, server)
	server.Do(t, t.Context(), server.Admin.B().ConfigSet().ParameterValue().ParameterValue("maxmemory", strconv.FormatInt(used, 10)).Build())
	if err := client.Measure(t.Context()); err != nil {
		t.Fatal(err)
	}
	client.Later(uuid.UUID{1}, kv.Set(key, "replacement", time.Hour))
	count := seen.total(t, "axiom.kv.dropped")
	values, err := client.Do(t.Context(), kv.Precheck, kv.Get(key))
	if err != nil {
		t.Fatal(err)
	}
	value, err := values[0].String()
	if count != 1 || value != "original" || err != nil {
		t.Fatalf("memory stop: dropped %d, value %q, err %v", count, value, err)
	}
	server.Do(t, t.Context(), server.Admin.B().ConfigSet().ParameterValue().ParameterValue("maxmemory", "268435456").Build())
	if err := client.Measure(t.Context()); err != nil {
		t.Fatal(err)
	}
	client.Later(uuid.UUID{1}, kv.Set(key, "replacement", time.Hour))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for {
		values, err = client.Do(ctx, kv.Precheck, kv.Get(key))
		if err != nil {
			t.Fatal(err)
		}
		value, err = values[0].String()
		if err != nil {
			t.Fatal(err)
		}
		if value == "replacement" {
			break
		}
	}
}
