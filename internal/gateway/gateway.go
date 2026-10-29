// Package gateway orchestrates one model call. This file holds what every step shares: the refusals the in-process
// limits produce and the three contexts of a call. The limits themselves (slots, account gate, connection classes,
// queue, rate counts) and the startup checks of their configuration are in the other files; internal/gateway/README.md
// has the design.
package gateway

import (
	"context"
	"time"
)

// Refusal is a call the gateway turns away. The error exit writes Status and Code, plus Retry-After when RetryAfter
// is not zero (docs/domains/gateway.md「错误」). A refusal that has an estimate of its wait, such as the queue's 429,
// gets that estimate from the caller that has it, as a copy; the shared ones below are never changed. Message, when
// set, replaces the text of the code, and Details are members added beside error_type. Reason is which limit turned
// the call away when several share a Code; it is counted (Backpressure.refused) and never sent to the client.
type Refusal struct {
	Status     int
	Code       string
	RetryAfter int // seconds
	Reason     string
	Message    string
	Details    map[string]any
}

func (r *Refusal) Error() string { return r.Code }

// rateLimited is a 429 for one of the limits below. They share the code rate_limited (docs/domains/gateway.md「错误」), so
// a client reads one thing whatever the limit; the reason tells an operator which limit it was.
func rateLimited(reason string) *Refusal {
	return &Refusal{Status: 429, Code: "rate_limited", Reason: reason}
}

var (
	errIPInFlight      = rateLimited("ip_in_flight")      // the client address has IPInFlight requests in flight
	errAccountInFlight = rateLimited("account_in_flight") // the account has its in-flight limit of requests
	errKeyRate         = rateLimited("key_rate")          // the key made more than KeyRate requests in RateWindow
	errCountRate       = rateLimited("count_rate")        // the key made more than CountRate count_tokens requests in RateWindow
	errAccountGate     = rateLimited("account_gate")      // the account's gate stayed busy for AccountGateWait
	errQueueFull       = rateLimited("queue_full")        // every candidate endpoint is full and the queue has QueueMax waiters
	errQueueKeyFull    = rateLimited("queue_key_full")    // the same, and the key has QueuePerKey of them
	errQueueWait       = rateLimited("queue_wait")        // the same, and the call waited QueueWait
	errCredentialsBusy = rateLimited("credentials_busy")  // every credential of the route is at its concurrency limit
	// ErrOverloaded is a node that has no capacity left and does not queue: 503.
	ErrOverloaded = &Refusal{Status: 503, Code: "overloaded", RetryAfter: 1}
)

// AttemptContext bounds one attempt at the upstream by the call's deadline. An endpoint that does not carry a
// cancellation to the upstream (followClient false) is read to its end even when the client has gone, because the
// usage is billed either way; the idle timer and the deadline still stop it. Cancelling the attempt never cancels
// the client's context.
func AttemptContext(client context.Context, deadline time.Time, followClient bool) (context.Context, context.CancelFunc) {
	if !followClient {
		client = context.WithoutCancel(client)
	}
	return context.WithDeadline(client, deadline)
}

// SettleContext is for the writes that end a call: it outlives a cancelled client, which is when they matter most,
// and has a deadline of its own.
func SettleContext(client context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(client), timeout)
}

// expired is the error of a wait that ended: the caller's own context error if that is why, otherwise the wait's
// limit ran out, which is the 429 limit.
func expired(parent context.Context, limit *Refusal) error {
	if err := parent.Err(); err != nil {
		return err
	}
	return limit
}

// rateLimits are the 429 refusals above, which the counter's attribute sets are made for ahead of time, so that
// counting one on the call path allocates nothing (internal/platform/README.md「遥测」).
var rateLimits = []*Refusal{
	errIPInFlight, errAccountInFlight, errKeyRate, errCountRate, errAccountGate,
	errQueueFull, errQueueKeyFull, errQueueWait, errCredentialsBusy,
}
