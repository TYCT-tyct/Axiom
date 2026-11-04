package main

import (
	"context"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// GetOperationsOverview answers the dashboard in one request: the site's calls and errors, what those calls were
// charged and cost, the cash the ledger received, the paid top-ups that made accounts paying customers, the margin
// between call revenue and supplier cost, and the paying-account retention of the month the window ends in
// (docs/domains/operations.md「运营看板」). It reads one bounded window from the reporting indexes, never a table scan.
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
	var targets operations.Expected
	err = a.db.ReadTx(ctx, call.scope, func(tx pg.Tx) error {
		if overview, err = observability.ReadAdminOverview(ctx, tx, site, w.Since, w.Until); err != nil {
			return err
		}
		if money, err = billing.ReadPlatformReport(ctx, tx, site, call.site.Currency, w.Since, w.Until); err != nil {
			return err
		}
		targets, err = operations.ExpectedFor(ctx, tx, site, call.site.Currency, w.Since, w.Until)
		return err
	})
	if err != nil {
		return nil, err
	}
	accountsBody, modelsBody, cashBody := overviewRanks(overview, money)
	callsComplete := overview.Coverage.Covers(w.Since, w.Until)
	margin := grossMarginBasisPoints(money.Revenue, money.Cost, money.UnknownCost)
	retention := retentionBasisPoints(money.Retention.Retained, money.Retention.Cohort)
	retentionBody := map[string]any{
		"month": money.Retention.Month.Format("2006-01"), "cohort": money.Retention.Cohort,
		"retained": money.Retention.Retained,
	}
	if retention != nil {
		retentionBody["rate"] = *retention
	}
	body := map[string]any{
		"site_id": call.site.ID.TypeID(), "since": w.Since, "until": w.Until,
		"calls": overview.Calls, "errors": overview.Errors,
		"calls_incomplete":   !callsComplete,
		"revenue":            adminapi.Money{Amount: money.Revenue, Currency: call.site.Currency.String()},
		"cost":               adminapi.Money{Amount: money.Cost, Currency: call.site.Currency.String()},
		"unknown_cost_calls": money.UnknownCost,
		"paid_users":         money.PaidUsers,
		"monthly_revenue":    adminapi.Money{Amount: money.MonthlyRevenue, Currency: call.site.Currency.String()},
		"retention":          retentionBody,
		"cash":               cashBody, "top_accounts": accountsBody, "top_models": modelsBody,
		"targets": targetComparison(targets, targetActuals{
			Calls: overview.Calls, Errors: overview.Errors, Revenue: money.Revenue, Cost: money.Cost,
			PaidUsers: money.PaidUsers, MonthlyRevenue: money.MonthlyRevenue,
			GrossMargin: margin, Retention: retention, CallsComplete: callsComplete, CostComplete: money.UnknownCost == 0,
		}, call.site.Currency),
	}
	if margin != nil {
		body["gross_margin"] = *margin
	}
	if overview.Coverage.Known {
		body["calls_available_since"] = overview.Coverage.Since
	}
	return adminJSON[adminapi.GetOperationsOverview200JSONResponse](body)
}

// overviewRanks shapes the dashboard's top accounts, top models and cash rows.
func overviewRanks(overview observability.AdminOverview, money billing.PlatformReport) ([]map[string]any, []map[string]any, []map[string]string) {
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
	return accountsBody, modelsBody, cashBody
}

// grossMarginBasisPoints is (revenue - cost) / revenue in basis points, rounded half away from zero. It is nil when
// there is no settled revenue to divide by or when a call's supplier cost is unknown, because the cost total would then
// be incomplete and the rate would state more than the data knows.
func grossMarginBasisPoints(revenue, cost string, unknown int64) *int64 {
	if unknown != 0 {
		return nil
	}
	r, ok := new(big.Int).SetString(revenue, 10)
	if !ok || r.Sign() <= 0 {
		return nil
	}
	c, ok := new(big.Int).SetString(cost, 10)
	if !ok {
		return nil
	}
	rate, ok := ratioBasisPoints(new(big.Int).Sub(r, c), r)
	if !ok {
		return nil
	}
	return &rate
}

// retentionBasisPoints is retained / cohort in basis points, rounded half up; an empty cohort has no rate.
func retentionBasisPoints(retained, cohort int64) *int64 {
	if cohort <= 0 {
		return nil
	}
	rate, ok := ratioBasisPoints(big.NewInt(retained), big.NewInt(cohort))
	if !ok {
		return nil
	}
	return &rate
}

// ratioBasisPoints is part / whole in basis points, rounded half away from zero. whole must be positive; the result is
// signed, so a gross margin below zero stays below zero.
func ratioBasisPoints(part, whole *big.Int) (int64, bool) {
	if whole.Sign() <= 0 {
		return 0, false
	}
	sign := int64(1)
	numerator := new(big.Int).Set(part)
	if numerator.Sign() < 0 {
		sign = -1
		numerator.Abs(numerator)
	}
	numerator.Mul(numerator, big.NewInt(10000))
	numerator.Lsh(numerator, 1)
	numerator.Add(numerator, whole)
	denominator := new(big.Int).Lsh(new(big.Int).Set(whole), 1)
	rate := numerator.Quo(numerator, denominator)
	if !rate.IsInt64() {
		return 0, false
	}
	return sign * rate.Int64(), true
}
