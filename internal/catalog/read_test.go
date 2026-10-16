package catalog_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
)

// listed seeds models and lists the first of them, leaving the second a draft.
func (s *store) listed() (listed, draft catalog.ModelID) {
	s.t.Helper()
	listed, _ = s.seed("acme/alpha", basicSpec, basic)
	s.list(listed)
	draft, _ = s.seed("acme/beta", basicSpec, basic)
	return listed, draft
}

// models lists a page of models.
func (s *store) models(q catalog.ModelQuery) ([]string, bool) {
	s.t.Helper()
	models, more, err := s.c.Models(s.t.Context(), q)
	if err != nil {
		s.t.Fatal(err)
	}
	var slugs []string
	for _, m := range models {
		slugs = append(slugs, m.Slug)
	}
	return slugs, more
}

// TestAuthors: every author by slug, and one by its slug.
func TestAuthors(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.must(s.c.SaveAuthor(t.Context(), catalog.SaveAuthorRequest{Op: op(), Slug: "zeta", Name: "Zeta"}))
	s.must(s.c.SaveAuthor(t.Context(), catalog.SaveAuthorRequest{Op: op(), Slug: "acme", Name: "Acme"}))
	authors, err := s.c.Authors(t.Context())
	if want := []catalog.Author{{Slug: "acme", Name: "Acme"}, {Slug: "zeta", Name: "Zeta"}}; err != nil || !slices.Equal(authors, want) {
		t.Errorf("the authors: %v %v, want %v", authors, err, want)
	}
	if author, err := s.c.GetAuthor(t.Context(), "zeta"); err != nil || author.Name != "Zeta" {
		t.Errorf("one author: %v %v", author, err)
	}
	if _, err := s.c.GetAuthor(t.Context(), "nobody"); !errors.Is(err, catalog.ErrAuthorMissing) {
		t.Errorf("an author that is not there: %v", err)
	}
}

// seedFour seeds four models, one of them listed, and returns their slugs.
func (s *store) seedFour() []string {
	s.t.Helper()
	s.listed()
	s.seed("zeta/gamma_1", basicSpec, basic)
	s.seed("zeta/gammax", basicSpec, basic)
	return []string{"acme/alpha", "acme/beta", "zeta/gamma_1", "zeta/gammax"}
}

// TestModelsInPages: models by slug, drafts included, in pages that follow each other.
func TestModelsInPages(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	want := s.seedFour()
	if all, more := s.models(catalog.ModelQuery{PageSize: 10}); more || !slices.Equal(all, want) {
		t.Fatalf("all the models: %v more=%v, want %v", all, more, want)
	}
	first, more := s.models(catalog.ModelQuery{PageSize: 3})
	rest, moreRest := s.models(catalog.ModelQuery{PageSize: 3, After: first[2]})
	if !more || !slices.Equal(first, want[:3]) || moreRest || !slices.Equal(rest, want[3:]) {
		t.Errorf("pages of 3: %v more=%v, then %v more=%v", first, more, rest, moreRest)
	}
}

// TestModelsFiltered: by status, by author and by the start of the slug, which is matched literally.
func TestModelsFiltered(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seedFour()
	for name, c := range map[string]struct {
		query catalog.ModelQuery
		want  []string
	}{
		"status draft":  {catalog.ModelQuery{Status: catalog.StatusDraft}, []string{"acme/beta", "zeta/gamma_1", "zeta/gammax"}},
		"status listed": {catalog.ModelQuery{Status: catalog.StatusListed}, []string{"acme/alpha"}},
		"author zeta":   {catalog.ModelQuery{Author: "zeta"}, []string{"zeta/gamma_1", "zeta/gammax"}},
		"prefix acme/":  {catalog.ModelQuery{SlugPrefix: "acme/"}, []string{"acme/alpha", "acme/beta"}},
		"prefix gamma_": {catalog.ModelQuery{SlugPrefix: "gamma_"}, nil},
		"prefix zeta/g": {catalog.ModelQuery{SlugPrefix: "zeta/g"}, []string{"zeta/gamma_1", "zeta/gammax"}},
		"prefix zeta/%": {catalog.ModelQuery{SlugPrefix: "zeta/%"}, nil},
		"prefix zeta/_": {catalog.ModelQuery{SlugPrefix: "zeta/_"}, nil},
	} {
		c.query.PageSize = 10
		if got, _ := s.models(c.query); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// TestModelView: a model with its spec and its names, by name.
func TestModelView(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.listed()
	s.must(s.c.AddName(t.Context(), catalog.AddNameRequest{Op: op(), Model: id, Name: "acme/alpha-latest-internal", Visibility: catalog.VisibilityInternal}))
	view, err := s.c.Model(t.Context(), id)
	if err != nil || view.Slug != "acme/alpha" || view.Status != catalog.StatusListed || view.Rev != 1 {
		t.Fatalf("the model: %+v %v", view, err)
	}
	if view.Author != "acme" || !strings.Contains(string(view.Spec), "context_length") {
		t.Errorf("its author and spec: %q %s", view.Author, view.Spec)
	}
	names := []catalog.NameView{{Name: "acme/alpha", Visibility: catalog.VisibilityPublic}, {Name: "acme/alpha-latest-internal", Visibility: catalog.VisibilityInternal}}
	if !slices.Equal(view.Names, names) {
		t.Errorf("the names: %v, want %v", view.Names, names)
	}
}

// TestModelViewPrices: the current price of each currency, by currency.
func TestModelViewPrices(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.listed()
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: id, Source: catalog.SourceManual, Schedule: raw(basicCNY), Evidence: raw(evidence),
	}))
	view, err := s.c.Model(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var currencies []string
	for _, p := range view.Prices {
		currencies = append(currencies, p.Currency)
	}
	if !slices.Equal(currencies, []string{"CNY", "USD"}) {
		t.Fatalf("the prices: %+v", view.Prices)
	}
	if p := view.Prices[1]; p.Schedule == nil || p.Source != catalog.SourceManual || p.PublishedAt.IsZero() {
		t.Errorf("the USD price: %+v", p)
	}
}

// TestModelNotFound: a model that is not there is ErrModelNotFound.
func TestModelNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.c.Model(t.Context(), catalog.ModelID(uuid.NewV7())); !errors.Is(err, catalog.ErrModelNotFound) {
		t.Errorf("a model that is not there: %v", err)
	}
}
