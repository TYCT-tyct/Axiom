package main

import (
	"context"
	"errors"
	"net/http"

	"axiom/internal/platform"
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

// CreateCloudSupplier connects Vertex AI, Bedrock or Azure OpenAI with its credential.
func (a adminAPI) CreateCloudSupplier(ctx context.Context, request adminapi.CreateCloudSupplierRequestObject) (adminapi.CreateCloudSupplierResponseObject, error) {
	r, err := cloudRequest(request.Body)
	if err != nil {
		return nil, err
	}
	var plan *suppliers.CloudPlan
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		plan, err = a.suppliers.PlanCloud(ctx, tx, r)
		return err
	}); err != nil {
		return nil, cloudProblem(err)
	}
	claim, err := idempotencyClaim(ctx, "createCloudSupplier", request.Params.IdempotencyKey,
		[]any{r.Kind, r.Name, r.Origin, r.Regions, r.Deployments, r.Currency.String(), r.Limits.MaxConcurrency, r.Limits.RequestsPerMinute, r.Reason, plan.Account(), plan.Checks[0].Last4})
	if err != nil {
		return nil, err
	}
	if err := a.checkSettlementCurrency(plan.Currency()); err != nil {
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
		made, err := a.suppliers.CreateCloud(ctx, tx, actor(ctx), snap, plan)
		return connectedBody(made, false), err
	})
	if err != nil {
		return nil, err
	}
	if body.Supplier == nil {
		return adminapi.CreateCloudSupplier200JSONResponse{Body: body, Headers: adminapi.CreateCloudSupplier200ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}, nil
	}
	a.leaveRouting(ctx, replay)
	return adminapi.CreateCloudSupplier201JSONResponse{Body: body, Headers: adminapi.CreateCloudSupplier201ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}, nil
}

// ReplaceSupplierCloudCredential checks a new credential in the regions given, or those the supplier serves from now,
// and puts it in place of the old one when it passes in any.
func (a adminAPI) ReplaceSupplierCloudCredential(ctx context.Context, request adminapi.ReplaceSupplierCloudCredentialRequestObject) (adminapi.ReplaceSupplierCloudCredentialResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	if b.Credential == nil {
		return nil, invalidField("/credential", "required")
	}
	r := suppliers.CloudRequest{
		Input: cloudInput(b.Credential), Reason: b.Reason,
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
		[]any{request.SupplierId, r.Regions, r.Limits.MaxConcurrency, r.Limits.RequestsPerMinute, r.Reason, plan.Account(), plan.Checks[0].Last4})
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
func cloudRequest(b *adminapi.CloudSupplierCreate) (suppliers.CloudRequest, error) {
	r := suppliers.CloudRequest{
		Kind: string(b.Provider), Name: b.Name, Reason: b.Reason,
		Limits: suppliers.Limits{MaxConcurrency: b.MaxConcurrency, RequestsPerMinute: b.RequestsPerMinute},
	}
	if b.Credential == nil {
		return r, invalidField("/credential", "required")
	}
	r.Input = cloudInput(b.Credential)
	if b.Regions != nil {
		r.Regions = *b.Regions
	}
	if b.Deployments != nil {
		for _, d := range *b.Deployments {
			r.Deployments = append(r.Deployments, suppliers.Deployment{Name: d.Name, Model: textValue(d.Model)})
		}
	}
	if b.Origin != nil {
		origin, err := suppliers.NormalizeOrigin(*b.Origin)
		if err != nil {
			return r, supplierProblem(err)
		}
		r.Origin = origin
	}
	if b.SettlementCurrency != nil {
		currency, err := platform.ParseCurrency(*b.SettlementCurrency)
		if err != nil {
			return r, invalidField("/settlement_currency", "not a currency code")
		}
		r.Currency = currency
	}
	return r, nil
}

// cloudInput is the credential's members as the module reads them; an absent member is empty.
func cloudInput(c *adminapi.CloudCredentialInput) suppliers.CloudInput {
	return suppliers.CloudInput{
		ServiceAccountJSON: textValue(c.ServiceAccountJson), AccessKeyID: textValue(c.AccessKeyId),
		SecretAccessKey: textValue(c.SecretAccessKey), APIKey: textValue(c.ApiKey),
	}
}

// cloudProblem maps the suppliers module's refusals of a cloud platform onto the contract, pointing at the member to
// fix.
func cloudProblem(err error) error {
	if credErr, ok := errors.AsType[*suppliers.CredentialError](err); ok {
		return invalidField("/credential/"+credErr.Member, credErr.Reason)
	}
	switch {
	case errors.Is(err, suppliers.ErrCloudProvider):
		return unprocessableFields("/provider", "this node has no adapter for that platform")
	case errors.Is(err, suppliers.ErrCloudOrigin):
		return invalidField("/origin", "only azure takes an origin, and it requires one")
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
