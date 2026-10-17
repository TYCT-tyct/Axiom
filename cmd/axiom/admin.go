package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/webui"
	"axiom/internal/suppliers"
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
	// suppliers is what the admin does with suppliers; detections limits how often a session may ask a third party's
	// site what it runs.
	logger     *slog.Logger
	suppliers  *suppliers.Admin
	detections *windowLimiter
}

// detectionsPerMinute is how many times a session may run detectSupplier in a minute.
const detectionsPerMinute = 10

// newAdminAPI is the admin's handlers on the modules and the database of the node.
func newAdminAPI(version string, a *accounts.Service, c *catalog.Catalog, s *suppliers.Admin, db *pg.DB, logger *slog.Logger) adminAPI {
	return adminAPI{
		version: version, accounts: a, catalog: c, suppliers: s, db: db, logger: logger,
		detections: newWindowLimiter(detectionsPerMinute, time.Minute, time.Now),
	}
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

// adminRoutes are the admin listener's routes: the admin's contract behind the guard (apiRoutes), and the admin pages
// for every path the API does not claim.
func adminRoutes(api adminapi.StrictServerInterface, g *guard, frontend *webui.Frontend, logger *slog.Logger) http.Handler {
	apiHandler := apiRoutes(g, logger, func(m mounting) http.Handler {
		admin := adminapi.NewStrictHandlerWithOptions(api, nil, adminapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: invalidRequest, ResponseErrorHandlerFunc: m.handlerFailed,
		})
		return adminapi.HandlerWithOptions(admin, adminapi.StdHTTPServerOptions{BaseRouter: m.mux, ErrorHandlerFunc: invalidRequest})
	})
	routes := http.NewServeMux()
	routes.Handle("/api/", httpserver.Compress(apiHandler))
	routes.Handle("/", frontend.Admin())
	return frontend.Secured(routes)
}
