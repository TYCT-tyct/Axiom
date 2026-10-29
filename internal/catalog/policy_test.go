package catalog_test

import (
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
)

var (
	siteScope = pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, nil)
	noScope   = pg.Scope{}
)

// TestVisibility: public data is read in any non-empty scope, but a draft model, its names and its prices only in the
// platform scope; platform data only there; a transaction with no scope reads nothing.
func TestVisibility(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.db.Check(t.Context(), catalog.Migrations); err != nil {
		t.Fatal(err)
	}
	draft, _ := s.seed("acme/draft", basicSpec, basic)
	listed, _ := s.seed("acme/listed", basicSpec, basic)
	s.list(listed)
	count := func(scope pg.Scope, table string) int {
		return query[int](s, scope, "SELECT count(*) FROM catalog."+table)
	}
	cases := []struct {
		table                string
		platform, site, none int
	}{
		{"author", 1, 1, 0},
		{"model", 2, 1, 0},
		{"name", 2, 1, 0},
		{"reference_price", 2, 1, 0},
		{"revision", 1, 1, 0},
		{"change", 9, 0, 0},
		{"sync_state", 1, 0, 0},
	}
	for _, c := range cases {
		if got := [3]int{count(platformScope, c.table), count(siteScope, c.table), count(noScope, c.table)}; got != [3]int{c.platform, c.site, c.none} {
			t.Errorf("%s as platform, site, none: %v, want %v", c.table, got, [3]int{c.platform, c.site, c.none})
		}
	}
	// A site scope cannot write, and a draft is out of its reach even by id.
	if err := s.exec(siteScope, "UPDATE catalog.model SET status = 'listed' WHERE id = $1", uuid.UUID(draft)); err != nil {
		t.Fatal(err) // zero rows: the row is invisible, not an error
	}
	if got := query[string](s, platformScope, "SELECT status FROM catalog.model WHERE id = $1", uuid.UUID(draft)); got != "draft" {
		t.Errorf("a site-scope transaction changed a draft to %q", got)
	}
	wantErr(t, s.exec(siteScope, "INSERT INTO catalog.author (slug, name) VALUES ('evil', 'Evil')"), "row-level security")
	if got := query[int](s, siteScope, "WITH u AS (UPDATE catalog.author SET name = 'x' RETURNING 1) SELECT count(*) FROM u"); got != 0 {
		t.Errorf("a site scope renamed %d authors", got)
	}
}

// TestPublicTablesHoldNoInternalColumns: the actor, reason and evidence live only in the platform data.
func TestPublicTablesHoldNoInternalColumns(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	leaked := query[string](s, platformScope, `SELECT coalesce(string_agg(table_name || '.' || column_name, ', '), '')
		FROM information_schema.columns WHERE table_schema = 'catalog' AND table_name <> 'change'
		AND column_name IN ('actor', 'reason', 'evidence', 'op_key')`)
	if leaked != "" {
		t.Errorf("public tables have internal columns: %s", leaked)
	}
}

// TestGrants: the application appends names, prices and changes and cannot rewrite or delete them; it cannot choose a
// model's id or rev, change its slug, or delete anything.
func TestGrants(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	cases := []struct{ name, sql string }{
		{"rewrite a name", "UPDATE catalog.name SET visibility = 'internal'"},
		{"delete a name", "DELETE FROM catalog.name"},
		{"rewrite a price", "UPDATE catalog.reference_price SET source = 'official'"},
		{"delete a price", "DELETE FROM catalog.reference_price"},
		{"rewrite a change", "UPDATE catalog.change SET reason = 'x'"},
		{"delete a change", "DELETE FROM catalog.change"},
		{"delete a model", "DELETE FROM catalog.model"},
		{"rename a model", "UPDATE catalog.model SET slug = 'acme/other'"},
		{"choose a model's id", "INSERT INTO catalog.model (id, author, slug, version_basis, price_source) VALUES (gen_random_uuid(), 'acme', 'acme/b', 'dated', 'manual')"},
		{"backdate a price", "INSERT INTO catalog.reference_price (model_id, currency, source, published_at) VALUES ('" + id.String() + "', 'EUR', 'manual', now() - interval '1 day')"},
		{"set the revision to anything but a column it may", "UPDATE catalog.revision SET singleton = false"},
		{"set the sync state's identity", "UPDATE catalog.sync_state SET singleton = false"},
	}
	for _, c := range cases {
		wantErr(t, s.exec(platformScope, c.sql), "permission denied")
	}
}

// TestPriceChainIsEnforcedByTheDatabase: whatever a program does, the history of a currency cannot fork, loop, cross
// to another model or currency, or start twice.
func TestPriceChainIsEnforcedByTheDatabase(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	a, first := s.seed("acme/a", basicSpec, basic)
	_, other := s.seed("acme/b", basicSpec, basic)
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: a, Source: catalog.SourceManual, Schedule: raw(basicCNY), Evidence: raw(evidence),
	}))
	insert := func(model catalog.ModelID, currency string, supersedes uuid.UUID) string {
		return "INSERT INTO catalog.reference_price (model_id, currency, source, supersedes) VALUES ('" + model.String() +
			"', '" + currency + "', 'manual', " + nullable(supersedes) + ")"
	}
	cases := []struct{ name, sql, want string }{
		{"a second first row", insert(a, "USD", uuid.UUID{}), "reference_price_first"},
		{"another model's row", insert(a, "USD", other), "reference_price_supersedes_model_id_currency_fkey"},
		{"another currency's row", insert(a, "CNY", first), "reference_price_supersedes_model_id_currency_fkey"},
		{"a row already replaced", insert(a, "USD", first) + "; " + insert(a, "USD", first), "supersedes_key"},
		{"a schedule of another currency", "INSERT INTO catalog.reference_price (model_id, currency, source, schedule, schedule_sha256) VALUES ('" +
			a.String() + "', 'EUR', 'manual', '" + basic + "', decode(repeat('00', 32), 'hex'))", "schedule_in_currency"},
		{"a schedule without its hash", "INSERT INTO catalog.reference_price (model_id, currency, source, schedule) VALUES ('" +
			a.String() + "', 'EUR', 'manual', '" + strings.ReplaceAll(basic, "USD", "EUR") + "')", "schedule_with_hash"},
	}
	for _, c := range cases {
		wantErr(t, s.exec(platformScope, c.sql), c.want)
	}
}

func nullable(id uuid.UUID) string {
	if id == (uuid.UUID{}) {
		return "NULL"
	}
	return "'" + id.String() + "'"
}
