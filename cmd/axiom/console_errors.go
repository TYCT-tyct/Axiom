package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/payments"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/page"
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
	{accounts.ErrPreset, http.StatusUnprocessableEntity, codeUnprocessable, "invalid preset configuration"},
	{accounts.ErrPresetConflict, http.StatusConflict, codeConflict, "preset slug exists or limit reached"},
	{accounts.ErrNotificationPreferences, http.StatusUnprocessableEntity, codeUnprocessable, "invalid notification preferences"},
	{accounts.ErrBillingPreference, http.StatusUnprocessableEntity, codeUnprocessable, "invalid billing preference"},
	{accounts.ErrAnnouncement, http.StatusUnprocessableEntity, codeUnprocessable, "invalid announcement validity window"},
	{accounts.ErrInvalidReset, http.StatusUnprocessableEntity, codeUnprocessable, "invalid or expired password reset link"},
	{accounts.ErrOAuthNotConfigured, http.StatusNotFound, codeNotFound, "third-party sign-in is not configured"},
	{accounts.ErrWrongLoginDomain, http.StatusForbidden, codeLoginDomainRequired, "use the site's login domain"},
	{accounts.ErrAccountNotLinked, http.StatusForbidden, codeAccountNotLinked, "this provider account is not linked to a user"},
	{accounts.ErrIdentityLinked, http.StatusConflict, codeIdentityTaken, "this provider account is linked to another user"},
	{accounts.ErrIdentityAlreadyLinked, http.StatusConflict, codeIdentityAlreadyLinked, "unlink this provider before linking another account from it"},
	{accounts.ErrMFARequired, http.StatusForbidden, codeMfaRequired, "this needs a session that passed two-step verification"},
	{accounts.ErrConsentRequired, http.StatusForbidden, codeConsentRequired, "the current legal documents must be accepted first"},
	{accounts.ErrOAuthFailed, http.StatusBadRequest, codeInvalidRequest, "the provider did not complete the sign-in"},
	{accounts.ErrPasskeysOff, http.StatusNotFound, codeNotFound, "passkeys are not enabled"},
	{accounts.ErrPasskeyFailed, http.StatusBadRequest, codeInvalidRequest, "the passkey ceremony failed"},
	{accounts.ErrTooManyPasskeys, http.StatusConflict, codeConflict, "the user has the most passkeys a user may hold"},
	{accounts.ErrLoginDomainLocked, http.StatusConflict, codeLoginDomainLocked, "the site's sign-in assets bind it to this login domain"},
	{accounts.ErrLoginDomainRequired, http.StatusBadRequest, codeLoginDomainRequired, "passkeys need a login domain"},
	{accounts.ErrPasskeysInUse, http.StatusConflict, codeConflict, "users of this site still hold passkeys; they must remove them before passkeys can be turned off"},
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
	{accounts.ErrRegistrationClosed, http.StatusForbidden, codeRegistrationClosed, "registration is closed"},
	{accounts.ErrHumanCheck, http.StatusUnprocessableEntity, codeHumanCheck, "the human check did not pass"},
	{accounts.ErrEmailDomainNotAllowed, http.StatusUnprocessableEntity, codeEmailDomain, "the email domain is not allowed"},
	{accounts.ErrInvalidVerification, http.StatusUnprocessableEntity, codeInvalidVerification, "invalid or expired verification link"},
	{accounts.ErrWeakPassword, http.StatusUnprocessableEntity, codeWeakPassword, "the password must be 12 to 256 characters"},
	{accounts.ErrBadEmail, http.StatusUnprocessableEntity, codeUnprocessable, "not an email address"},
	{accounts.ErrEmailTaken, http.StatusConflict, codeEmailTaken, "the email is already registered"},
	{accounts.ErrTOTPOn, http.StatusConflict, codeConflict, "two-step verification is already on"},
	{accounts.ErrTOTPOff, http.StatusConflict, codeConflict, "two-step verification is not on"},
	{accounts.ErrTooManyKeys, http.StatusConflict, codeTooManyKeys, "the account may not make another key"},
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
	if errors.Is(err, observability.ErrBadCursor) || errors.Is(err, billing.ErrBadStatementCursor) || errors.Is(err, page.ErrBadCursor) {
		return invalidField("cursor", "not a cursor this API gave out")
	}
	if errors.Is(err, accounts.ErrBadSecret) {
		return invalidField("/secret", "not a TOTP secret this API gave out")
	}
	for _, r := range slices.Concat(refusals, adminRefusals, orderRefusals) {
		if !errors.Is(err, r.target) {
			continue
		}
		apiErr := &apiError{status: r.status, code: r.code, message: r.message}
		if tooMany, ok := errors.AsType[*accounts.TooManyAttempts](err); ok {
			apiErr.retryAfter = retrySeconds(tooMany.RetryAfter)
		}
		if wait, ok := errors.AsType[*payments.Wait](err); ok {
			apiErr.retryAfter = retrySeconds(wait.After)
		}
		if keys, ok := errors.AsType[*accounts.TooManyKeys](err); ok {
			tooManyKeys(apiErr, keys)
		}
		return apiErr
	}
	return nil
}

// retrySeconds is a wait as the whole seconds a Retry-After header holds, at least one.
func retrySeconds(wait time.Duration) int {
	return max(1, int((wait+time.Second-1)/time.Second))
}

// tooManyKeys says which limit a refused key met: the keys the account holds is a 409 that deleting a key lifts, and
// the keys it made in the last hour a 429 that waiting lifts.
func tooManyKeys(apiErr *apiError, keys *accounts.TooManyKeys) {
	if keys.RetryAfter > 0 {
		apiErr.status, apiErr.code, apiErr.retryAfter = http.StatusTooManyRequests, codeRateLimited, retrySeconds(keys.RetryAfter)
		apiErr.message = fmt.Sprintf("the account made %d keys in the last hour, and the site allows %d", keys.Count, keys.Limit)
		return
	}
	apiErr.message = fmt.Sprintf("the account holds %d keys, and the site allows %d", keys.Count, keys.Limit)
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
