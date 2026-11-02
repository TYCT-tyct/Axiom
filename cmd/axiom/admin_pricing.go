package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The pricing rules of M1 are the main site's (the contract says so); a second site's come with its owner's pricing.

// converted reads v, a member of one contract type, as the same member of another type with the same JSON: a rule's
// target, audience, method and constraints are written alike by the contract and by billing.
func converted[T any](v any) (T, error) {
	var out T
	encoded, err := jsonx.MarshalCanonical(v)
	if err != nil {
		return out, err
	}
	return out, jsonx.Unmarshal(encoded, &out)
}

// ruleInputOf is what a request decides about a rule, in billing's form.
func ruleInputOf(b adminapi.PricingRuleInput) (in billing.RuleInput, err error) {
	in.Enabled = b.Enabled
	if in.Target, err = converted[billing.Selector](b.Target); err != nil {
		return in, err
	}
	if in.Audience, err = converted[billing.Selector](b.Audience); err != nil {
		return in, err
	}
	if in.Method, err = converted[billing.Method](b.Method); err != nil {
		return in, err
	}
	in.Constraints, err = converted[billing.Constraints](b.Constraints)
	return in, err
}

// ruleBody is a rule as the contract gives it.
func ruleBody(r billing.Rule) (adminapi.PricingRule, error) {
	body := adminapi.PricingRule{Id: r.ID.String(), Rev: r.Rev, Enabled: r.Enabled, UpdatedAt: r.UpdatedAt}
	var err error
	if body.Target, err = converted[adminapi.PricingTarget](r.Target); err != nil {
		return body, err
	}
	if body.Audience, err = converted[adminapi.PricingAudience](r.Audience); err != nil {
		return body, err
	}
	if body.Method, err = converted[adminapi.PricingMethod](r.Method); err != nil {
		return body, err
	}
	body.Constraints, err = converted[adminapi.PricingConstraints](r.Constraints)
	return body, err
}

// ruleProblem is a rule that breaks a rule of rules as a 422 naming the member of the rule.
func ruleProblem(err error) error { return pricingProblem(err, "/rule/") }

func pricingProblem(err error, prefix string) error {
	if e, ok := errors.AsType[*billing.RuleError](err); ok {
		return &apiError{
			status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the rule breaks a rule of pricing rules",
			fields: []errorField{{Pointer: prefix + e.Field, Message: e.Reason}},
		}
	}
	return err
}

// ListPricingRules lists the main site's rules in the order they are tried.
func (a adminAPI) ListPricingRules(ctx context.Context, _ adminapi.ListPricingRulesRequestObject) (adminapi.ListPricingRulesResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	var rules []billing.Rule
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rules, err = billing.Rules(ctx, tx, uuid.UUID(site.ID))
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.PricingRuleList{Data: make([]adminapi.PricingRule, len(rules))}
	for i, r := range rules {
		var err error
		if list.Data[i], err = ruleBody(r); err != nil {
			return nil, err
		}
	}
	return adminapi.ListPricingRules200JSONResponse(list), nil
}

// checkSubjects refuses a rule for a model or an author the catalog lacks: an ID with a typo would price nothing, and
// quietly. Audience membership is checked in the same platform transaction as the write.
func (a adminAPI) checkSubjects(ctx context.Context, in billing.RuleInput) error {
	switch in.Target.Kind {
	case "model":
		id, err := modelIDOf(in.Target.ID)
		if err == nil {
			_, err = a.catalog.Model(ctx, id)
		}
		if errors.Is(err, catalog.ErrModelNotFound) {
			return unprocessable("the rule names a model the catalog lacks")
		}
		return err
	case "author":
		if _, err := a.catalog.GetAuthor(ctx, in.Target.ID); errors.Is(err, catalog.ErrAuthorMissing) {
			return unprocessable("the rule names an author the catalog lacks")
		} else if err != nil {
			return err
		}
	}
	return nil
}

