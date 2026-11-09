package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/quality"
	"axiom/internal/suppliers"
)

// SetSupplierEndpointEnabled turns an endpoint on or off for routing. Turning it on is a person saying it is well, so
// the same transaction resets its breaker: switch and reset commit or fail together (docs/domains/quality.md
// 「三条摘除通道，各归各的主人」). The operations agent's switch does not: automatic evidence is not overruled by an
// automatic actor.
func (a adminAPI) SetSupplierEndpointEnabled(ctx context.Context, request adminapi.SetSupplierEndpointEnabledRequestObject) (adminapi.SetSupplierEndpointEnabledResponseObject, error) {
	supplier, endpoint, err := endpointIDsOf(request.SupplierId, request.EndpointId)
	if err != nil {
		return nil, err
	}
	var reset quality.HealthReset
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierEndpointEnabled", request.Params.IdempotencyKey,
		[]any{request.SupplierId, request.EndpointId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierEndpoint, error) {
			e, err := a.suppliers.SetEndpointEnabled(ctx, tx, actor(ctx), supplier, endpoint, request.Body.Enabled, request.Body.Reason)
			if err == nil && request.Body.Enabled {
				reset, err = quality.ResetHealth(ctx, tx, endpoint, time.Now())
			}
			return endpointBody(e), err
		})
	if err != nil {
		return nil, err
	}
	if a.health != nil && !replayed { // the admin's tests run no call path
		a.health.AdoptReset(reset)
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
