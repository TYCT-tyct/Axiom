package main

import (
	"errors"
	"net/http"

	"axiom/internal/platform/idem"
)

// errNotImplemented is the answer of an operation of the admin's contract that has no handler yet (tools/pendingapi).
var errNotImplemented = errors.New("not implemented")

// adminRefusals are the refusals the admin API's modules make beyond those the console's do (refusals), tried after
// them.
var adminRefusals = []refusal{
	{errNotImplemented, http.StatusNotImplemented, codeNotImplemented, "this operation is not implemented yet"},
	{idem.ErrReused, http.StatusConflict, codeIdempotencyReuse, "the Idempotency-Key was first used for another request"},
}

// unprocessable is a request that is well formed but that a rule refuses; message says which.
func unprocessable(message string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: message}
}
