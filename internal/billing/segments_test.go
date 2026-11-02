package billing

import (
	"errors"
	"testing"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestSegmentSettingsCheck(t *testing.T) {
	output := func(n int64) *int64 { return &n }
	overshoot := func(d time.Duration) *time.Duration { return &d }
	for _, tc := range []struct {
		name     string
		settings SegmentSettings
		field    string
	}{
		{"empty", SegmentSettings{}, ""},
		{"both", SegmentSettings{Output: output(500), Overshoot: overshoot(3 * time.Second)}, ""},
		{"the extension wait exactly", SegmentSettings{Overshoot: overshoot(time.Second)}, ""},
		{"no output", SegmentSettings{Output: output(0)}, "output_tokens"},
		{"output over the bound", SegmentSettings{Output: output(1_000_001)}, "output_tokens"},
		{"overshoot under the wait", SegmentSettings{Overshoot: overshoot(0)}, "overshoot_seconds"},
		{"overshoot over the bound", SegmentSettings{Overshoot: overshoot(601 * time.Second)}, "overshoot_seconds"},
		{"overshoot of fractional seconds", SegmentSettings{Overshoot: overshoot(1500 * time.Millisecond)}, "overshoot_seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.settings.Check(time.Second)
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
	if got := (SegmentSettings{}).Effective(); got != DefaultSegments() {
		t.Fatalf("effective %+v, want %+v", got, DefaultSegments())
	}
	short := 4 * time.Second
	var e *SettingError
	if err := (SegmentSettings{Overshoot: &short}).Check(5 * time.Second); !errors.As(err, &e) || e.Field != "overshoot_seconds" {
		t.Fatalf("overshoot under a longer extension wait: %v", err)
	}
}

func TestSegmentPolicyReloadsTheSavedRecord(t *testing.T) {
	db := pgtest.New(t, Migrations())
	app, err := pg.Open(t.Context(), db.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	scope := pg.NewScope(true, nil, nil)
	p, err := LoadSegmentPolicy(t.Context(), app, scope, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Load(); got != DefaultSegments() {
		t.Fatalf("empty record: %+v", got)
	}
	output := int64(2500)
	overshoot := 4 * time.Second
	if err := app.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return SetSegmentSettings(t.Context(), tx, audit.System("test"), SegmentSettings{Output: &output, Overshoot: &overshoot}, time.Second, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := p.Load(); got.Output != output || got.Overshoot != overshoot {
		t.Fatalf("after the save: %+v", got)
	}
	if err := app.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return SetSegmentSettings(t.Context(), tx, audit.System("test"), SegmentSettings{}, time.Second, "reset")
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := p.Load(); got != DefaultSegments() {
		t.Fatalf("after the reset: %+v", got)
	}
}
