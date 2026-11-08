package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/fileparse"
	"axiom/internal/gateway"
	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/webui"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// adminAPI implements the admin's API (api/admin.yaml). It stands behind the guard, which has found the admin domain,
// the session of two-step verification and its permission on the platform before a method runs, and kept the request
// to the contract's schemas.
type adminAPI struct {
	smtp           mail.Config
	search         websearch.Config
	fileParser     fileparse.Config
	version        string
	accounts       *accounts.Service
	catalog        *catalog.Catalog
	db             *pg.DB
	logger         *slog.Logger
	keys           *secret.Keyring
	exit           *egress.Client
	alerts         alert.Sink
	instance       string
	paymentSignals *payments.SignalState
	// suppliers is what the admin does with suppliers; calls is the node's call path, which a test generation goes
	// through, and detections and tests limit how often a session may ask a third party's site what it runs and may
	// make it generate.
	suppliers  *suppliers.Admin
	calls      *suppliers.Service
	detections *windowLimiter
	tests      *windowLimiter
	// router is the node's call path routing, whose snapshot a check of a preset's rules is judged against; routes
	// builds that snapshot, and a committed change of what it is built from rebuilds it (refreshAfterCommit).
	router *routing.Router
	routes *gateway.RouteDeps
}

// detectionsPerMinute is how many times a session may run detectSupplier in a minute; testsPerMinute is the same for
// testSupplierEndpoint, whose every request is a real generation the platform pays for.
const (
	detectionsPerMinute = 10
	testsPerMinute      = 10
)

// newAdminAPI is the admin's handlers on the modules and the database of the node.
func newAdminAPI(version string, a *accounts.Service, c *catalog.Catalog, s *suppliers.Admin, calls *suppliers.Service, db *pg.DB, logger *slog.Logger) adminAPI {
	return adminAPI{
		version: version, accounts: a, catalog: c, suppliers: s, calls: calls, db: db, logger: logger,
		detections: newWindowLimiter(detectionsPerMinute, time.Minute, time.Now),
		tests:      newWindowLimiter(testsPerMinute, time.Minute, time.Now),
	}
}

// adminContract is the admin's contract as the generated code embeds it, with the rules read out of it. The rules are
// immutable, so they are read once: parsing the generated document under the race detector costs more than most of the
// requests the guard checks against it.
func adminContract() (*contract, error) { return adminContractOnce() }

var adminContractOnce = sync.OnceValues(func() (*contract, error) {
	doc, err := adminapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("the admin contract: %w", err)
	}
	c, err := loadContract(doc)
	if err != nil {
		return nil, fmt.Errorf("the admin contract: %w", err)
	}
	return c, nil
})

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
	return servedRoutes(apiHandler, frontend.Secured, frontend.Admin())
}
