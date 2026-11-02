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
	"axiom/internal/suppliers"
)

// The refusals the error exit knows besides the limits' (gateway.go). Code is the unified error_type of
// docs/domains/gateway.md「错误」.
var (
	errUnauthorized = &Refusal{Status: http.StatusUnauthorized, Code: "authentication"}
	errNoModel      = &Refusal{Status: http.StatusNotFound, Code: "model_not_found"}
	errTooLarge     = &Refusal{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large"}
	errBodyTimeout  = &Refusal{Status: http.StatusRequestTimeout, Code: "timeout"}
	// errParseFailed is the file parser backend failing before an attempt, the same 502 the web plugin's search gives
	// (docs/domains/gateway.md「错误」).
	errParseFailed = &Refusal{
		Status: http.StatusBadGateway, Code: "upstream_error",
		Message: "The file-parser plugin could not parse the request's file.",
	}
)

func invalid(message string) *Refusal {
	return &Refusal{Status: http.StatusBadRequest, Code: "invalid_request", Message: message}
}

// requestRefusals retain partial aggregated content; local cancellation, memory and deadline failures discard it.
var requestRefusals = map[protocol.ErrorType]bool{
	protocol.InvalidRequest: true, protocol.ContextLengthExceeded: true, protocol.ContentPolicyViolation: true,
	protocol.PermissionDenied: true, protocol.RequestTooLarge: true,
}

// requestFailure uses the adapter's classification even when the response has no usable error text.
func requestFailure(f *suppliers.Failure) protocol.Error {
	typ := protocol.InvalidRequest
	switch f.Cause.(type) {
	case suppliers.ContextTooLong:
		typ = protocol.ContextLengthExceeded
	case suppliers.ContentRejected:
		typ = protocol.ContentPolicyViolation
	}
	return requestError(typ, f.Rejection)
}

// requestError preserves the specific meaning of a request rejection over HTTP and SSE alike. Supplier credential
// failures never reach this path: their scope tells the router to try another credential or endpoint instead.
func requestError(typ protocol.ErrorType, r *protocol.Rejection) protocol.Error {
	if r != nil {
		for _, class := range []string{r.Code, r.Type} {
			switch class {
			case "context_length_exceeded":
				typ = protocol.ContextLengthExceeded
			case "content_filter", "content_policy_violation", "image_content_policy_violation":
				typ = protocol.ContentPolicyViolation
			case "permission_denied", "permission_error":
				typ = protocol.PermissionDenied
			case "payload_too_large", "request_too_large", "413":
				typ = protocol.RequestTooLarge
			default:
				continue
			}
			break // a specific code takes precedence over the generic type
		}
	}
	return protocol.Error{Type: typ, Rejection: r}
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
	"not_found":            "The path is not part of this API.",
	"method_not_allowed":   "The method is not allowed on this path.",
	"invalid_request":      "The request is not valid.",
	"payload_too_large":    "The request body is larger than the limit.",
	"timeout":              "The request body was not received in time.",
	"generation_not_found": "No call with this ID is visible to this key.",
	"upstream_error":       "The upstream failed; retry shortly.",
}

// errorTypes are the Messages `type` of a status; Chat's `type` uses the same words (protocol's errorTypes, which the
// same errors answered after an attempt are written with: TestARefusalIsAnErrorOfProtocol).
var errorTypes = map[int]string{
	http.StatusForbidden:  "permission_error",
	http.StatusBadRequest: "invalid_request_error", http.StatusUnauthorized: "authentication_error",
	http.StatusPaymentRequired: "billing_error", http.StatusNotFound: "not_found_error",
	http.StatusRequestTimeout: "timeout_error", http.StatusRequestEntityTooLarge: "request_too_large",
	http.StatusTooManyRequests: "rate_limit_error", http.StatusServiceUnavailable: "overloaded_error",
	http.StatusBadGateway: "api_error", http.StatusMethodNotAllowed: "invalid_request_error",
}

// maxRetryAfter is the longest Retry-After sent: Claude Code does not retry after more than 60 seconds.
const maxRetryAfter = 60

// writeRefusal is the one error exit before a stream starts. Messages has its own shell; Chat's serves Responses too,
// as docs/domains/gateway.md「错误」 says. The code is the numeric status, as OpenRouter's is; the unified error type is
// metadata.error_type. A 401 closes the connection, so a client that keeps sending a body to an address it may not use
// is not read; so does a 408, whose body stopped coming and is not worth waiting for again.
func writeRefusal(w http.ResponseWriter, f protocol.Format, r *Refusal) {
	message := r.Message
	if message == "" {
		message = messages[r.Code]
	}
	typ := errorTypes[r.Status]
	if r.Code == "credits_insufficient" {
		typ = "billing_error" // a budget is a billing problem whether it is the balance's 402 or a key's or workspace's 403
	}
	detail := map[string]any{"error_type": r.Code}
	maps.Copy(detail, r.Details)
	var body map[string]any
	if f == protocol.Messages {
		detail["type"], detail["message"] = typ, message
		body = map[string]any{"type": "error", "error": detail}
	} else {
		body = map[string]any{"error": map[string]any{"message": message, "type": typ, "param": "", "code": r.Status, "metadata": detail}}
	}
	out, _ := jsonx.MarshalCanonical(body) //nolint:errcheck // a map of strings and numbers always encodes
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if r.RateLimit > 0 {
		h.Set("X-Ratelimit-Limit", strconv.Itoa(r.RateLimit))
		h.Set("X-Ratelimit-Remaining", "0")
		h.Set("X-Ratelimit-Reset", strconv.FormatInt(r.RateReset, 10))
	}
	if r.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(min(r.RetryAfter, maxRetryAfter)))
	}
	if r.Status == http.StatusUnauthorized || r.Status == http.StatusRequestTimeout {
		h.Set("Connection", "close")
	}
	w.WriteHeader(r.Status)
	w.Write(out) //nolint:errcheck,gosec // the client may be gone; nothing more can be told
}

