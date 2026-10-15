package observability

import (
	"log/slog"
	"net/netip"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var quiet = slog.New(slog.DiscardHandler)

// openDB gives a test its own migrated database, logged in as the application.
func openDB(t *testing.T) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

var platformScope = pg.NewScope(true, nil, nil)

func accountScope(accounts ...uuid.UUID) pg.Scope { return pg.NewScope(false, nil, accounts) }

// record is a complete call of account, started at at.
func record(account uuid.UUID, at time.Time) CallRecord {
	return CallRecord{
		ID: uuid.NewV7(), At: at, Site: uuid.NewV7(), Account: account, Workspace: uuid.NewV7(), Key: uuid.NewV7(),
		IP: netip.MustParseAddr("203.0.113.7"), ClientApp: "claude-code", Class: "chat", Language: "en", UsedTools: true,
		Model: "claude-opus", Endpoint: uuid.NewV7(), SourceClass: "official", Attempts: 2, Route: []byte(`{"preset":"p1"}`),
		Ledger: []uuid.UUID{uuid.NewV7()}, Usage: map[catalog.Item]int64{catalog.ItemInput: 120, catalog.ItemOutput: 30},
		ServiceTier: catalog.ServiceTierStandard, PriceNano: 5_000_000, CostNano: 3_000_000,
		Currency: mustCurrency("USD"), Latency: 1200 * time.Millisecond, FirstToken: 300 * time.Millisecond,
		EndUser: HashEndUser([]byte("site-key"), "user-1"), FinishReason: "stop", ConsentVersion: "2026-10", TrainingAllowed: true,
	}
}

func mustCurrency(s string) platform.Currency {
	c, err := platform.ParseCurrency(s)
	if err != nil {
		panic(err)
	}
	return c
}
