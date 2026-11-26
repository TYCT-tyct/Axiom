package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B7 of #948, cloud platforms reached with their credentials. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) CreateCloudSupplier(context.Context, adminapi.CreateCloudSupplierRequestObject) (adminapi.CreateCloudSupplierResponseObject, error) {
	return nil, errNotImplemented
}

func (adminAPI) ReplaceSupplierCloudCredential(context.Context, adminapi.ReplaceSupplierCloudCredentialRequestObject) (adminapi.ReplaceSupplierCloudCredentialResponseObject, error) {
	return nil, errNotImplemented
}
