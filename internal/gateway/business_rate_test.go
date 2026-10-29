package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

func TestBusinessRateInheritance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, tier, account, key   string
		protection, allowed, limit int
	}{
		{"tier default", "2", "NULL", "NULL", 100, 2, 2},
		{"account overrides tier", "1", "3", "NULL", 100, 3, 3},
		{"key tightens account", "5", "3", "1", 100, 1, 1},
		{"unlimited key retains account", "5", "2", "0", 100, 2, 2},
		{"unlimited account retains key", "1", "0", "2", 100, 2, 2},
		{"unlimited retains node protection", "0", "NULL", "NULL", 2, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.CountRate = tc.protection } })
			a := r.newAccount(10e9)
			r.sql("UPDATE accounts.tiers SET rate_requests = "+tc.tier,
				"UPDATE accounts.accounts SET rate_requests = "+tc.account,
				"UPDATE accounts.api_keys SET rate_requests = "+tc.key)
			for n := 0; n <= tc.allowed; n++ {
				resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
				if n < tc.allowed {
					requireRateResponse(t, resp, body, 0)
				} else {
					requireRateResponse(t, resp, body, tc.limit)
				}
			}
			// Policies are read freshly even when the immutable key identity was cached.
			r.sql("UPDATE accounts.api_keys SET rate_requests = 0", "UPDATE accounts.accounts SET rate_requests = 0")
			if tc.protection > tc.allowed {
				resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
				requireRateResponse(t, resp, body, 0)
			}
		})
	}
}

func TestBusinessAccountRateAggregatesKeys(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := r.newAccount(10e9), r.newAccount(10e9)
	second, _ := r.newKey(a)
	r.sql("UPDATE accounts.tiers SET rate_requests = 2")
	for i, key := range []string{a.key, second, a.key, b.key} {
		resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", key, strings.NewReader(countBody))
		want := http.StatusOK
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if resp.StatusCode != want {
			t.Fatalf("request %d: %d %s", i, resp.StatusCode, body)
		}
	}
	// Calls have their own allowance, even after count_tokens exhausts its pool.
	if resp, body := r.message(a, "acme/beta"); resp.StatusCode != http.StatusOK {
		t.Fatalf("call: %d %s", resp.StatusCode, body)
	}
}

func TestNodeRateRejectsPreserveAccountPool(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"local", "shared", "outage"} {
		for _, dimension := range []string{callDimension, countDimension} {
			t.Run(mode+"/"+dimension, func(t *testing.T) {
				t.Parallel()
				testNodeRateAccountIsolation(t, mode, dimension)
			})
		}
	}
}

func testNodeRateAccountIsolation(t *testing.T, mode, dimension string) {
	t.Helper()
	var server *kvtest.Server
	var cache *kv.Client
	if mode != "local" {
		server = kvtest.Own(t)
		cache = server.OpenIn(t, kvtest.Namespace(), kv.Deps{})
	}
	r := newRig(t, func(o *rigOptions) {
		o.cache = cache
		o.config = func(c *Config) { c.KeyRate, c.CountRate = 2, 2 }
	})
	a := r.newAccount(10e9)
	second, _ := r.newKey(a)
	r.sql("UPDATE accounts.accounts SET rate_requests = 3", "UPDATE accounts.api_keys SET rate_requests = 0")
	send := func(key string) (reply, string) {
		if dimension == countDimension {
			return r.do(http.MethodPost, "/api/v1/messages/count_tokens", key, strings.NewReader(countBody))
		}
		c := a
		c.key = key
		return r.message(c, "acme/beta")
	}
	for n := range 6 {
		if mode == "outage" && n == 2 {
			server.Stop(t)
		}
		resp, body := send(a.key)
		limit := 0
		if n >= 2 {
			limit = 2
		}
		requireRateResponse(t, resp, body, limit)
	}
	resp, body := send(second)
	requireRateResponse(t, resp, body, 0)
}

func TestBusinessKeyRejectionsConsumeAccountPool(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(10e9)
	second, _ := r.newKey(a)
	r.sql("UPDATE accounts.accounts SET rate_requests = 3", "UPDATE accounts.api_keys SET rate_requests = 1")
	for n := range 3 {
		resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
		limit := 0
		if n > 0 {
			limit = 1
		}
		requireRateResponse(t, resp, body, limit)
	}
	resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", second, strings.NewReader(countBody))
	requireRateResponse(t, resp, body, 3)
}

