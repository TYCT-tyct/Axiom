package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
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
func ruleProblem(err error) error {
	if e, ok := errors.AsType[*billing.RuleError](err); ok {
		return &apiError{
			status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the rule breaks a rule of pricing rules",
			fields: []errorField{{Pointer: "/rule/" + e.Field, Message: e.Reason}},
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

// checkSubjects refuses a rule for a tier or an account the main site lacks, or a model or an author the catalog lacks:
// an ID with a typo would price nothing, and quietly.
func (a adminAPI) checkSubjects(ctx context.Context, site accounts.Site, in billing.RuleInput) error {
	if err := a.checkAudience(ctx, site, in.Audience); err != nil {
		return err
	}
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

// checkAudience refuses a rule for a tier or an account the site lacks.
func (a adminAPI) checkAudience(ctx context.Context, site accounts.Site, audience billing.Selector) error {
	return a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		switch audience.Kind {
		case "tier":
			return a.checkTier(ctx, tx, site, audience.ID)
		case "account":
			return a.checkAccount(ctx, tx, site, audience.ID)
		}
		return nil
	})
}

func (a adminAPI) checkTier(ctx context.Context, tx pg.Tx, site accounts.Site, id string) error {
	tiers, err := a.accounts.Tiers(ctx, tx, site.ID)
	if err == nil && !slices.ContainsFunc(tiers, func(t accounts.Tier) bool { return t.ID.TypeID() == id }) {
		return unprocessable("the rule names a tier the site lacks")
	}
	return err
}

func (a adminAPI) checkAccount(ctx context.Context, tx pg.Tx, site accounts.Site, id string) error {
	parsed, err := accounts.ParseAccountID(id)
	if err != nil {
		return unprocessable("the rule names an account the site lacks")
	}
	account, err := a.accounts.Account(ctx, tx, parsed)
	if errors.Is(err, accounts.ErrNotFound) || err == nil && account.Site != site.ID {
		return unprocessable("the rule names an account the site lacks")
	}
	return err
}

// PreviewPricing shows what models would sell for, with the main site's rules or with one added or replaced for the
// preview alone. Nothing is stored.
//
// M1 prices from the reference price, so a row carries the sale price and no cost: what an endpoint's cost is comes with
// routing's endpoints in M2 (m1-plan.md §4.2), and until then `cost_unknown` and `below_floor` are statuses nothing
// produces. A model no rule prices is `unpriced`, never sold at its reference price
// (docs/domains/billing.md「定价」).
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
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, err
	}
	sold, err := billing.SalePrices(rules, snap, site.Currency)
	if err != nil {
		return nil, err
	}
	rows := adminapi.PricingPreview{Data: make([]adminapi.PricingPreviewRow, 0, len(request.Body.ModelIds))}
	for _, id := range request.Body.ModelIds {
		model, err := modelIDOf(id)
		if err != nil {
			return nil, err
		}
		m, ok := snap.Model(model)
		if !ok {
			return nil, catalog.ErrModelNotFound
		}
		row := adminapi.PricingPreviewRow{ModelId: id, Slug: m.Slug, Status: adminapi.Unpriced}
		if s, ok := sold[model]; ok {
			row.Status, row.Sale = adminapi.Priced, scheduleBody(s.Schedule)
		}
		rows.Data = append(rows.Data, row)
	}
	return adminapi.PreviewPricing200JSONResponse(rows), nil
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
		if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error { return a.checkTier(ctx, tx, site, tier.TypeID()) }); err != nil {
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
	var rules []billing.Rule
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rules, err = billing.Rules(ctx, tx, uuid.UUID(site.ID))
		return err
	}); err != nil {
		return nil, err
	}
	if body.Rule == nil {
		if body.ReplacesRuleId != nil {
			return nil, invalidField("/replaces_rule_id", "is not without rule")
		}
		return rules, nil
	}
	in, err := ruleInputOf(*body.Rule)
	if err != nil {
		return nil, err
	}
	if err := in.Check(site.Currency); err != nil {
		return nil, ruleProblem(err)
	}
	if err := a.checkSubjects(ctx, site, in); err != nil {
		return nil, err
	}
	// The preview's rule is as specific as its target and audience make it, and has no ID of its own to sort by: it
	// goes after the rules it ties with, so that it never displaces one it is no more specific than.
	preview := billing.Rule{RuleInput: in}
	if body.ReplacesRuleId != nil {
		id, err := uuid.Parse(*body.ReplacesRuleId)
		if err != nil || id.String() != *body.ReplacesRuleId {
			return nil, invalidField("/replaces_rule_id", "not a rule's ID")
		}
		i := slices.IndexFunc(rules, func(r billing.Rule) bool { return r.ID == id })
		if i < 0 {
			return nil, billing.ErrRuleNotFound
		}
		rules[i] = preview
		return rules, nil
	}
	return billing.Place(preview, rules), nil
}

// scheduleBody is a schedule as the contract gives it, through the canonical form both sides read.
func scheduleBody(s catalog.Schedule) *adminapi.PriceSchedule {
	body, err := converted[adminapi.PriceSchedule](s)
	if err != nil {
		return nil // a schedule the contract's form cannot hold, which the catalog never makes one of
	}
	return &body
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
	if err := a.checkSubjects(ctx, site, in); err != nil {
		return nil, err
	}
	rule, replayed, err := idempotently(ctx, a.db, "createPricingRule", request.Params.IdempotencyKey, request.Body,
		func(tx pg.Tx) (adminapi.PricingRule, error) {
			made, err := billing.CreateRule(ctx, tx, actor(ctx), uuid.UUID(site.ID), site.Currency, in, request.Body.Reason)
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
	if err := in.Check(site.Currency); err != nil {
		return nil, ruleProblem(err)
	}
	if err := a.checkSubjects(ctx, site, in); err != nil {
		return nil, err
	}
	rule, replayed, err := idempotently(ctx, a.db, "updatePricingRule", request.Params.IdempotencyKey, []any{request.RuleId, request.Body},
		func(tx pg.Tx) (adminapi.PricingRule, error) {
			made, err := billing.UpdateRule(ctx, tx, actor(ctx), uuid.UUID(site.ID), site.Currency, id, request.Body.Rev, in, request.Body.Reason)
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
