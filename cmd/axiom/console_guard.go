package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/clientip"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// maxBody bounds a request body: the largest legitimate one, a key with 256 model names of 200 characters, is 60 KiB.
const maxBody = 1 << 20

// call is what the guard learned of a request and its handler may rely on.
type call struct {
	r         *http.Request // for the operation that reads the cookie and headers itself (verifyMfa)
	site      accounts.Site
	ip        netip.Addr
	principal accounts.Principal
	scope     pg.Scope // the account's, when the operation needs a permission on it
}

type callKey struct{}

// callOf is the call the guard let through; every operation but getVersion has one.
func callOf(ctx context.Context) call {
	c, _ := ctx.Value(callKey{}).(call)
	return c
}

// guard stands in front of every operation and enforces what the contract declares for it (internal/platform/README.md
// 「接口约定」): the host is a console of a site; a request that changes data comes from the site's origin as JSON; a
// session operation has a session of the level it names, carrying the CSRF token if it changes data, and the
// permission it needs on the session's account; and the request keeps to the contract's schemas. The first thing
// that fails is the answer, in that order, so what a client without a session can learn is only that it has none. It
// runs in front of the generated routes, which read the query before any middleware would run, and gives the whole
// request a deadline.
type guard struct {
	contract *contract
	accounts *accounts.Service
	proxies  []netip.Prefix
	logger   *slog.Logger
	// timeout is the request's deadline when a test wants another than the surface's.
	timeout time.Duration
	// admin makes this the admin listener's guard: the host must be an admin domain, and the permission an operation
	// names is asked of the platform (accounts.PlatformTarget) instead of the session's own account.
	admin bool
}

// surface is the kind of domain this guard serves.
func (g *guard) surface() accounts.Surface {
	if g.admin {
		return accounts.Admin
	}
	return accounts.Console
}

// target is what a permission is asked of for p.
func (g *guard) target(p accounts.Principal) accounts.Target {
	if g.admin {
		return accounts.PlatformTarget()
	}
	return accounts.AccountTarget(p.AccountID())
}

// deadline is the time a request has: the guard's own when a test sets one, else the surface's.
func (g *guard) deadline() time.Duration {
	switch {
	case g.timeout != 0:
		return g.timeout
	case g.admin:
		return adminDeadline
	}
	return consoleDeadline
}

// consoleDeadline bounds a console request from the moment it is read: a sign-in waiting for the user's row, a key
// update waiting for the key's row, or a database that stopped answering would otherwise hold the request and its
// connection until the client gives up, and enough of them exhaust the pool. The slowest legitimate request, an
// argon2id hash after its wait for a slot (accounts.Config.HashWait), takes a few seconds.
const consoleDeadline = 15 * time.Second

// adminDeadline is the admin's: its operations include a probe of a third party's site (detectSupplier, which gives up
// after 45 seconds) and the catalog's, which read and rebuild a snapshot, and its callers are people who wait.
const adminDeadline = time.Minute

// errDeadline is a request that ran out of time. The work may or may not have been done, so the client is told to
// look before it tries again.
var errDeadline = &apiError{
	status: http.StatusServiceUnavailable, code: codeUnavailable,
	message: "the request took too long; check whether it took effect before trying again",
}

// unscoped are the operations that belong to no site, which the deployment checks ask by address. They are named as the
// generated code names them (its embedded contract capitalizes the operation IDs, so they are the Go method names).
var unscoped = map[string]bool{"GetVersion": true}

