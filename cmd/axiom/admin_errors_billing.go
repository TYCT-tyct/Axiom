package main

import (
	"net/http"

	"axiom/internal/billing"
)

// adminBillingRefusals are how the admin API answers the billing module's refusals.
var adminBillingRefusals = []refusal{
	{billing.ErrRuleNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{billing.ErrRuleExists, http.StatusConflict, codeConflict, "the site has a pricing rule for that target and audience"},
	{billing.ErrStalePrice, http.StatusConflict, codeStaleRevision, "the price proposal or policy changed since it was read"},
	{billing.ErrStaleRule, http.StatusConflict, codeStaleRevision, "the pricing rule changed since it was read"},
	{billing.ErrStaleExchange, http.StatusConflict, codeStaleRevision, "the exchange rate changed since it was read"},
}
