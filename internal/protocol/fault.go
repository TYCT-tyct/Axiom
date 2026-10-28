package protocol

import (
	"cmp"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"errors"
)

// Fault is what an upstream failure says of the endpoint it came from: the stream broke, the upstream ended it with an
// error event of some class, or the model generated a tool call whose input is not JSON. quality judges an endpoint's
// health by it (docs/domains/quality.md「什么算一次失败」); the gateway asks whether the class is the request's own, which
// no other endpoint would serve and which the client is answered with the upstream's words (docs/domains/gateway.md
// 「错误」). The fault itself carries no text of the upstream's; the error event's words travel beside it (RejectionOf).
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
	"400": FaultBadRequest, "413": FaultBadRequest, "422": FaultBadRequest,
	"429": FaultRateLimited,
	"500": FaultServer, "502": FaultServer, "503": FaultServer, "504": FaultServer,
	"api_error": FaultServer, "overloaded_error": FaultServer, "server_error": FaultServer,
	"rate_limit_error": FaultRateLimited, "rate_limit_exceeded": FaultRateLimited,
	"context_length_exceeded": FaultContextTooLong,
	"content_filter":          FaultModeration, "content_policy_violation": FaultModeration,
	"image_content_policy_violation": FaultModeration,
	"invalid_request_error":          FaultBadRequest, "invalid_prompt": FaultBadRequest, "invalid_value": FaultBadRequest,
	"request_too_large": FaultBadRequest,
}

// faultError is ErrUpstream with its fault and, when the error event named them, the members of the error object it
// refused with, in its own words bounded: taking the supplier's identity out of them is the supplier's, which knows it,
// and giving them to the client the gateway's (docs/domains/gateway.md「错误」).
type faultError struct {
	fault     Fault
	detail    string
	rejection Rejection
}

func (e *faultError) Error() string { return ErrUpstream.Error() + ": " + e.detail }

// Is makes a faultError an ErrUpstream, so callers that only ask whether the upstream failed read it as before.
func (*faultError) Is(target error) bool { return target == ErrUpstream }

// RejectionOf is what an upstream said when it ended the stream with an error event: its words and the members a client
// branches on, still holding whatever names the supplier, and nil when the failure says nothing of it, as a stream that
// broke off, or one whose error event named nothing, does. The words are bounded and are not logged or stored.
func RejectionOf(err error) *Rejection {
	f, _ := errors.AsType[*faultError](err)
	if f == nil || f.rejection == (Rejection{}) {
		return nil
	}
	r := f.rejection
	return &r
}

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

// class reads the decoded class and preserves its bounded text for the response. Codes take precedence over types.
func (s *reader) class(f *Fault, text *string) error {
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	switch {
	case v.Kind() == '"':
		*text, err = s.errorText(v)
	case v.Kind() == '0':
		if len(v) <= maxErrorText {
			*text = string(v) // HTTP rejections also preserve numeric codes as strings.
		}
	}
	if c, known := faultClasses[*text]; known {
		*f = c
	}
	return err
}

// textOf ignores other member types: the error event already fails the stream.
func (s *reader) textOf(text *string) error {
	v, err := s.dec.ReadValue()
	if err == nil && v.Kind() == '"' {
		*text, err = s.errorText(v)
	}
	return err
}

const maxErrorText = 2 << 10

// errorText omits oversized fields whole: cutting before supplier sanitization could leave a credential prefix that
// no longer matches the credential. The bound also limits the temporary storage needed to decode escapes.
func (s *reader) errorText(v jsontext.Value) (string, error) {
	if unquotedLen(v) > maxErrorText {
		return "", nil
	}
	b, err := s.unquote(v)
	return string(b), err
}

var errorFields = map[string]func(*eventReader) error{
	"type":    func(s *eventReader) error { return s.class(&s.errClass, &s.rejection.Type) },
	"code":    func(s *eventReader) error { return s.class(&s.code, &s.rejection.Code) },
	"message": func(s *eventReader) error { return s.textOf(&s.rejection.Message) },
	"param":   func(s *eventReader) error { return s.textOf(&s.rejection.Param) },
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

// errorEvent is the error event just read, as ErrUpstream with its fault: the code's when it names one, then the class's,
// and the members it refused with.
func (s *eventReader) errorEvent(what string) error {
	return &faultError{fault: cmp.Or(s.code, s.errClass, FaultUnrecognised), detail: what, rejection: s.rejection}
}
