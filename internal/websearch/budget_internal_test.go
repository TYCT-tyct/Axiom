package websearch

import (
	"runtime"
	"strings"
	"testing"
)

// TestParseResultsDoesNotAllocatePerEmptyObject: the response body is bounded, but the number of result objects in it
// is not. Parsing the array into a slice first would allocate one wireResult (three strings) for every empty object;
// the bounded reader must instead keep only the results that can be returned.
func TestParseResultsDoesNotAllocatePerEmptyObject(t *testing.T) {
	const n = 1 << 20
	var b strings.Builder
	b.Grow(3*n + 64)
	b.WriteString(`{"results":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteString(`,{"url":"https://example.com/last","title":"last","content":"answer"}]}`)
	body := []byte(b.String())

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := parseResults(body, MaxResults)
	if err != nil {
		t.Fatalf("parseResults() error = %v", err)
	}
	runtime.ReadMemStats(&after)
	if len(got) != 1 || got[0].URL != "https://example.com/last" {
		t.Fatalf("parseResults() = %+v, want the one valid result", got)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 24<<20 {
		t.Fatalf("parseResults() allocated %d bytes for %d empty objects, want at most 24 MiB", allocated, n)
	}
}

func TestParseResultsSkipsOverlongFields(t *testing.T) {
	body := []byte(`{"results":[{"url":"https://example.com/` + strings.Repeat("x", maxURLBytes) + `"},` +
		`{"url":"https://example.com/ok","title":"` + strings.Repeat("t", maxTitleBytes+1) + `","content":"` + strings.Repeat("c", maxContentBytes+1) + `"}]}`)
	got, err := parseResults(body, MaxResults)
	if err != nil {
		t.Fatalf("parseResults() error = %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://example.com/ok" {
		t.Fatalf("parseResults() = %+v, want only the result with a bounded URL", got)
	}
	if len(got[0].Title) != maxTitleBytes || len(got[0].Content) != maxContentBytes {
		t.Fatalf("bounded fields = %d/%d, want %d/%d", len(got[0].Title), len(got[0].Content), maxTitleBytes, maxContentBytes)
	}
}
