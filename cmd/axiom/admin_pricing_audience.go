package main

import (
	"context"
	"slices"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func (a adminAPI) checkRuleAudience(ctx context.Context, tx pg.Tx, site accounts.Site, in billing.RuleInput) error {
	switch in.Audience.Kind {
	case "tier":
		return a.checkTier(ctx, tx, site, in.Audience.ID)
	case "account":
		id, err := accounts.ParseAccountID(in.Audience.ID)
		if err != nil {
			return err
		}
		account, err := a.accounts.Account(ctx, tx, id)
		if err != nil {
			return err
		}
		if account.Site != site.ID {
			return unprocessableFields("/rule/audience/id", "is not an account of this site")
		}
	}
	return nil
}

// checkTier refuses a tier the site lacks, for a preview of what that tier pays.
func (a adminAPI) checkTier(ctx context.Context, tx pg.Tx, site accounts.Site, id string) error {
	tiers, err := a.accounts.Tiers(ctx, tx, site.ID)
	if err == nil && !slices.ContainsFunc(tiers, func(t accounts.Tier) bool { return t.ID.TypeID() == id }) {
		return unprocessable("the preview names a tier the site lacks")
	}
	return err
}

func (a adminAPI) checkRuleEdit(ctx context.Context, tx pg.Tx, site accounts.Site, id uuid.UUID, in billing.RuleInput) error {
	rules, err := billing.Rules(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return err
	}
	at := slices.IndexFunc(rules, func(r billing.Rule) bool { return r.ID == id })
	if at < 0 {
		return billing.ErrRuleNotFound
	}
	if err := billing.CheckRuleUpdate(rules[at], in, site.Currency); err != nil {
		return ruleProblem(err)
	}
	if !in.Enabled {
		return nil
	}
	if err := a.checkSubjects(ctx, in); err != nil {
		return err
	}
	return a.checkRuleAudience(ctx, tx, site, in)
}
