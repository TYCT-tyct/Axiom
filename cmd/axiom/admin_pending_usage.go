package main

import (
	"context"

	"axiom/internal/platform/adminapi"
)

// B9 of #948, plugin use. It answers 501 until
// that unit replaces it with its handler; the unit deletes this file with its last stub.

func (adminAPI) GetPluginStats(context.Context, adminapi.GetPluginStatsRequestObject) (adminapi.GetPluginStatsResponseObject, error) {
	return nil, errNotImplemented
}
