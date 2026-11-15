package main

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func (c consoleAPI) GetUsageStatistics(ctx context.Context, request consoleapi.GetUsageStatisticsRequestObject) (consoleapi.GetUsageStatisticsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, callReportTimeout)
	defer cancel()
	cl := callOf(ctx)
	q, err := usageQuery(cl.principal.AccountID(), request.Params)
	if errors.Is(err, observability.ErrUsageWindow) {
		return nil, invalidField("since", err.Error())
	}
	if err != nil {
		return nil, err
	}
	var result observability.UsageStatistics
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) { result, err = observability.ReadUsageStatistics(ctx, tx, q); return err })
	if errors.Is(err, observability.ErrAggregateSize) {
		return nil, invalidField("group_by", "too many groups; narrow the window or filters")
	}
	if err != nil {
		return nil, err
	}
	return consoleapi.GetUsageStatistics200JSONResponse(usageBody(result, q)), nil
}

func usageQuery(account accounts.AccountID, p consoleapi.GetUsageStatisticsParams) (observability.UsageQuery, error) {
	q := observability.UsageQuery{Account: uuid.UUID(account), Granularity: "day"}
	if p.Model != nil {
		q.Model = *p.Model
	}
	if p.KeyId != nil {
		id, err := accounts.ParseKeyID(*p.KeyId)
		if err != nil {
			return q, invalidField("key_id", "invalid identifier")
		}
		value := uuid.UUID(id)
		q.Key = &value
	}
	if p.WorkspaceId != nil {
		id, err := accounts.ParseWorkspaceID(*p.WorkspaceId)
		if err != nil {
			return q, invalidField("workspace_id", "invalid identifier")
		}
		value := uuid.UUID(id)
		q.Workspace = &value
	}
	if p.Granularity != nil {
		q.Granularity = string(*p.Granularity)
	}
	if p.GroupBy != nil {
		q.GroupBy = string(*p.GroupBy)
	}
	q.Previous = p.Compare != nil
	q.Until = time.Now().UTC().Truncate(time.Hour)
	if p.Until != nil {
		q.Until = *p.Until
	}
	days := 30
	if q.Granularity == "hour" {
		days = 7
	}
	q.Since = q.Until.Add(-time.Duration(days) * 24 * time.Hour)
	if p.Since != nil {
		q.Since = *p.Since
	}
	return q, observability.ValidateUsageWindow(q.Since, q.Until, q.Granularity)
}

func usageBody(result observability.UsageStatistics, q observability.UsageQuery) consoleapi.UsageStatistics {
	buckets := make([]consoleapi.UsageBucket, len(result.Buckets))
	for i, row := range result.Buckets {
		group := row.Group
		switch q.GroupBy {
		case "key":
			group = accounts.KeyID(uuid.MustParse(group)).TypeID()
		case "workspace":
			group = accounts.WorkspaceID(uuid.MustParse(group)).TypeID()
		}
		metrics := usageMetricsBody(row.UsageMetrics)
		buckets[i] = consoleapi.UsageBucket{At: &row.At, Group: optionalText(group), Metrics: &metrics}
	}
	totals := usageTotalsBody(result.Totals)
	granularity := consoleapi.UsageStatisticsGranularity(q.Granularity)
	body := consoleapi.UsageStatistics{
		Since: &q.Since, Until: &q.Until, Granularity: &granularity,
		Buckets: &buckets, Totals: &totals, BackfillComplete: &result.BackfillComplete,
	}
	if q.GroupBy != "" {
		by := consoleapi.UsageStatisticsGroupBy(q.GroupBy)
		body.GroupBy = &by
	}
	if q.Previous {
		previous := usageTotalsBody(result.Previous)
		body.Previous = &previous
	}
	return body
}

func usageTotalsBody(rows []observability.UsageMetrics) []consoleapi.UsageMetrics {
	out := make([]consoleapi.UsageMetrics, len(rows))
	for i, row := range rows {
		out[i] = usageMetricsBody(row)
	}
	return out
}

func usageMetricsBody(m observability.UsageMetrics) consoleapi.UsageMetrics {
	return consoleapi.UsageMetrics{
		Requests: &m.Requests, Successes: &m.Successes,
		Spend: &consoleapi.CallAggregateMoney{Amount: &m.Spend, Currency: &m.Currency},
		Tokens: &consoleapi.UsageTokens{
			Input: optionalText(m.Input), Output: optionalText(m.Output),
			CacheRead: optionalText(m.CacheRead), CacheWrite: optionalText(m.CacheWrite),
		},
		CacheRateRequests: &m.CacheRateRequests, CacheHitRate: m.CacheHitRate, SuccessRate: m.SuccessRate,
	}
}
