package main

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API's sale price writes (docs/domains/operations.md「售价的累计变动上限」): the agent publishes a
// proposal the automatic publisher made, as the admin's confirmation does. How far it moves the model's prices and
// what margin it keeps are the server's, from the proposal and the current version, never the agent's; within the
// site's price envelope it publishes at once, otherwise the owner approves it.

// PricesList reads the main site's sale prices and proposals as the admin does.
func (o operationsAPI) PricesList(ctx context.Context, _ operationsapi.PricesListRequestObject) (operationsapi.PricesListResponseObject, error) {
	answer, err := o.admin.ListPricePublications(ctx, adminapi.ListPricePublicationsRequestObject{})
	return relayed[operationsapi.PricesList200JSONResponse, adminapi.ListPricePublications200JSONResponse](answer, err)
}

// PricePublish publishes a proposal of the main site within its price envelope, or asks the owner to.
func (o operationsAPI) PricePublish(ctx context.Context, r operationsapi.PricePublishRequestObject) (operationsapi.PricePublishResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	c, b := callOf(ctx), r.Body
	params := map[string]any{"candidate_id": b.CandidateId}
	if b.CurrentId != nil {
		params["current_id"] = *b.CurrentId
	}
	pub, err := publicationOf(r.ModelId, params)
	if err != nil {
		return nil, err
	}
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: "price_publish", Target: operations.Target{Kind: "model", ID: r.ModelId},
		Params: params, Risk: operations.RiskPrice, Reason: b.Reason, Run: runOf(r.Params.XAxiomRunID),
	}
	op, replayed, err := o.runWrite(ctx, b.DryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		return o.publishPrice(ctx, tx, pub, p)
	})
	if err != nil {
		return nil, err
	}
	headers := operationsapi.PricePublish200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.PricePublish202JSONResponse{Body: operationBody(op), Headers: operationsapi.PricePublish202ResponseHeaders(headers)}, nil
	}
	return operationsapi.PricePublish200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// publication is the proposal of a model's price an operation publishes, read against the version that was current.
type publication struct {
	model     catalog.ModelID
	candidate uuid.UUID
	current   *uuid.UUID
}

// publicationOf reads a publication from an operation's target and parameters, which are also what is approved.
func publicationOf(model string, params map[string]any) (publication, error) {
	id, err := modelIDOf(model)
	if err != nil {
		return publication{}, err
	}
	pub := publication{model: id}
	text, _ := params["candidate_id"].(string)
	if pub.candidate, err = uuid.Parse(text); err != nil {
		return publication{}, invalidField("/candidate_id", "not a UUID")
	}
	if text, ok := params["current_id"].(string); ok {
		current, err := uuid.Parse(text)
		if err != nil {
			return publication{}, invalidField("/current_id", "not a UUID")
		}
		pub.current = &current
	}
	return pub, nil
}

// publishPrice proposes p with the credential held: one within the envelope is published and settled in tx at once.
func (o operationsAPI) publishPrice(ctx context.Context, tx pg.Tx, pub publication, p operations.Proposal) (operations.Operation, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, false, err
	}
	// A retry of this key sent alongside the first request waits for it on the site's price policy and then finds its
	// operation here, whatever the publisher has proposed since; only a first request is weighed.
	if err := o.holdPrices(ctx, tx); err != nil {
		return operations.Operation{}, false, err
	}
	if op, found, err := operations.Replay(ctx, tx, p); found || err != nil {
		return op, found, err
	}
	site, r, err := o.admin.repricing(ctx, tx, pub)
	if err != nil {
		return operations.Operation{}, false, err
	}
	op, replayed, err := operations.ProposePrice(ctx, tx, p, r)
	if err != nil || replayed || op.State != operations.StateApproved {
		return op, replayed, err
	}
	result, err := publish(ctx, tx, site, pub, op, r)
	if err != nil {
		return operations.Operation{}, false, err
	}
	op, err = operations.Settle(ctx, tx, op, result, "")
	return op, false, err
}

// holdPrices locks the main site's price policy, which serializes every publication of its prices, for the caller's
// credential: the prices are the main site's, and a credential kept to other sites does not reach them.
func (o operationsAPI) holdPrices(ctx context.Context, tx pg.Tx) error {
	site, ok := o.admin.accounts.MainSite()
	if !ok {
		return accounts.ErrNotFound
	}
	if sites := callOf(ctx).agent.Info().Sites; sites != nil && !slices.Contains(sites, site.ID) {
		return accounts.ErrAgentNotFound
	}
	_, err := billing.LockPricePolicy(ctx, tx, uuid.UUID(site.ID))
	return err
}

