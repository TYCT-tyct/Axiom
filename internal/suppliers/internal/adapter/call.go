package adapter

import (
	"net/http"
	"time"

	"axiom/internal/protocol"
)

// CallQuirks is the call half of an adapter: what is particular to one system when a request is sent to it. The path
// and the authorization header follow from the call format and are the same for every system, so they are not here.
type CallQuirks interface {
	// Formats lists the call formats a group serves, from the facts the supplier gave about it.
	Formats(g GroupFacts) []protocol.Format
	// ChargeRef reads the supplier's request id from response headers, which reconciliation matches against the
	// charge records.
	ChargeRef(h http.Header) (ChargeRef, bool)
	// Classify turns a non-2xx response into a Failure. body is cut to 64 KiB. It sets Executed to NotExecuted only
	// for responses by which the supplier says it refused before running the request.
	Classify(status int, h http.Header, body []byte) Failure
	Policy() CallPolicy
}

// CallPolicy is the adapter's default for what a supplier does with a call; quality's measurement per endpoint
// replaces Cancels (docs/domains/billing.md「客户端断开」).
type CallPolicy struct {
	// Streams: the supplier can stream a response.
	Streams bool
	// Cancels: the supplier stops generating when the client disconnects.
	Cancels bool
}

// Execution tells whether the supplier ran the request. The zero value, Unknown, is what is assumed when nothing is
// known: the cost is then estimated and reconciliation corrects it, so a failure is never treated as free by
// accident.
type Execution uint8

// The values of Execution.
const (
	ExecutionUnknown Execution = iota
	NotExecuted
)

// Scope is what a failure says is at fault, and so what to stop using.
type Scope uint8

// The scopes.
const (
	ScopeRequest    Scope = iota // the request itself: retrying it elsewhere fails the same way
	ScopeEgress                  // the route to the supplier
	ScopeEndpoint                // the one group and model
	ScopeCredential              // the key
	ScopeGroup                   // everything in the group
	ScopeSupplier                // everything at the supplier
)

// Failure is a failed attempt as the module's other parts see it. It carries no text from the supplier: what was
// wrong is a variant of Cause with numbers and times only, so a response that echoes a credential cannot reach a log
// or a table through it.
type Failure struct {
	Executed Execution
	Scope    Scope
	Cause    Cause
}

// Cause is what went wrong. The variants are closed: adding one makes every switch on Cause fail to compile or lint
// until it handles the new case.
//
//sumtype:decl
type Cause interface{ cause() }

// RateLimited means too many requests. RetryAfter is zero when the supplier did not say.
type RateLimited struct{ RetryAfter time.Duration }

// Overloaded means the supplier has no capacity or no available channel right now.
type Overloaded struct{}

// UpstreamError means the supplier failed with a 5xx.
type UpstreamError struct{ Status int }

// SupplierBalanceLow means the supplier's balance cannot pay for the call.
type SupplierBalanceLow struct{}

// CredentialQuotaExhausted means the key's own allowance is used up. ResetAt is zero when not stated.
type CredentialQuotaExhausted struct{ ResetAt time.Time }

// CredentialRejected means the key is refused for a reason the response does not tell apart (revoked, overdrawn).
type CredentialRejected struct{}

// NotPermitted means the key may not use this model or group.
type NotPermitted struct{}

// ContextTooLong means the request exceeds the model's context.
type ContextTooLong struct{}

// BadRequest means the supplier rejects the request's form.
type BadRequest struct{}

// ContentRejected means the supplier's moderation refused the content.
type ContentRejected struct{}

// ModelNotFound means the supplier has no such model.
type ModelNotFound struct{}

// Unknown means a response no rule recognises. SuspectedEgressBlock is set for an HTML page, which is what a firewall or an
// anti-bot page looks like.
type Unknown struct {
	Status               int
	SuspectedEgressBlock bool
}

// Transport means no usable response came back (connection, timeout, the egress). It is set by the module's shared call
// code, never by an adapter.
type Transport struct{}

func (RateLimited) cause()              {}
func (Overloaded) cause()               {}
func (UpstreamError) cause()            {}
func (SupplierBalanceLow) cause()       {}
func (CredentialQuotaExhausted) cause() {}
func (CredentialRejected) cause()       {}
func (NotPermitted) cause()             {}
func (ContextTooLong) cause()           {}
func (BadRequest) cause()               {}
func (ContentRejected) cause()          {}
func (ModelNotFound) cause()            {}
func (Unknown) cause()                  {}
func (Transport) cause()                {}
