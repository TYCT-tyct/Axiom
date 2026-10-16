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
