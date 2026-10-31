package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// GetOperationsOverview answers the dashboard in one request: the site's calls and errors, what those calls were
// charged and cost, the cash the ledger received, and its busiest accounts and models (docs/domains/operations.md
// 「运营看板」). It reads one bounded window from the reporting indexes, never a table scan.
func (a adminAPI) GetOperationsOverview(ctx context.Context, r adminapi.GetOperationsOverviewRequestObject) (adminapi.GetOperationsOverviewResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	w, err := boundedCallWindow(observability.OwnQuery{}, r.Params.Since, r.Params.Until, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	call := callOf(ctx)
	site := uuid.UUID(call.site.ID)
	var overview observability.AdminOverview
	var money billing.PlatformReport
	err = a.db.ReadTx(ctx, call.scope, func(tx pg.Tx) error {
		if overview, err = observability.ReadAdminOverview(ctx, tx, site, w.Since, w.Until); err != nil {
			return err
		}
		money, err = billing.ReadPlatformReport(ctx, tx, site, call.site.Currency, w.Since, w.Until)
		return err
	})
	if err != nil {
		return nil, err
	}
	accountsBody := make([]map[string]any, len(overview.Accounts))
	for i, account := range overview.Accounts {
		accountsBody[i] = map[string]any{
			"account_id": accounts.AccountID(account.Account).TypeID(), "calls": account.Calls, "errors": account.Errors,
		}
	}
	modelsBody := make([]map[string]any, len(overview.Models))
	for i, model := range overview.Models {
		modelsBody[i] = map[string]any{"model": model.Model, "calls": model.Calls, "errors": model.Errors}
	}
	cashBody := make([]map[string]string, len(money.Cash))
	for i, cash := range money.Cash {
		cashBody[i] = map[string]string{"amount": cash.Amount, "currency": cash.Currency.String()}
	}
	body := map[string]any{
		"site_id": call.site.ID.TypeID(), "since": w.Since, "until": w.Until,
		"calls": overview.Calls, "errors": overview.Errors,
		"calls_incomplete":   !overview.Coverage.Covers(w.Since),
		"revenue":            adminapi.Money{Amount: money.Revenue, Currency: call.site.Currency.String()},
		"cost":               adminapi.Money{Amount: money.Cost, Currency: call.site.Currency.String()},
		"unknown_cost_calls": money.UnknownCost,
		"cash":               cashBody, "top_accounts": accountsBody, "top_models": modelsBody,
	}
	if overview.Coverage.Known {
		body["calls_available_since"] = overview.Coverage.Since
	}
	return adminJSON[adminapi.GetOperationsOverview200JSONResponse](body)
}
