package gateway

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// The refusals the error exit knows besides the limits' (gateway.go). Code is the unified error_type of
// docs/domains/gateway.md「错误」.
var (
	errUnauthorized = &Refusal{Status: http.StatusUnauthorized, Code: "authentication"}
	errNoModel      = &Refusal{Status: http.StatusNotFound, Code: "model_not_found"}
	errTooLarge     = &Refusal{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large"}
	errBodyTimeout  = &Refusal{Status: http.StatusRequestTimeout, Code: "timeout"}
)

func invalid(message string) *Refusal {
	return &Refusal{Status: http.StatusBadRequest, Code: "invalid_request", Message: message}
}

// messages are what a client reads for each code. None of them uses a phrase new-api's auto-disable rules match
// (TestMessagesAvoidAutoDisablePhrases): a relay that has us as its upstream would take a billing problem for a dead
// channel and switch it off.
var messages = map[string]string{
	"authentication":       "The API key is missing, malformed, or not valid for this address.",
	"credits_insufficient": "The credits available are not enough for this request.",
	"rate_limited":         "Too many requests; retry shortly.",
	"overloaded":           "The service is busy; retry shortly.",
	"model_not_found":      "The model does not exist or is not available to this key.",
	"no_endpoint":          "No endpoint can serve this request.",
	"invalid_request":      "The request is not valid.",
	"request_too_large":    "The request body is larger than the limit.",
	"timeout":              "The request body was not received in time.",
}

// errorTypes are the Messages `type` of a status; Chat's `type` uses the same words.
var errorTypes = map[int]string{
	http.StatusBadRequest: "invalid_request_error", http.StatusUnauthorized: "authentication_error",
	http.StatusPaymentRequired: "billing_error", http.StatusNotFound: "not_found_error",
	http.StatusRequestTimeout: "timeout_error", http.StatusRequestEntityTooLarge: "request_too_large",
	http.StatusTooManyRequests: "rate_limit_error", http.StatusServiceUnavailable: "overloaded_error",
}

// maxRetryAfter is the longest Retry-After sent: Claude Code does not retry after more than 60 seconds.
const maxRetryAfter = 60

// writeRefusal is the one error exit before a stream starts. Messages has its own shell; Chat's serves Responses too,
// as docs/domains/gateway.md「错误」 says. A 401 closes the connection, so a client that keeps sending a body to an
// address it may not use is not read.
func writeRefusal(w http.ResponseWriter, f protocol.Format, r *Refusal) {
	message := r.Message
	if message == "" {
		message = messages[r.Code]
	}
	typ := errorTypes[r.Status]
	detail := map[string]any{"error_type": r.Code}
	maps.Copy(detail, r.Details)
	var body map[string]any
	if f == protocol.Messages {
		detail["type"], detail["message"] = typ, message
		body = map[string]any{"type": "error", "error": detail}
	} else {
		body = map[string]any{"error": map[string]any{"message": message, "type": typ, "code": r.Status, "metadata": detail}}
	}
	out, _ := jsonx.MarshalCanonical(body) //nolint:errcheck // a map of strings and numbers always encodes
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if r.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(min(r.RetryAfter, maxRetryAfter)))
	}
	if r.Status == http.StatusUnauthorized {
		h.Set("Connection", "close")
	}
	w.WriteHeader(r.Status)
	w.Write(out) //nolint:errcheck,gosec // the client may be gone; nothing more can be told
}

// refusal says how a failure of the admission path is answered, and whether it is one the gateway expected. An
// unexpected one is answered as the node being busy, which is what a database that does not answer looks like to a
// client, and the caller logs it.
func refusal(err error) (r *Refusal, expected bool) {
	if r, ok := errors.AsType[*Refusal](err); ok {
		return r, true
	}
	if ins, ok := errors.AsType[*billing.Insufficient](err); ok {
		return insufficient(ins), true
	}
	if none, ok := errors.AsType[*routing.NoEndpoint](err); ok {
		return noEndpoint(none), true
	}
	switch {
	case errors.Is(err, accounts.ErrUnknownKey), errors.Is(err, accounts.ErrUnknownHost):
		return errUnauthorized, true
	case errors.Is(err, catalog.ErrModelNotFound), errors.Is(err, catalog.ErrRetired):
		return errNoModel, true
	case errors.Is(err, protocol.ErrInvalid):
		return invalid(strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": ")), true
	case errors.Is(err, accounts.ErrStale), errors.Is(err, catalog.ErrStale), errors.Is(err, routing.ErrStale),
		errors.Is(err, billing.ErrTerminated), errors.Is(err, errCommitUnknown), errors.Is(err, context.DeadlineExceeded):
		return ErrOverloaded, true // the node cannot serve now; the first request that finds it so is not a bug
	}
	return ErrOverloaded, false
}

var limitSources = map[billing.Source]string{
	billing.SourceBalance: "balance", billing.SourceSiteBalance: "balance",
	billing.SourceWorkspace: "workspace_budget", billing.SourceKey: "key_limit",
}

// insufficient is the 402. Only a limit that resets can pass later, so only that one says when to come back; billing
// does not say when the window ends, so a client is told the longest wait it would honour.
func insufficient(e *billing.Insufficient) *Refusal {
	r := &Refusal{Status: http.StatusPaymentRequired, Code: "credits_insufficient", Details: map[string]any{"limit_source": limitSources[e.Source]}}
	if e.Transient {
		r.RetryAfter = maxRetryAfter
	}
	return r
}

// noEndpoint is the 404 or 503 of a route with nothing in it. A key that may not use the model is told the model does
// not exist, never 403, so what the key may use is not revealed.
func noEndpoint(e *routing.NoEndpoint) *Refusal {
	var reasons []string
	hidden := len(e.Reasons) > 0
	for _, x := range e.Reasons {
		hidden = hidden && x.Reason == routing.NotAllowed
		if s := x.Reason.String(); x.Reason != routing.NotAllowed && !slices.Contains(reasons, s) {
			reasons = append(reasons, s)
		}
	}
	switch {
	case hidden:
		return errNoModel
	case e.Permanent:
		return &Refusal{Status: http.StatusNotFound, Code: "no_endpoint", Details: map[string]any{"reasons": reasons}}
	}
	return &Refusal{Status: http.StatusServiceUnavailable, Code: "no_endpoint", RetryAfter: 1, Details: map[string]any{"reasons": reasons}}
}
