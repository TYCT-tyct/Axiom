package observability

import (
	"context"
	"errors"
	"fmt"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

const maxOwnAggregateGroups = 1000

// ErrAggregateSize asks the caller to narrow the window or filters instead of returning a partial total.
var ErrAggregateSize = errors.New("too many call aggregate groups")

// OwnAggregate contains user charges, never supplier costs. Text retains sums beyond int64.
type OwnAggregate struct {
	Bucket, Currency, Calls, PriceNano string
}

// AggregateOwn groups all matching calls, independently of pagination. Day buckets use UTC; currencies stay separate.
func AggregateOwn(ctx context.Context, tx pg.Tx, q OwnQuery, groupBy string) ([]OwnAggregate, error) {
	switch groupBy {
	case "day", "model", "key":
	default:
		return nil, fmt.Errorf("aggregate calls: unknown grouping %q", groupBy)
	}
	if err := planOwnQuery(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := store.New().AggregateOwnCalls(ctx, tx, store.AggregateOwnCallsParams{
		AccountID: q.Account.String(), Since: q.Since, Until: q.Until, Model: q.Model, ClientApp: q.ClientApp,
		KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace), Status: q.Status, RequestID: uuidFilter(q.RequestID),
		GroupBy: groupBy, GroupLimit: maxOwnAggregateGroups + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("aggregate calls: %w", err)
	}
	if len(rows) > maxOwnAggregateGroups {
		return nil, ErrAggregateSize
	}
	out := make([]OwnAggregate, len(rows))
	for i, r := range rows {
		out[i] = OwnAggregate{Bucket: r.Bucket, Currency: r.Currency, Calls: r.Calls, PriceNano: r.PriceNano}
	}
	return out, nil
}
