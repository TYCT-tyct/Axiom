package quality

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func TestProbeSettingsCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings ProbeSettings
		field    string
	}{
		{"empty", ProbeSettings{}, ""},
		{"all", ProbeSettings{HourlyBudget: 120, Concurrency: 2, VerifyEvery: time.Hour, VerifyMaxAge: 2 * time.Hour, VerifyRetry: 10 * time.Minute, ProbeBase: 10 * time.Second}, ""},
		{"no budget", ProbeSettings{HourlyBudget: -1}, "hourly_budget"},
		{"budget over the bound", ProbeSettings{HourlyBudget: 100_001}, "hourly_budget"},
		{"no concurrency", ProbeSettings{Concurrency: -1}, "concurrency"},
		{"concurrency over the bound", ProbeSettings{Concurrency: 65}, "concurrency"},
		{"interval under a minute", ProbeSettings{VerifyEvery: time.Second}, "verify_every_seconds"},
		{"interval of fractional seconds", ProbeSettings{VerifyEvery: 1500 * time.Millisecond}, "verify_every_seconds"},
		{"age under the interval", ProbeSettings{VerifyEvery: 2 * time.Hour, VerifyMaxAge: time.Hour}, "verify_max_age_seconds"},
		{"retry above the interval", ProbeSettings{VerifyEvery: time.Hour, VerifyRetry: 2 * time.Hour}, "verify_retry_seconds"},
		{"base of zero", ProbeSettings{ProbeBase: -time.Second}, "probe_base_seconds"},
		{"base over the breaker's maximum", ProbeSettings{ProbeBase: 601 * time.Second}, "probe_base_seconds"},
		{"base under a second", ProbeSettings{ProbeBase: 500 * time.Millisecond}, "probe_base_seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.settings.Check()
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var e *SettingError
			if !errors.As(err, &e) || e.Field != tc.field {
				t.Fatalf("error %v, want %s", err, tc.field)
			}
		})
	}
}

func TestProbeBudgetHonorsAShrunkLimit(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var b probeBudget
	for range 3 {
		b.spend(now)
	}
	if b.spent(now, 4) {
		t.Fatal("three of four spent")
	}
	if !b.spent(now, 3) {
		t.Fatal("three of three not spent")
	}
	if !b.spent(now.Add(59*time.Minute), 3) {
		t.Fatal("probes within the hour did not count")
	}
	if b.spent(now.Add(time.Hour+time.Second), 1) {
		t.Fatal("a probe older than an hour still counted")
	}
}

// TestProbeSettingsReload: a saved record reaches a running loop within settingsEvery, without a restart.
func TestProbeSettingsReload(t *testing.T) {
	_, l, _, now := fixtureLoop(t, Enforce)
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		return SetProbeSettings(t.Context(), tx, audit.System("test"), ProbeSettings{HourlyBudget: 120, Concurrency: 2}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if got := l.currentSettings().HourlyBudget; got != 600 {
		t.Fatalf("budget before the reload %d", got)
	}
	*now = now.Add(settingsEvery)
	tick(t, l)
	if got := l.currentSettings(); got.HourlyBudget != 120 || got.Concurrency != 2 {
		t.Fatalf("after the reload: %+v", got)
	}
}

// TestProbeSettingsReplanVerifications: a changed interval or first retry moves the checks already scheduled, not
// just the values a later check would read.
func TestProbeSettingsReplanVerifications(t *testing.T) {
	m, l, _, now := fixtureLoop(t, Enforce)
	checked := *now
	passed := m.Live(uuid.NewV7(), 1)
	contradicted := m.Live(uuid.NewV7(), 1)
	passed.mu.Lock()
	passed.verify = verification{checked: checked, next: checked.Add(6 * time.Hour), passed: true, outcome: verifyPassed}
	passed.mu.Unlock()
	contradicted.mu.Lock()
	contradicted.verify = verification{checked: checked, next: checked.Add(6 * time.Hour), failures: 2, outcome: verifyContradicted}
	contradicted.mu.Unlock()
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		return SetProbeSettings(t.Context(), tx, audit.System("test"), ProbeSettings{
			VerifyEvery: 15 * time.Minute, VerifyMaxAge: 30 * time.Minute, VerifyRetry: 5 * time.Minute,
		}, "shorter checks")
	}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	tick(t, l)
	passed.mu.Lock()
	gotPassed := passed.verify.next
	passed.mu.Unlock()
	if want := checked.Add(15 * time.Minute); !gotPassed.Equal(want) {
		t.Fatalf("pass rescheduled to %v, want %v", gotPassed, want)
	}
	contradicted.mu.Lock()
	gotContradicted := contradicted.verify.next
	contradicted.mu.Unlock()
	if want := checked.Add(10 * time.Minute); !gotContradicted.Equal(want) {
		t.Fatalf("contradiction rescheduled to %v, want %v", gotContradicted, want)
	}
}
