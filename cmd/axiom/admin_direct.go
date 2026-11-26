package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// CreateDirectSupplier connects a provider's own API with pasted keys (docs/domains/suppliers.md「直连接口」). The keys
// are checked before the idempotency transaction, which must not hold a connection while a provider answers; a replay
// answers from its claim before any key is sent anywhere again. The keys are not in the fingerprint, only how many
// there are and their last four characters, which the answer shows anyway.
func (a adminAPI) CreateDirectSupplier(ctx context.Context, request adminapi.CreateDirectSupplierRequestObject) (adminapi.CreateDirectSupplierResponseObject, error) {
	r, err := directRequest(request.Body)
	if err != nil {
		return nil, err
	}
	claim, err := idempotencyClaim(ctx, "createDirectSupplier", request.Params.IdempotencyKey, directIdentity(r))
	if err != nil {
		return nil, err
	}
	if body, found, err := replayed[adminapi.SupplierConnected](ctx, a.db, claim); err != nil || found {
		return directAnswer(body, true), err
	}
	plan, err := a.planDirect(ctx, r)
	if err != nil {
		// The first request under this key may have committed between the replay check and the checks.
		if body, found, replayErr := replayed[adminapi.SupplierConnected](ctx, a.db, claim); replayErr == nil && found {
			return directAnswer(body, true), nil
		}
		return nil, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	body, replay, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.SupplierConnected, error) {
		made, err := a.suppliers.CreateDirect(ctx, tx, actor(ctx), snap, plan)
		return connectedBody(made), err
	})
	if err != nil {
		return nil, err
	}
	return directAnswer(body, replay), nil
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
func directRequest(b *adminapi.DirectSupplierCreate) (suppliers.DirectRequest, error) {
	r := suppliers.DirectRequest{
		Kind: string(b.Provider), Name: b.Name, MaxConcurrency: int32(b.MaxConcurrency), Reason: b.Reason, //nolint:gosec // the schema bounds it to 2³¹-1
	}
	if b.Keys != nil {
		r.Keys = *b.Keys
	}
	if b.Origin != nil {
		origin, err := suppliers.NormalizeOrigin(*b.Origin)
		if err != nil {
			return r, supplierProblem(err)
		}
		r.Origin = origin
	}
	if b.Format != nil {
		r.Family = string(*b.Format)
	}
	if b.SettlementCurrency != nil {
		currency, err := platform.ParseCurrency(*b.SettlementCurrency)
		if err != nil {
			return r, invalidField("/settlement_currency", "not a currency code")
		}
		r.Currency = currency
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
	return []any{r.Kind, r.Name, r.Origin, r.Family, r.Currency.String(), tails, r.MaxConcurrency, r.RequestsPerMinute, r.Reason}
}

// directAnswer is 201 when a supplier was connected and 200 when no key passed.
func directAnswer(body adminapi.SupplierConnected, replay bool) adminapi.CreateDirectSupplierResponseObject {
	if body.Supplier == nil {
		return adminapi.CreateDirectSupplier200JSONResponse{Body: body, Headers: adminapi.CreateDirectSupplier200ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}
	}
	return adminapi.CreateDirectSupplier201JSONResponse{Body: body, Headers: adminapi.CreateDirectSupplier201ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}
}

// connectedBody is the API's view of what the keys came to. A key is never in it, only its last four characters.
func connectedBody(c suppliers.Connected) adminapi.SupplierConnected {
	out := adminapi.SupplierConnected{Checks: make([]adminapi.CredentialCheck, len(c.Checks))}
	if c.Supplier != nil {
		s := supplierBody(*c.Supplier)
		out.Supplier = &s
	}
	for i, k := range c.Checks {
		check := adminapi.CredentialCheck{Index: &k.Index, Last4: &k.Last4, Outcome: adminapi.CredentialCheckOutcome(k.Outcome)}
		if k.Cause != "" {
			cause := adminapi.CredentialCheckCause(k.Cause)
			check.Cause = &cause
		}
		if k.Credential != (uuid.UUID{}) {
			id := k.Credential.String()
			check.CredentialId = &id
		}
		if k.Outcome != suppliers.KeyDuplicate { // a duplicate was not sent, so nothing answered it
			check.UpstreamStatus, check.CheckedAt = &k.Status, &k.CheckedAt
		}
		out.Checks[i] = check
	}
	return out
}

// directProblem maps the suppliers module's refusals of a direct API onto the contract, pointing at the member to fix.
func directProblem(err error) error {
	if keyErr, ok := errors.AsType[*suppliers.KeyError](err); ok {
		return invalidField("/keys/"+strconv.Itoa(keyErr.Index), "not a key a provider would accept")
	}
	for _, m := range []struct {
		err             error
		bad             bool // 400 rather than 422
		pointer, reason string
	}{
		{suppliers.ErrDirectProvider, false, "/provider", "this node has no adapter for that provider"},
		{suppliers.ErrDirectOrigin, true, "/origin", "only compatible takes an origin, and it requires one"},
		{suppliers.ErrDirectFormat, true, "/format", "only compatible takes a format, and it requires one"},
		{suppliers.ErrDirectGemini, false, "/format", "Gemini's own format is not served yet; openai and anthropic are"},
		{suppliers.ErrDirectCurrency, true, "/settlement_currency", "required for compatible"},
		{suppliers.ErrDirectName, true, "/name", "1 to 200 characters"},
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
