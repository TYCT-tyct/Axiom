package gateway

import (
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
)

func TestRateCounterSlidingWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRateCounter(time.Minute)
		for i := 1; i <= 10; i++ {
			if got := r.Count("a"); got != i {
				t.Fatalf("count %d = %d", i, got)
			}
		}
		if got := r.Count("b"); got != 1 {
			t.Fatalf("another key = %d, want 1", got)
		}
		time.Sleep(90 * time.Second) // half of the previous window is still covered: 10/2 = 5, plus this call
		if got := r.Count("a"); got != 1+5 {
			t.Fatalf("after 90s = %d, want 6", got)
		}
		time.Sleep(2 * time.Minute)
		if got := r.Count("a"); got != 1 {
			t.Fatalf("after two idle windows = %d, want 1", got)
		}
	})
}

// Cold keys must not accumulate: after two windows without them nothing is held.
func TestRateCounterDropsIdleKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRateCounter(time.Minute)
		for i := range 100 {
			r.Count(strconv.Itoa(i))
		}
		time.Sleep(2 * time.Minute)
		r.Count("x")
		if held := len(r.cur) + len(r.prev); held != 1 {
			t.Fatalf("%d keys held, want 1", held)
		}
	})
}

// The batched builder must preserve the established keys during rolling upgrades.
func TestRateKeyPairsPreserveNamespace(t *testing.T) {
	t.Parallel()
	cache, err := kv.Open(t.Context(), kv.Config{Nodes: 1}, kv.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	h := &Handler{KV: cache}
	site := accounts.SiteID{1}
	for _, tc := range []struct {
		call  *Call
		index int64
	}{{&Call{}, 0}, {&Call{}, 9223372036854775807}, {&Call{}, 1}, {&Call{}, 123456789}} {
		keys, elapsed := h.rateKeys(site, callDimension, "subject", tc.call, tc.index, 123*time.Millisecond)
		if elapsed != "123" {
			t.Fatalf("elapsed %s", elapsed)
		}
		for i, key := range keys {
			dimension, subject := callDimension, "subject"
			if i >= 2 {
				dimension, subject = "account_key", strings.Repeat("0", 32)
			}
			want := cache.Key(kv.RateSpace, uuid.UUID(site), dimension, subject, strconv.FormatInt(tc.index-int64(i%2), 10))
			if key != want {
				t.Fatalf("key %d: %s, want %s", i, key, want)
			}
		}
	}
}
