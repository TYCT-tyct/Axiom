package main

import (
	"context"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// SetSupplierEndpointEnabled turns an endpoint on or off for routing.
func (a adminAPI) SetSupplierEndpointEnabled(ctx context.Context, request adminapi.SetSupplierEndpointEnabledRequestObject) (adminapi.SetSupplierEndpointEnabledResponseObject, error) {
	supplier, endpoint, err := endpointIDsOf(request.SupplierId, request.EndpointId)
	if err != nil {
		return nil, err
	}
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierEndpointEnabled", request.Params.IdempotencyKey,
		[]any{request.SupplierId, request.EndpointId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierEndpoint, error) {
			e, err := a.suppliers.SetEndpointEnabled(ctx, tx, actor(ctx), supplier, endpoint, request.Body.Enabled, request.Body.Reason)
			return endpointBody(e), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSupplierEndpointEnabled200JSONResponse{Body: changed, Headers: adminapi.SetSupplierEndpointEnabled200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// endpointIDsOf reads the path of one of a supplier's endpoints; an endpoint ID that is not one is not found.
func endpointIDsOf(supplierID, endpointID string) (supplier, endpoint uuid.UUID, err error) {
	if supplier, err = supplierIDOf(supplierID); err != nil {
		return uuid.UUID{}, uuid.UUID{}, err
	}
	if endpoint, err = supplierIDOf(endpointID); err != nil {
		return uuid.UUID{}, uuid.UUID{}, suppliers.ErrNoSupplierEndpoint
	}
	return supplier, endpoint, nil
}
