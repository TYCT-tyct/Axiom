package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The catalog's operations run inside the transaction that claims the Idempotency-Key (idempotently), and carry it as the
// catalog's own op_key (internal/catalog/README.md「管理操作」): a retry gets the response the first request stored.

// opOf is the identity and intent every catalog operation carries.
func opOf(ctx context.Context, key, reason string) (catalog.Op, error) {
	id, err := keyOf(key)
	if err != nil {
		return catalog.Op{}, err
	}
	return catalog.Op{Actor: actor(ctx), Key: id, Reason: reason}, nil
}

// modelIDOf reads a model's ID from a path. A malformed ID and an unknown one are the same 404.
func modelIDOf(s string) (catalog.ModelID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id.String() != s {
		return catalog.ModelID{}, catalog.ErrModelNotFound
	}
	return catalog.ModelID(id), nil
}

// rawOf is a request member as the catalog keeps it, canonical JSON.
func rawOf(v any) (jsonx.Raw, error) {
	encoded, err := jsonx.MarshalCanonical(v)
	return jsonx.Raw(encoded), err
}

// authorSlug is the contract's AuthorSlug, which the guard does not check a path parameter against: a slug that is none is
// a request that cannot be made, not a resource that is not there.
var authorSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// authorBody is an author as the contract gives it.
func authorBody(a catalog.Author) adminapi.Author { return adminapi.Author{Slug: a.Slug, Name: a.Name} }

// ListAuthors lists every author.
func (a adminAPI) ListAuthors(ctx context.Context, _ adminapi.ListAuthorsRequestObject) (adminapi.ListAuthorsResponseObject, error) {
	authors, err := a.catalog.Authors(ctx)
	if err != nil {
		return nil, err
	}
	list := adminapi.AuthorList{Data: make([]adminapi.Author, len(authors))}
	for i, author := range authors {
		list.Data[i] = authorBody(author)
	}
	return adminapi.ListAuthors200JSONResponse(list), nil
}

