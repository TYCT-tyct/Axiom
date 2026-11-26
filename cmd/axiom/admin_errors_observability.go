package main

import (
	"net/http"

	"axiom/internal/observability"
)

// adminObservabilityRefusals are how the admin API answers the observability module's refusals.
var adminObservabilityRefusals = []refusal{
	{observability.ErrAlertRotationWindow, http.StatusConflict, codeConflict, "the preceding signing secret is still valid; wait until the one-hour overlap ends before rotating again"},
	{observability.ErrAlertConfiguration, http.StatusUnprocessableEntity, codeUnprocessable, "the alert configuration is invalid"},
	{observability.ErrRetention, http.StatusUnprocessableEntity, codeUnprocessable, "the hot window is 0 to 3650 days"},
	{observability.ErrAlertRevision, http.StatusConflict, codeStaleRevision, "the alert configuration changed since it was read"},
}
