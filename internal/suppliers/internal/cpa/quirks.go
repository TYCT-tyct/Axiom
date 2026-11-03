package cpa

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

type quirks struct{}

// Formats implements adapter.CallQuirks: CPA serves all three chat formats and converts between them.
func (quirks) Formats(adapter.GroupFacts) []protocol.Format {
	return []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat}
}

// ChargeRef implements adapter.CallQuirks. CPA sets X-CPA-TRACE-ID to <selectedAt>-<authIndex>-<requestID>, and the
// requestID (the last 36 characters) is the request_id its usage record carries. A header that does not end in a UUID
// gives no key, rather than a guessed one.
func (quirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := strings.TrimSpace(h.Get("X-Cpa-Trace-Id"))
	if len(v) < 36 {
		return "", false
	}
	id := v[len(v)-36:]
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", false
	}
	return adapter.ChargeRef(parsed.String()), true
}

// Classify implements adapter.CallQuirks. CPA speaks the OpenAI error shape on its own paths and often passes the
// upstream's status through; the rules below keep the distinctions the retry and billing code acts on.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if looksLikeHTML(body) {
		return adapter.Failure{
			Executed: adapter.ExecutionUnknown, Scope: adapter.ScopeEgress,
			Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true},
		}
	}
	return classify(status, errorCode(body), h)
}

func classify(status int, code string, h http.Header) adapter.Failure {
	switch {
	case status >= 500:
		return serverFailure(status)
	case status == http.StatusTooManyRequests:
		return adapter.Failure{
			Executed: adapter.NotExecuted, Scope: adapter.ScopeGroup,
			Cause: adapter.RateLimited{RetryAfter: retryAfter(h.Get("Retry-After"))},
		}
	case status == http.StatusUnauthorized:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}
	case status == http.StatusPaymentRequired:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeSupplier, Cause: adapter.SupplierBalanceLow{}}
	case status == http.StatusForbidden:
		return forbidden(code)
	case status == http.StatusNotFound:
		return notFound(status, code)
	case status == http.StatusBadRequest:
		return badRequest(code)
	case status == http.StatusUnprocessableEntity:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	default:
		return adapter.Failure{Executed: adapter.ExecutionUnknown, Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
	}
}

func serverFailure(status int) adapter.Failure {
	if status == http.StatusServiceUnavailable || status == 529 {
		return adapter.Failure{Executed: adapter.ExecutionUnknown, Scope: adapter.ScopeSupplier, Cause: adapter.Overloaded{}}
	}
	return adapter.Failure{Executed: adapter.ExecutionUnknown, Scope: adapter.ScopeSupplier, Cause: adapter.UpstreamError{Status: status}}
}

func forbidden(code string) adapter.Failure {
	switch code {
	case "insufficient_quota", "quota_exceeded", "credential_quota_exceeded":
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialQuotaExhausted{}}
	case "model_not_found":
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.ModelNotFound{}}
	default:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.NotPermitted{}}
	}
}

func notFound(status int, code string) adapter.Failure {
	if knownModelCode[code] {
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.ModelNotFound{}}
	}
	return adapter.Failure{Executed: adapter.ExecutionUnknown, Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

func badRequest(code string) adapter.Failure {
	switch code {
	case "context_length_exceeded", "context_too_long":
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.ContextTooLong{}}
	case "content_policy_violation", "content_filter", "moderation_blocked":
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.ContentRejected{}}
	default:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	}
}

var knownModelCode = map[string]bool{
	"model_not_found": true, "model_not_found_error": true, "unknown_model": true,
}

var knownCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, code := range []string{
		"invalid_api_key", "insufficient_quota", "quota_exceeded", "credential_quota_exceeded",
		"rate_limit_exceeded", "model_not_found", "model_not_found_error", "unknown_model",
		"context_length_exceeded", "context_too_long", "content_policy_violation", "content_filter",
		"moderation_blocked",
	} {
		m[code] = true
	}
	return m
}()

// Policy implements adapter.CallQuirks. A CPA streams; whether it stops the upstream generation when the client goes
// away is measured per endpoint, so the conservative default is that it does not.
func (quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true} }

// errorCode returns a known machine-readable code, never a message: a CPA error may echo the upstream body, which can
// hold a credential.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
		Code any `json:"code"`
	}
	if jsonx.Unmarshal(body, &e) != nil {
		return ""
	}
	for _, v := range []any{e.Error.Code, e.Code} {
		if s, ok := v.(string); ok {
			if s = strings.ToLower(strings.TrimSpace(s)); knownCodes[s] {
				return s
			}
		}
	}
	return ""
}

func looksLikeHTML(body []byte) bool {
	s := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html") || strings.Contains(s, "<title>just a moment")
}

func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