// SaveAuthor makes the author or changes its display name.
func (a adminAPI) SaveAuthor(ctx context.Context, request adminapi.SaveAuthorRequestObject) (adminapi.SaveAuthorResponseObject, error) {
	if !authorSlug.MatchString(request.Slug) {
		return nil, invalidField("slug", "not an author slug")
	}
	op, err := opOf(ctx, request.Params.IdempotencyKey, request.Body.Reason)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "saveAuthor", request.Params.IdempotencyKey, []any{request.Slug, request.Body},
		func(tx pg.Tx) (adminapi.Author, error) {
			op.Tx = &tx
			if _, err := a.catalog.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op, Slug: request.Slug, Name: request.Body.Name}); err != nil {
				return adminapi.Author{}, err
			}
			return authorBody(catalog.Author{Slug: request.Slug, Name: request.Body.Name}), nil
		})
	if err != nil {
		return nil, err
	}
	a.refreshCatalog(ctx, replayed)
	return adminapi.SaveAuthor200JSONResponse{Body: body, Headers: adminapi.SaveAuthor200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// cursorOf reads the cursor of a model list: the slug the page before ended with, which a client cannot tell from any
// other opaque text.
func cursorOf(cursor *string) (string, error) {
	if cursor == nil {
		return "", nil
	}
	slug, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil || len(slug) == 0 {
		return "", invalidField("cursor", "not a cursor this API gave out")
	}
	return string(slug), nil
}

// ListModels lists models by slug, drafts included, a page at a time.
func (a adminAPI) ListModels(ctx context.Context, request adminapi.ListModelsRequestObject) (adminapi.ListModelsResponseObject, error) {
	p := request.Params
	q := catalog.ModelQuery{PageSize: pageSize(p.Limit)}
	if p.Status != nil {
		q.Status = catalog.Status(*p.Status)
	}
	if p.Author != nil {
		q.Author = *p.Author
	}
	if p.SlugPrefix != nil {
		q.SlugPrefix = *p.SlugPrefix
	}
	var err error
	if q.After, err = cursorOf(p.Cursor); err != nil {
		return nil, err
	}
	models, more, err := a.catalog.Models(ctx, q)
	if err != nil {
		return nil, err
	}
	list := adminapi.ModelList{Data: make([]adminapi.ModelSummary, len(models))}
	for i, m := range models {
		list.Data[i] = adminapi.ModelSummary{
			Id: m.ID.String(), Slug: m.Slug, Author: m.Author, Status: adminapi.ModelStatus(m.Status),
			VersionBasis: adminapi.VersionBasis(m.VersionBasis), PriceSource: adminapi.PriceSource(m.PriceSource), Rev: m.Rev,
		}
	}
	if more {
		next := base64.RawURLEncoding.EncodeToString([]byte(models[len(models)-1].Slug))
		list.NextCursor = &next
	}
	return adminapi.ListModels200JSONResponse(list), nil
}

// modelBody is a model as the contract gives it. The spec and the schedules are the catalog's canonical JSON, which is
// the contract's own form of them.
func modelBody(v catalog.ModelView) (adminapi.Model, error) {
	body := adminapi.Model{
		Id: v.ID.String(), Slug: v.Slug, Author: v.Author, Status: adminapi.ModelStatus(v.Status),
		VersionBasis: adminapi.VersionBasis(v.VersionBasis), PriceSource: adminapi.PriceSource(v.PriceSource), Rev: v.Rev,
		Names: make([]adminapi.ModelName, len(v.Names)), Prices: make([]adminapi.ReferencePrice, len(v.Prices)),
	}
	if err := jsonx.Unmarshal(v.Spec, &body.Spec); err != nil {
		return adminapi.Model{}, err
	}
	for i, n := range v.Names {
		body.Names[i] = adminapi.ModelName{Name: n.Name, Visibility: adminapi.ModelNameVisibility(n.Visibility)}
	}
	for i, p := range v.Prices {
		body.Prices[i] = adminapi.ReferencePrice{Id: p.ID.String(), Currency: p.Currency, Source: adminapi.PriceSource(p.Source), PublishedAt: p.PublishedAt}
		if p.Schedule != nil {
			body.Prices[i].Schedule = new(adminapi.PriceSchedule)
			if err := jsonx.Unmarshal(p.Schedule, body.Prices[i].Schedule); err != nil {
				return adminapi.Model{}, err
			}
		}
	}
	return body, nil
}

// readModelIn is the model as it is in tx, the transaction of a change.
func (adminAPI) readModelIn(ctx context.Context, tx pg.Tx, id catalog.ModelID) (adminapi.Model, error) {
	view, err := catalog.ModelIn(ctx, tx, id)
	if err != nil {
		return adminapi.Model{}, err
	}
	return modelBody(view)
}

// readModel is the model as it is now.
func (a adminAPI) readModel(ctx context.Context, id catalog.ModelID) (adminapi.Model, error) {
	view, err := a.catalog.Model(ctx, id)
	if err != nil {
		return adminapi.Model{}, err
	}
	return modelBody(view)
}

// GetModel reads one model with its names and prices.
func (a adminAPI) GetModel(ctx context.Context, request adminapi.GetModelRequestObject) (adminapi.GetModelResponseObject, error) {
	id, err := modelIDOf(request.ModelId)
	if err != nil {
		return nil, err
	}
	body, err := a.readModel(ctx, id)
	if err != nil {
		return nil, err
	}
	return adminapi.GetModel200JSONResponse(body), nil
}

// refreshCatalog rebuilds this node's snapshot after a change committed. A failure is not the change's: the other nodes
// poll for the revision, and so does this one.
func (a adminAPI) refreshCatalog(ctx context.Context, replayed bool) {
	if replayed {
		return
	}
	if err := a.catalog.Refresh(context.WithoutCancel(ctx)); err != nil {
		a.logger.WarnContext(ctx, "catalog change saved but this node did not reload it", "err", err)
	}
}

// changed runs a change of a model that takes an Idempotency-Key as the operation, answers with the model as the change
// left it, and says whether the key had been used by this request before: then the answer is the first one's. request is
// the body the answer depends on.
func (a adminAPI) changed(ctx context.Context, operation, model, key string, request any, reason string, change func(op catalog.Op, id catalog.ModelID) (catalog.Result, error)) (adminapi.Model, *bool, error) {
	id, err := modelIDOf(model)
	if err != nil {
		return adminapi.Model{}, nil, err
	}
	op, err := opOf(ctx, key, reason)
	if err != nil {
		return adminapi.Model{}, nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, operation, key, []any{model, request}, func(tx pg.Tx) (adminapi.Model, error) {
		op.Tx = &tx
		if _, err := change(op, id); err != nil {
			return adminapi.Model{}, err
		}
		return a.readModelIn(ctx, tx, id)
	})
	a.refreshCatalog(ctx, replayed || err != nil)
	return body, replayedHeader(replayed), err
}

// CreateModel drafts a model.
func (a adminAPI) CreateModel(ctx context.Context, request adminapi.CreateModelRequestObject) (adminapi.CreateModelResponseObject, error) {
	b := request.Body
	spec, err := rawOf(b.Spec)
	if err != nil {
		return nil, err
	}
	op, err := opOf(ctx, request.Params.IdempotencyKey, b.Reason)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "createModel", request.Params.IdempotencyKey, b, func(tx pg.Tx) (adminapi.Model, error) {
		op.Tx = &tx
		result, err := a.catalog.CreateModel(ctx, catalog.CreateModelRequest{
			Op: op, Slug: b.Slug, VersionBasis: catalog.VersionBasis(b.VersionBasis), PriceSource: catalog.PriceSource(b.PriceSource), Spec: spec,
		})
		if err != nil {
			return adminapi.Model{}, err
		}
		id, err := modelIDOf(result.Subject)
		if err != nil {
			return adminapi.Model{}, err
		}
		return a.readModelIn(ctx, tx, id)
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return nil, &apiError{status: http.StatusConflict, code: codeConflict, message: "a model with that slug exists"}
	}
	if err != nil {
		return nil, err
	}
	a.refreshCatalog(ctx, replayed)
	return adminapi.CreateModel201JSONResponse{Body: body, Headers: adminapi.CreateModel201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdateModelSpec replaces a model's baseline as of the rev the request was decided on.
func (a adminAPI) UpdateModelSpec(ctx context.Context, request adminapi.UpdateModelSpecRequestObject) (adminapi.UpdateModelSpecResponseObject, error) {
	b := request.Body
	spec, err := rawOf(b.Spec)
	if err != nil {
		return nil, err
	}
	body, replayed, err := a.changed(ctx, "updateModelSpec", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		return a.catalog.UpdateSpec(ctx, catalog.UpdateSpecRequest{Op: op, Model: id, Rev: b.Rev, Spec: spec})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateModelSpec200JSONResponse{Body: body, Headers: adminapi.UpdateModelSpec200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}

// AddModelName gives a model another name.
func (a adminAPI) AddModelName(ctx context.Context, request adminapi.AddModelNameRequestObject) (adminapi.AddModelNameResponseObject, error) {
	b := request.Body
	body, replayed, err := a.changed(ctx, "addModelName", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		return a.catalog.AddName(ctx, catalog.AddNameRequest{Op: op, Model: id, Name: b.Name, Visibility: catalog.Visibility(b.Visibility)})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.AddModelName200JSONResponse{Body: body, Headers: adminapi.AddModelName200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}

// SetModelStatus puts a draft model on sale or takes a model on sale out of use, as of the rev the request was decided on.
func (a adminAPI) SetModelStatus(ctx context.Context, request adminapi.SetModelStatusRequestObject) (adminapi.SetModelStatusResponseObject, error) {
	b := request.Body
	body, replayed, err := a.changed(ctx, "setModelStatus", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		r := catalog.ModelRequest{Op: op, Model: id, Rev: b.Rev}
		if b.Status == adminapi.ModelStatusUpdateStatusRetired {
			return a.catalog.Retire(ctx, r)
		}
		return a.catalog.ListModel(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetModelStatus200JSONResponse{Body: body, Headers: adminapi.SetModelStatus200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}

// baseOf reads the ID of the price row a request was decided on; nil is a currency with no history.
func baseOf(base *adminapi.Uuid) (*uuid.UUID, error) {
	if base == nil {
		return nil, nil //nolint:nilnil // no base is a meaning of its own: the currency has no history
	}
	id, err := uuid.Parse(*base)
	if err != nil {
		return nil, invalidField("/base", "not a price row's ID")
	}
	return &id, nil
}

// PublishReferencePrice adds a version to a model's price history in one currency.
func (a adminAPI) PublishReferencePrice(ctx context.Context, request adminapi.PublishReferencePriceRequestObject) (adminapi.PublishReferencePriceResponseObject, error) {
	b := request.Body
	base, err := baseOf(b.Base)
	if err != nil {
		return nil, err
	}
	schedule, err := rawOf(b.Schedule)
	if err != nil {
		return nil, err
	}
	evidence, err := rawOf(b.Evidence)
	if err != nil {
		return nil, err
	}
	body, replayed, err := a.changed(ctx, "publishReferencePrice", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		return a.catalog.PublishPrice(ctx, catalog.PublishPriceRequest{
			Op: op, Model: id, Base: base, Source: catalog.PriceSource(b.Source), Schedule: schedule, Evidence: evidence,
			ConfirmBasisChange: b.ConfirmBasisChange != nil && *b.ConfirmBasisChange,
		})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.PublishReferencePrice200JSONResponse{Body: body, Headers: adminapi.PublishReferencePrice200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}

// sourcePricesOf reads the prices of a switch of source: the schedules with their evidence, and the current row of each
// currency the request was decided on.
func sourcePricesOf(b *adminapi.PriceSourceSelect) (prices []catalog.SourcePrice, bases map[string]uuid.UUID, err error) {
	for _, p := range b.Prices {
		var price catalog.SourcePrice
		if price.Schedule, err = rawOf(p.Schedule); err != nil {
			return nil, nil, err
		}
		if price.Evidence, err = rawOf(p.Evidence); err != nil {
			return nil, nil, err
		}
		prices = append(prices, price)
	}
	bases = make(map[string]uuid.UUID, len(b.Bases))
	for currency, text := range b.Bases {
		if bases[currency], err = uuid.Parse(text); err != nil {
			return nil, nil, invalidField("/bases/"+currency, "not a price row's ID")
		}
	}
	return prices, bases, nil
}

// SelectPriceSource switches a model to another source and publishes the new source's price in each currency.
func (a adminAPI) SelectPriceSource(ctx context.Context, request adminapi.SelectPriceSourceRequestObject) (adminapi.SelectPriceSourceResponseObject, error) {
	b := request.Body
	prices, bases, err := sourcePricesOf(b)
	if err != nil {
		return nil, err
	}
	body, replayed, err := a.changed(ctx, "selectPriceSource", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		return a.catalog.SelectPriceSource(ctx, catalog.SelectPriceSourceRequest{
			Op: op, Model: id, Rev: b.Rev, Source: catalog.PriceSource(b.Source), Bases: bases, Prices: prices,
			ConfirmBasisChange: b.ConfirmBasisChange != nil && *b.ConfirmBasisChange,
		})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SelectPriceSource200JSONResponse{Body: body, Headers: adminapi.SelectPriceSource200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}
