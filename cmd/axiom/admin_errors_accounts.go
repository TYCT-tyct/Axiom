package main

import (
	"net/http"

	"axiom/internal/accounts"
)

// adminAccountsRefusals are how the admin API answers the accounts module's refusals.
var adminAccountsRefusals = []refusal{
	{accounts.ErrUnknownAgent, http.StatusUnauthorized, codeUnauthenticated, "no live operations credential"},
	{accounts.ErrAgentNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
}
