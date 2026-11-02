package main

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
)

//nolint:cyclop,gocognit // the defaults, save, replay, partial reset and refusals are one admin journey.
func TestAdminSegmentSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/settings/segments"
	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read defaults", rec, 200, "")
	var view adminapi.SegmentSettingsView
	decode(t, rec, &view)
	if view.Settings.OutputTokens != nil || view.Settings.OvershootSeconds != nil ||
		view.Defaults.OutputTokens == nil || *view.Defaults.OutputTokens != 1000 ||
		view.Defaults.OvershootSeconds == nil || *view.Defaults.OvershootSeconds != 2 {
		t.Fatalf("defaults: %s", rec.Body)
	}

	body := `{"settings":{"output_tokens":500,"overshoot_seconds":3},"reason":"smaller segments"}`
	key := newKey()
	for i := range 2 {
		rec = f.change(s, http.MethodPut, path, body, key)
		expectAdmin(t, "save", rec, 200, "")
		if i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal("not replayed")
		}
	}
	if n := f.auditCount("segment_settings.update"); n != "1" {
		t.Fatalf("audit entries %s", n)
	}
	rec = f.do(http.MethodGet, path, "", as(s))
	decode(t, rec, &view)
	if view.Settings.OutputTokens == nil || *view.Settings.OutputTokens != 500 ||
		view.Settings.OvershootSeconds == nil || *view.Settings.OvershootSeconds != 3 {
		t.Fatalf("saved settings: %s", rec.Body)
	}

	// A value left out goes back to the default; the record keeps the one that is set.
	rec = f.change(s, http.MethodPut, path, `{"settings":{"output_tokens":2000},"reason":"only the size"}`, newKey())
	expectAdmin(t, "partial save", rec, 200, "")
	view = adminapi.SegmentSettingsView{}
	decode(t, rec, &view)
	if view.Settings.OvershootSeconds != nil || *view.Settings.OutputTokens != 2000 {
		t.Fatalf("partial settings: %s", rec.Body)
	}

	// No value at all deletes the record: the defaults are the only source again.
	rec = f.change(s, http.MethodPut, path, `{"settings":{},"reason":"back to defaults"}`, newKey())
	expectAdmin(t, "reset", rec, 200, "")
	view = adminapi.SegmentSettingsView{}
	decode(t, rec, &view)
	if view.Settings.OutputTokens != nil || view.Settings.OvershootSeconds != nil {
		t.Fatalf("reset settings: %s", rec.Body)
	}

	for _, bad := range []string{
		`{"settings":{"output_tokens":0},"reason":"r"}`,
		`{"settings":{"overshoot_seconds":0},"reason":"r"}`,
		`{"settings":{"output_tokens":1000001},"reason":"r"}`,
		`{"settings":{"overshoot_seconds":601},"reason":"r"}`,
	} {
		rec := f.change(s, http.MethodPut, path, bad, newKey())
		expectAdmin(t, "invalid "+bad, rec, 400, "")
		if !strings.Contains(rec.Body.String(), `"/settings/`) {
			t.Fatalf("invalid field is not named: %s", rec.Body)
		}
	}
}

//nolint:cyclop // the defaults, save, replay, restart read, relations and bounds are one admin journey.
func TestAdminQualityProbeSettings(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	path := "/settings/quality-probes"
	rec := f.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read defaults", rec, 200, "")
	var view adminapi.ProbeSettingsView
	decode(t, rec, &view)
	if view.Settings.HourlyBudget != nil || view.Defaults.HourlyBudget == nil || *view.Defaults.HourlyBudget != 600 ||
		view.Defaults.Concurrency == nil || *view.Defaults.Concurrency != 4 ||
		view.Defaults.VerifyEverySeconds == nil || *view.Defaults.VerifyEverySeconds != 21600 ||
		view.Defaults.VerifyMaxAgeSeconds == nil || *view.Defaults.VerifyMaxAgeSeconds != 86400 ||
		view.Defaults.VerifyRetrySeconds == nil || *view.Defaults.VerifyRetrySeconds != 900 ||
		view.Defaults.ProbeBaseSeconds == nil || *view.Defaults.ProbeBaseSeconds != 30 {
		t.Fatalf("defaults: %s", rec.Body)
	}

	body := `{"settings":{"hourly_budget":120,"concurrency":2,"verify_every_seconds":3600,` +
		`"verify_max_age_seconds":7200,"verify_retry_seconds":600,"probe_base_seconds":10},"reason":"tighter probes"}`
	key := newKey()
	for i := range 2 {
		rec = f.change(s, http.MethodPut, path, body, key)
		expectAdmin(t, "save", rec, 200, "")
		if i == 1 && rec.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal("not replayed")
		}
	}
	if n := f.auditCount("probe_settings.update"); n != "1" {
		t.Fatalf("audit entries %s", n)
	}
	rec = f.do(http.MethodGet, path, "", as(s))
	decode(t, rec, &view)
	if view.Settings.HourlyBudget == nil || *view.Settings.HourlyBudget != 120 ||
		view.Settings.Concurrency == nil || *view.Settings.Concurrency != 2 ||
		view.Settings.VerifyEverySeconds == nil || *view.Settings.VerifyEverySeconds != 3600 ||
		view.Settings.VerifyMaxAgeSeconds == nil || *view.Settings.VerifyMaxAgeSeconds != 7200 ||
		view.Settings.VerifyRetrySeconds == nil || *view.Settings.VerifyRetrySeconds != 600 ||
		view.Settings.ProbeBaseSeconds == nil || *view.Settings.ProbeBaseSeconds != 10 {
		t.Fatalf("saved settings: %s", rec.Body)
	}

	// A node that starts after the save reads the record; nothing about it needs a restart.
	restarted := f.restarted()
	rec = restarted.do(http.MethodGet, path, "", as(s))
	expectAdmin(t, "read after restart", rec, 200, "")
	decode(t, rec, &view)
	if view.Settings.HourlyBudget == nil || *view.Settings.HourlyBudget != 120 {
		t.Fatalf("settings after restart: %s", rec.Body)
	}

	// No value at all deletes the record: the defaults are the only source again.
	rec = f.change(s, http.MethodPut, path, `{"settings":{},"reason":"back to defaults"}`, newKey())
	expectAdmin(t, "reset", rec, 200, "")
	view = adminapi.ProbeSettingsView{}
	decode(t, rec, &view)
	if view.Settings.HourlyBudget != nil || view.Settings.Concurrency != nil {
		t.Fatalf("reset settings: %s", rec.Body)
	}

	// The maximum age must cover the interval, and the retry must not outlast it.
	for _, bad := range []string{
		`{"settings":{"hourly_budget":0},"reason":"r"}`,
		`{"settings":{"concurrency":65},"reason":"r"}`,
		`{"settings":{"verify_every_seconds":7200,"verify_max_age_seconds":3600},"reason":"r"}`,
		`{"settings":{"verify_every_seconds":3600,"verify_retry_seconds":7200},"reason":"r"}`,
		`{"settings":{"probe_base_seconds":601},"reason":"r"}`,
	} {
		rec := f.change(s, http.MethodPut, path, bad, newKey())
		expectAdmin(t, "invalid "+bad, rec, 400, "")
	}
}
