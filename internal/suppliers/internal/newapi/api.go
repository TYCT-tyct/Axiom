package newapi

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// The rate buckets new-api counts requests in, per client address. Every /api/ path counts in the global one, and the
// few sensitive paths (login, reading a key's text, the key-only usage reads) in the critical one as well. The limits
// are a quarter of new-api's defaults of 360 per 180 seconds and 20 per 1200 seconds, so that other users of the same
// address do not push us over.
const (
	bucketGlobal   = "global"
	bucketCritical = "critical"
)

var (
	global         = []string{bucketGlobal}
	globalCritical = []string{bucketGlobal, bucketCritical}
)

// number is a JSON number kept as the text it was written in. Ratios and prices never pass through float64: a rounded
// price would show up as a difference at reconciliation.
type number string

// UnmarshalJSON keeps the text; a quoted number is read as the number it holds, as new-api writes some that way.
func (n *number) UnmarshalJSON(b []byte) error {
	*n = number(strings.Trim(string(b), `"`))
	return nil
}

// rat reads the number as an exact fraction.
func (n number) rat() (*big.Rat, bool) { return new(big.Rat).SetString(string(n)) }

// envelope is what new-api's handlers wrap their data in. A handler that fails answers 200 with success false.
type envelope struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Params  struct {
		Scope string `json:"scope"`
	} `json:"message_params"`
}

// authCodes are the codes of the answers that mean the account credentials are no good (401 and 403 from the
// authentication layer). Only these are put in an error, never the supplier's message, which can echo what was sent.
var authCodes = map[string]bool{
	"AUTH_UNAUTHORIZED": true, "AUTH_TOKEN_EXPIRED": true, "AUTH_SESSION_REVOKED": true, "ACCESS_TOKEN_EXPIRED": true,
	"ACCESS_TOKEN_LEGACY_RETIRED": true, "ACCESS_TOKEN_SCOPE_DENIED": true, "ACCESS_TOKEN_ROUTE_UNDECLARED": true,
}

// scopeName is the form of new-api's scope names (api_key:read, usage:read, ...); its bounds keep a reason that names
// one within the 200 characters suppliers.status_reason holds.
var scopeName = regexp.MustCompile(`^[a-z_]{1,32}:[a-z_]{1,32}$`)

// errServer marks a 5xx: the supplier is temporarily unable to answer, so the task backs off instead of halting.
var errServer = errors.New("new-api: the server failed")

// errUnsuccessful is a handler's answer of 200 with success false: the request failed for a reason new-api states only
// in words.
var errUnsuccessful = fmt.Errorf("%w: the request failed", adapter.ErrUnrecognised)

// api sends the management requests of one account, with the headers its login is sent in.
type api struct {
	c      adapter.Client
	header http.Header
}

// get sends a GET and reads the data of its answer into out (or, when out is nil, only checks the answer).
func (a *api) get(ctx context.Context, path string, buckets []string, out any) error {
	return a.do(ctx, http.MethodGet, path, buckets, out)
}

func (a *api) do(ctx context.Context, method, path string, buckets []string, out any) error {
	resp, err := a.send(ctx, method, path, buckets, nil)
	if err != nil {
		return err
	}
	return read(resp, out)
}

// read classifies an answer and reads its data into out (or, when out is nil, only checks it).
func read(resp adapter.Response, out any) error {
	var env envelope
	parsed := jsonx.Unmarshal(resp.Body, &env) == nil
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return authError(env, parsed)
	case resp.Status >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", errServer, resp.Status)
	case resp.Status != http.StatusOK || !parsed:
		return fmt.Errorf("%w: status %d", adapter.ErrUnrecognised, resp.Status)
	case !env.Success:
		return errUnsuccessful
	}
	if out == nil {
		return nil
	}
	if err := jsonx.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%w: the data has an unexpected form", adapter.ErrUnrecognised)
	}
	return nil
}

// send sends one request without reading the status: the caller classifies a status that is not a plain answer, because
// a refused key operation means something different from a refused read.
func (a *api) send(ctx context.Context, method, path string, buckets []string, body []byte) (adapter.Response, error) {
	header := a.header.Clone()
	header.Set("Accept", "application/json")
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	return a.c.Do(ctx, adapter.Request{Method: method, Path: path, Buckets: buckets, Header: header, Body: body})
}

// authError names a refused credential by a code from the fixed list. A token missing a scope is a ScopeDenied that
// names the scope, so the person replacing the token knows which one to grant.
func authError(env envelope, parsed bool) error {
	code := "unknown"
	if parsed && authCodes[env.Code] {
		code = env.Code
	}
	if code == "ACCESS_TOKEN_SCOPE_DENIED" && scopeName.MatchString(env.Params.Scope) {
		return adapter.ScopeDenied{Scope: env.Params.Scope}
	}
	return fmt.Errorf("%w: %s", adapter.ErrAuthRejected, code)
}

// scopeDenied reports whether err is a token missing a scope, which for a key operation means the login may not
// manage keys.
func scopeDenied(err error) bool {
	var denied adapter.ScopeDenied
	return errors.As(err, &denied)
}