// serve answers the request, or passes it to routes (the generated routes) with what it learned in its context.
// pattern is the route the request matched.
func (g *guard) serve(w http.ResponseWriter, r *http.Request, pattern string, routes http.Handler) {
	ctx, cancel := context.WithTimeout(r.Context(), g.deadline())
	defer cancel()
	r = r.WithContext(ctx)
	rl, ok := g.contract.byPattern[pattern]
	if !ok { // every route is registered from the contract, so this is a bug
		respond(w, r, g.logger, errors.New("a route the contract does not declare: "+pattern))
		return
	}
	if unscoped[rl.operation] {
		routes.ServeHTTP(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	c, err := g.admit(r, rl)
	if err != nil {
		respond(w, r, g.logger, err)
		return
	}
	if rl.body != nil {
		if err := checkBody(r); err != nil {
			respond(w, r, g.logger, err)
			return
		}
	}
	if fields := rl.validate(ctx, r); len(fields) > 0 {
		respond(w, r, g.logger, &apiError{
			status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "the request breaks the contract", fields: fields,
		})
		return
	}
	routes.ServeHTTP(w, r.WithContext(context.WithValue(ctx, callKey{}, c)))
}

// notJSON is a body the project's JSON policy refuses (internal/platform/jsonx): not UTF-8, a member name twice, or
// nested too deep. Two parsers may keep different copies of a repeated name, so none is let through.
var notJSON = &apiError{
	status: http.StatusBadRequest, code: codeInvalidRequest, message: "the request body is not JSON of the kind this API reads",
	fields: []errorField{{Pointer: "", Message: "must be valid UTF-8 JSON with no member name twice, nested at most 256 levels"}},
}

// checkBody reads the request's body whole, applies the JSON policy to it, and puts it back for the schema check and
// the handler, which read it with parsers that apply none.
func checkBody(r *http.Request) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return notJSON
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	r.ContentLength = int64(len(data))
	if len(data) > 0 && jsonx.Validate(data) != nil {
		return notJSON
	}
	return nil
}

// admit finds the site and, as the operation needs, the session and its permission.
func (g *guard) admit(r *http.Request, rl *rules) (call, error) {
	site, surface, err := g.accounts.SiteForHost(r.Host)
	if err != nil {
		return call{}, err
	}
	if surface != g.surface() { // another surface's domain is no domain of this API, and no session of it serves here
		return call{}, accounts.ErrUnknownHost
	}
	c := call{r: r, site: site, ip: clientip.Addr(r, g.proxies)}
	switch {
	case !rl.session && rl.changes:
		return c, g.accounts.CheckOrigin(r)
	case !rl.session:
		return c, nil
	}
	if c.principal, err = g.authenticate(r, rl, c.ip); err != nil {
		return call{}, err
	}
	if rl.permission != nil {
		c.scope, err = g.accounts.Authorize(r.Context(), c.principal, *rl.permission, g.target(c.principal))
		if errors.Is(err, accounts.ErrNotFound) { // the user has no part in the target (no role in the account, no grant on the platform): as forbidden as it gets
			err = accounts.ErrForbidden
		}
		if err != nil {
			return call{}, err
		}
	}
	if g.consentRefused(c, rl) {
		return call{}, accounts.ErrConsentRequired
	}
	return c, nil
}

// consentRefused reports whether the console operation must wait for the current legal documents. The admin console is
// not a site's, and a pending session must still reach verifyMfa.
func (g *guard) consentRefused(c call, rl *rules) bool {
	return !g.admin && c.principal.ConsentPending() && !c.principal.Pending() && !consentExempt[rl.operation]
}

// consentExempt are the console operations a session may use before it accepts the current legal documents: read the
// session (which names them), accept them, and end the session. Every other operation is refused with
// consent_required, which the page answers by showing the documents.
var consentExempt = map[string]bool{
	"GetSession": true, "AcceptLegalDocuments": true, "SignOut": true,
}

// authenticate is the session of the request, of the level the operation needs.
func (g *guard) authenticate(r *http.Request, rl *rules, ip netip.Addr) (accounts.Principal, error) {
	switch rl.level {
	case levelAny:
		return g.accounts.AuthenticateAny(r.Context(), r, ip)
	case levelPendingMFA:
		// Checked here, before the body is read, so that a client without a pending session learns only that it has
		// none; VerifyMFA checks it again under the user's lock, which is what keeps one challenge to one session.
		p, err := g.accounts.AuthenticateAny(r.Context(), r, ip)
		if err == nil && !p.Pending() {
			return accounts.Principal{}, accounts.ErrNoSession
		}
		return p, err
	}
	p, err := g.accounts.Authenticate(r.Context(), r, ip)
	if err == nil && rl.level == levelMFA && !p.Verified() {
		return accounts.Principal{}, errMFARequired
	}
	return p, err
}

// errMFARequired is a session that has not passed two-step verification asking for what needs it.
var errMFARequired = &apiError{
	status: http.StatusForbidden, code: codeMfaRequired, message: "this needs a session that passed two-step verification",
}
