package main

import (
	"net/http"

	"axiom/internal/operations"
)

// adminOperationsRefusals are how the admin API answers the operations module's refusals.
var adminOperationsRefusals = []refusal{
	{operations.ErrStaleRevision, http.StatusConflict, codeStaleRevision, "the targets changed since they were read"},
	{operations.ErrKeyReused, http.StatusConflict, codeIdempotencyReuse, "this Idempotency-Key was used for another request"},
	{operations.ErrNoOperation, http.StatusNotFound, codeNotFound, "no such resource"},
	{operations.ErrNoApproval, http.StatusNotFound, codeNotFound, "no such resource"},
	{operations.ErrNotPending, http.StatusConflict, codeConflict, "the approval was already decided, withdrawn or has expired"},
	{operations.ErrParamsTooLarge, http.StatusUnprocessableEntity, codeUnprocessable, "the request is larger than an operation keeps (8 KiB of parameters)"},
	{operations.ErrNoCapPolicy, http.StatusUnprocessableEntity, codeCapExceeded, "no cap policy covers this money, so it moves none"},
	{operations.ErrPolicyChanged, http.StatusConflict, codeStaleRevision, "the cap policy changed since it was read"},
	{operations.ErrCurrencyFixed, http.StatusConflict, codeConflict, "a cap policy keeps the currency it was made with"},
	{operations.ErrLimitsChanged, http.StatusConflict, codeStaleRevision, "the agent's limits changed since they were read"},
	{operations.ErrBadLimit, http.StatusBadRequest, codeInvalidRequest, "a limit is from 1 to its ceiling"},
	{operations.ErrBadPolicy, http.StatusBadRequest, codeInvalidRequest, "each limit is at least 0 and each hard limit at least its autonomous limit"},
	{operations.ErrNoPriceEnvelope, http.StatusUnprocessableEntity, codeCapExceeded, "no price envelope covers this site, so the agent changes none of its prices"},
	{operations.ErrEnvelopeChanged, http.StatusConflict, codeStaleRevision, "the price envelope changed since it was read"},
	{operations.ErrBadEnvelope, http.StatusBadRequest, codeInvalidRequest, "the margin is 0 to 10000 basis points, the daily move at most 100000000 and each limit at least 0"},
	{operations.ErrWindowEnded, http.StatusConflict, codeConflict, "the UTC day ended while the proposal waited; propose it again"},
	{operations.ErrNotReserved, http.StatusConflict, codeConflict, "the proposal holds no reservation now: it waits for approval, was confirmed or released, or its day ended"},
}
