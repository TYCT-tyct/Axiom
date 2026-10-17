package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// What the admin lists and reads of the catalog, drafts included. They run in the platform scope the catalog was
// opened with, in one consistent read each (README.md「管理操作」).

// Author is a model author as the admin lists it.
type Author struct{ Slug, Name string }

// Authors are all the authors, by slug.
func (c *Catalog) Authors(ctx context.Context) ([]Author, error) {
	var rows []CatalogAuthor
	if err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		rows, err = New().ListAuthors(ctx, tx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("list the authors: %w", err)
	}
	authors := make([]Author, len(rows))
	for i, r := range rows {
		authors[i] = Author(r)
	}
	return authors, nil
}

// GetAuthor is the author called slug; ErrAuthorMissing when there is none.
func (c *Catalog) GetAuthor(ctx context.Context, slug string) (Author, error) {
	var row CatalogAuthor
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		row, err = New().GetAuthor(ctx, tx, slug)
		return err
	})
	if errors.Is(err, pg.ErrNoRows) {
		return Author{}, fmt.Errorf("%w: %q", ErrAuthorMissing, slug)
	}
	if err != nil {
		return Author{}, fmt.Errorf("read the author: %w", err)
	}
	return Author(row), nil
}

// ModelSummary is a model as a list shows it.
type ModelSummary struct {
	ID           ModelID
	Slug, Author string
	Status       Status
	VersionBasis VersionBasis
	PriceSource  PriceSource
	Rev          int64
}

func summaryOf(row CatalogModel) ModelSummary {
	return ModelSummary{
		ID: ModelID(row.ID), Slug: row.Slug, Author: row.Author, Status: Status(row.Status),
		VersionBasis: VersionBasis(row.VersionBasis), PriceSource: PriceSource(row.PriceSource), Rev: row.Rev,
	}
}

// ModelQuery chooses the models to list, by slug. An empty field is no filter; SlugPrefix is matched literally; After is
// the slug the page before ended with, empty for the first page.
type ModelQuery struct {
	Status     Status
	Author     string
	SlugPrefix string
	After      string
	PageSize   int
}

// likeEscaper makes a prefix a literal LIKE pattern (backslash is LIKE's default escape).
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func filter(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Models lists a page of models; more tells whether models remain beyond it.
func (c *Catalog) Models(ctx context.Context, q ModelQuery) (models []ModelSummary, more bool, err error) {
	params := ListModelsPageParams{
		Status: filter(string(q.Status)), Author: filter(q.Author), After: filter(q.After),
		PageSize: int32(q.PageSize + 1), // #nosec G115 -- a page is at most 200
	}
	if q.SlugPrefix != "" {
		prefix := likeEscaper.Replace(q.SlugPrefix)
		params.SlugPrefix = &prefix
	}
	var rows []CatalogModel
	if err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		rows, err = New().ListModelsPage(ctx, tx, params)
		return err
	}); err != nil {
		return nil, false, fmt.Errorf("list the models: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	models = make([]ModelSummary, len(rows))
	for i, r := range rows {
		models[i] = summaryOf(r)
	}
	return models, more, nil
}

// NameView is a name of a model.
type NameView struct {
	Name       string
	Visibility Visibility
}

// PriceView is the current version of a model's reference price in one currency. Schedule is nil when the price was
// withdrawn: the row is still the current one to build on.
type PriceView struct {
	ID          uuid.UUID
	Currency    string
	Source      PriceSource
	Schedule    jsonx.Raw
	PublishedAt time.Time
}

// ModelView is a model with its spec, its names and its current prices.
type ModelView struct {
	ModelSummary
	Spec   jsonx.Raw
	Names  []NameView
	Prices []PriceView
}

// Model reads one model with its names, by name, and its current prices, by currency; ErrModelNotFound when there is
// none.
func (c *Catalog) Model(ctx context.Context, id ModelID) (view ModelView, err error) {
	err = c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		view, err = ModelIn(ctx, tx, id)
		return err
	})
	return view, err
}

// ModelIn is Model read in tx, the transaction of a change, which sees what the change wrote.
func ModelIn(ctx context.Context, tx pg.Tx, id ModelID) (ModelView, error) {
	row, err := New().GetModel(ctx, tx, uuid.UUID(id))
	if errors.Is(err, pg.ErrNoRows) {
		return ModelView{}, ErrModelNotFound
	}
	if err != nil {
		return ModelView{}, fmt.Errorf("read the model: %w", err)
	}
	names, err := New().ListNamesOfModel(ctx, tx, row.ID)
	if err != nil {
		return ModelView{}, fmt.Errorf("read the model's names: %w", err)
	}
	prices, err := New().ListCurrentPricesOfModel(ctx, tx, row.ID)
	if err != nil {
		return ModelView{}, fmt.Errorf("read the model's prices: %w", err)
	}
	view := ModelView{ModelSummary: summaryOf(row), Spec: row.Spec}
	for _, n := range names {
		view.Names = append(view.Names, NameView{n.Name, Visibility(n.Visibility)})
	}
	slices.SortFunc(view.Names, func(a, b NameView) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range prices {
		view.Prices = append(view.Prices, PriceView{p.ID, p.Currency, PriceSource(p.Source), p.Schedule, p.PublishedAt})
	}
	slices.SortFunc(view.Prices, func(a, b PriceView) int { return strings.Compare(a.Currency, b.Currency) })
	return view, nil
}
