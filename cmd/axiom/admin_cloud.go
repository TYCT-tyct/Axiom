package main

import (
	"context"
	"errors"
	"net/http"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// Cloud platforms reached with their credential (docs/domains/suppliers.md「云平台」). The credential is sealed on arrival
// and is never in an answer, an idempotency fingerprint or an audit entry: the fingerprint holds what is not secret
// about it, which the answer shows anyway. Each region is checked before the idempotency transaction, which must not
// hold a connection while a platform answers; a replay answers from its claim before anything is sent again.

// newRegionConcurrency is the calls a region added by a replacement has at once when the request does not say.
const newRegionConcurrency = 4

// createCloud connects a cloud platform with its credential.
func (a adminAPI) createCloud(ctx context.Context, key, kind string, b *adminapi.TypedSupplierCreate) (adminapi.SupplierConnected, bool, error) {
	r, err := cloudRequest(kind, b)
	if err != nil {
		return adminapi.SupplierConnected{}, false, err
	}
	var plan *suppliers.CloudPlan
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		plan, err = a.suppliers.PlanCloud(ctx, tx, r)
		return err
	}); err != nil {
		return adminapi.SupplierConnected{}, false, cloudProblem(err)
	}
	claim, err := idempotencyClaim(ctx, "createTypedSupplier", key, []any{
		r.Kind, r.Name, r.Entry, r.Origin, r.Form, r.Regions, r.Deployments, r.Currency.String(), plan.Settings(),
		r.Limits.MaxConcurrency, r.Limits.RequestsPerMinute, r.Reason, plan.Account(), plan.Checks[0].Last4,
	})
	if err != nil {
		return adminapi.SupplierConnected{}, false, err
	}
	if err := a.checkSettlementCurrency(plan.Currency()); err != nil {
		return adminapi.SupplierConnected{}, false, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return adminapi.SupplierConnected{}, false, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	body, replay, err := probed(ctx, a, claim, func(ctx context.Context) error {
		return cloudProblem(a.suppliers.CheckCloud(ctx, plan))
	}, func(tx pg.Tx) (adminapi.SupplierConnected, error) {
		made, err := a.suppliers.CreateCloud(ctx, tx, actor(ctx), snap, plan)
		return connectedBody(made, false), err
	})
	if err == nil && body.Supplier != nil {
		a.leaveRouting(ctx, replay)
	}
	return body, replay, err
}

// ReplaceSupplierCloudCredential checks a new credential in the regions given, or those the supplier serves from now,
// and puts it in place of the old one when it passes in any.
func (a adminAPI) ReplaceSupplierCloudCredential(ctx context.Context, request adminapi.ReplaceSupplierCloudCredentialRequestObject) (adminapi.ReplaceSupplierCloudCredentialResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	if b.Fields == nil {
		return nil, invalidField("/fields", "required")
	}
	r := suppliers.CloudRequest{
		Form: textValue(b.Form), Fields: *b.Fields, Reason: b.Reason,
		Limits: suppliers.Limits{MaxConcurrency: newRegionConcurrency, RequestsPerMinute: b.RequestsPerMinute},
	}
	if b.Regions != nil {
		r.Regions = *b.Regions
	}
	if b.MaxConcurrency != nil {
		r.Limits.MaxConcurrency = *b.MaxConcurrency
	}
	var plan *suppliers.CloudPlan
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		plan, err = a.suppliers.PlanCloudReplace(ctx, tx, supplier, r)
		return err
	}); err != nil {
		return nil, cloudProblem(err)
	}
	claim, err := idempotencyClaim(ctx, "replaceSupplierCloudCredential", request.Params.IdempotencyKey,
		[]any{request.SupplierId, r.Form, r.Regions, plan.Settings(), r.Limits.MaxConcurrency, r.Limits.RequestsPerMinute, r.Reason, plan.Account(), plan.Checks[0].Last4})
	if err != nil {
		return nil, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	body, replay, err := probed(ctx, a, claim, func(ctx context.Context) error {
		return cloudProblem(a.suppliers.CheckCloud(ctx, plan))
	}, func(tx pg.Tx) (adminapi.SupplierConnected, error) {
		changed, err := a.suppliers.ReplaceCloud(ctx, tx, actor(ctx), snap, plan)
		return connectedBody(changed, false), cloudProblem(err)
	})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replay || body.Supplier == nil)
	return adminapi.ReplaceSupplierCloudCredential200JSONResponse{Body: body, Headers: adminapi.ReplaceSupplierCloudCredential200ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}, nil
}

// cloudRequest reads the body; what the contract's schema already refuses is not checked again.
func cloudRequest(kind string, b *adminapi.TypedSupplierCreate) (suppliers.CloudRequest, error) {
	r := suppliers.CloudRequest{
		Kind: kind, Name: b.Name, Form: textValue(b.Form), Fields: fieldsOf(b.Fields), Reason: b.Reason,
		Limits: suppliers.Limits{MaxConcurrency: b.MaxConcurrency, RequestsPerMinute: b.RequestsPerMinute},
	}
	switch {
	case b.Keys != nil:
		return r, invalidField("/keys", "a cloud platform's credential is its fields")
	case b.Format != nil:
		return r, invalidField("/format", "a cloud platform's call formats are its own")
	}
	var err error
	if r.Entry, r.Origin, r.Currency, err = typedEntry(b); err != nil {
		return r, err
	}
	if b.Regions != nil {
		r.Regions = *b.Regions
	}
	if b.Deployments != nil {
		for _, d := range *b.Deployments {
			r.Deployments = append(r.Deployments, suppliers.Deployment{Name: d.Name, Model: textValue(d.Model)})
		}
	}
	return r, nil
}

// cloudProblem maps the suppliers module's refusals of a cloud platform onto the contract, pointing at the member to
// fix.
func cloudProblem(err error) error {
	if mapped := typedProblem(err); mapped != nil {
		return mapped
	}
	switch {
	case errors.Is(err, suppliers.ErrCloudRegions):
		return invalidField("/regions", "vertex and bedrock require regions, and azure takes none")
	case errors.Is(err, suppliers.ErrCloudDeployments):
		return invalidField("/deployments", "azure requires its deployments, and the others take none")
	case errors.Is(err, suppliers.ErrDirectName):
		return invalidField("/name", "1 to 200 characters")
	case errors.Is(err, suppliers.ErrNotCloud):
		return &apiError{status: http.StatusConflict, code: codeConflict, message: "the supplier is not a cloud platform's"}
	}
	return supplierProblem(err)
}
