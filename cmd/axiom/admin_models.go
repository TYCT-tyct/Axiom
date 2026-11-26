package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// The model library of the admin (#948): a model's family, listing a family at once, and drafts imported from a public
// price list. They are the catalog's operations, through the same idempotent transactions as admin_catalog.go.

// catalogUnits are the contract's MeteringUnit of each of the catalog's units, with a piece written as the image it is.
var catalogUnits = map[catalog.Unit]adminapi.MeteringUnit{
	catalog.UnitToken: adminapi.MeteringUnitToken, catalog.UnitUse: adminapi.MeteringUnitUse, catalog.UnitPiece: adminapi.MeteringUnitImage,
	catalog.UnitSecond: adminapi.MeteringUnitSecond, catalog.UnitCharacter: adminapi.MeteringUnitCharacter,
}

// namedUses are the items whose use the contract names: a request and a page.
var namedUses = map[catalog.Item]adminapi.MeteringUnit{
	catalog.ItemRequest: adminapi.MeteringUnitRequest, catalog.ItemFileParse: adminapi.MeteringUnitPage,
}

// meteringUnit is the contract's MeteringUnit of an item: its catalog unit, or the use the contract names.
func meteringUnit(it catalog.Item) adminapi.MeteringUnit {
	if u, ok := namedUses[it]; ok {
		return u
	}
	return catalogUnits[it.Unit()]
}

// billingUnits are the units a model's current reference prices charge in, in the contract's order: what the prices
// price, read from them, never stored.
func billingUnits(prices []catalog.PriceView) (*[]adminapi.BillingUnit, error) {
	var seen [catalog.NumItems]bool
	for _, p := range prices {
		if p.Schedule == nil {
			continue
		}
		s, err := catalog.ParseSchedule(p.Schedule)
		if err != nil {
			return nil, err
		}
		for _, r := range s.View().Rates {
			seen[r.Item] = true
		}
	}
	var units []adminapi.BillingUnit
	for it := range catalog.NumItems {
		if u := adminapi.BillingUnit(meteringUnit(it)); seen[it] && !slices.Contains(units, u) {
			units = append(units, u)
		}
	}
	slices.SortFunc(units, func(a, b adminapi.BillingUnit) int { return cmp.Compare(billingUnitOrder[a], billingUnitOrder[b]) })
	return &units, nil
}

// billingUnitOrder is the contract's enum order.
var billingUnitOrder = map[adminapi.BillingUnit]int{
	adminapi.BillingUnitToken: 0, adminapi.BillingUnitImage: 1, adminapi.BillingUnitSecond: 2, adminapi.BillingUnitCharacter: 3,
	adminapi.BillingUnitRequest: 4, adminapi.BillingUnitUse: 5, adminapi.BillingUnitPage: 6,
}

// familyBody is a family as the contract gives it: absent for none.
func familyBody(family string) *adminapi.ModelFamily {
	if family == "" {
		return nil
	}
	return &family
}

// modelSummaryBody is a model as a list gives it.
func modelSummaryBody(m catalog.ModelSummary) adminapi.ModelSummary {
	return adminapi.ModelSummary{
		Id: m.ID.String(), Slug: m.Slug, Author: m.Author, Status: adminapi.ModelStatus(m.Status),
		VersionBasis: adminapi.VersionBasis(m.VersionBasis), PriceSource: adminapi.PriceSource(m.PriceSource), Rev: m.Rev,
		Family: familyBody(m.Family),
	}
}

