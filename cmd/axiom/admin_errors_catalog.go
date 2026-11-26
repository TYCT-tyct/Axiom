package main

import (
	"net/http"

	"axiom/internal/catalog"
)

// adminCatalogRefusals are how the admin API answers the catalog module's refusals.
var adminCatalogRefusals = []refusal{
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
	{catalog.ErrFileParse, http.StatusUnprocessableEntity, codeUnprocessable, "the file_parse item waits for a release every node can read"},
	{catalog.ErrPriceLost, http.StatusUnprocessableEntity, codeUnprocessable, "the new schedule has no price where the current one has"},
	{catalog.ErrTierBasisChanged, http.StatusUnprocessableEntity, codeUnprocessable, "the thresholds' basis differs from the current price's: confirm the change of basis"},
	{catalog.ErrInvalid, http.StatusUnprocessableEntity, codeUnprocessable, "the spec or the schedule breaks a rule of the catalog"},
	{catalog.ErrNameLength, http.StatusUnprocessableEntity, codeUnprocessable, "a name is 1 to 200 bytes"},
	{catalog.ErrNoEvidence, http.StatusUnprocessableEntity, codeUnprocessable, "evidence, a non-empty object, is required"},
	{catalog.ErrNoPrices, http.StatusUnprocessableEntity, codeUnprocessable, "one price per currency, and at least one, is required"},
}
