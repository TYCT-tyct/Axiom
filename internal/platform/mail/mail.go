// Package mail delivers outbound email over SMTP through the job queue: one message per job, retried while the
// failure may pass and given up when the server rejects it, and at most one message per recipient per minute. Callers
// write the plain-text and HTML bodies themselves; the account flows that decide what an email says live elsewhere
// (docs/domains/accounts.md「给用户的邮件」). The rules and the choices behind them are in this directory's README.
package mail

import "errors"

// Errors a caller or a job acts on; an SMTP server's reply and the network only ever add detail to them.
var (
	// ErrNotConfigured says this node has no SMTP settings: email is disabled and nothing is queued, so a flow can
	// report that it cannot send instead of losing the message.
	ErrNotConfigured = errors.New("mail: email is disabled: AXIOM_SMTP_HOST and AXIOM_SMTP_FROM are unset")
	// ErrRateLimited says the recipient was messaged within rateInterval; the job waits the interval out without
	// spending an attempt.
	ErrRateLimited = errors.New("mail: the recipient was messaged within the last minute")
	// ErrRejected says the server answered 5xx: it will refuse the same message again, so the job is not retried.
	ErrRejected = errors.New("mail: refused by the SMTP server with a 5xx reply")
	// ErrInvalidMessage says the caller's message cannot be sent at all: it is reported at enqueue time, before the
	// transaction commits, and a job that finds one is cancelled rather than retried.
	ErrInvalidMessage = errors.New("mail: invalid message")
)
