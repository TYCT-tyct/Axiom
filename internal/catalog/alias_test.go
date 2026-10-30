package catalog_test

import (
	"crypto/sha256"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
)

// saveAlias points an alias at a model as the admin command does.
func (s *store) saveAlias(name string, model catalog.ModelID) error {
	_, err := s.c.SaveAlias(s.t.Context(), catalog.SaveAliasRequest{Op: op(), Name: name, Model: model})
	return err
}

// resolve reads a public name from the current snapshot.
func (s *store) resolve(name string) (catalog.Resolution, error) {
	snap, err := s.c.Current()
	if err != nil {
		return catalog.Resolution{}, err
	}
	return snap.ResolvePublic(name)
}

// addName gives a model a public name.
func (s *store) addName(model catalog.ModelID, name string) error {
	_, err := s.c.AddName(s.t.Context(), catalog.AddNameRequest{
		Op: op(), Model: model, Name: name, Visibility: catalog.VisibilityPublic,
	})
	return err
}

// freeEntry is a listed acme/alpha:free: the base model is seeded and listed, and the free entry has its standard name and
// a zero USD price.
func (s *store) freeEntry() (base, free catalog.ModelID) {
	s.t.Helper()
	base, _ = s.seed("acme/alpha", basicSpec, basic)
	s.list(base)
	free = s.model(s.must(s.c.CreateModel(s.t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: "acme/alpha:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(s.t.Context(), catalog.AddNameRequest{
		Op: op(), Model: free, Name: "acme/alpha:free", Visibility: catalog.VisibilityPublic,
	}))
	s.must(s.c.PublishPrice(s.t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: free, Source: catalog.SourceManual,
		Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`), Evidence: raw(evidence),
	}))
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(free))
	s.must(s.c.ListModel(s.t.Context(), catalog.ModelRequest{Op: op(), Model: free, Rev: rev}))
	return base, free
}

// TestAlias: an alias is catalog data that points at one concrete model. Moving it appends a revision and the snapshot
// follows the latest, so discovery and calls reach the new target.
func TestAlias(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	alpha, _ := s.seed("acme/alpha", basicSpec, basic)
	beta, _ := s.seed("acme/beta", basicSpec, basic)
	s.list(alpha)
	s.list(beta)

	if err := s.saveAlias("~acme/family-latest", alpha); err != nil {
		t.Fatal(err)
	}
	if got, err := s.resolve("~acme/family-latest"); err != nil || got.Model != alpha {
		t.Errorf("alias resolves to %v, %v; want %v", got.Model, err, alpha)
	}
	if err := s.saveAlias("~acme/family-latest", beta); err != nil {
		t.Fatal(err)
	}
	if got, err := s.resolve("~acme/family-latest"); err != nil || got.Model != beta {
		t.Errorf("moved alias resolves to %v, %v; want %v", got.Model, err, beta)
	}
	aliases, err := s.c.Aliases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Model != beta || aliases[0].Rev != 2 {
		t.Errorf("aliases = %+v, want the second revision pointing at beta", aliases)
	}
	if n := query[int64](s, platformScope, "SELECT count(*) FROM catalog.alias WHERE name = $1", "~acme/family-latest"); n != 2 {
		t.Errorf("alias history has %d rows, want 2", n)
	}
}

// TestAliasRetirement: a retired target is retired through the alias too, because the alias names that concrete model.
func TestAliasRetirement(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	alpha, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(alpha)
	if err := s.saveAlias("~acme/family-latest", alpha); err != nil {
		t.Fatal(err)
	}
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(alpha))
	s.must(s.c.Retire(t.Context(), catalog.ModelRequest{Op: op(), Model: alpha, Rev: rev}))
	if _, err := s.resolve("~acme/family-latest"); !errors.Is(err, catalog.ErrRetired) {
		t.Errorf("an alias of a retired model gives %v, want ErrRetired", err)
	}
}

// TestAliasValidation: a family name may carry a dot, and an alias never reaches another vendor's model or a name that
// is not an alias.
func TestAliasValidation(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	alpha, _ := s.seed("acme/alpha", basicSpec, basic)
	beta, _ := s.seed("acme/beta", basicSpec, basic)
	s.list(alpha)
	s.list(beta)

	if err := s.saveAlias("~acme/family.5-latest", beta); err != nil {
		t.Errorf("a family with a dot: %v", err)
	}
	wantErr(t, s.saveAlias("~other/family-latest", beta), "belongs to")
	wantErr(t, s.saveAlias("acme/family-latest", alpha), "an alias is ~author/family-latest")
}

// TestFreeEntry: a :free model is its own catalog entry with its own name, so the suffix resolves to it and never to the
// paid base.
func TestFreeEntry(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	base, free := s.freeEntry()
	for _, tc := range []struct {
		name    string
		want    catalog.ModelID
		missing bool
	}{
		{"acme/alpha:free", free, false},
		{"acme/alpha", base, false},
		{"acme/alpha:free:free", catalog.ModelID{}, true},
	} {
		got, err := s.resolve(tc.name)
		switch {
		case tc.missing && !errors.Is(err, catalog.ErrModelNotFound):
			t.Errorf("%s gives %v, want ErrModelNotFound", tc.name, err)
		case !tc.missing && (err != nil || got.Model != tc.want):
			t.Errorf("%s resolves to %v, %v; want %v", tc.name, got.Model, err, tc.want)
		}
	}
}

// TestFreeEntryPriceWritesStayZero: a later price write must not turn the free entry into a paid one. PublishPrice and
// SelectPriceSource pass through the same zero-price gate.
func TestFreeEntryPriceWritesStayZero(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, free := s.freeEntry()
	base := s.head(free, "USD")
	wantErr(t, func() error {
		_, err := s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
			Op: op(), Model: free, Base: &base, Source: catalog.SourceManual,
			Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0"}}`), Evidence: raw(evidence),
		})
		return err
	}(), "zero")
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(free))
	wantErr(t, func() error {
		_, err := s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
			Op: op(), Model: free, Rev: rev, Source: catalog.SourceManual,
			Bases: map[string]uuid.UUID{"USD": base},
			Prices: []catalog.SourcePrice{{
				Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0"}}`), Evidence: raw(evidence),
			}},
		})
		return err
	}(), "zero")
	if got, err := s.resolve("acme/alpha:free"); err != nil || got.Model != free {
		t.Errorf("the free entry after refused price writes resolves to %v, %v; want %v", got.Model, err, free)
	}
}

