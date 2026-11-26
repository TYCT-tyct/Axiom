package main

import (
	"net/http"

	"axiom/internal/quality"
)

// adminQualityRefusals are how the admin API answers the quality module's refusals.
var adminQualityRefusals = []refusal{
	{quality.ErrClassNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{quality.ErrClassExists, http.StatusConflict, codeConflict, "the slug is already taken or was retired; choose another"},
	{quality.ErrClassStale, http.StatusConflict, codeStaleRevision, "the source class changed since it was read"},
	{quality.ErrClassSystem, http.StatusConflict, codeConflict, "a seeded source class cannot be retired"},
}
