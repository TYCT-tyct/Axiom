package catalog_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
)

// TestAlias: an alias is catalog data that points at one concrete model. Moving it appends a revision, the snapshot
// follows the latest, and a retired target is retired through the alias too.
func TestAlias(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	alpha, _ := s.seed("acme/alpha", basicSpec, basic)
	beta, _ := s.seed("acme/beta", basicSpec, basic)
	s.list(alpha)
	s.list(beta)

	save := func(name string, model catalog.ModelID) error {
		_, err := s.c.SaveAlias(ctx, catalog.SaveAliasRequest{Op: op(), Name: name, Model: model})
		return err
	}
	if err := save("~acme/family-latest", alpha); err != nil {
		t.Fatal(err)
	}
	snap, err := s.c.Current()
	if err != nil {
		t.Fatal(err)
	}
	got, err := snap.ResolvePublic("~acme/family-latest")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != alpha {
		t.Errorf("alias resolves to %v, want %v", got.Model, alpha)
	}
	if err := save("~acme/family-latest", beta); err != nil {
		t.Fatal(err)
	}
	snap, err = s.c.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got, err = snap.ResolvePublic("~acme/family-latest"); err != nil || got.Model != beta {
		t.Errorf("moved alias resolves to %v, %v; want %v", got.Model, err, beta)
	}
	aliases, err := s.c.Aliases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Model != beta || aliases[0].Rev != 2 {
		t.Errorf("aliases = %+v, want the second revision pointing at beta", aliases)
	}
	if n := query[int64](s, platformScope, "SELECT count(*) FROM catalog.alias WHERE name = $1", "~acme/family-latest"); n != 2 {
		t.Errorf("alias history has %d rows, want 2", n)
	}

	if err := save("~acme/family-latest", alpha); err != nil {
		t.Fatal(err)
	}
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(alpha))
	s.must(s.c.Retire(ctx, catalog.ModelRequest{Op: op(), Model: alpha, Rev: rev}))
	snap, err = s.c.Current()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snap.ResolvePublic("~acme/family-latest"); !errors.Is(err, catalog.ErrRetired) {
		t.Errorf("an alias of a retired model gives %v, want ErrRetired", err)
	}

	if err := save("~acme/family.5-latest", beta); err != nil {
		t.Errorf("a family with a dot: %v", err)
	}
	wantErr(t, save("~other/family-latest", beta), "belongs to")
	wantErr(t, save("acme/family-latest", alpha), "an alias is ~author/family-latest")
}

// TestFreeEntry: a :free model is its own catalog entry with its own name and zero price, so the suffix resolves to
// it, and a paid price behind the same suffix is refused.
func TestFreeEntry(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	alpha, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(alpha)

	free := s.model(s.must(s.c.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: "acme/alpha:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{
		Op: op(), Model: free, Name: "acme/alpha:free", Visibility: catalog.VisibilityPublic,
	}))
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: free, Source: catalog.SourceManual,
		Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`), Evidence: raw(evidence),
	}))
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(free))
	s.must(s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: free, Rev: rev}))

	snap, err := s.c.Current()
	if err != nil {
		t.Fatal(err)
	}
	got, err := snap.ResolvePublic("acme/alpha:free")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != free {
		t.Errorf(":free resolves to %v, want %v", got.Model, free)
	}
	if got, err := snap.ResolvePublic("acme/alpha"); err != nil || got.Model != alpha {
		t.Errorf("the base resolves to %v, %v; want %v", got.Model, err, alpha)
	}
	if _, err := snap.ResolvePublic("acme/alpha:free:free"); !errors.Is(err, catalog.ErrModelNotFound) {
		t.Errorf("a stacked :free gives %v, want ErrModelNotFound", err)
	}
	wantErr(t, func() error {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: alpha, Name: "acme/other:free", Visibility: catalog.VisibilityPublic})
		return err
	}(), "must be its model's standard ID")
	wantErr(t, func() error {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: free, Name: "acme/free-model", Visibility: catalog.VisibilityPublic})
		return err
	}(), "public names must end in :free")

	paid := s.model(s.must(s.c.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: "acme/paid:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: paid, Name: "acme/paid:free", Visibility: catalog.VisibilityPublic}))
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: paid, Source: catalog.SourceManual, Schedule: raw(basic), Evidence: raw(evidence),
	}))
	rev = query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(paid))
	wantErr(t, func() error {
		_, err := s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: paid, Rev: rev})
		return err
	}(), "must have a zero price")
}

// TestFreeEntryNeedsBase: the suffix only means something when the base is a listed public model.
func TestFreeEntryNeedsBase(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	s.seed("acme/alpha", basicSpec, basic)
	free := s.model(s.must(s.c.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: "acme/missing:free", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: free, Name: "acme/missing:free", Visibility: catalog.VisibilityPublic}))
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: free, Source: catalog.SourceManual,
		Schedule: raw(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`), Evidence: raw(evidence),
	}))
	rev := query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(free))
	wantErr(t, func() error {
		_, err := s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: free, Rev: rev})
		return err
	}(), "no public name")
	wantErr(t, func() error {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: free, Name: "acme/missing:paid", Visibility: catalog.VisibilityPublic})
		return err
	}(), "no other colon")
}
