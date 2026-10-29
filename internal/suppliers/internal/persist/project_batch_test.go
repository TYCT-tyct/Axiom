package persist_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"        //nolint:depguard // DBTX result types only; every query still uses scoped pg.Tx
	"github.com/jackc/pgx/v5/pgconn" //nolint:depguard // DBTX command tags and constraint assertions; no unscoped connections

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/suppliers/internal/store"
)

// rowProjection is the pre-batching write path. Explicit group and endpoint IDs let
// both paths share the same identities; schedule IDs are compared by fingerprint.
func rowProjection(ctx context.Context, db store.DBTX, w store.ProjectionWrites) error {
	q := store.New()
	writes := []func() error{
		func() error { return rowQueries(ctx, db, w.Groups, insertRowGroup) },
		func() error { return rowQueries(ctx, db, w.ChangedGroups, q.UpdateProjectedGroup) },
		func() error { return rowQueries(ctx, db, w.AbsentGroups, q.SetProjectedGroupAbsent) },
		func() error { return rowQueries(ctx, db, w.Models, q.UpsertProjectedModel) },
		func() error { return rowQueries(ctx, db, w.AbsentModels, q.SetProjectedModelAbsent) },
		func() error { return rowQueries(ctx, db, w.Endpoints, insertRowEndpoint) },
		func() error { return rowQueries(ctx, db, w.ChangedEndpoints, q.UpdateProjectedEndpoint) },
		func() error { return rowQueries(ctx, db, w.Costs, setRowCost) },
		func() error { return rowQueries(ctx, db, w.UnknownCosts, q.UnsetProjectedEndpointCost) },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	return nil
}

func rowQueries[T any](ctx context.Context, db store.DBTX, rows []T, write func(context.Context, store.DBTX, T) error) error {
	for _, r := range rows {
		if err := write(ctx, db, r); err != nil {
			return err
		}
	}
	return nil
}

func insertRowGroup(ctx context.Context, db store.DBTX, r store.ProjectedGroup) error {
	_, err := db.Exec(ctx, `INSERT INTO suppliers.supplier_groups
 (id, supplier_id, upstream_ref, name, ratio, facts, last_seen_at) VALUES ($1,$2,$3,$4,$5::numeric,$6::jsonb,$7)`,
		r.ID, r.SupplierID, r.UpstreamRef, r.Name, r.Ratio, r.Facts, r.SeenAt)
	return err
}

func insertRowEndpoint(ctx context.Context, db store.DBTX, r store.ProjectedEndpoint) error {
	_, err := db.Exec(ctx, `INSERT INTO suppliers.endpoints
 (id,supplier_id,supplier_group_id,model_id,upstream_name,formats) VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.SupplierID, r.SupplierGroupID, r.ModelID, r.UpstreamName, r.Formats)
	return err
}

func setRowCost(ctx context.Context, db store.DBTX, r store.EnsureCostScheduleParams) error {
	q := store.New()
	id, err := q.EnsureCostSchedule(ctx, db, r)
	if err != nil {
		return err
	}
	return q.SetProjectedEndpointCost(ctx, db, store.SetProjectedEndpointCostParams{ID: r.EndpointID, CostScheduleID: &id})
}

func projectionDB(t testing.TB) (*pg.DB, uuid.UUID) {
	t.Helper()
	database := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var id uuid.UUID
	err = db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var err error
		id, err = store.New().InsertSupplier(t.Context(), tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: "batch", Kind: "fake", Origin: "https://fake.example", UpstreamAccount: "batch",
			NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, id
}

func projectedRows(ctx context.Context, db store.DBTX) ([]string, error) {
	var out []string
	for _, query := range []string{
		`SELECT to_jsonb(g)::text FROM suppliers.supplier_groups g ORDER BY id`,
		`SELECT to_jsonb(m)::text FROM suppliers.supplier_models m ORDER BY supplier_group_id,upstream_name`,
		`SELECT ((to_jsonb(e)-'cost_schedule_id') || jsonb_build_object('fingerprint',c.fingerprint))::text
   FROM suppliers.endpoints e LEFT JOIN suppliers.cost_schedules c ON c.id=e.cost_schedule_id ORDER BY e.id`,
		`SELECT (to_jsonb(c)-'id')::text FROM suppliers.cost_schedules c ORDER BY endpoint_id,fingerprint`,
	} {
		rows, err := db.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func newProjection(supplier uuid.UUID, n int) store.ProjectionWrites {
	var w store.ProjectionWrites
	group := uuid.NewV7()
	now := t0
	w.Groups = append(w.Groups, store.ProjectedGroup{
		ID:         group,
		SupplierID: supplier, UpstreamRef: "default", Name: "Default", Ratio: "1.25", Facts: []byte(`{"platform":"openai"}`), SeenAt: &now,
	})
	for i := range n {
		model, endpoint := uuid.NewV7(), uuid.NewV7()
		name := fmt.Sprintf("model-%05d", i)
		formats := []string{"chat"}
		if i%2 == 0 {
			formats = append(formats, "responses")
		}
		w.Models = append(w.Models, store.UpsertProjectedModelParams{SupplierID: supplier, SupplierGroupID: group, UpstreamName: name, ModelID: &model, Formats: formats})
		w.Endpoints = append(w.Endpoints, store.ProjectedEndpoint{
			ID:         endpoint,
			SupplierID: supplier, SupplierGroupID: group, ModelID: model, UpstreamName: name, Formats: formats,
		})
		w.Costs = append(w.Costs, store.EnsureCostScheduleParams{
			SupplierID: supplier, EndpointID: endpoint, NativeUnit: "quota",
			Schedule: []byte(unitPrice.schedule), Basis: unitPrice.basis, Fingerprint: unitPrice.fingerprint,
		})
	}
	return w
}

// Random listing deltas are replayed through the original queries and through pages
// from the same savepoint, so defaults, retained history and NULLs compare exactly.
func TestProjectionPagesEqualRowWrites(t *testing.T) {
	db, supplier := projectionDB(t)
	rng := rand.New(rand.NewPCG(212, 420)) //nolint:gosec // G404: reproducible test listings, not secrets
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		initial := newProjection(supplier, store.ProjectionPageSize+3)
		seed := initial
		duplicate := initial.Models[0]
		duplicate.Formats = []string{"messages"}
		seed.Models = append(slices.Clone(initial.Models), duplicate)
		if err := compareProjection(t.Context(), tx, seed); err != nil {
			return err
		}
		for round := range 40 {
			if err := compareProjection(t.Context(), tx, randomProjection(rng, initial, round)); err != nil {
				return fmt.Errorf("round %d: %w", round, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func randomProjection(rng *rand.Rand, initial store.ProjectionWrites, round int) store.ProjectionWrites {
	var w store.ProjectionWrites
	g := initial.Groups[0]
	switch rng.IntN(3) {
	case 0:
		w.AbsentGroups = append(w.AbsentGroups, g.ID)
	case 1:
		w.ChangedGroups = append(w.ChangedGroups, store.UpdateProjectedGroupParams{
			ID: g.ID, Name: "intermediate", Ratio: "1", Facts: []byte(`{}`), SeenAt: &t0,
		}, store.UpdateProjectedGroupParams{
			ID: g.ID, Name: fmt.Sprintf("round-%d", round), Ratio: "0.75", Facts: []byte(`{}`), SeenAt: &t0,
		})
	}
	for i, m := range initial.Models {
		if rng.IntN(4) != 0 {
			continue
		}
		e := initial.Endpoints[i]
		switch rng.IntN(6) {
		case 0:
			w.AbsentModels = append(w.AbsentModels, store.SetProjectedModelAbsentParams{SupplierGroupID: m.SupplierGroupID, UpstreamName: m.UpstreamName})
		case 1:
			m.ModelID = nil
			m.Formats = []string{"messages"}
			w.Models = append(w.Models, m)
		case 2:
			w.ChangedEndpoints = append(w.ChangedEndpoints, store.UpdateProjectedEndpointParams{ID: e.ID, UpstreamName: m.UpstreamName + "-alias", Formats: []string{"messages", "chat"}})
		case 3:
			cost := initial.Costs[i]
			if rng.IntN(2) == 0 {
				cost.Fingerprint = plusVAT.fingerprint
				cost.Schedule = []byte(plusVAT.schedule)
				cost.Basis = plusVAT.basis
			}
			w.Costs = append(w.Costs, cost)
		case 5:
			w.Models = append(w.Models, m)
		case 4:
			w.UnknownCosts = append(w.UnknownCosts, store.UnsetProjectedEndpointCostParams{ID: e.ID, Reason: new("the listing no longer offers the model")})
		}
	}
	return w
}

func compareProjection(ctx context.Context, tx pg.Tx, w store.ProjectionWrites) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT compare"); err != nil {
		return err
	}
	if err := rowProjection(ctx, tx, w); err != nil {
		return err
	}
	want, err := projectedRows(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT compare"); err != nil {
		return err
	}
	if err := w.Apply(ctx, tx); err != nil {
		return err
	}
	got, err := projectedRows(ctx, tx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("batch differs from row writes")
	}
	_, err = tx.Exec(ctx, "RELEASE SAVEPOINT compare")
	return err
}

type countedProjection struct {
	store.DBTX
	statements int
}

func (c *countedProjection) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.statements++
	return c.DBTX.Exec(ctx, sql, args...)
}

func (c *countedProjection) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.statements++
	return c.DBTX.QueryRow(ctx, sql, args...)
}

func BenchmarkProjection10k(b *testing.B) {
	db, supplier := projectionDB(b)
	w := newProjection(supplier, 10000)
	for name, apply := range map[string]func(context.Context, store.DBTX, store.ProjectionWrites) error{
		"row":   rowProjection,
		"batch": func(ctx context.Context, db store.DBTX, w store.ProjectionWrites) error { return w.Apply(ctx, db) },
	} {
		b.Run(name, func(b *testing.B) {
			var total time.Duration
			for b.Loop() {
				elapsed, statements, err := measureProjection(b.Context(), db, w, apply)
				if err != nil {
					b.Fatal(err)
				}
				total += elapsed
				b.ReportMetric(float64(statements), "statements/op")
			}
			b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "write-ns/op")
		})
	}
}

func measureProjection(ctx context.Context, db *pg.DB, w store.ProjectionWrites, apply func(context.Context, store.DBTX, store.ProjectionWrites) error) (time.Duration, int, error) {
	var elapsed time.Duration
	var statements int
	err := db.Tx(ctx, platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, "SAVEPOINT listing"); err != nil {
			return err
		}
		count := &countedProjection{DBTX: tx}
		start := time.Now()
		if err := apply(ctx, count, w); err != nil {
			return err
		}
		elapsed = time.Since(start)
		statements = count.statements
		_, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT listing")
		return err
	})
	return elapsed, statements, err
}

func TestProjectionPageFailureRollsBackEarlierPages(t *testing.T) {
	db, supplier := projectionDB(t)
	w := newProjection(supplier, store.ProjectionPageSize+1)
	w.Models[len(w.Models)-1].Formats = []string{"invalid"}
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return w.Apply(t.Context(), tx) })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("want format constraint failure on second page, got %v", err)
	}
	err = db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		rows, err := projectedRows(t.Context(), tx)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return fmt.Errorf("failed listing left %d rows", len(rows))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEmptyProjectionWritesNoStatements(t *testing.T) {
	count := &countedProjection{}
	var w store.ProjectionWrites
	if err := w.Apply(t.Context(), count); err != nil {
		t.Fatal(err)
	}
	if count.statements != 0 {
		t.Fatalf("empty differences issued %d statements", count.statements)
	}
}
