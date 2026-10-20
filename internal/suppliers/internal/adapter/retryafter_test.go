package adapter_test

import (
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

func TestRetryAfterFormsAndBounds(t *testing.T) {
	// synctest's clock starts at this instant in every bubble, so the dates below are relative to the bubble's now.
	now := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, value string
		want        time.Duration
	}{
		{"seconds", "30", 30 * time.Second},
		{"whitespace", " 30 ", 30 * time.Second},
		{"date", now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{"past date", now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"date now", now.Format(http.TimeFormat), 0},
		{"missing", "", 0},
		{"invalid", "tomorrow", 0},
		{"negative", "-30", 0},
		{"zero", "0", 0},
		{"fraction", "1.5", 0},
		{"non-finite", "NaN", 0},
		{"large seconds", "86400", 24 * time.Hour},
		{"over bound", "86401", 24 * time.Hour},
		{"duration overflow", "9223372036854775807", 24 * time.Hour},
		{"integer overflow", strings.Repeat("9", 100), 24 * time.Hour},
		{"malformed huge", strings.Repeat("9", 100) + "x", 0},
		{"distant date", now.AddDate(100, 0, 0).Format(http.TimeFormat), 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if got := adapter.RetryAfter(http.Header{"Retry-After": {tc.value}}); got != tc.want {
					t.Fatalf("Retry-After %q: %v, want %v", tc.value, got, tc.want)
				}
			})
		})
	}
}
