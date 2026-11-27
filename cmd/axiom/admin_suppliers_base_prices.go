package main

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// ConfirmSupplierBasePrices confirms the base price of endpoints of a relay that shows no prices.
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

// basePriceItems is the request's endpoints, each with the price given for it in the catalog's form.
func basePriceItems(b *adminapi.BasePriceConfirmation) ([]suppliers.BasePriceItem, error) {
	items := make([]suppliers.BasePriceItem, len(b.Items))
	for i, it := range b.Items {
		id, err := idIn(fmt.Sprintf("/items/%d/endpoint_id", i), it.EndpointId)
		if err != nil {
			return nil, err
		}
		items[i].Endpoint = id
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

// ClearSupplierBasePrices takes confirmed base prices away.
func (a adminAPI) ClearSupplierBasePrices(ctx context.Context, request adminapi.ClearSupplierBasePricesRequestObject) (adminapi.ClearSupplierBasePricesResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	ids := make([]uuid.UUID, len(b.EndpointIds))
	for i, s := range b.EndpointIds {
		if ids[i], err = idIn(fmt.Sprintf("/endpoint_ids/%d", i), s); err != nil {
			return nil, err
		}
	}
	list, replayed, err := idempotently(ctx, a.db, "clearSupplierBasePrices", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.BasePriceList, error) {
			bases, err := a.suppliers.ClearBasePrices(ctx, tx, actor(ctx), supplier, ids, b.Reason)
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
		EndpointID string           `json:"endpoint_id"`
		Base       catalog.Schedule `json:"base"`
	}
	data := make([]item, len(bases))
	for i, b := range bases {
		data[i] = item{EndpointID: b.Endpoint.String(), Base: b.Base}
	}
	return converted[adminapi.BasePriceList](struct {
		Data []item `json:"data"`
	}{data})
}
