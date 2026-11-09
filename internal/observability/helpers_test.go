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
	db, _ := openDBWithURL(t)
	return db
}

// openDBWithURL also gives the database's URLs, for a test that seeds as the superuser.
func openDBWithURL(t *testing.T) (*pg.DB, pgtest.Database) {
	t.Helper()
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, database
}

var platformScope = pg.NewScope(true, nil, nil)

func accountScope(accounts ...uuid.UUID) pg.Scope { return pg.NewScope(false, nil, accounts) }

// record is a complete call of account, started at at, with two attempts: the first failed and the second committed.
func record(account uuid.UUID, at time.Time) CallRecord {
	return CallRecord{
		PriceVersion: uuid.NewV7().String(), ID: uuid.NewV7(), At: at, Site: uuid.NewV7(), Account: account, Workspace: uuid.NewV7(), Key: uuid.NewV7(),
		IP: netip.MustParseAddr("203.0.113.7"), ClientApp: "claude-code", SessionID: "session-1", SubagentID: "agent-1",
		Class: "chat", Language: "en", UsedTools: true, HasImage: true, HasFile: true, Model: "claude-opus",
		Attempts: []AttemptRecord{
			{
				Seq: 1, Endpoint: uuid.NewV7(), Supplier: uuid.NewV7(), SourceClass: "relay", Egress: "direct",
				Outcome: AttemptFailed, ErrorKind: "rate_limited", StartedAt: at, FinishedAt: at.Add(100 * time.Millisecond),
			},
			{
				Seq: 2, Endpoint: uuid.NewV7(), Supplier: uuid.NewV7(), SourceClass: "official", Egress: "direct",
				Outcome: AttemptCommitted, StartedAt: at.Add(200 * time.Millisecond),
				FinishedAt: at.Add(900 * time.Millisecond), FirstToken: 300 * time.Millisecond,
			},
		},
		Route:       []byte(`{"preset":"p1"}`),
		Usage:       map[catalog.Item]int64{catalog.ItemInput: 120, catalog.ItemOutput: 30},
		ServiceTier: catalog.ServiceTierStandard, PriceNano: 5_000_000, CostNano: 3_000_000,
		Currency: mustCurrency("USD"), Free: true, Latency: 1200 * time.Millisecond, FirstToken: 300 * time.Millisecond,
		FinishReason: "stop", ErrorKind: "upstream_error",
		EndUser: HashEndUser([]byte("site-key"), uuid.Nil(), "user-1"), ConsentVersion: "2026-10", TrainingAllowed: true,
	}
}

func mustCurrency(s string) platform.Currency {
	c, err := platform.ParseCurrency(s)
	if err != nil {
		panic(err)
	}
	return c
}
