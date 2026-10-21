package catalog_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/telemetry"
)

func TestTheSyncStateIsMeasuredWithoutAnotherRound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	cfg := catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: slog.New(slog.DiscardHandler),
		Queue: s.db.NewQueue(slog.New(slog.DiscardHandler), platformScope, nil),
		Meter: tel.Metrics.Meter("axiom/catalog"), Clock: func() time.Time { return now },
	}
	if err := catalog.RegisterSync(cfg); err != nil {
		t.Fatal(err)
	}
	check := func(age, failures string) {
		t.Helper()
		for name, want := range map[string]string{
			"axiom_catalog_sync_last_success_age_seconds": age,
			"axiom_catalog_sync_consecutive_failures":     failures,
		} {
			if got := scrape(t, tel, name); got != want {
				t.Errorf("%s: %s, want %s", name, got, want)
			}
		}
	}
	round := func() {
		t.Helper()
		if err := catalog.RunSyncRound(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	check("0", "0")
	d.failing.Store(true)
	round()
	check("0", "1")
	now = now.Add(2 * time.Hour)
	check("7200", "1")
	d.failing.Store(false)
	round()
	check("0", "0")
	now = now.Add(25 * time.Hour)
	check("90000", "0")
	d.failing.Store(true)
	round()
	check("90000", "1")
	now = now.Add(time.Hour)
	check("93600", "1")
	d.failing.Store(false)
	round()
	check("0", "0")
}

func TestTheSyncRecordsTheDatabaseTime(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	before := query[time.Time](s, platformScope, "SELECT clock_timestamp()")
	if err := catalog.RunSyncRound(t.Context(), catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatal(err)
	}
	stamp := s.syncState().LastSuccessAt
	after := query[time.Time](s, platformScope, "SELECT clock_timestamp()")
	if stamp == nil || stamp.Before(before) || stamp.After(after) {
		t.Fatalf("last success: %v, want database time between %s and %s", stamp, before, after)
	}
}

func TestTheSyncDoesNotCountAMissingEntryAsAPendingPrice(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	before := s.usd(id)
	d := served(`"different-model"=` + xPrices("1e-06", "2e-06", "1e-07"))
	logs := &logBuffer{}
	if err := catalog.RunSyncRound(t.Context(), catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: logs.logger(),
	}); err != nil {
		t.Fatal(err)
	}
	if state := s.syncState(); state.Pending != 0 || state.Failures != 0 || state.LastSuccessAt == nil {
		t.Fatalf("missing entry record: %+v", state)
	}
	if got := s.usd(id); got != before || s.priceCount(id) != 1 {
		t.Fatalf("missing entry changed the price: %s", got)
	}
	if !strings.Contains(logs.String(), "catalog sync held a model") {
		t.Fatal("missing entry was not reported")
	}
}

func TestTheSyncReturnsARecordFailure(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	d := served(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))
	s.db.Close()
	err := catalog.RunSyncRound(t.Context(), catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: slog.New(slog.DiscardHandler),
	})
	wantErr(t, err, "record the sync")
}
