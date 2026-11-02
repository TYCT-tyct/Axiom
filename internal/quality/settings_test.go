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
	at := *now
	earlier := at.Add(-time.Hour)
	contradicted := m.Live(uuid.NewV7(), 1)
	contradicted.mu.Lock()
	l.applyVerification(t.Context(), contradicted, verifyEnd{outcome: verifyContradicted}, at)
	contradicted.mu.Unlock()
	passed := m.Live(uuid.NewV7(), 1)
	passed.mu.Lock()
	l.applyVerification(t.Context(), passed, verifyEnd{outcome: verifyPassed}, at)
	passed.mu.Unlock()
	passedThenContradicted := m.Live(uuid.NewV7(), 1)
	passedThenContradicted.mu.Lock()
	l.applyVerification(t.Context(), passedThenContradicted, verifyEnd{outcome: verifyPassed}, earlier)
	l.applyVerification(t.Context(), passedThenContradicted, verifyEnd{outcome: verifyContradicted}, at)
	passedThenContradicted.mu.Unlock()
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		return SetProbeSettings(t.Context(), tx, audit.System("test"), ProbeSettings{
			VerifyEvery: 15 * time.Minute, VerifyMaxAge: 30 * time.Minute, VerifyRetry: 5 * time.Minute,
		}, "shorter checks")
	}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	tick(t, l)
	for _, tc := range []struct {
		name        string
		e           *State
		want        time.Duration
		wantChecked time.Time
	}{
		{"pass", passed, 15 * time.Minute, at},
		{"new endpoint", contradicted, 5 * time.Minute, time.Time{}},
		{"previously passed", passedThenContradicted, 5 * time.Minute, earlier},
	} {
		tc.e.mu.Lock()
		got, checked, attempted := tc.e.verify.next, tc.e.verify.checked, tc.e.verify.attempted
		tc.e.mu.Unlock()
		if !attempted.Equal(at) {
			t.Fatalf("%s: attempted = %v, want %v", tc.name, attempted, at)
		}
		if want := at.Add(tc.want); !got.Equal(want) {
			t.Fatalf("%s: rescheduled to %v, want %v", tc.name, got, want)
		}
		if !checked.Equal(tc.wantChecked) {
			t.Fatalf("%s: checked = %v, want %v", tc.name, checked, tc.wantChecked)
		}
	}
}
