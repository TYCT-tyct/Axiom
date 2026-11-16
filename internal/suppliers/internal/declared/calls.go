package declared

import (
	"net/http"
	"slices"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// callPlan is the call half of the declaration: the formats, the default behavior and the rules that name a refusal.
type callPlan struct {
	formats   []protocol.Format
	streams   bool
	cancels   bool
	chargeRef string
	code      *path
	resetAt   *path
	rules     []errorRule
}

type errorRule struct {
	status   int
	code     string
	cause    causeKind
	scope    adapter.Scope
	executed adapter.Execution
}

// causeKind is the closed list of causes a rule may name. "transport" and "unknown" are not here: transport is the
// shared call code's, and unknown is what a response no rule names becomes.
type causeKind uint8

const (
	causeRateLimited causeKind = iota
	causeOverloaded
	causeUpstreamError
	causeSupplierBalanceLow
	causeCredentialQuotaExhausted
	causeCredentialRejected
	causeNotPermitted
	causeContextTooLong
	causeBadRequest
	causeContentRejected
	causeModelNotFound
)

var causes = map[string]causeKind{
	"rate_limited": causeRateLimited, "overloaded": causeOverloaded, "upstream_error": causeUpstreamError,
	"supplier_balance_low": causeSupplierBalanceLow, "credential_quota_exhausted": causeCredentialQuotaExhausted,
	"credential_rejected": causeCredentialRejected, "not_permitted": causeNotPermitted,
	"context_too_long": causeContextTooLong, "bad_request": causeBadRequest, "content_rejected": causeContentRejected,
	"model_not_found": causeModelNotFound,
}

var scopes = map[string]adapter.Scope{
	"request": adapter.ScopeRequest, "egress": adapter.ScopeEgress, "endpoint": adapter.ScopeEndpoint,
	"credential": adapter.ScopeCredential, "group": adapter.ScopeGroup, "supplier": adapter.ScopeSupplier,
}

// quirks is the call half, as the module's shared call code asks about it.
type quirks struct{ p callPlan }

// Policy implements adapter.CallQuirks.
func (q quirks) Policy() adapter.CallPolicy {
	return adapter.CallPolicy{Streams: q.p.streams, Cancels: q.p.cancels}
}

// Formats implements adapter.CallQuirks. The group's facts carry the formats the declaration read for it; a group
// without them keeps the formats the whole system declares.
func (q quirks) Formats(g adapter.GroupFacts) []protocol.Format {
	var facts struct {
		Formats []string `json:"formats"`
	}
	if len(g) == 0 || jsonx.Unmarshal(g, &facts) != nil || facts.Formats == nil {
		return q.p.formats
	}
	seen := map[protocol.Format]bool{}
	var out []protocol.Format
	for _, name := range facts.Formats {
		if f, ok := formatNames[name]; ok && slices.Contains(q.p.formats, f) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// ChargeRef implements adapter.CallQuirks: the header the relay puts its request id in, if it is printable ASCII of
// sane length. Anything else is not taken for a request id.
func (q quirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := h.Get(q.p.chargeRef)
	if v == "" || len(v) > 128 {
		return "", false
	}
	for _, r := range v {
		if r <= ' ' || r > '~' {
			return "", false
		}
	}
	return adapter.ChargeRef(v), true
}

// Classify implements adapter.CallQuirks. An HTML page is a block at the egress whatever its status; otherwise the
// declaration's rules come first and the shared defaults last. The supplier's own words never leave here: a cause is a
// variant with a status and a time only.
func (q quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	if adapter.IsHTML(body) {
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	}
	code := ""
	if q.p.code != nil {
		if raw, ok := q.p.code.lookup(body); ok {
			code, _ = readText(raw)
		}
	}
	for _, r := range q.p.rules {
		if r.status != 0 && r.status != status || r.code != "" && r.code != code {
			continue
		}
		return adapter.Failure{Executed: r.executed, Scope: r.scope, Cause: q.cause(r.cause, status, h, body)}
	}
	switch {
	case status == http.StatusTooManyRequests:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
	case status == http.StatusBadRequest:
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case status >= http.StatusInternalServerError:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

// cause builds the variant a rule names. Only the causes that carry data read anything: the response's wait, its
// status, and the reset time the declaration names.
func (q quirks) cause(kind causeKind, status int, h http.Header, body []byte) adapter.Cause {
	switch kind {
	case causeRateLimited:
		return adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}
	case causeOverloaded:
		return adapter.Overloaded{}
	case causeUpstreamError:
		return adapter.UpstreamError{Status: status}
	case causeSupplierBalanceLow:
		return adapter.SupplierBalanceLow{}
	case causeCredentialQuotaExhausted:
		return adapter.CredentialQuotaExhausted{ResetAt: q.resetAt(body)}
	case causeCredentialRejected:
		return adapter.CredentialRejected{}
	case causeNotPermitted:
		return adapter.NotPermitted{}
	case causeContextTooLong:
		return adapter.ContextTooLong{}
	case causeBadRequest:
		return adapter.BadRequest{}
	case causeContentRejected:
		return adapter.ContentRejected{}
	case causeModelNotFound:
		return adapter.ModelNotFound{}
	}
	return adapter.Unknown{Status: status}
}

// resetAt is the time a refusal says an exhausted allowance returns, or zero when it does not say.
func (q quirks) resetAt(body []byte) time.Time {
	if q.p.resetAt == nil {
		return time.Time{}
	}
	raw, ok := q.p.resetAt.lookup(body)
	if !ok {
		return time.Time{}
	}
	at, _ := readTime(raw)
	return at
}
