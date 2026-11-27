package main

import (
	"fmt"
	"net/http"

	"axiom/internal/suppliers"
)

// adminSuppliersRefusals are how the admin API answers the suppliers module's refusals.
var adminSuppliersRefusals = []refusal{
	{suppliers.ErrNoSupplier, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNoGroup, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNoCredential, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNoSupplierEndpoint, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrSupplierExists, http.StatusConflict, codeConflict, "that origin and account are already connected"},
	{suppliers.ErrEgressUnavailable, http.StatusConflict, codeConflict, "the egress proxy is disabled and has no active fallback"},
	{suppliers.ErrStatusLocked, http.StatusConflict, codeConflict, "the supplier's status does not allow this"},
	{suppliers.ErrGroupGone, http.StatusConflict, codeConflict, "the supplier no longer lists that group"},
	{suppliers.ErrKeyBound, http.StatusConflict, codeConflict, "that key is already bound"},
	{suppliers.ErrAliasRevision, http.StatusConflict, codeStaleRevision, "the alias changed since it was read"},
	{suppliers.ErrAliasExists, http.StatusConflict, codeConflict, "that name already has an alias in that scope"},
	{suppliers.ErrNotPending, http.StatusConflict, codeConflict, "the group does not list that name as waiting for a model"},
	{suppliers.ErrModelTaken, http.StatusConflict, codeConflict, "the group already has a name for that model"},
	{suppliers.ErrUnknownFormat, http.StatusUnprocessableEntity, codeUnprocessable, "a call format is not one this node knows"},
	{suppliers.ErrNoAdapter, http.StatusUnprocessableEntity, codeUnprocessable, "no adapter of that kind that can be connected"},
	{suppliers.ErrAddressRefused, http.StatusUnprocessableEntity, codeUnprocessable, "the outbound client refuses that address"},
	{suppliers.ErrUnreachable, http.StatusUnprocessableEntity, codeUnprocessable, "the site did not answer"},
	{suppliers.ErrGroupNotSelected, http.StatusUnprocessableEntity, codeUnprocessable, "the group is not selected"},
	{suppliers.ErrUnitValue, http.StatusUnprocessableEntity, codeUnprocessable, "a unit value is above 0 and at most 1000, with at most 15 decimals"},
	{suppliers.ErrUnitValueFixed, http.StatusConflict, codeConflict, "only a relay's unit value is confirmed: the supplier is billed in money"},
	{suppliers.ErrNoEndpoint, http.StatusConflict, codeConflict, "the endpoint cannot be called now: it is not in this node's snapshot"},
	{suppliers.ErrNoSlot, http.StatusConflict, codeConflict, "no credential of the endpoint has a slot free now"},
	{suppliers.ErrTestOwner, http.StatusUnprocessableEntity, codeUnprocessable, "only platform endpoints may be tested"},
	{suppliers.ErrTestCost, http.StatusUnprocessableEntity, codeUnprocessable, "the endpoint has no complete price for a test generation"},
	{suppliers.ErrNoFormat, http.StatusUnprocessableEntity, codeUnprocessable, "the endpoint does not serve that call format"},
	{suppliers.ErrLimits, http.StatusUnprocessableEntity, codeUnprocessable, fmt.Sprintf("concurrency is 1 to 2147483647, and requests a minute 1 to %d or none", suppliers.MaxRequestsPerMinute)},
	{suppliers.ErrCannotMakeKey, http.StatusUnprocessableEntity, codeUnprocessable, "the supplier's adapter cannot make keys"},
	{suppliers.ErrCannotFindKey, http.StatusUnprocessableEntity, codeUnprocessable, "the supplier's adapter cannot tell which group a key is in"},
	{suppliers.ErrSupplierRefused, http.StatusUnprocessableEntity, codeUnprocessable, "the supplier refused Axiom's login; replace it"},
	{suppliers.ErrSupplierUnanswered, http.StatusServiceUnavailable, codeUnavailable, "the supplier did not answer; try again shortly"},
	{suppliers.ErrGroupHasKey, http.StatusConflict, codeConflict, "the group already has a key: pause or resume it by selecting it"},
	{suppliers.ErrTooManyCredentials, http.StatusUnprocessableEntity, codeUnprocessable, fmt.Sprintf("more than %d credentials to test: test them by supplier", suppliers.MaxCredentialTests)},
	{suppliers.ErrIntakeRule, http.StatusUnprocessableEntity, codeUnprocessable, "names takes include and exclude patterns, and only names does"},
	{suppliers.ErrIntakeRevision, http.StatusConflict, codeStaleRevision, "the intake rule changed since it was read"},
	{suppliers.ErrNotSynced, http.StatusConflict, codeConflict, "a direct API or a cloud supplier is not synced: its models come from its keys"},
	{suppliers.ErrNoInboxItem, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNoModelName, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrNameNotMapped, http.StatusUnprocessableEntity, codeUnprocessable, "the name maps to no model yet: map it first"},
	{suppliers.ErrNoUnitValue, http.StatusConflict, codeConflict, "confirm the supplier's unit value first"},
	{suppliers.ErrNoOfficialPrice, http.StatusUnprocessableEntity, codeUnprocessable, "a model has no official price in the settlement currency: give one"},
	{suppliers.ErrBasePrice, http.StatusUnprocessableEntity, codeUnprocessable, "a base price is in the settlement currency, charges something, and stays within a unit price in the supplier's unit"},
	{suppliers.ErrNoEgressProxy, http.StatusNotFound, codeNotFound, "no such resource"},
	{suppliers.ErrEgressProxyExists, http.StatusConflict, codeConflict, "an egress proxy has that id"},
	{suppliers.ErrEgressInUse, http.StatusConflict, codeConflict, "credentials or another proxy's fallback use the egress proxy"},
	{suppliers.ErrEgressProxy, http.StatusUnprocessableEntity, codeUnprocessable, "an egress proxy is http, https or socks5 at a host and port, with a username and password of at most 255 bytes"},
	{suppliers.ErrEgressFallback, http.StatusUnprocessableEntity, codeUnprocessable, "a fallback is another proxy without a fallback, set on a proxy that is no proxy's fallback"},
	{suppliers.ErrUnknownEgress, http.StatusUnprocessableEntity, codeUnprocessable, "no egress proxy has that id"},
}