// refusal says how a failure of the admission path is answered, and whether it is one the gateway expected. An
// unexpected one is answered as the node being busy, which is what a database that does not answer looks like to a
// client, and the caller logs it.
func refusal(f protocol.Format, err error) (r *Refusal, expected bool) {
	if r, ok := errors.AsType[*Refusal](err); ok {
		return r, true
	}
	if ins, ok := errors.AsType[*billing.Insufficient](err); ok {
		return insufficient(ins), true
	}
	if none, ok := errors.AsType[*routing.NoEndpoint](err); ok {
		return noEndpoint(none), true
	}
	if extension, ok := errors.AsType[*protocol.ExtensionError](err); ok {
		return invalid(extension.Message), true
	}
	switch {
	case errors.Is(err, accounts.ErrAccountSuspended):
		return &Refusal{Status: http.StatusForbidden, Code: "permission_denied", Message: "Calls on this account are paused. Contact the site administrator."}, true
	case errors.Is(err, accounts.ErrUnknownKey), errors.Is(err, accounts.ErrUnknownHost):
		return errUnauthorized, true
	case errors.Is(err, catalog.ErrModelNotFound), errors.Is(err, catalog.ErrRetired):
		return errNoModel, true
	case errors.Is(err, protocol.ErrInvalid):
		return invalid(strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": ")), true
	case errors.Is(err, billing.ErrUnbounded):
		return unbounded(f, err), true
	case errors.Is(err, accounts.ErrStale), errors.Is(err, catalog.ErrStale), errors.Is(err, routing.ErrStale),
		errors.Is(err, billing.ErrTerminated), errors.Is(err, errCommitUnknown), errors.Is(err, context.DeadlineExceeded):
		return ErrOverloaded, true // the node cannot serve now; the first request that finds it so is not a bug
	}
	return ErrOverloaded, false
}

// toolNames are what a client calls a server tool in each format, which is not the catalog's name for its item: the
// refusal is read by the client, who looks for that word in its request. Chat has no code tool, and its search is the
// web_search_options field.
var toolNames = map[protocol.Format]map[catalog.Item]string{
	protocol.Messages: {catalog.ItemWebSearch: "the web_search tool", catalog.ItemCodeExecution: "the code_execution tool"},
	protocol.Responses: {
		catalog.ItemWebSearch: "the web_search tool", catalog.ItemCodeExecution: "the code_interpreter tool",
		catalog.ItemFileSearch: "the file_search tool",
	},
	protocol.Chat: {catalog.ItemWebSearch: "the web_search_options setting"},
}

// unbounded tells a client what to do about a server tool it enabled without a limit on its uses: write the limit where
// the format has a field for it (Messages' web_search has max_uses, Responses has max_tool_calls), and where it has none
// (Chat's search, Messages' code_execution) say the tool is not supported yet in that format: no request there can be
// served, so asking for a limit would send the client looking for a field that does not exist. A tool this cannot name
// is "a server tool".
func unbounded(f protocol.Format, err error) *Refusal {
	var item catalog.Item
	name := "a server tool"
	if u, ok := errors.AsType[*billing.UnboundedError](err); ok {
		item = u.Item
		if n, ok := toolNames[f][item]; ok {
			name = n
		}
	}
	switch {
	case f == protocol.Responses:
		return invalid(name + " is enabled without a limit on its uses, and this model has no default one: set max_tool_calls")
	case f == protocol.Messages && item == catalog.ItemWebSearch:
		return invalid(name + " is enabled without a limit on its uses, and this model has no default one: set max_uses on the tool")
	}
	return invalid(name + " is not supported yet in this format for this model, which has no default limit on its uses")
}

var limitSources = map[billing.Source]string{
	billing.SourceBalance: "balance", billing.SourceSiteBalance: "balance",
	// OpenRouter's names for the two budget layers, which its clients branch on; the balance keeps ours, since its 402
	// carries none (OpenRouter《Errors and Debugging》的 budget errors，2026-11-02 核对).
	billing.SourceWorkspace: "openrouter_workspace_budget", billing.SourceKey: "openrouter_key_limit",
}

// insufficient is the balance's 402, or a key's or workspace's 403, as OpenRouter answers a spent budget. Only a limit
// that resets can pass later, so only that one says when to come back; billing does not say when the window ends, so a
// client is told the longest wait it would honour (docs/domains/gateway.md「错误」).
func insufficient(e *billing.Insufficient) *Refusal {
	status := http.StatusPaymentRequired
	if e.Source == billing.SourceWorkspace || e.Source == billing.SourceKey {
		status = http.StatusForbidden
	}
	r := &Refusal{Status: status, Code: "credits_insufficient", Details: map[string]any{"limit_source": limitSources[e.Source]}}
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