// PreviewPricing shows what models would sell for, with the main site's rules or with one added or replaced for the
// preview alone. Nothing is stored.
//
// The preview uses the same rule, cost-envelope and floor calculation as publication, without changing a price.
func (a adminAPI) PreviewPricing(ctx context.Context, request adminapi.PreviewPricingRequestObject) (adminapi.PreviewPricingResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	audience, err := a.previewAudience(ctx, site, *request.Body)
	if err != nil {
		return nil, err
	}
	rules, err := a.previewRules(ctx, site, *request.Body)
	if err != nil {
		return nil, err
	}
	rules = slices.DeleteFunc(rules, func(r billing.Rule) bool {
		return !r.Audience.Prices(audience.account, audience.tier)
	})
	var snap *catalog.Snapshot
	models, err := modelIDsOf(request.Body.ModelIds)
	if err != nil {
		return nil, err
	}

	var proposals map[catalog.ModelID]billing.Publication
	var current map[catalog.ModelID]billing.Sold
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		snap, err = catalog.ReadSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		costs, rates, err := publicationCosts(ctx, tx, site.Currency)
		if err != nil {
			return err
		}
		proposals, err = billing.PreviewPrices(ctx, tx, uuid.UUID(site.ID), site.Currency, snap, rules, models, costs, rates...)
		if err != nil {
			return err
		}
		current, err = billing.PublishedPrices(ctx, tx, uuid.UUID(site.ID))
		return err
	})
	if err != nil {
		return nil, err
	}
	a.reportPriceProblems(proposals)
	return previewRows(*request.Body, snap, models, proposals, current, audience)
}

func previewRows(body adminapi.PricingPreviewRequest, snap *catalog.Snapshot, models []catalog.ModelID, proposals map[catalog.ModelID]billing.Publication, current map[catalog.ModelID]billing.Sold, audience audience) (adminapi.PreviewPricing200JSONResponse, error) {
	rows := adminapi.PricingPreview{Data: make([]adminapi.PricingPreviewRow, 0, len(models))}
	for i, id := range body.ModelIds {
		p := proposals[models[i]]
		if len(p.Endpoints) > 0 {
			endpoints, err := previewEndpointRows(id, snap, models[i], p, current[models[i]], audience, body.Rule == nil)
			if err != nil {
				return adminapi.PreviewPricing200JSONResponse{}, err
			}
			rows.Data = append(rows.Data, endpoints...)
			continue
		}
		row, err := previewRow(id, snap, models[i], proposals)
		if err != nil {
			return adminapi.PreviewPricing200JSONResponse{}, err
		}
		if err := previewCurrent(&row, current[models[i]], audience, body.Rule == nil, uuid.Nil()); err != nil {
			return adminapi.PreviewPricing200JSONResponse{}, err
		}
		rows.Data = append(rows.Data, row)
	}
	return adminapi.PreviewPricing200JSONResponse(rows), nil
}

