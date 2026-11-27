package main

import (
	"context"
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// ConfirmSupplierBasePrices confirms the base price of models of a relay that shows no prices.
func (a adminAPI) ConfirmSupplierBasePrices(ctx context.Context, request adminapi.ConfirmSupplierBasePricesRequestObject) (adminapi.ConfirmSupplierBasePricesResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	items, err := basePriceItems(b)
	if err != nil {
		return nil, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, err
	}
	list, replayed, err := idempotently(ctx, a.db, "confirmSupplierBasePrices", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.BasePriceList, error) {
			bases, err := a.suppliers.ConfirmBasePrices(ctx, tx, actor(ctx), supplier, items, snap, b.Reason)
			if err != nil {
				return adminapi.BasePriceList{}, err
			}
			return basePricesBody(bases)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.ConfirmSupplierBasePrices200JSONResponse{Body: list, Headers: adminapi.ConfirmSupplierBasePrices200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// basePriceItems is the request's models, each with the price given for it in the catalog's form.
func basePriceItems(b *adminapi.BasePriceConfirmation) ([]suppliers.BasePriceItem, error) {
	items := make([]suppliers.BasePriceItem, len(b.Items))
	for i, it := range b.Items {
		model, err := baseModelOf(fmt.Sprintf("/items/%d", i), it.GroupId, it.UpstreamName)
		if err != nil {
			return nil, err
		}
		items[i].BaseModel = model
		if it.ModelId != nil {
			expect, err := idIn(fmt.Sprintf("/items/%d/model_id", i), *it.ModelId)
			if err != nil {
				return nil, err
			}
			items[i].Expect = &expect
		}
		if it.Price == nil {
			continue
		}
		raw, err := jsonx.MarshalCanonical(it.Price)
		if err != nil {
			return nil, err
		}
		s, err := catalog.ParseSchedule(raw)
		if err != nil {
			return nil, invalidField(fmt.Sprintf("/items/%d/price", i), err.Error())
		}
		items[i].Price = &s
	}
	return items, nil
}

// baseModelOf is a model of a group as the request names it at pointer.
func baseModelOf(pointer, group, name string) (suppliers.BaseModel, error) {
	id, err := idIn(pointer+"/group_id", group)
	if err != nil {
		return suppliers.BaseModel{}, err
	}
	return suppliers.BaseModel{Group: id, Name: name}, nil
}

// ClearSupplierBasePrices takes confirmed base prices away.
func (a adminAPI) ClearSupplierBasePrices(ctx context.Context, request adminapi.ClearSupplierBasePricesRequestObject) (adminapi.ClearSupplierBasePricesResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	models := make([]suppliers.BaseModel, len(b.Items))
	for i, it := range b.Items {
		if models[i], err = baseModelOf(fmt.Sprintf("/items/%d", i), it.GroupId, it.UpstreamName); err != nil {
			return nil, err
		}
	}
	list, replayed, err := idempotently(ctx, a.db, "clearSupplierBasePrices", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.BasePriceList, error) {
			bases, err := a.suppliers.ClearBasePrices(ctx, tx, actor(ctx), supplier, models, b.Reason)
			if err != nil {
				return adminapi.BasePriceList{}, err
			}
			return basePricesBody(bases)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.ClearSupplierBasePrices200JSONResponse{Body: list, Headers: adminapi.ClearSupplierBasePrices200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// basePricesBody is the bases as the contract gives them.
func basePricesBody(bases []suppliers.BasePrice) (adminapi.BasePriceList, error) {
	type item struct {
		GroupID      string           `json:"group_id"`
		UpstreamName string           `json:"upstream_name"`
		Base         catalog.Schedule `json:"base"`
	}
	data := make([]item, len(bases))
	for i, b := range bases {
		data[i] = item{GroupID: b.Group.String(), UpstreamName: b.Name, Base: b.Base}
	}
	return converted[adminapi.BasePriceList](struct {
		Data []item `json:"data"`
	}{data})
}
