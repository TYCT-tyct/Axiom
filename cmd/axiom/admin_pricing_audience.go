package main

import (
	"context"
	"errors"
	"slices"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func (a adminAPI) checkRuleAudience(ctx context.Context, tx pg.Tx, site accounts.Site, in billing.RuleInput) error {
	switch in.Audience.Kind {
	case "tier":
		return a.checkTier(ctx, tx, site, in.Audience.ID, "/rule/audience/id")
	case "account":
		id, err := accounts.ParseAccountID(in.Audience.ID)
		if err != nil {
			return err
		}
		account, err := a.accounts.Account(ctx, tx, id)
		if errors.Is(err, accounts.ErrNotFound) {
			return unprocessableFields("/rule/audience/id", "is not an account of this site")
		}
		if err != nil {
			return err
		}
		if account.Site != site.ID {
			return unprocessableFields("/rule/audience/id", "is not an account of this site")
		}
	}
	return nil
}

// checkTier validates membership for rule and preview subjects.
func (a adminAPI) checkTier(ctx context.Context, tx pg.Tx, site accounts.Site, id, pointer string) error {
	tiers, err := a.accounts.Tiers(ctx, tx, site.ID)
	if err == nil && !slices.ContainsFunc(tiers, func(t accounts.Tier) bool { return t.ID.TypeID() == id }) {
		return unprocessableFields(pointer, "is not a tier of this site")
	}
	return err
}