// previewEndpointRows is one row per endpoint of a cost-plus proposal: each endpoint's own sale, cost and floor. The
// model-level row would hide exactly the difference endpoint pricing exists for. Endpoints sort by ID, so a page is
// stable across requests.
func previewEndpointRows(id string, snap *catalog.Snapshot, model catalog.ModelID, p billing.Publication, current billing.Sold, audience audience, serving bool) ([]adminapi.PricingPreviewRow, error) {
	m, ok := snap.Model(model)
	if !ok {
		return nil, catalog.ErrModelNotFound
	}
	endpoints := make([]uuid.UUID, 0, len(p.Endpoints))
	for endpoint := range p.Endpoints {
		endpoints = append(endpoints, endpoint)
	}
	slices.SortFunc(endpoints, func(a, b uuid.UUID) int { return a.Compare(b) })
	out := make([]adminapi.PricingPreviewRow, 0, len(endpoints))
	for _, endpoint := range endpoints {
		row := adminapi.PricingPreviewRow{ModelId: id, Slug: m.Slug, Status: adminapi.PricingPreviewRowStatusUnpriced}
		endpointID := endpoint.String()
		row.EndpointId = &endpointID
		sale, ok := p.Endpoints[endpoint]
		if !ok {
			out = append(out, row)
			continue
		}
		var err error
		row.Sale, err = scheduleBody(sale)
		if err != nil {
			return nil, err
		}
		if cost, ok := p.Costs[endpoint]; ok {
			body, err := scheduleBody(cost)
			if err != nil {
				return nil, err
			}
			row.Cost = body
			row.MarginRatio = marginOf(sale, cost)
		}
		requested, ok := p.RequestedEndpoints[endpoint]
		if !ok {
			requested = sale
		}
		if floor, ok := p.Floors[endpoint]; ok {
			row.Status, err = previewFloorStatusOf(requested, floor)
			if err != nil {
				return nil, err
			}
		} else {
			row.Status = adminapi.PricingPreviewRowStatusCostUnknown
		}
		if err := previewCurrent(&row, current, audience, serving, endpoint); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func previewCurrent(row *adminapi.PricingPreviewRow, price billing.Sold, audience audience, serving bool, endpoint uuid.UUID) error {
	price = price.For(billing.Audience{Account: audience.account, Tier: audience.tier})
	if price.Version == uuid.Nil() {
		return nil
	}
	schedule := price.Schedule
	if endpoint != uuid.Nil() {
		var ok bool
		if schedule, ok = price.PriceFor(endpoint); !ok {
			return nil // this version has no price for the endpoint: there is no current sale to show
		}
	}
	sale, err := scheduleBody(schedule)
	if err != nil {
		return err
	}
	version := price.Version.String()
	row.CurrentSale, row.CurrentVersion = sale, &version
	if serving {
		row.Sale = sale
	}
	return nil
}

// marginOf is the lowest margin of sale over cost across the items, service tiers and context bands both schedules
// price: (sale - cost) / sale, as the preview contract defines it. A rate the sale prices and the cost does not is a
// hundred percent margin, and a zero sale rate is skipped rather than divided by zero.
func marginOf(sale, cost catalog.Schedule) *string {
	type key struct {
		tier  catalog.ServiceTier
		above int64
		item  catalog.Item
	}
	costs := map[key]platform.UnitPrice{}
	for _, r := range cost.View().Rates {
		costs[key{r.ServiceTier, r.Above, r.Item}] = r.Price
	}
	var lowest *big.Rat
	for _, r := range sale.View().Rates {
		price := r.Price.Rat()
		if price.Sign() == 0 {
			continue
		}
		c := costs[key{r.ServiceTier, r.Above, r.Item}]
		margin := new(big.Rat).Sub(price, c.Rat())
		margin.Quo(margin, price)
		if lowest == nil || margin.Cmp(lowest) < 0 {
			lowest = margin
		}
	}
	if lowest == nil {
		return nil
	}
	text := strings.TrimRight(strings.TrimRight(lowest.FloatString(15), "0"), ".")
	if text == "" || text == "-" || text == "-0" {
		text = "0"
	}
	return &text
}

func (a adminAPI) reportPriceProblems(proposals map[catalog.ModelID]billing.Publication) {
	for id, p := range proposals {
		if p.Problem != "" {
			a.logger.Warn("a model has no sale price; it is not sold", "model", id.String(), "err", p.Problem)
		}
	}
}

func modelIDsOf(ids []string) ([]catalog.ModelID, error) {
	models := make([]catalog.ModelID, len(ids))
	for i, id := range ids {
		var err error
		if models[i], err = modelIDOf(id); err != nil {
			return nil, err
		}
	}
	return models, nil
}

// previewRow is the row of one model: priced when sold has its sale price, else unpriced.
func previewRow(id string, snap *catalog.Snapshot, model catalog.ModelID, proposals map[catalog.ModelID]billing.Publication) (adminapi.PricingPreviewRow, error) {
	m, ok := snap.Model(model)
	if !ok {
		return adminapi.PricingPreviewRow{}, catalog.ErrModelNotFound
	}
	row := adminapi.PricingPreviewRow{ModelId: id, Slug: m.Slug, Status: adminapi.PricingPreviewRowStatusUnpriced}
	p := proposals[model]
	if len(p.Requested) == 0 {
		return row, nil
	}
	raw := p.Schedule
	if len(raw) == 0 {
		raw = p.Requested
	}
	s, err := catalog.ParseSchedule(raw)
	if err != nil {
		return row, err
	}
	row.Sale, err = scheduleBody(s)
	if err != nil {
		return row, err
	}
	if len(p.Cost) > 0 {
		cost, err := converted[adminapi.PriceSchedule](p.Cost)
		if err != nil {
			return row, err
		}
		row.Cost = &cost
	}
	requested, err := catalog.ParseSchedule(p.Requested)
	if err != nil {
		return row, err
	}
	row.Status, err = previewFloorStatus(requested, p.Floor)
	if err != nil {
		return row, err
	}
	return row, nil
}

func previewFloorStatus(s catalog.Schedule, raw []byte) (adminapi.PricingPreviewRowStatus, error) {
	if len(raw) == 0 {
		return adminapi.PricingPreviewRowStatusCostUnknown, nil
	}
	floor, err := catalog.ParseSchedule(raw)
	if err != nil {
		return "", err
	}
	return previewFloorStatusOf(s, floor)
}

func previewFloorStatusOf(s, floor catalog.Schedule) (adminapi.PricingPreviewRowStatus, error) {
	safe, err := s.CoversCost(floor)
	if err != nil {
		return "", err
	}
	if !safe {
		return adminapi.PricingPreviewRowStatusBelowFloor, nil
	}
	return adminapi.PricingPreviewRowStatusPriced, nil
}

// previewAudience is whom the preview prices for: the account and its tier when the request names an account, else the
// tier it names, else the site's default tier, the tier a new account is put in (api/admin.yaml「PricingPreviewRequest」).
func (a adminAPI) previewAudience(ctx context.Context, site accounts.Site, body adminapi.PricingPreviewRequest) (audience, error) {
	var out audience
	if body.AccountId != nil && body.TierId != nil {
		return out, invalidField("/tier_id", "is not with account_id")
	}
	var tier accounts.TierID
	switch {
	case body.AccountId != nil:
		account, err := accounts.ParseAccountID(*body.AccountId)
		if err != nil {
			return out, invalidField("/account_id", "not an account's ID")
		}
		var detail accounts.AccountDetail
		if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
			detail, err = a.accounts.Account(ctx, tx, account)
			return err
		}); err != nil {
			return out, err
		}
		if detail.Site != site.ID {
			return out, invalidField("/account_id", "is not an account of this site")
		}
		out.account, tier = account, detail.Tier.ID
	case body.TierId != nil:
		parsed, err := accounts.ParseTierID(*body.TierId)
		if err != nil {
			return out, invalidField("/tier_id", "not a tier's ID")
		}
		tier = parsed
	default:
		tier = site.DefaultTier
	}
	if tier != (accounts.TierID{}) {
		if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error { return a.checkTier(ctx, tx, site, tier.TypeID(), "/tier_id") }); err != nil {
			return out, err
		}
	}
	out.tier = tier
	return out, nil
}

