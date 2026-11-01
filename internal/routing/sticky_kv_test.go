package routing

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

func TestStickyKeysAndValues(t *testing.T) {
	t.Parallel()
	cache := kvtest.New(t).Open(t, kv.Deps{})
	site := uuid.UUID{1}
	session := SessionKey{2}
	key := StickyKey(cache, site, session)
	if !strings.HasSuffix(key, ":stick:"+kv.Hex(site)+":02"+strings.Repeat("0", 62)) {
		t.Fatalf("wrong binding key: %s", key)
	}
	if key == StickyKey(cache, uuid.UUID{2}, session) || key == StickyKey(cache, site, SessionKey{3}) {
		t.Fatal("bindings share a key")
	}
	endpoint := EndpointID{3}
	for _, text := range []string{"", "bad", strings.Repeat("z", 32), kv.Hex(uuid.UUID(endpoint))} {
		values, err := cache.Do(t.Context(), kv.Precheck, kv.Set(key, text, time.Hour), kv.Get(key))
		if err != nil {
			t.Fatal(err)
		}
		want := EndpointID{}
		if text == kv.Hex(uuid.UUID(endpoint)) {
			want = endpoint
		}
		if got := StickyEndpoint(values[1]); got != want {
			t.Fatalf("binding %q: %v, want %v", text, got, want)
		}
	}
	values, err := cache.Do(t.Context(), kv.Precheck, kv.Delete(key), kv.Get(key))
	if err != nil || !values[1].Missing() || StickyEndpoint(values[1]) != (EndpointID{}) {
		t.Fatalf("deleted binding was not an ordinary miss: %v", err)
	}
}

func TestStickyLifetime(t *testing.T) {
	t.Parallel()
	// The life is a read, as the shared server's admin user may do (README「权限」).
	server := kvtest.New(t)
	cache := server.Open(t, kv.Deps{})
	router, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	key := StickyKey(cache, uuid.UUID{1}, SessionKey{2})
	endpoint := EndpointID{3}
	for _, ttl := range []time.Duration{0, 2 * time.Minute} {
		if _, err := cache.Do(t.Context(), kv.StickyWrite, router.StickyWrite(key, endpoint, ttl)); err != nil {
			t.Fatal(err)
		}
		life, err := server.TTL(t, t.Context(), key)
		want := ttl.Milliseconds()
		if ttl == 0 {
			want = time.Hour.Milliseconds()
		}
		if err != nil || life <= 0 || life > want {
			t.Fatalf("ttl %v: life %d, err %v", ttl, life, err)
		}
		values, err := cache.Do(t.Context(), kv.Precheck, kv.Get(key))
		if err != nil || StickyEndpoint(values[0]) != endpoint {
			t.Fatalf("ttl %v: endpoint %v, err %v", ttl, StickyEndpoint(values[0]), err)
		}
	}
}