// repricing refreshes the main site's proposals, as the admin's confirmation does, and reads how far publishing pub
// would move the model's prices. A proposal or head that is no longer the model's is billing.ErrStalePrice; one that
// is already published, or has no safe price to publish, is refused.
func (a adminAPI) repricing(ctx context.Context, tx pg.Tx, pub publication) (accounts.Site, operations.Repricing, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return site, operations.Repricing{}, accounts.ErrNotFound
	}
	if err := refreshSalePrices(ctx, tx, uuid.UUID(site.ID), site.Currency); err != nil {
		return site, operations.Repricing{}, err
	}
	_, prices, err := billing.PriceState(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return site, operations.Repricing{}, err
	}
	at := slices.IndexFunc(prices, func(p billing.Publication) bool { return p.Model == pub.model && p.Candidate == pub.candidate })
	if at < 0 || !sameVersion(prices[at].Current, pub.current) {
		return site, operations.Repricing{}, billing.ErrStalePrice
	}
	p := prices[at]
	switch {
	case p.Status == "published":
		return site, operations.Repricing{}, &apiError{status: http.StatusConflict, code: codeConflict, message: "the proposal is the model's published price already"}
	case len(p.Schedule) == 0:
		return site, operations.Repricing{}, &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the proposal has no safe price to publish"}
	}
	change, margin, err := billing.PriceMove(p)
	if err != nil {
		return site, operations.Repricing{}, err
	}
	summary, err := a.priceSummary(ctx, tx, p, site.Currency.String())
	if err != nil {
		return site, operations.Repricing{}, err
	}
	return site, operations.Repricing{Site: uuid.UUID(site.ID), Model: uuid.UUID(p.Model), Change: change, Margin: margin, Summary: summary}, nil
}

// priceSummary is what the owner is shown of publishing p: the model by name, and the base price of each billing item
// now, as proposed and as it costs, in the site's currency per unit (basePrices).
func (a adminAPI) priceSummary(ctx context.Context, tx pg.Tx, p billing.Publication, currency string) (map[string]any, error) {
	model, err := a.readModelIn(ctx, tx, p.Model)
	if err != nil {
		return nil, err
	}
	summary := map[string]any{
		"model_id": p.Model.String(), "candidate_id": p.Candidate.String(), "status": p.Status,
		"audience": map[string]any{"kind": p.Audience.Kind, "id": p.Audience.ID},
		"slug":     model.Slug, "currency": currency,
	}
	if i := slices.IndexFunc(model.Names, func(n adminapi.ModelName) bool { return n.Visibility == adminapi.ModelNameVisibilityPublic }); i >= 0 {
		summary["name"] = model.Names[i].Name
	}
	if p.Current != nil {
		summary["current_id"] = p.Current.String()
	}
	prices, complete, err := basePrices(p)
	if err != nil {
		return nil, err
	}
	maps.Copy(summary, prices)
	summary["prices_complete"] = complete
	return summary, nil
}

// basePrices is the base rate of each item now (`current`), as proposed (`proposed`) and as it costs (`cost`). Only the
// base rates are kept, not the context or service tiers nor each endpoint's own price, so the summary stays well within
// its 8 KiB; complete says whether they are every price either side charges. When it is false, equal base rates prove
// nothing: an endpoint's price or a tier's can move under an unchanged model envelope.
func basePrices(p billing.Publication) (map[string]any, bool, error) {
	prices := map[string]any{}
	complete := p.Endpoints == nil && p.CurrentEndpoints == nil
	for key, raw := range map[string]jsonx.Raw{"current": p.CurrentSchedule, "proposed": p.Schedule, "cost": p.Cost} {
		if len(raw) == 0 {
			continue
		}
		var schedule struct {
			Rates        map[string]string `json:"rates"`
			ContextTiers jsonx.Raw         `json:"context_tiers"`
			ServiceTiers jsonx.Raw         `json:"service_tiers"`
		}
		if err := jsonx.Unmarshal(raw, &schedule); err != nil {
			return nil, false, fmt.Errorf("read the %s price of model %s: %w", key, p.Model, err)
		}
		prices[key] = schedule.Rates
		if key != "cost" && (len(schedule.ContextTiers) > 0 || len(schedule.ServiceTiers) > 0) {
			complete = false
		}
	}
	return prices, complete, nil
}

func sameVersion(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// publish publishes pub as the agent of op, never below the floor, and answers the version with r's move and margin.
func publish(ctx context.Context, tx pg.Tx, site accounts.Site, pub publication, op operations.Operation, r operations.Repricing) (map[string]any, error) {
	version, err := billing.PublishPrice(ctx, tx, audit.OperationsAgent(op.Credential), uuid.UUID(site.ID), pub.model, pub.candidate, pub.current, true, false, op.Reason)
	if err != nil {
		return nil, pricingProblem(err, "/")
	}
	result := r.Detail()
	result["version"] = version.String()
	return result, nil
}

// runPricePublish publishes the proposal of op, which the owner approved, and counts it in the day's envelope.
func (a adminAPI) runPricePublish(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	var params map[string]any
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	pub, err := publicationOf(op.Target.ID, params)
	if err != nil {
		return nil, err
	}
	site, r, err := a.repricing(ctx, tx, pub)
	if err != nil {
		return nil, err
	}
	if err := operations.CountPrice(ctx, tx, r); err != nil {
		return nil, err
	}
	return publish(ctx, tx, site, pub, op, r)
}