// audience is whom a preview prices for: one account and its tier, a tier alone, or the site's default tier alone.
type audience struct {
	account accounts.AccountID
	tier    accounts.TierID
}

// previewRules is the site's rules, with the request's rule added or put in place of replaces_rule_id.
func (a adminAPI) previewRules(ctx context.Context, site accounts.Site, body adminapi.PricingPreviewRequest) ([]billing.Rule, error) {
	rules, err := storedRules(ctx, a.db, site)
	if err != nil {
		return nil, err
	}
	return previewedRules(ctx, &a, site, rules, body)
}

// storedRules are the site's rules in the order they are tried.
func storedRules(ctx context.Context, db *pg.DB, site accounts.Site) ([]billing.Rule, error) {
	var rules []billing.Rule
	err := db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rules, err = billing.Rules(ctx, tx, uuid.UUID(site.ID))
		return err
	})
	return rules, err
}

// previewedRules is what storedRules read, with the request's rule among them, as a preview or as a rule about to be
// created would try them. A rule the site keeps for the same target and audience is one a second rule could not beat:
// it would shadow it, and the model would keep selling at the stored rule's price. So adding one is refused rather than
// shown, and the refusal is the one creating the rule gives: 409, the same change again, where a preview says 422 and
// names replaces_rule_id, the member that changes the stored rule instead.
func previewedRules(ctx context.Context, a *adminAPI, site accounts.Site, rules []billing.Rule, body adminapi.PricingPreviewRequest) ([]billing.Rule, error) {
	createsRule := body.ReplacesRuleId == nil
	if body.Rule == nil {
		if !createsRule {
			return nil, invalidField("/replaces_rule_id", "is not without rule")
		}
		return rules, nil
	}
	in, err := a.checkedRuleInput(ctx, site, *body.Rule)
	if err != nil {
		return nil, err
	}
	rest, err := withoutReplaced(rules, body.ReplacesRuleId)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(rest, func(r billing.Rule) bool { return r.Target == in.Target && r.Audience == in.Audience }) {
		if createsRule {
			return nil, billing.ErrRuleExists
		}
		return nil, unprocessableFields("/replaces_rule_id", "names the rule the site keeps for that target and audience")
	}
	// The preview's rule is as specific as its target and audience make it, and has no ID of its own to sort by: it
	// goes after the rules it ties with, so that it never displaces one it is no more specific than. It takes the place
	// of replaces_rule_id by its own target and audience, not by where that rule stood: a rule for a model put in place
	// of the one for everything prices that model, rather than being tried after every rule the site has.
	return billing.Place(billing.Rule{RuleInput: in}, rest), nil
}