// TestFreeEntryNames: the :free suffix belongs to its own entry's standard ID, and no other name of that entry or of the
// paid base may carry it.
func TestFreeEntryNames(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	base, free := s.freeEntry()
	wantErr(t, s.addName(base, "acme/other:free"), "must be its model's standard ID")
	wantErr(t, s.addName(free, "acme/free-model"), "public names must end in :free")
	wantErr(t, s.addName(free, "acme/missing:paid"), "no other colon")
}

// TestFreeEntryNeedsBase: the suffix only means something when the base is a listed public model.
func TestFreeEntryNeedsBase(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	free := s.model(s.must(s.c.CreateModel(t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: "acme/missing:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(t.Context(), catalog.AddNameRequest{
		Op: op(), Model: free, Name: "acme/missing:free", Visibility: catalog.VisibilityPublic,
	}))
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: free, Source: catalog.SourceManual,
		Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`), Evidence: raw(evidence),
	}))
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(free))
	wantErr(t, func() error {
		_, err := s.c.ListModel(t.Context(), catalog.ModelRequest{Op: op(), Model: free, Rev: rev})
		return err
	}(), "no public name")
}

// TestFreeEntryListingGate: a paid head inserted behind the admin entry points' backs is still refused at listing.
func TestFreeEntryListingGate(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic) // the author the entry belongs to
	paid := s.model(s.must(s.c.CreateModel(t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: "acme/paid:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(t.Context(), catalog.AddNameRequest{
		Op: op(), Model: paid, Name: "acme/paid:free", Visibility: catalog.VisibilityPublic,
	}))
	schedule, err := catalog.ParseSchedule([]byte(basic))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := schedule.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := catalog.New().InsertReferencePrice(t.Context(), tx, catalog.InsertReferencePriceParams{
			ModelID: uuid.UUID(paid), Currency: "USD", Source: string(catalog.SourceManual),
			Schedule: canonical, ScheduleSha256: sum[:],
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(paid))
	wantErr(t, func() error {
		_, err := s.c.ListModel(t.Context(), catalog.ModelRequest{Op: op(), Model: paid, Rev: rev})
		return err
	}(), "must have a zero price")
}
