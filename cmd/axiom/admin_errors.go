package main

import (
	"errors"
	"net/http"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/idem"
	"axiom/internal/suppliers"
)

// errNotImplemented is the answer of an operation of the admin's contract that has no handler yet (tools/pendingapi).
var errNotImplemented = errors.New("not implemented")

// adminRefusals are the refusals the admin API's modules make beyond those the console's do (refusals), tried after
// them. A message is English for logs and developers and never repeats what the client sent.
var adminRefusals = []refusal{
	{errNotImplemented, http.StatusNotImplemented, codeNotImplemented, "this operation is not implemented yet"},
	{idem.ErrReused, http.StatusConflict, codeIdempotencyReuse, "the Idempotency-Key was first used for another request"},

	{catalog.ErrOpKeyReused, http.StatusConflict, codeIdempotencyReuse, "the Idempotency-Key was first used for another change"},
	{catalog.ErrStaleRev, http.StatusConflict, codeStaleRevision, "the model changed since it was read"},
	{catalog.ErrPriceChanged, http.StatusConflict, codeStaleRevision, "the model's current price is not the one the request was decided on"},
	{catalog.ErrNameTaken, http.StatusConflict, codeNameTaken, "the name, normalized, is already taken"},
	{catalog.ErrWrongStatus, http.StatusConflict, codeConflict, "the model's status does not allow this"},
	{catalog.ErrSourceMismatch, http.StatusConflict, codeConflict, "the model's price source is not the request's, or the model is retired"},
	{catalog.ErrModelNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{catalog.ErrAuthorMissing, http.StatusUnprocessableEntity, codeUnprocessable, "the author does not exist"},
	{catalog.ErrNotReady, http.StatusUnprocessableEntity, codeNotReady, "the model is not ready for this"},
	{catalog.ErrIncomplete, http.StatusUnprocessableEntity, codeNotReady, "the schedule lacks prices for what the model declares"},
	{catalog.ErrPriceLost, http.StatusUnprocessableEntity, codeUnprocessable, "the new schedule has no price where the current one has"},
	{catalog.ErrTierBasisChanged, http.StatusUnprocessableEntity, codeUnprocessable, "the thresholds' basis differs from the current price's: confirm the change of basis"},
	{catalog.ErrInvalid, http.StatusUnprocessableEntity, codeUnprocessable, "the spec or the schedule breaks a rule of the catalog"},
	{catalog.ErrNameLength, http.StatusUnprocessableEntity, codeUnprocessable, "a name is 1 to 200 bytes"},
	{catalog.ErrNoEvidence, http.StatusUnprocessableEntity, codeUnprocessable, "evidence, a non-empty object, is required"},
	{catalog.ErrNoPrices, http.StatusUnprocessableEntity, codeUnprocessable, "one price per currency, and at least one, is required"},

	{billing.ErrRuleNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{billing.ErrRuleExists, http.StatusConflict, codeConflict, "the site has a pricing rule for that target and audience"},
	{billing.ErrStaleRule, http.StatusConflict, codeStaleRevision, "the pricing rule changed since it was read"},

	{suppliers.ErrNoSupplier, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNoGroup, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrSupplierExists, http.StatusConflict, codeConflict, "that origin and account are already connected"},
	{suppliers.ErrStatusLocked, http.StatusConflict, codeConflict, "the supplier's status does not allow this"},
	{suppliers.ErrGroupGone, http.StatusConflict, codeConflict, "the supplier no longer lists that group"},
	{suppliers.ErrKeyBound, http.StatusConflict, codeConflict, "that key is already bound"},
	{suppliers.ErrAliasExists, http.StatusConflict, codeConflict, "that name already has an alias in that scope"},
	{suppliers.ErrNoAdapter, http.StatusUnprocessableEntity, codeUnprocessable, "no adapter of that kind that can be connected"},
	{suppliers.ErrAddressRefused, http.StatusUnprocessableEntity, codeUnprocessable, "the outbound client refuses that address"},
	{suppliers.ErrUnreachable, http.StatusUnprocessableEntity, codeUnprocessable, "the site did not answer"},
	{suppliers.ErrGroupNotSelected, http.StatusUnprocessableEntity, codeUnprocessable, "the group is not selected"},
	{suppliers.ErrUnitValue, http.StatusUnprocessableEntity, codeUnprocessable, "a unit value is above 0 and at most 1000, with at most 15 decimals"},
}

// unprocessable is a request that is well formed but that a rule refuses; message says which.
func unprocessable(message string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: message}
}
