package collector

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

var platform = pg.NewScope(true, nil, nil)

func open(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func insertSupplier(t *testing.T, db *pg.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var err error
		id, err = store.New().InsertSupplier(t.Context(), tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: "cpa", Kind: "cpa", Origin: "https://cpa.example", UpstreamAccount: "cpa",
			NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type parser struct{}

func (parser) DrainUsage(context.Context, int) ([]adapter.UsageRecord, error) { return nil, nil }

func (parser) ParseUsage(raw []byte) (adapter.Charge, error) {
	if strings.Contains(string(raw), `"bad"`) {
		return adapter.Charge{}, errors.New("unreadable")
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 3, catalog.ItemOutput: 2}, catalog.ServiceTierStandard, nil)
	if err != nil {
		return adapter.Charge{}, err
	}
	return adapter.Charge{
		Ref: "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f", OccurredAt: time.Now().UTC(), Amount: "0",
		Usage: u, Reported: 1<<catalog.ItemInput | 1<<catalog.ItemOutput, Funding: adapter.FundingWallet, Model: "m",
	}, nil
}

func worker(db *pg.DB) *Worker {
	return &Worker{cfg: Config{
		DB: db, Scope: platform, Now: time.Now,
		Logger: slog.New(slog.DiscardHandler),
	}}
}

func count(t *testing.T, db *pg.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStagingSurvivesARestartAndIngestIsIdempotent(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	records := []adapter.UsageRecord{{Ref: "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f", Raw: []byte(`{"v":1,"ok":true}`)}}
	if err := w.stage(t.Context(), id, records); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1 AND state = 'new'`, id); got != 1 {
		t.Fatalf("staged rows = %d", got)
	}

	// A new worker represents the process after a restart: only the staging row is left.
	if err := worker(db).ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows = %d", got)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1`, id); got != 0 {
		t.Fatalf("staging rows after ingest = %d", got)
	}

	// A replay of the same popped batch inserts its staging row again, then reconciliation dedupes the charge.
	if err := w.stage(t.Context(), id, records); err != nil {
		t.Fatal(err)
	}
	if err := w.ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows after replay = %d", got)
	}
}

func TestAnUnreadableRecordIsKeptAndMarked(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	if err := w.stage(t.Context(), id, []adapter.UsageRecord{{Raw: []byte(`{"v":1,"bad":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := w.ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1 AND state = 'rejected'`, id); got != 1 {
		t.Fatalf("rejected rows = %d", got)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 0 {
		t.Fatalf("charge rows = %d", got)
	}
}
