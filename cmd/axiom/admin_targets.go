package main

import (
	"context"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// GetOperationsTargets answers the targets the owner set for one UTC calendar month. A month without target rows is
// an empty set, not a 404: the editor needs to know that there is nothing to replace yet, and its revision so a
// simultaneous editor cannot overwrite a newer set.
func (a adminAPI) GetOperationsTargets(ctx context.Context, request adminapi.GetOperationsTargetsRequestObject) (adminapi.GetOperationsTargetsResponseObject, error) {
	month, err := targetMonthIn(request.Month)
	if err != nil {
		return nil, err
	}
	call := callOf(ctx)
	var targets operations.Targets
	if err := a.db.ReadTx(ctx, call.scope, func(tx pg.Tx) error {
		targets, err = operations.Read(ctx, tx, uuid.UUID(call.site.ID), month)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetOperationsTargets200JSONResponse(targetsBody(call.site.ID.TypeID(), month, targets)), nil
}

// UpdateOperationsTargets replaces all four targets of a month: a target left out is cleared. The write, the read of
// the values it replaced and the audit entry commit together, under the site-and-month lock the module takes.
func (a adminAPI) UpdateOperationsTargets(ctx context.Context, request adminapi.UpdateOperationsTargetsRequestObject) (adminapi.UpdateOperationsTargetsResponseObject, error) {
	month, err := targetMonthIn(request.Month)
	if err != nil {
		return nil, err
	}
	call := callOf(ctx)
	site := uuid.UUID(call.site.ID)
	set, err := targetsIn(request.Body, call.site.Currency, month)
	if err != nil {
		return nil, err
	}
	view, replayed, err := idempotently(ctx, a.db, "updateOperationsTargets", request.Params.IdempotencyKey, []any{request.Month, request.Body},
		func(tx pg.Tx) (adminapi.OperationsTargets, error) {
			if err := operations.Set(ctx, tx, actor(ctx), site, set, request.Body.Rev, request.Body.Reason); err != nil {
				return adminapi.OperationsTargets{}, err
			}
			stored, err := operations.Read(ctx, tx, site, month)
			return targetsBody(call.site.ID.TypeID(), month, stored), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateOperationsTargets200JSONResponse{
		Body: view, Headers: adminapi.UpdateOperationsTargets200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// targetMonthIn parses the contract's YYYY-MM path parameter; the guard has already matched its shape, so a failure
// here is a request the handler still refuses as a bad field rather than as an internal error.
func targetMonthIn(month string) (time.Time, error) {
	parsed, err := time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, invalidField("month", "must be a UTC calendar month, YYYY-MM")
	}
	return parsed, nil
}

// targetsIn checks what the contract cannot tie to the site: a money target is in the site's accounting currency, is
// not negative, and fits the target table's 38 digits. It keeps the decimal text instead of reading an int64, because
// a planning target is not a ledger entry and the table deliberately stores a wider amount. Counts are the contract's
// own bounds and need no second check.
func targetsIn(body *adminapi.OperationsTargetsUpdate, currency platform.Currency, month time.Time) (operations.Targets, error) {
	targets := operations.Targets{Month: month, Calls: body.Calls, Errors: body.Errors}
	var p problems
	targets.Revenue = targetMoney(body.Revenue, currency, "/revenue", &p)
	targets.Cost = targetMoney(body.Cost, currency, "/cost", &p)
	return targets, p.err()
}

// targetMoney keeps a money target as decimal text. The endpoint's Money type allows 39 characters so reports can
// exceed int64; a target is stored in numeric(38, 0), so the canonical non-negative amount must have at most 38
// digits. Other ledger writers keep their narrower int64 check.
func targetMoney(money *adminapi.Money, currency platform.Currency, pointer string, p *problems) *operations.Money {
	if money == nil {
		return nil
	}
	if money.Currency != currency.String() {
		p.add(pointer+"/currency", "must be the site's currency, "+currency.String())
		return nil
	}
	amount, ok := new(big.Int).SetString(money.Amount, 10)
	if !ok || amount.Sign() < 0 {
		p.add(pointer+"/amount", "must be 0 or more")
		return nil
	}
	if len(amount.String()) > operations.MaxMoneyDigits {
		p.add(pointer+"/amount", "must fit 38 digits")
		return nil
	}
	return &operations.Money{Amount: amount.String(), Currency: currency}
}

// targetsBody is the API's view of one month's targets, with absent targets left out rather than written as zero.
func targetsBody(siteID string, month time.Time, targets operations.Targets) adminapi.OperationsTargets {
	return adminapi.OperationsTargets{
		SiteId: siteID, Month: month.Format("2006-01"), Rev: targets.Revision,
		Calls: targets.Calls, Errors: targets.Errors,
		Revenue: moneyBody(targets.Revenue), Cost: moneyBody(targets.Cost),
	}
}

func moneyBody(money *operations.Money) *adminapi.Money {
	if money == nil {
		return nil
	}
	return &adminapi.Money{Amount: money.Amount, Currency: money.Currency.String()}
}

// targetComparison prorates the month targets to the dashboard's window and states each gap as actual minus target.
// The count gaps wait for a complete call log; the cost gap also waits for a complete cost total, because calls whose
// supplier cost is unknown are left out of that total and a gap would claim more than the data knows. A money gap
// waits for the target and the actual to be in the same currency, which the module already enforces by leaving out a
// target set in another one.
func targetComparison(expected operations.Expected, calls, errors int64, revenue, cost string, currency platform.Currency, callsComplete, costComplete bool) *adminapi.OperationsTargetComparison {
	comparison := &adminapi.OperationsTargetComparison{}
	if expected.Calls != nil {
		target := adminapi.OperationsCountTarget{Target: *expected.Calls}
		if callsComplete {
			gap := calls - *expected.Calls
			target.Gap = &gap
		}
		comparison.Calls = &target
	}
	if expected.Errors != nil {
		target := adminapi.OperationsCountTarget{Target: *expected.Errors}
		if callsComplete {
			gap := errors - *expected.Errors
			target.Gap = &gap
		}
		comparison.Errors = &target
	}
	if expected.Revenue != nil && expected.Revenue.Currency == currency {
		target := adminapi.OperationsMoneyTarget{Target: adminapi.Money{Amount: expected.Revenue.Amount, Currency: currency.String()}}
		if gap, ok := moneyGap(revenue, expected.Revenue.Amount, currency); ok {
			target.Gap = gap
		}
		comparison.Revenue = &target
	}
	if expected.Cost != nil && expected.Cost.Currency == currency {
		target := adminapi.OperationsMoneyTarget{Target: adminapi.Money{Amount: expected.Cost.Amount, Currency: currency.String()}}
		if costComplete {
			if gap, ok := moneyGap(cost, expected.Cost.Amount, currency); ok {
				target.Gap = gap
			}
		}
		comparison.Cost = &target
	}
	return comparison
}

// moneyGap is actual - target in the same currency, exact in big.Int because a report's sum can be wider than int64.
// It is absent when either side is not a decimal amount, which the writers never produce.
func moneyGap(actual, target string, currency platform.Currency) (*adminapi.Money, bool) {
	a, ok := new(big.Int).SetString(actual, 10)
	if !ok {
		return nil, false
	}
	t, ok := new(big.Int).SetString(target, 10)
	if !ok {
		return nil, false
	}
	gap := new(big.Int).Sub(a, t)
	return &adminapi.Money{Amount: gap.String(), Currency: currency.String()}, true
}
