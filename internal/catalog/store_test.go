package catalog_test

import (
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var platformScope = pg.NewScope(true, nil, nil)

// store is a catalog on a fresh, migrated database, with the platform scope the admin operations run in.
type store struct {
	t        *testing.T
	database pgtest.Database
	db       *pg.DB
	c        *catalog.Catalog
}

func newStore(t *testing.T) *store {
	t.Helper()
	database := pgtest.New(t, catalog.Migrations)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	c, err := catalog.Open(db, platformScope, catalog.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &store{t: t, database: database, db: db, c: c}
}

// op is a fresh operation: a new key each time, so no two are mistaken for a retry.
func op() catalog.Op {
	return catalog.Op{Actor: audit.System("catalog.test"), Key: uuid.NewV7(), Reason: "test"}
}

// must fails the test on an error and returns what the operation did.
func (s *store) must(r catalog.Result, err error) catalog.Result {
	s.t.Helper()
	if err != nil {
		s.t.Fatal(err)
	}
	return r
}

func (s *store) model(r catalog.Result) catalog.ModelID {
	s.t.Helper()
	id, err := uuid.Parse(r.Subject)
	if err != nil {
		s.t.Fatal(err)
	}
	return catalog.ModelID(id)
}

// query reads one value in the given scope.
func query[T any](s *store, scope pg.Scope, sql string, args ...any) T {
	s.t.Helper()
	var v T
	if err := s.db.Tx(s.t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(s.t.Context(), sql, args...).Scan(&v)
	}); err != nil {
		s.t.Fatal(err)
	}
	return v
}

// exec runs a statement in the given scope and returns its error.
func (s *store) exec(scope pg.Scope, sql string, args ...any) error {
	s.t.Helper()
	return s.db.Tx(s.t.Context(), scope, func(tx pg.Tx) error {
		_, err := tx.Exec(s.t.Context(), sql, args...)
		return err
	})
}

// wantErr checks err mentions each of the texts.
func wantErr(t *testing.T, err error, texts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one with %q", texts)
	}
	for _, text := range texts {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("error %q lacks %q", err, text)
		}
	}
}

func raw(s string) jsonx.Raw { return jsonx.Raw(s) }

// Schedules and specs the tests publish.
const (
	basic    = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`
	basicCNY = `{"v":1,"currency":"CNY","rates":{"input":"0.000007","output":"0.000014"}}`
	// priority has both prices, as a listed model that declares priority needs.
	withPriority = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"},
		"service_tiers":{"priority":{"rates":{"input":"0.000002","output":"0.000004"}}}}`
	evidence    = `{"url":"https://example.com/pricing","quote":"input $1 per million"}`
	basicSpec   = `{"items":["input","output"],"tiers":[],"context_length":128000}`
	priorityEnv = `{"items":["input","output"],"tiers":["priority"]}`
)

// seed is a model in its draft state: the author, the model, its standard ID as a public name and a USD price.
func (s *store) seed(slug, spec, schedule string) (catalog.ModelID, uuid.UUID) {
	s.t.Helper()
	author, _, _ := strings.Cut(slug, "/")
	s.must(s.c.SaveAuthor(s.t.Context(), catalog.SaveAuthorRequest{Op: op(), Slug: author, Name: strings.ToUpper(author)}))
	id := s.model(s.must(s.c.CreateModel(s.t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(spec),
	})))
	s.must(s.c.AddName(s.t.Context(), catalog.AddNameRequest{Op: op(), Model: id, Name: strings.Split(slug, "@")[0], Visibility: catalog.VisibilityPublic}))
	s.must(s.c.PublishPrice(s.t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: id, Source: catalog.SourceManual, Schedule: raw(schedule), Evidence: raw(evidence),
	}))
	return id, s.head(id, "USD")
}

// head is the current reference price row of the model in the currency.
func (s *store) head(id catalog.ModelID, currency string) uuid.UUID {
	s.t.Helper()
	return query[uuid.UUID](s, platformScope, `SELECT r.id FROM catalog.reference_price r WHERE r.model_id = $1 AND r.currency = $2
		AND NOT EXISTS (SELECT FROM catalog.reference_price n WHERE n.supersedes = r.id)`, uuid.UUID(id), currency)
}

// list lists a seeded model.
func (s *store) list(id catalog.ModelID) {
	s.t.Helper()
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(id))
	s.must(s.c.ListModel(s.t.Context(), catalog.ModelRequest{Op: op(), Model: id, Rev: rev}))
}
