package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// createDirect connects a provider's own API with pasted keys (docs/domains/suppliers-direct.md「接入」). The keys are
// checked before the idempotency transaction, which must not hold a connection while a provider answers; a replay
// answers from its claim before any key is sent anywhere again. The keys are not in the fingerprint, only how many
// there are and their last four characters, which the answer shows anyway.
func (a adminAPI) createDirect(ctx context.Context, key, kind string, b *adminapi.TypedSupplierCreate) (adminapi.SupplierConnected, bool, error) {
	r, err := directRequest(kind, b)
	if err != nil {
		return adminapi.SupplierConnected{}, false, err
	}
	claim, err := idempotencyClaim(ctx, "createTypedSupplier", key, directIdentity(r))
	if err != nil {
		return adminapi.SupplierConnected{}, false, err
	}
	if body, found, err := replayed[adminapi.SupplierConnected](ctx, a.db, claim); err != nil || found {
		return body, true, err
	}
	plan, err := a.planDirect(ctx, r)
	if err != nil {
		// The first request under this key may have committed between the replay check and the checks.
		if body, found, replayErr := replayed[adminapi.SupplierConnected](ctx, a.db, claim); replayErr == nil && found {
			return body, true, nil
		}
		return adminapi.SupplierConnected{}, false, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return adminapi.SupplierConnected{}, false, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	return idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.SupplierConnected, error) {
		made, err := a.suppliers.CreateDirect(ctx, tx, actor(ctx), snap, plan)
		return connectedBody(made, true), err
	})
}

// planDirect checks the request against the provider and the keys already stored, then has the provider check each
// key.
func (a adminAPI) planDirect(ctx context.Context, r suppliers.DirectRequest) (*suppliers.DirectPlan, error) {
	var plan *suppliers.DirectPlan
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		plan, err = a.suppliers.PlanDirect(ctx, tx, r)
		return err
	}); err != nil {
		return nil, directProblem(err)
	}
	if err := a.checkSettlementCurrency(plan.Currency()); err != nil {
		return nil, err
	}
	if err := a.suppliers.CheckDirect(ctx, plan); err != nil {
		return nil, directProblem(err)
	}
	return plan, nil
}

// directRequest reads the body; what the contract's schema already refuses is not checked again.
func directRequest(kind string, b *adminapi.TypedSupplierCreate) (suppliers.DirectRequest, error) {
	r := suppliers.DirectRequest{
		Kind: kind, Name: b.Name, Fields: fieldsOf(b.Fields), MaxConcurrency: int32(b.MaxConcurrency), Reason: b.Reason, //nolint:gosec // the schema bounds it to 2³¹-1
		Ratio: textValue(b.CostRatio), EgressID: textValue(b.EgressId),
	}
	switch {
	case b.Regions != nil:
		return r, invalidField("/regions", "a direct API has no regions")
	case b.Deployments != nil:
		return r, invalidField("/deployments", "a direct API has no deployments")
	case b.Form != nil && *b.Form != "api_key":
		return r, invalidField("/form", "a direct API's credential is its keys")
	}
	var err error
	if r.Entry, r.Origin, r.Currency, err = typedEntry(b); err != nil {
		return r, err
	}
	if b.Keys != nil {
		r.Keys = *b.Keys
	}
	if b.Format != nil {
		r.Family = string(*b.Format)
	}
	if b.RequestsPerMinute != nil {
		rpm := int32(*b.RequestsPerMinute) //nolint:gosec // the schema bounds it to 100000
		r.RequestsPerMinute = &rpm
	}
	return r, nil
}

// directIdentity is what a retry under the same key is compared by: everything but the keys' texts.
func directIdentity(r suppliers.DirectRequest) []any {
	tails := make([]string, len(r.Keys))
	for i, k := range r.Keys {
		tails[i] = suppliers.Last4(k)
	}
	return []any{r.Kind, r.Name, r.Entry, r.Origin, r.Family, r.Currency.String(), r.Fields, tails, r.MaxConcurrency, r.RequestsPerMinute, r.Ratio, r.EgressID, r.Reason}
}

// connectedBody is the API's view of what the keys, or a cloud credential's regions, came to: a pasted key's check has
// its place in the request. A key is never in it, only its last four characters.
func connectedBody(c suppliers.Connected, pasted bool) adminapi.SupplierConnected {
	out := adminapi.SupplierConnected{Checks: make([]adminapi.CredentialCheck, len(c.Checks))}
	if c.Supplier != nil {
		s := supplierBody(*c.Supplier)
		out.Supplier = &s
	}
	for i, k := range c.Checks {
		out.Checks[i] = keyCheckBody(k, pasted)
	}
	return out
}

// directProblem maps the suppliers module's refusals of a direct API onto the contract, pointing at the member to fix.
func directProblem(err error) error {
	if keyErr, ok := errors.AsType[*suppliers.KeyError](err); ok {
		return invalidField("/keys/"+strconv.Itoa(keyErr.Index), "not a key a provider would accept")
	}
	if mapped := typedProblem(err); mapped != nil {
		return mapped
	}
	for _, m := range []struct {
		err             error
		bad             bool // 400 rather than 422
		pointer, reason string
	}{
		{suppliers.ErrDirectFormat, true, "/format", "only compatible takes a format, and it requires one of its families"},
		{suppliers.ErrDirectGemini, false, "/format", "Gemini's own format is not served yet; openai and anthropic are"},
		{suppliers.ErrDirectCurrency, true, "/settlement_currency", "required for compatible"},
		{suppliers.ErrBalanceCurrency, false, "/entry", "a key's balance is in another currency than the entry's prices; connect it at the entry of its currency"},
	} {
		switch {
		case !errors.Is(err, m.err):
		case m.bad:
			return invalidField(m.pointer, m.reason)
		default:
			return unprocessableFields(m.pointer, m.reason)
		}
	}
	return supplierProblem(err)
}