// SetModelFamily puts a model in a family or takes it out, as of the rev the request was decided on.
func (a adminAPI) SetModelFamily(ctx context.Context, request adminapi.SetModelFamilyRequestObject) (adminapi.SetModelFamilyResponseObject, error) {
	b := request.Body
	body, replayed, err := a.changed(ctx, "setModelFamily", request.ModelId, request.Params.IdempotencyKey, b, b.Reason, func(op catalog.Op, id catalog.ModelID) (catalog.Result, error) {
		return a.catalog.SetFamily(ctx, catalog.FamilyRequest{Op: op, Model: id, Rev: b.Rev, Family: b.Family})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetModelFamily200JSONResponse{Body: body, Headers: adminapi.SetModelFamily200ResponseHeaders{IdempotencyReplayed: replayed}}, nil
}

// listingOutcomes are the refusals of a listing that are one model's outcome rather than the request's failure.
var listingOutcomes = []struct {
	err     error
	outcome adminapi.ModelListingResultDataOutcome
}{
	{catalog.ErrStaleRev, "stale"},
	{catalog.ErrNotReady, "not_ready"},
	{catalog.ErrWrongStatus, "not_draft"},
}

// PutModelsOnSale lists each model named at the rev the page read, through the listing gate of setModelStatus, one
// outcome per model in the order sent. The whole request is one idempotent transaction, so a retry answers what the
// first did; each model is listed after a savepoint of its own, so one that is refused leaves the others listed. The
// models are locked in the order of their IDs, so two requests listing some of the same models cannot deadlock.
func (a adminAPI) PutModelsOnSale(ctx context.Context, request adminapi.PutModelsOnSaleRequestObject) (adminapi.PutModelsOnSaleResponseObject, error) {
	b := request.Body
	ids := make([]catalog.ModelID, len(b.Models))
	for i, m := range b.Models {
		id, err := modelIDOf(m.Id)
		if err != nil {
			return nil, invalidField(fmt.Sprintf("/models/%d/id", i), "not a model's ID")
		}
		ids[i] = id
	}
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int { return uuid.UUID(ids[i]).Compare(uuid.UUID(ids[j])) })
	op, err := opOf(ctx, request.Params.IdempotencyKey, b.Reason)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "putModelsOnSale", request.Params.IdempotencyKey, b, func(tx pg.Tx) (adminapi.ModelListingResult, error) {
		var result adminapi.ModelListingResult
		result.Data = slices.Grow(result.Data, len(ids))[:len(ids)]
		for _, i := range order {
			outcome, model, err := a.listOne(ctx, tx, op, ids[i], b.Models[i].Rev)
			if errors.Is(err, catalog.ErrModelNotFound) {
				return result, invalidField(fmt.Sprintf("/models/%d/id", i), "no model has this ID")
			}
			if err != nil {
				return result, err
			}
			result.Data[i].ModelId, result.Data[i].Outcome, result.Data[i].Model = b.Models[i].Id, outcome, model
		}
		return result, nil
	})
	a.refreshAfterCommit(ctx, replayed || err != nil)
	if err != nil {
		return nil, err
	}
	return adminapi.PutModelsOnSale200JSONResponse{Body: body, Headers: adminapi.PutModelsOnSale200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// listOne lists one model of a batch in tx after a savepoint, which a refusal is undone to. Its catalog operation has a
// key of its own: the batch's Idempotency-Key answers the batch's retries.
func (a adminAPI) listOne(ctx context.Context, tx pg.Tx, op catalog.Op, id catalog.ModelID, rev int64) (adminapi.ModelListingResultDataOutcome, *adminapi.ModelSummary, error) {
	if _, err := tx.Exec(ctx, "SAVEPOINT model_listing"); err != nil {
		return "", nil, err
	}
	op.Key, op.Tx = uuid.NewV7(), &tx
	_, err := a.catalog.ListModel(ctx, catalog.ModelRequest{Op: op, Model: id, Rev: rev})
	for _, refusal := range listingOutcomes {
		if errors.Is(err, refusal.err) {
			_, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT model_listing")
			return refusal.outcome, nil, err
		}
	}
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT model_listing"); err != nil {
		return "", nil, err
	}
	view, err := catalog.ModelIn(ctx, tx, id)
	if err != nil {
		return "", nil, err
	}
	summary := modelSummaryBody(view.ModelSummary)
	if summary.BillingUnits, err = billingUnits(view.Prices); err != nil {
		return "", nil, err
	}
	return "listed", &summary, nil
}

// errNoDatasets is a node that reads no dataset: one started without the price job.
var errNoDatasets = &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "this node does not read the public price lists"}

// ImportModels adds a draft for each model the price list has and the catalog does not. The list is read before the
// transaction, with the dataset sync's read (catalog.Fetch: one fixed address, a size limit, JSON parsed as data and
// nothing of it run), so no transaction waits on the network; a replay is answered from the stored response without
// reading it again.
func (a adminAPI) ImportModels(ctx context.Context, request adminapi.ImportModelsRequestObject) (adminapi.ImportModelsResponseObject, error) {
	b := request.Body
	answer := func(body adminapi.ModelImport, replayed bool) adminapi.ImportModelsResponseObject {
		return adminapi.ImportModels201JSONResponse{Body: body, Headers: adminapi.ImportModels201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}
	}
	claim, err := idempotencyClaim(ctx, "importModels", request.Params.IdempotencyKey, b)
	if err != nil {
		return nil, err
	}
	if body, found, err := replayed[adminapi.ModelImport](ctx, a.db, claim); err != nil || found {
		return answer(body, true), err
	}
	if a.datasets == nil {
		return nil, errNoDatasets
	}
	fetched, err := a.catalog.Fetch(ctx, a.datasets, catalog.PriceSource(b.Source))
	if err != nil {
		a.logger.WarnContext(ctx, "the price list to import could not be read", "dataset", b.Source, "err", err)
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the price list could not be read; try again later"}
	}
	who := actor(ctx)
	body, again, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.ModelImport, error) {
		r, err := a.catalog.Import(ctx, tx, who, b.Reason, fetched)
		if err != nil {
			return adminapi.ModelImport{}, err
		}
		body := adminapi.ModelImport{Source: adminapi.ModelImportSource(r.Source), Added: r.Added, Known: r.Known, Held: r.Held}
		if len(r.HeldNames) > 0 {
			body.HeldNames = &r.HeldNames
		}
		return body, nil
	})
	if err != nil {
		return nil, err
	}
	return answer(body, again), nil
}
