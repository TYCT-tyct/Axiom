package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
)

// apiError is a failure with the status and code the contract gives it. A handler returns it, or an error that
// failure turns into one; any other error is ours, and is answered with a bare 500.
type apiError struct {
	status  int
	code    errorCode
	message string
	// retryAfter is how many seconds a 429 asks the client to wait.
	retryAfter int
	fields     []errorField
}

func (e *apiError) Error() string { return string(e.code) + ": " + e.message }

// refusal is how one error of a module is answered.
type refusal struct {
	target  error
	status  int
	code    errorCode
	message string
}

// refusals are how the modules' refusals are answered, in the order they are tried. The message is English for logs
// and developers, and never repeats what the client sent.
var refusals = []refusal{
	{accounts.ErrBadCredentials, http.StatusUnauthorized, codeWrongLogin, "wrong email or password"},
	{accounts.ErrNoSession, http.StatusUnauthorized, codeUnauthenticated, "not signed in"},
	{accounts.ErrBadCode, http.StatusUnauthorized, codeInvalidCode, "wrong code"},
	{accounts.ErrBadOrigin, http.StatusForbidden, codeOriginRejected, "the origin, content type or CSRF token is not accepted"},
	{accounts.ErrForbidden, http.StatusForbidden, codeForbidden, "this session may not do that"},
	{accounts.ErrNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{accounts.ErrKeyNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{accounts.ErrUnknownHost, http.StatusNotFound, codeNotFound, "no such resource"},
	{pg.ErrNoRows, http.StatusNotFound, codeNotFound, "no such resource"},
	{accounts.ErrInvalidInvite, http.StatusUnprocessableEntity, codeInvalidInvite, "the invite code is not usable"},
	{accounts.ErrWeakPassword, http.StatusUnprocessableEntity, codeWeakPassword, "the password must be 12 to 256 characters"},
	{accounts.ErrBadEmail, http.StatusUnprocessableEntity, codeUnprocessable, "not an email address"},
	{accounts.ErrEmailTaken, http.StatusConflict, codeEmailTaken, "the email is already registered"},
	{accounts.ErrTOTPOn, http.StatusConflict, codeConflict, "two-step verification is already on"},
	{accounts.ErrTooManyKeys, http.StatusConflict, codeConflict, "the account holds as many keys as the site allows"},
	{accounts.ErrTooManyAttempts, http.StatusTooManyRequests, codeRateLimited, "too many attempts"},
	{accounts.ErrBusy, http.StatusServiceUnavailable, codeUnavailable, "busy, try again shortly"},
	{accounts.ErrStale, http.StatusServiceUnavailable, codeUnavailable, "temporarily unavailable"},
}

// invalidField is a 400 that names the one part of the request that is wrong.
func invalidField(pointer, message string) *apiError {
	return &apiError{
		status: http.StatusBadRequest, code: codeInvalidRequest, message: "the request breaks the contract",
		fields: []errorField{{Pointer: pointer, Message: message}},
	}
}

// failure is err as an apiError if it is a refusal the contract answers, and nil if it is ours.
func failure(err error) *apiError {
	if apiErr, ok := errors.AsType[*apiError](err); ok {
		return apiErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errDeadline
	}
	if errors.Is(err, observability.ErrBadCursor) {
		return invalidField("cursor", "not a cursor this API gave out")
	}
	if errors.Is(err, accounts.ErrBadSecret) {
		return invalidField("/secret", "not a TOTP secret this API gave out")
	}
	for _, r := range slices.Concat(refusals, adminRefusals) {
		if !errors.Is(err, r.target) {
			continue
		}
		apiErr := &apiError{status: r.status, code: r.code, message: r.message}
		if tooMany, ok := errors.AsType[*accounts.TooManyAttempts](err); ok {
			apiErr.retryAfter = max(1, int((tooMany.RetryAfter+time.Second-1)/time.Second))
		}
		return apiErr
	}
	return nil
}

// respond answers err: with the contract's error body when it is a refusal, and otherwise, since it describes our
// internals, with a bare 500 after logging it.
func respond(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	apiErr := failure(err)
	if apiErr == nil {
		logger.Error("API handler failed", "err", err, "request_id", httpserver.RequestID(r.Context()))
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	if apiErr.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(apiErr.retryAfter))
	}
	writeErrorBody(w, apiErr.status, errorBody{Error: errorDetail{
		Code: apiErr.code, Message: apiErr.message, Fields: fieldsPointer(apiErr.fields),
	}})
}

func fieldsPointer(fields []errorField) *[]errorField {
	if len(fields) == 0 {
		return nil
	}
	return &fields
}
