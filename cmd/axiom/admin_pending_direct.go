package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B6 of #948, direct APIs reached with pasted keys. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) CreateDirectSupplier(context.Context, adminapi.CreateDirectSupplierRequestObject) (adminapi.CreateDirectSupplierResponseObject, error) {
	return nil, errNotImplemented
}
