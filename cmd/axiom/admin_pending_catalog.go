package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B4 and B5 of #948, a supplier's piles of names and an endpoint's price lines. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) ListSupplierModels(context.Context, adminapi.ListSupplierModelsRequestObject) (adminapi.ListSupplierModelsResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) GetSupplierEndpointCost(context.Context, adminapi.GetSupplierEndpointCostRequestObject) (adminapi.GetSupplierEndpointCostResponseObject, error) {
	return nil, errNotImplemented
}