// checkedRuleInput is the request's rule, once it passes what creating it would check.
func (a adminAPI) checkedRuleInput(ctx context.Context, site accounts.Site, rule adminapi.PricingRuleInput) (billing.RuleInput, error) {
	in, err := ruleInputOf(rule)
	if err != nil {
		return in, err
	}
	if err := in.Check(site.Currency); err != nil {
		return in, ruleProblem(err)
	}
	if err := in.CheckItems(a.catalog.AllowSchedule); err != nil {
		return in, ruleProblem(err)
	}
	if err := a.checkSubjects(ctx, in); err != nil {
		return in, err
	}
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error { return a.checkRuleAudience(ctx, tx, site, in) })
	return in, err
}

// withoutReplaced is rules less the one replaces names, or rules when it names none.
func withoutReplaced(rules []billing.Rule, replaces *string) ([]billing.Rule, error) {
	if replaces == nil {
		return rules, nil
	}
	id, err := uuid.Parse(*replaces)
	if err != nil || id.String() != *replaces {
		return nil, invalidField("/replaces_rule_id", "not a rule's ID")
	}
	i := slices.IndexFunc(rules, func(r billing.Rule) bool { return r.ID == id })
	if i < 0 {
		return nil, billing.ErrRuleNotFound
	}
	return slices.Delete(rules, i, i+1), nil
}

// scheduleBody is a schedule as the contract gives it, through the canonical form both sides read.
func scheduleBody(s catalog.Schedule) (*adminapi.PriceSchedule, error) {
	body, err := converted[adminapi.PriceSchedule](s)
	if err != nil {
		return nil, fmt.Errorf("billing: a sale price the admin contract cannot hold: %w", err)
	}
	return &body, nil
}

// CreatePricingRule adds a rule to the main site.
func (a adminAPI) CreatePricingRule(ctx context.Context, request adminapi.CreatePricingRuleRequestObject) (adminapi.CreatePricingRuleResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	in, err := ruleInputOf(request.Body.Rule)
	if err != nil {
		return nil, err
	}
	if err := in.Check(site.Currency); err != nil {
		return nil, ruleProblem(err)
	}
	if err := a.checkSubjects(ctx, in); err != nil {
		return nil, err
	}
	rule, replayed, err := idempotently(ctx, a.db, "createPricingRule", request.Params.IdempotencyKey, request.Body,
		func(tx pg.Tx) (adminapi.PricingRule, error) {
			if err := a.checkRuleAudience(ctx, tx, site, in); err != nil {
				return adminapi.PricingRule{}, err
			}
			made, err := billing.CreateRule(ctx, tx, actor(ctx), uuid.UUID(site.ID), site.Currency, in, a.catalog.AllowSchedule, request.Body.Reason)
			if err != nil {
				return adminapi.PricingRule{}, ruleProblem(err)
			}
			return ruleBody(made)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.CreatePricingRule201JSONResponse{Body: rule, Headers: adminapi.CreatePricingRule201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdatePricingRule replaces a rule of the main site as of the rev it was read at.
func (a adminAPI) UpdatePricingRule(ctx context.Context, request adminapi.UpdatePricingRuleRequestObject) (adminapi.UpdatePricingRuleResponseObject, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	id, err := uuid.Parse(request.RuleId)
	if err != nil || id.String() != request.RuleId {
		return nil, billing.ErrRuleNotFound
	}
	in, err := ruleInputOf(request.Body.Rule)
	if err != nil {
		return nil, err
	}
	rule, replayed, err := idempotently(ctx, a.db, "updatePricingRule", request.Params.IdempotencyKey, []any{request.RuleId, request.Body},
		func(tx pg.Tx) (adminapi.PricingRule, error) {
			made, err := billing.UpdateRule(ctx, tx, actor(ctx), uuid.UUID(site.ID), site.Currency, id, request.Body.Rev, in, a.catalog.AllowSchedule, request.Body.Reason, func() error {
				if err := a.checkSubjects(ctx, in); err != nil {
					return err
				}
				return a.checkRuleAudience(ctx, tx, site, in)
			})
			if err != nil {
				return adminapi.PricingRule{}, ruleProblem(err)
			}
			return ruleBody(made)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdatePricingRule200JSONResponse{Body: rule, Headers: adminapi.UpdatePricingRule200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
