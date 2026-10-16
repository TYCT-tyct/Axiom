package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
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
// that fails is the answer, in that order, so what a client without a session can learn is only that it has none.
type guard struct {
	contract *contract
	accounts *accounts.Service
	proxies  []netip.Prefix
	logger   *slog.Logger
}

// unscoped are the operations that belong to no site, which the deployment checks ask by address. They are named as the
// generated code names them (its embedded contract capitalizes the operation IDs, so they are the Go method names).
var unscoped = map[string]bool{"GetVersion": true}

func (g *guard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl, ok := g.contract.byPattern[r.Pattern]
		if !ok { // every route is registered from the contract, so this is a bug
			respond(w, r, g.logger, errors.New("a route the contract does not declare: "+r.Pattern))
			return
		}
		if unscoped[rl.operation] {
			next.ServeHTTP(w, r)
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
		if fields := rl.validate(r.Context(), r); len(fields) > 0 {
			respond(w, r, g.logger, &apiError{
				status: http.StatusBadRequest, code: consoleapi.ErrorCodeInvalidRequest,
				message: "the request breaks the contract", fields: fields,
			})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
	})
}

// notJSON is a body the project's JSON policy refuses (internal/platform/jsonx): not UTF-8, a member name twice, or
// nested too deep. Two parsers may keep different copies of a repeated name, so none is let through.
var notJSON = &apiError{
	status: http.StatusBadRequest, code: consoleapi.ErrorCodeInvalidRequest, message: "the request body is not JSON of the kind this API reads",
	fields: []consoleapi.ErrorField{{Pointer: "", Message: "must be valid UTF-8 JSON with no member name twice, nested at most 256 levels"}},
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
	if surface != accounts.Console { // an admin or API domain is no console, and no session of it serves here
		return call{}, accounts.ErrUnknownHost
	}
	c := call{r: r, site: site, ip: clientIP(r, g.proxies)}
	switch {
	case !rl.session && rl.changes:
		return c, g.accounts.CheckOrigin(r)
	case !rl.session, rl.level == levelPendingMFA:
		// A pending session is checked by VerifyMFA itself, together with the Origin and its own CSRF token, under
		// the user's lock.
		return c, nil
	}
	if c.principal, err = g.authenticate(r, rl); err != nil {
		return call{}, err
	}
	if rl.permission != nil {
		c.scope, err = g.accounts.Authorize(r.Context(), c.principal, *rl.permission, accounts.AccountTarget(c.principal.AccountID()))
		if errors.Is(err, accounts.ErrNotFound) { // the user has no part in their own account: as forbidden as it gets
			err = accounts.ErrForbidden
		}
		if err != nil {
			return call{}, err
		}
	}
	return c, nil
}

// authenticate is the session of the request, of the level the operation needs.
func (g *guard) authenticate(r *http.Request, rl *rules) (accounts.Principal, error) {
	if rl.level == levelAny {
		return g.accounts.AuthenticateAny(r.Context(), r)
	}
	p, err := g.accounts.Authenticate(r.Context(), r)
	if err == nil && rl.level == levelMFA && !p.Verified() {
		return accounts.Principal{}, errMFARequired
	}
	return p, err
}

// errMFARequired is a session that has not passed two-step verification asking for what needs it.
var errMFARequired = &apiError{
	status: http.StatusForbidden, code: consoleapi.ErrorCodeMfaRequired, message: "this needs a session that passed two-step verification",
}

// clientIP is the address a request came from. Only the connection's own address is believed, unless it is a trusted
// proxy: then X-Forwarded-For is read from its right, past the proxies, to the first address that is not one (a
// client can put anything at the left of the header, but not past the proxy that appends to it).
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := remote.Addr().Unmap()
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(addr) {
		return addr
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for hop := range strings.SplitSeq(header, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	for _, text := range slices.Backward(hops) {
		hop, err := netip.ParseAddr(text)
		if err != nil {
			return addr // a header we cannot read is not believed
		}
		if hop = hop.Unmap(); !isTrusted(hop) {
			return hop
		}
	}
	return addr
}