func TestBusinessRateConcurrentAdmission(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(10e9)
	r.sql("UPDATE accounts.accounts SET rate_requests = 5")
	second, _ := r.newKey(a)
	if _, err := r.accounts.AuthenticateKey(t.Context(), accounts.SiteID(siteID), second); err != nil {
		t.Fatal(err)
	}
	keys := []string{a.key, second}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		key := keys[i%len(keys)]
		wg.Go(func() {
			resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", key, strings.NewReader(countBody))
			switch resp.StatusCode {
			case http.StatusOK:
				allowed.Add(1)
			case http.StatusTooManyRequests:
			default:
				t.Errorf("%d %s", resp.StatusCode, body)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 5 {
		t.Fatalf("allowed %d of 20, want 5", n)
	}
}

func TestBusinessRateSharedAndFallback(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	namespace, clock := kvtest.Namespace(), newFrozenClock()
	reader := sdkmetric.NewManualReader()
	cache := server.OpenIn(t, namespace, kv.Deps{Now: clock.Now, Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")})
	r := newRig(t, func(o *rigOptions) { o.cache = cache })
	a := r.newAccount(10e9)
	r.sql("UPDATE accounts.accounts SET rate_requests = 2")
	other := node(t, server, namespace, clock)
	otherKey, _ := r.newKey(a)
	caller, err := r.accounts.AuthenticateKey(t.Context(), accounts.SiteID(siteID), otherKey)
	if err != nil {
		t.Fatal(err)
	}
	clock.midWindow(cache, r.cfg.RateWindow)
	first := &Call{caller: caller}
	other.precheck(t.Context(), accounts.SiteID(siteID), countDimension, countRateKey(caller), keyHex(caller), first, false)
	before := stickyCalls(t, reader, kv.Precheck)
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
		if resp.StatusCode != want {
			t.Fatalf("shared request %d: %d %s", i, resp.StatusCode, body)
		}
	}
	if accesses := stickyCalls(t, reader, kv.Precheck) - before; accesses != 2 {
		t.Fatalf("%d accesses for two requests", accesses)
	}
	before = stickyCalls(t, reader, kv.Precheck)
	if resp, body := r.message(a, "acme/beta"); resp.StatusCode != http.StatusOK {
		t.Fatalf("shared inference: %d %s", resp.StatusCode, body)
	}
	if accesses := stickyCalls(t, reader, kv.Precheck) - before; accesses != 1 {
		t.Fatalf("inference used %d cache accesses", accesses)
	}
	// Both requests advanced the local account pool, even while shared storage was healthy.
	server.Stop(t)
	resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fallback lost its history: %d %s", resp.StatusCode, body)
	}
	// No stickiness or second lookup is needed to count both dimensions on inference either.
	if resp, body := r.message(a, "acme/beta"); resp.StatusCode != http.StatusOK {
		t.Fatalf("call during outage: %d %s", resp.StatusCode, body)
	}
}

func TestBusinessRateRejectsBeforeReservation(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"accounts", "api_keys"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.newAccount(10e9)
			r.sql("UPDATE accounts.accounts SET rate_requests = 0", "UPDATE accounts."+target+" SET rate_requests = 1")
			if resp, body := r.message(a, "acme/beta"); resp.StatusCode != http.StatusOK {
				t.Fatalf("first: %d %s", resp.StatusCode, body)
			}
			before := r.count(allRows)
			resp, body := r.message(a, "acme/beta")
			if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-Ratelimit-Limit") != "1" || r.count(allRows) != before {
				t.Fatalf("refusal reserved or reached upstream: %d %s %v", resp.StatusCode, body, resp.Header)
			}
		})
	}
}

func requireRateResponse(t *testing.T, resp reply, body string, limit int) {
	t.Helper()
	if limit == 0 {
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Ratelimit-Limit") != "" {
			t.Fatalf("success: %d %s %v", resp.StatusCode, body, resp.Header)
		}
		return
	}
	reset, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-Ratelimit-Limit") != strconv.Itoa(limit) || resp.Header.Get("X-Ratelimit-Remaining") != "0" || resp.Header.Get("Retry-After") == "" || err != nil || reset < time.Now().Unix() {
		t.Fatalf("refusal: %d %s %v", resp.StatusCode, body, resp.Header)
	}
}

func TestCountRateRejectsWithoutDatabase(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.CountRate = 1 } })
	a := r.newAccount(10e9)
	resp, body := r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
	requireRateResponse(t, resp, body, 0)
	r.db.Close()
	resp, body = r.do(http.MethodPost, "/api/v1/messages/count_tokens", a.key, strings.NewReader(countBody))
	requireRateResponse(t, resp, body, 1)
}
