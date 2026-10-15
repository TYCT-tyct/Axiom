// Package gateway orchestrates one model call. This file holds what every step shares: the refusals the in-process
// limits produce and the three contexts of a call. The limits themselves (slots, account gate, connection classes,
// queue, rate counts) and the startup checks of their configuration are in the other files; internal/gateway/README.md
// has the design.
package gateway

import (
	"context"
	"time"
)

// Refusal is a call the gateway turns away because a limit is full. The error exit writes Status and Code, plus
// Retry-After when RetryAfter is not zero (docs/domains/gateway.md「错误」). A refusal that has an estimate of its
// wait, such as the queue's 429, gets that estimate from the caller that has it.
type Refusal struct {
	Status     int
	Code       string
	RetryAfter int // seconds
}

func (r *Refusal) Error() string { return r.Code }

var (
	// ErrRateLimited is a per-IP or per-account limit, a full queue, or an account gate that stayed busy: 429.
	ErrRateLimited = &Refusal{Status: 429, Code: "rate_limited"}
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
// limit ran out, which is a 429.
func expired(parent context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	return ErrRateLimited
}
