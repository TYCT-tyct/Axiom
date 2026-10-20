package catalog_test

import (
	"log/slog"
	"testing"
	"time"

	"axiom/internal/catalog"
)

func TestTheSyncRecoversAfterAPartialRound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	now := time.Date(2025, 11, 1, 3, 0, 0, 0, time.UTC)
	cfg := catalog.SyncJobConfig{
		DB: s.db, Scope: platformScope, Logger: slog.New(slog.DiscardHandler), Clock: func() time.Time { return now },
	}
	round := func(d *datasets, wantErr bool, want string) {
		t.Helper()
		cfg.Fetcher = d.fetcher(t)
		if err := catalog.RunSyncRound(t.Context(), cfg); (err != nil) != wantErr {
			t.Fatalf("round: %v, want error %t", err, wantErr)
		}
		if got := s.record(); got != want {
			t.Fatalf("record: %s, want %s", got, want)
		}
	}
	held := served(`"gpt-x-2025-01-01"=` + xPrices("5e-06", "9e-06", "1e-07"))
	round(held, false, "2025-11-01T03:00:00Z|never|0|1")

	// LiteLLM publishes before OpenRouter fails to parse. Retries must keep the complete pending count until recovery.
	partial := served(`"gpt-x-2025-01-01"=` + xPrices("1.1e-06", "2.2e-06", "1e-07"))
	partial.openrouter = []byte(`{`)
	now = now.Add(time.Hour)
	round(partial, true, "2025-11-01T03:00:00Z|2025-11-01T04:00:00Z|1|1")
	if s.priceCount(id) != 2 {
		t.Fatal("the readable dataset did not publish its safe change")
	}
	now = now.Add(time.Hour)
	round(partial, true, "2025-11-01T03:00:00Z|2025-11-01T04:00:00Z|2|1")
	if s.priceCount(id) != 2 {
		t.Fatal("retry published the same price twice")
	}
	now = now.Add(time.Hour)
	healthy := served(`"gpt-x-2025-01-01"=` + xPrices("1.1e-06", "2.2e-06", "1e-07"))
	round(healthy, false, "2025-11-01T06:00:00Z|never|0|0")
}

func TestTheScheduledSyncKeepsThePriceGuards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source catalog.PriceSource
		input  string
	}{
		{"manual", catalog.SourceManual, "1.1e-06"},
		{"official", catalog.SourceOfficial, "1.1e-06"},
		{"rise", catalog.SourceLiteLLM, "1.3e-06"},
		{"fall", catalog.SourceLiteLLM, "0.7e-06"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, c.source, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
			before := s.usd(id)
			d := served(`"gpt-x-2025-01-01"=` + xPrices(c.input, "2e-06", "1e-07"))
			if err := catalog.RunSyncRound(t.Context(), catalog.SyncJobConfig{
				DB: s.db, Scope: platformScope, Fetcher: d.fetcher(t), Logger: slog.New(slog.DiscardHandler),
			}); err != nil {
				t.Fatal(err)
			}
			if got := s.usd(id); got != before || s.priceCount(id) != 1 {
				t.Errorf("scheduled sync changed a protected price: %s", got)
			}
			want := int64(0)
			if c.source == catalog.SourceLiteLLM {
				want = 1
			}
			if got := s.syncState().Pending; got != want {
				t.Errorf("pending: %d, want %d", got, want)
			}
		})
	}
}
