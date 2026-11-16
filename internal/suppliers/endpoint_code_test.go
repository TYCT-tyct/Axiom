package suppliers

import (
	"fmt"
	"testing"
	"time"
	"uuid"
)

func TestEndpointCode(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	id := uuid.NewV7()
	s := &Service{cfg: Config{MaxStale: time.Minute}, now: func() time.Time { return now }}
	s.snap.Store(&snapshot{confirmed: now, endpoints: map[EndpointID]*endpoint{
		id: {codename: "public-code", creds: []*credential{{}}},
	}})
	if got := s.EndpointCode(id); got != "public-code" {
		t.Fatalf("code = %q", got)
	}
	if got := s.EndpointCode(uuid.NewV7()); got != "" {
		t.Fatalf("missing endpoint = %q", got)
	}
	now = now.Add(time.Minute)
	if got := s.EndpointCode(id); got != "public-code" {
		t.Fatalf("code at stale boundary = %q", got)
	}
	now = now.Add(time.Nanosecond)
	if got := s.EndpointCode(id); got != "" {
		t.Fatalf("stale endpoint = %q", got)
	}
	s.snap.Store(&snapshot{confirmed: now, endpoints: map[EndpointID]*endpoint{}})
	if got := s.EndpointCode(id); got != "" {
		t.Fatalf("removed endpoint = %q", got)
	}
}

// The two attempts and selected endpoint are fixed; unrelated endpoints and
// their credentials must not become part of resolving their public identities.
func BenchmarkEndpointCodes(b *testing.B) {
	for _, size := range []int{10, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			now := time.Now()
			s := &Service{cfg: Config{MaxStale: time.Minute}, now: func() time.Time { return now }}
			snap := &snapshot{confirmed: now, endpoints: make(map[EndpointID]*endpoint, size)}
			ids := make([]EndpointID, size)
			for i := range ids {
				ids[i] = uuid.NewV7()
				snap.endpoints[ids[i]] = &endpoint{codename: "public-code", creds: []*credential{{}}}
			}
			s.snap.Store(snap)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if s.EndpointCode(ids[0]) != "public-code" || s.EndpointCode(ids[1]) != "public-code" || s.EndpointCode(ids[1]) != "public-code" {
					b.Fatal("missing code")
				}
			}
		})
	}
}
