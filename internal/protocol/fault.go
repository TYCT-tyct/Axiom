package protocol

import (
	"cmp"
	"errors"
)

// Fault is what an upstream failure says of the endpoint it came from: the stream broke, the upstream ended it with an
// error event of some class, or the model generated a tool call whose input is not JSON. quality judges an endpoint's
// health by it (docs/domains/quality.md「什么算一次失败」); routing and the gateway decide nothing by it. It carries no
// text of the upstream's.
type Fault uint8

// The faults. The classes of an error event are those of Anthropic《Errors》and OpenAI《Error codes》(read
// 2026-10-19); a class neither names, or an error event that names none, is FaultUnrecognised.
const (
	FaultNone           Fault = iota // not a failure of the upstream's
	FaultBroken                      // the stream broke off, broke its format or ended before its end event
	FaultServer                      // an error event of the upstream's own failure: api_error, overloaded_error, server_error
	FaultRateLimited                 // an error event saying too many requests
	FaultContextTooLong              // an error event saying the request exceeds the context
	FaultModeration                  // an error event saying moderation refused the content
	FaultBadRequest                  // an error event saying the request is invalid
	FaultUnrecognised                // an error event of a class not listed above
	FaultToolInput                   // a tool call's input is not JSON: what the model generated, not the endpoint's failure
)

// faultClasses are the error classes a fault is known by: Anthropic's error.type, OpenAI's error.type and error.code
// (Chat), code (the Responses error event) and response.error.code (response.failed).
var faultClasses = map[string]Fault{
	"api_error": FaultServer, "overloaded_error": FaultServer, "server_error": FaultServer,
	"rate_limit_error": FaultRateLimited, "rate_limit_exceeded": FaultRateLimited,
	"context_length_exceeded": FaultContextTooLong,
	"content_filter":          FaultModeration, "content_policy_violation": FaultModeration,
	"image_content_policy_violation": FaultModeration,
	"invalid_request_error":          FaultBadRequest, "invalid_prompt": FaultBadRequest, "request_too_large": FaultBadRequest,
}

// faultError is ErrUpstream with its fault.
type faultError struct {
	fault  Fault
	detail string
}

func (e *faultError) Error() string { return ErrUpstream.Error() + ": " + e.detail }

// Is makes a faultError an ErrUpstream, so callers that only ask whether the upstream failed read it as before.
func (*faultError) Is(target error) bool { return target == ErrUpstream }

// FaultOf is the fault an error of the Relay reports: the class an error event or a tool input gave it; FaultBroken for
// any other failed read, ErrUpstream or a read that failed as it is; FaultNone for nil and for ErrMemory, which is the
// node's. A read that failed because the attempt was cancelled is FaultBroken too: the caller knows why it cancelled,
// and judges that first.
func FaultOf(err error) Fault {
	if f, ok := errors.AsType[*faultError](err); ok {
		return f.fault
	}
	if err == nil || errors.Is(err, ErrMemory) {
		return FaultNone
	}
	return FaultBroken
}

// class reads an error's class into f when it is a string that names a fault. Anything else leaves f as it is: the
// error event fails the stream either way, and its fault is then FaultUnrecognised.
func (s *reader) class(f *Fault) error {
	v, err := s.dec.ReadValue()
	if err != nil || v.Kind() != '"' {
		return err
	}
	if c, known := faultClasses[string(v[1:len(v)-1])]; known { // an escaped class matches nothing, as it names none
		*f = c
	}
	return nil
}

// errorObject reads an error member by fields when it is an object and skips it otherwise: what it says of its class
// only refines the fault of an event that fails anyway, and a member of another shape is no reason to fail one that
// does not.
func errorObject[S any](r *reader, s S, fields map[string]func(S) error) error {
	if r.dec.PeekKind() != '{' {
		return r.skip()
	}
	return walk(r, s, fields)
}

// errorEvent is the error event just read, as ErrUpstream with its fault: the code's when it names one, then the class's.
func (s *eventReader) errorEvent(what string) error {
	return &faultError{fault: cmp.Or(s.code, s.errClass, FaultUnrecognised), detail: what}
}
