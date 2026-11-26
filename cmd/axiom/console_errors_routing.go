package main

import (
	"net/http"

	"axiom/internal/routing"
)

// routingRefusals are how the routing module's refusals are answered.
var routingRefusals = []refusal{
	{routing.ErrDefaultInvalid, http.StatusConflict, codePresetDefaultInvalid, "the default routing preset of this workspace cannot be used; an administrator must change it"},
	{routing.ErrPresetNotSelectable, http.StatusBadRequest, codePresetNotSelectable, "the routing preset cannot be chosen here"},
	{routing.ErrPresetNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{routing.ErrPresetCurrency, http.StatusBadRequest, codeInvalidRequest, "a platform preset keeps the currency it was made with; make a new preset for prices in another currency"},
	{routing.ErrPresetReadOnly, http.StatusForbidden, codeForbidden, "the routing preset belongs to another layer"},
	{routing.ErrPresetSlugTaken, http.StatusConflict, codeNameTaken, "a routing preset of this layer has that slug"},
	{routing.ErrPresetStale, http.StatusConflict, codeStaleRevision, "the routing preset or default changed since it was read"},
	{routing.ErrPresetInUse, http.StatusConflict, codePresetInUse, "a default still points at the routing preset; change the default first"},
	{routing.ErrPresetQuota, http.StatusConflict, codePresetQuota, "over the limit of routing presets"},
	{routing.ErrPresetsSuspended, http.StatusServiceUnavailable, codePresetsSuspended, "the platform has suspended routing presets; try again once they are resumed"},
}
