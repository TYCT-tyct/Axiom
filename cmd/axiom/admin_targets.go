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

// UpdateOperationsTargets replaces the first-release four targets. It is the write every node has, so a page from
// before the commercial metrics keeps using it during a rolling release. The commercial rows are preserved; a full
// replacement uses ReplaceOperationsTargets, whose path an old node does not route.
func (a adminAPI) UpdateOperationsTargets(ctx context.Context, request adminapi.UpdateOperationsTargetsRequestObject) (adminapi.UpdateOperationsTargetsResponseObject, error) {
	if request.Params.XAxiomTargetMetrics != nil || request.Body.AllMetrics != nil ||
		request.Body.PaidUsers != nil || request.Body.MonthlyRevenue != nil ||
		request.Body.GrossMargin != nil || request.Body.Retention != nil {
		return nil, invalidField("/all_metrics", "a full replacement uses PUT /api/admin/v1/reports/targets/{month}/all")
	}
	view, replayed, err := a.writeTargets(ctx, "updateOperationsTargets", request.Month, request.Params.IdempotencyKey, request.Body, false)
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateOperationsTargets200JSONResponse{
		Body: view, Headers: adminapi.UpdateOperationsTargets200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// ReplaceOperationsTargets replaces all eight targets. The operation is mounted at a path that a node before the
// commercial metrics does not route: that node answers 404 before it can mistake the request for a legacy four-field
// write and clear only half the set. The header and body marker are still required, so a page states its intent and a
// new node never treats a half-marked request as a full write.
func (a adminAPI) ReplaceOperationsTargets(ctx context.Context, request adminapi.ReplaceOperationsTargetsRequestObject) (adminapi.ReplaceOperationsTargetsResponseObject, error) {
	headerFull := request.Params.XAxiomTargetMetrics != nil && string(*request.Params.XAxiomTargetMetrics) == "all"
	bodyFull := request.Body.AllMetrics != nil && *request.Body.AllMetrics
	if !headerFull || !bodyFull {
		return nil, invalidField("/all_metrics", "must be true exactly when X-Axiom-Target-Metrics is all")
	}
	view, replayed, err := a.writeTargets(ctx, "replaceOperationsTargets", request.Month, request.Params.IdempotencyKey, request.Body, true)
	if err != nil {
		return nil, err
	}
	return adminapi.ReplaceOperationsTargets200JSONResponse{
		Body: view, Headers: adminapi.ReplaceOperationsTargets200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// writeTargets is the shared read-check-write of both target operations. A legacy write keeps the first-release
// fingerprint [month, body], so a request an old node already completed replays here after a lost response; a full
// write has its own idempotency namespace and appends its marker to the fingerprint.
func (a adminAPI) writeTargets(ctx context.Context, operation, month, idempotencyKey string, body *adminapi.OperationsTargetsUpdate, full bool) (adminapi.OperationsTargets, bool, error) {
	parsed, err := targetMonthIn(month)
	if err != nil {
		return adminapi.OperationsTargets{}, false, err
	}
	call := callOf(ctx)
	site := uuid.UUID(call.site.ID)
	set, err := targetsIn(body, call.site.Currency, parsed)
	if err != nil {
		return adminapi.OperationsTargets{}, false, err
	}
	fingerprint := []any{month, body}
	if full {
		fingerprint = append(fingerprint, true)
	}
	view, replayed, err := idempotently(ctx, a.db, operation, idempotencyKey, fingerprint,
		func(tx pg.Tx) (adminapi.OperationsTargets, error) {
			if err := operations.Set(ctx, tx, actor(ctx), site, set, body.Rev, body.Reason, operations.SetOptions{KeepCommercial: !full}); err != nil {
				return adminapi.OperationsTargets{}, err
			}
			stored, err := operations.Read(ctx, tx, site, parsed)
			return targetsBody(call.site.ID.TypeID(), parsed, stored), err
		})
	if err != nil {
		return adminapi.OperationsTargets{}, false, err
	}
	return view, replayed, nil
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
	targets := operations.Targets{Month: month, Calls: body.Calls, Errors: body.Errors, PaidUsers: body.PaidUsers}
	var p problems
	targets.Revenue = targetMoney(body.Revenue, currency, "/revenue", &p)
	targets.Cost = targetMoney(body.Cost, currency, "/cost", &p)
	targets.MonthlyRevenue = targetMoney(body.MonthlyRevenue, currency, "/monthly_revenue", &p)
	targets.GrossMargin = rateTarget(body.GrossMargin)
	targets.Retention = rateTarget(body.Retention)
	return targets, p.err()
}

// rateTarget keeps a basis-point target as the module's wider integer; the contract's bounds are checked by the
// request validation and again by the table, so this only changes the width.
func rateTarget(rate *int) *int64 {
	if rate == nil {
		return nil
	}
	value := int64(*rate)
	return &value
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
		Metrics:        metricNames(),
		Calls:          targets.Calls,
		Errors:         targets.Errors,
		Revenue:        moneyBody(targets.Revenue),
		Cost:           moneyBody(targets.Cost),
		PaidUsers:      targets.PaidUsers,
		MonthlyRevenue: moneyBody(targets.MonthlyRevenue),
		GrossMargin:    rateBody(targets.GrossMargin),
		Retention:      rateBody(targets.Retention),
	}
}

// metricNames is the contract's list of the metrics this node stores, in the module's fixed order.
func metricNames() *[]adminapi.OperationsTargetMetric {
	metrics := operations.Metrics()
	names := make([]adminapi.OperationsTargetMetric, len(metrics))
	for i, metric := range metrics {
		names[i] = adminapi.OperationsTargetMetric(metric)
	}
	return &names
}

// rateBody is a basis-point target in the contract's integer.
func rateBody(rate *int64) *int {
	if rate == nil {
		return nil
	}
	value := int(*rate)
	return &value
}

func moneyBody(money *operations.Money) *adminapi.Money {
	if money == nil {
		return nil
	}
	return &adminapi.Money{Amount: money.Amount, Currency: money.Currency.String()}
}

// targetActuals is the dashboard's side of the comparison. Rates are basis points; a nil rate is unavailable, so its
// target keeps its value and loses its gap.
type targetActuals struct {
	Calls          int64
	Errors         int64
	Revenue        string
	Cost           string
	PaidUsers      int64
	MonthlyRevenue string
	GrossMargin    *int64
	Retention      *int64
	CallsComplete  bool
	CostComplete   bool
}

// targetComparison prorates the month's count and money targets to the dashboard's window and states each gap as
// actual minus target. The count gaps wait for a complete call log; the cost gap also waits for a complete cost total,
// because calls whose supplier cost is unknown are left out of that total and a gap would claim more than the data
// knows. Paid users and monthly revenue come from the credit batches, not the call log, so their gaps are always
// stated. Gross margin and retention are rates: the month's target is compared with the window's actual rate when that
// is available, without proration.
func targetComparison(expected operations.Expected, actual targetActuals, currency platform.Currency) *adminapi.OperationsTargetComparison {
	return &adminapi.OperationsTargetComparison{
		Calls:          countComparison(expected.Calls, actual.Calls, actual.CallsComplete),
		Errors:         countComparison(expected.Errors, actual.Errors, actual.CallsComplete),
		Revenue:        moneyComparison(expected.Revenue, actual.Revenue, currency, true),
		Cost:           moneyComparison(expected.Cost, actual.Cost, currency, actual.CostComplete),
		PaidUsers:      countComparison(expected.PaidUsers, actual.PaidUsers, true),
		MonthlyRevenue: moneyComparison(expected.MonthlyRevenue, actual.MonthlyRevenue, currency, true),
		GrossMargin:    rateComparison(expected.GrossMargin, actual.GrossMargin),
		Retention:      rateComparison(expected.Retention, actual.Retention),
	}
}

// countComparison states one count target and, when the actual count is complete, its gap.
func countComparison(expected *int64, actual int64, complete bool) *adminapi.OperationsCountTarget {
	if expected == nil {
		return nil
	}
	target := adminapi.OperationsCountTarget{Target: *expected}
	if complete {
		gap := actual - *expected
		target.Gap = &gap
	}
	return &target
}

// rateComparison states one basis-point rate target and, when the actual rate is available, its gap.
func rateComparison(expected, actual *int64) *adminapi.OperationsRateTarget {
	if expected == nil {
		return nil
	}
	target := adminapi.OperationsRateTarget{Target: int(*expected)}
	if actual != nil {
		gap := int(*actual - *expected)
		target.Gap = &gap
	}
	return &target
}

// moneyComparison states one money target and, when the actual total is complete and in the same currency, its gap.
// The target survives an incomplete actual: the page shows what the owner asked for and withholds the conclusion.
func moneyComparison(expected *operations.Money, actual string, currency platform.Currency, complete bool) *adminapi.OperationsMoneyTarget {
	if expected == nil || expected.Currency != currency {
		return nil
	}
	target := adminapi.OperationsMoneyTarget{Target: adminapi.Money{Amount: expected.Amount, Currency: currency.String()}}
	if complete {
		if gap, ok := moneyGap(actual, expected.Amount, currency); ok {
			target.Gap = gap
		}
	}
	return &target
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
