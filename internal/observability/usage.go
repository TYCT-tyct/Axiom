package observability

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

// ErrUsageWindow identifies an invalid statistics range at the HTTP boundary.
var ErrUsageWindow = errors.New("usage statistics require whole UTC hours, at most 90 days (7 days for hour granularity)")

// UsageQuery reads only hourly rollups. Since is inclusive and Until exclusive; both are whole hours.
type UsageQuery struct {
	Account                     uuid.UUID
	Since, Until                time.Time
	Granularity, GroupBy, Model string
	Key, Workspace              *uuid.UUID
	Previous                    bool
}

// ValidateUsageWindow enforces the rollup's resolution without silently expanding a money query's range.
func ValidateUsageWindow(since, until time.Time, granularity string) error {
	limit := 90 * 24 * time.Hour
	if granularity == "hour" {
		limit = 7 * 24 * time.Hour
	}
	if !since.Before(until) || until.Sub(since) > limit ||
		!since.Equal(since.Truncate(time.Hour)) || !until.Equal(until.Truncate(time.Hour)) {
		return ErrUsageWindow
	}
	return nil
}

// UsageMetrics keeps exact sums as text. Empty token values mean none of the records reported that kind.
// CacheHitRate uses only records with reported physical input and cache_read; nil rates have no denominator.
type UsageMetrics struct {
	Currency, Requests, Successes, Spend string
	Input, Output, CacheRead, CacheWrite string
	CacheRateRequests                    string
	CacheHitRate, SuccessRate            *float64
}

// UsageBucket is one UTC bucket and optional public model/key/workspace group. Empty buckets are omitted.
type UsageBucket struct {
	At    time.Time
	Group string
	UsageMetrics
}

// UsageStatistics separates currencies and returns previous totals in the same snapshot as the current period.
type UsageStatistics struct {
	Buckets          []UsageBucket
	Totals, Previous []UsageMetrics
	BackfillComplete bool
}

const maxUsageGroups = 10000

// ReadUsageStatistics uses the same account scope as ListOwn. It never reads call-record detail.
func ReadUsageStatistics(ctx context.Context, tx pg.Tx, q UsageQuery) (UsageStatistics, error) {
	out := UsageStatistics{Buckets: []UsageBucket{}, Totals: []UsageMetrics{}, Previous: []UsageMetrics{}}
	start := q.Since
	if q.Previous {
		start = start.Add(-q.Until.Sub(q.Since))
	}
	if err := planOwnQuery(ctx, tx); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT done FROM observability.usage_backfill WHERE singleton`).Scan(&out.BackfillComplete); err != nil {
		return out, err
	}
	rows, err := store.New().ReadUsageStatistics(ctx, tx, store.ReadUsageStatisticsParams{
		AccountID: q.Account.String(), Since: q.Since, StartAt: start, Until: q.Until,
		Granularity: q.Granularity, GroupBy: q.GroupBy, Model: q.Model,
		KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace), GroupLimit: maxUsageGroups + 1,
	})
	if err != nil {
		return out, fmt.Errorf("read usage statistics: %w", err)
	}
	if len(rows) > maxUsageGroups {
		return out, ErrAggregateSize
	}
	for _, r := range rows {
		m := UsageMetrics{
			Currency: r.Currency, Requests: r.Requests, Successes: r.Successes, Spend: r.Spend,
			Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
			CacheRateRequests: r.CacheRateRequests, CacheHitRate: usageRatio(r.CacheRateRead, r.CacheRateInput),
			SuccessRate: usageRatio(r.Successes, r.Requests),
		}
		switch {
		case r.Previous:
			out.Previous = append(out.Previous, m)
		case r.Total == 1:
			out.Totals = append(out.Totals, m)
		default:
			out.Buckets = append(out.Buckets, UsageBucket{At: r.Bucket, Group: r.GroupName, UsageMetrics: m})
		}
	}
	return out, nil
}

func usageRatio(numerator, denominator string) *float64 {
	if denominator == "0" {
		return nil
	}
	a, _ := new(big.Rat).SetString(numerator)
	b, _ := new(big.Rat).SetString(denominator)
	v, _ := new(big.Rat).Quo(a, b).Float64()
	return &v
}
