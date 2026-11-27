package main

import (
	"net/http"
	"slices"

	"axiom/internal/platform/idem"
)

// adminRefusals are the refusals the admin API's modules make beyond those the console's do (refusals), tried after
// them; each module's are in a file of its own (admin_errors_<module>.go). A message is English for logs and developers
// and never repeats what the client sent.
var adminRefusals = slices.Concat(
	adminAccountsRefusals,
	adminObservabilityRefusals,
	adminPaymentsRefusals,
	adminOperationsRefusals,
	[]refusal{{idem.ErrReused, http.StatusConflict, codeIdempotencyReuse, "the Idempotency-Key was first used for another request"}},
	adminQualityRefusals,
	adminCatalogRefusals,
	adminBillingRefusals,
	adminSuppliersRefusals,
)

// unprocessable is a request that is well formed but that a rule refuses; message says which.
func unprocessable(message string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: message}
}

// unprocessableFields is unprocessable with the members the request should change: pointer is the JSON Pointer of the
// one that fixes it, as the contract's errors name fields.
func unprocessableFields(pointer, message string) *apiError {
	e := unprocessable("the request breaks a rule of the admin API")
	e.fields = []errorField{{Pointer: pointer, Message: message}}
	return e
}
