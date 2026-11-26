package main

import (
	"net/http"

	"axiom/internal/payments"
)

// adminPaymentsRefusals are how the admin API answers the payments module's refusals.
var adminPaymentsRefusals = []refusal{
	{payments.ErrInvestigated, http.StatusConflict, codeConflict, "this record was already investigated; read its saved note"},
	{payments.ErrStale, http.StatusConflict, codeStaleRevision, "the merchant account changed since it was read"},
	{payments.ErrTampered, http.StatusConflict, codeConflict, "the merchant account's settings do not match its sealed secrets; enter the secrets again"},
}
