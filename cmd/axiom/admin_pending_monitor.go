package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B8 of #948, the operations monitor. Each answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) GetOperationsMonitor(context.Context, adminapi.GetOperationsMonitorRequestObject) (adminapi.GetOperationsMonitorResponseObject, error) {
	return nil, errNotImplemented
}
