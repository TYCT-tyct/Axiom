package main

// The body of every failure, and its codes, as the guard and the handlers of both APIs write them. The console's and
// the admin's contracts define Error, ErrorDetail, ErrorField and ErrorCode alike (internal/platform/README.md「接口约定」:
// the definitions they share are identical, which `just api` checks), so one set of types serves both, and
// TestErrorCodesAreTheContractsCodes holds the codes to the contracts' lists.

// errorCode is one of the closed list of failure codes a client branches on and translates.
type errorCode string

const (
	codeInvalidRequest        errorCode = "invalid_request"
	codeUnauthenticated       errorCode = "unauthenticated"
	codeWrongLogin            errorCode = "invalid_credentials"
	codeInvalidCode           errorCode = "invalid_code"
	codeForbidden             errorCode = "forbidden"
	codeMfaRequired           errorCode = "mfa_required"
	codeConsentRequired       errorCode = "consent_required"
	codeOriginRejected        errorCode = "origin_rejected"
	codeNotFound              errorCode = "not_found"
	codeConflict              errorCode = "conflict"
	codeLoginDomainLocked     errorCode = "login_domain_locked"
	codeLoginDomainRequired   errorCode = "login_domain_required"
	codeIdentityTaken         errorCode = "identity_taken"
	codeIdentityAlreadyLinked errorCode = "identity_already_linked"
	codeAccountNotLinked      errorCode = "account_not_linked"
	codeEmailTaken            errorCode = "email_taken"
	codeNameTaken             errorCode = "name_taken"
	codeTooManyKeys           errorCode = "too_many_keys"
	codeStaleRevision         errorCode = "stale_revision"
	codeIdempotencyReuse      errorCode = "idempotency_key_reused"
	codeInvalidInvite         errorCode = "invalid_invite"
	codeRegistrationClosed    errorCode = "registration_closed"
	codeHumanCheck            errorCode = "human_check_failed"
	codeEmailDomain           errorCode = "email_domain_not_allowed"
	codeInvalidVerification   errorCode = "invalid_verification"
	codeWeakPassword          errorCode = "weak_password"
	codeNotReady              errorCode = "not_ready"
	codeUnprocessable         errorCode = "unprocessable"
	codeCapExceeded           errorCode = "cap_exceeded"
	codeRedeemInvalid         errorCode = "redeem_code_invalid"
	codeRedeemExpired         errorCode = "redeem_code_expired"
	codeRedeemDisabled        errorCode = "redeem_code_disabled"
	codeRedeemUsed            errorCode = "redeem_code_used"
	codeRateLimited           errorCode = "rate_limited"
	codeInternal              errorCode = "internal"
	codeNotImplemented        errorCode = "not_implemented"
	codeUnavailable           errorCode = "unavailable"
	codePresetNotSelectable   errorCode = "preset_not_selectable"
	codePresetDefaultInvalid  errorCode = "preset_default_invalid"
	codePresetInUse           errorCode = "preset_in_use"
	codePresetQuota           errorCode = "preset_quota"
	codePresetsSuspended      errorCode = "presets_suspended"
)

// errorField is one part of a request that broke a constraint: a JSON pointer into the body, or the name of a query
// parameter. It never holds the value.
type errorField struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

type errorDetail struct {
	Code    errorCode     `json:"code"`
	Message string        `json:"message"`
	Fields  *[]errorField `json:"fields,omitempty"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}
