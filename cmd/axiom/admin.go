package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

//go:generate go run -C ../../tools ./pendingapi ../api/admin.yaml ../cmd/axiom admin_pending.gen.go axiom/internal/platform/adminapi adminAPI pendingAdmin

// adminAPI implements the admin's API (api/admin.yaml). It stands behind the guard, which has found the admin domain,
// the session of two-step verification and its permission on the platform before a method runs, and kept the request
// to the contract's schemas. An operation it declares no method for is answered by the embedded pendingAdmin with a
// 501, until its handler is written.
type adminAPI struct {
	pendingAdmin
	version  string
	accounts *accounts.Service
	catalog  *catalog.Catalog
	db       *pg.DB
}

// adminContract is the admin's contract as the generated code embeds it, with the rules read out of it.
func adminContract() (*contract, error) {
	doc, err := adminapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("the admin contract: %w", err)
	}
	c, err := loadContract(doc)
	if err != nil {
		return nil, fmt.Errorf("the admin contract: %w", err)
	}
	return c, nil
}

// GetVersion tells the admin which build answers, uncached, so a page left open across a deployment notices it.
func (a adminAPI) GetVersion(context.Context, adminapi.GetVersionRequestObject) (adminapi.GetVersionResponseObject, error) {
	noStore := "no-store"
	return adminapi.GetVersion200JSONResponse{
		Body:    adminapi.Version{Version: a.version},
		Headers: adminapi.GetVersion200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// adminRoutes are the admin listener's routes: the admin's contract behind the guard (apiRoutes).
func adminRoutes(api adminapi.StrictServerInterface, g *guard, logger *slog.Logger) http.Handler {
	return apiRoutes(g, logger, func(m mounting) http.Handler {
		admin := adminapi.NewStrictHandlerWithOptions(api, nil, adminapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: invalidRequest, ResponseErrorHandlerFunc: m.handlerFailed,
		})
		return adminapi.HandlerWithOptions(admin, adminapi.StdHTTPServerOptions{BaseRouter: m.mux, ErrorHandlerFunc: invalidRequest})
	})
}
