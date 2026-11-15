package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

// OwnSession contains only visible call metadata, grouped within the requested window and currency.
type OwnSession struct {
	SessionID, ClientApp, Currency, Calls, PriceNano, MainModel string
	FirstAt, LastAt                                             time.Time
}

// ListOwnSessions groups after applying the same scope and filters as ListOwn. The newest call identifies a group
// uniquely, so the shared time/UUID cursor also orders sessions whose last timestamps are equal.
func ListOwnSessions(ctx context.Context, tx pg.Tx, q OwnQuery) ([]OwnSession, *Cursor, error) {
	if !q.Since.Before(q.Until) || q.Until.Sub(q.Since) > 31*24*time.Hour {
		return nil, nil, fmt.Errorf("list sessions: require a nonempty window of at most 31 days")
	}
	size := q.PageSize
	if size == 0 {
		size = DefaultPageSize
	}
	if size < 1 || size > MaxPageSize {
		return nil, nil, fmt.Errorf("list sessions: invalid page size")
	}
	if err := planOwnQuery(ctx, tx); err != nil {
		return nil, nil, err
	}
	at, id := cursorOf(q)
	rows, err := store.New().ListOwnSessions(ctx, tx, store.ListOwnSessionsParams{
		AccountID: q.Account.String(), Since: q.Since, Until: q.Until, Model: q.Model, ClientApp: q.ClientApp,
		KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace), Status: q.Status, RequestID: uuidFilter(q.RequestID),
		FellBack: q.FellBack, AfterAt: at, AfterID: id, PageSize: int64(size) + 1,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}
	var next *Cursor
	if len(rows) > size {
		rows = rows[:size]
		last := rows[size-1]
		parsed, err := uuid.Parse(last.LastID)
		if err != nil {
			return nil, nil, err
		}
		next = &Cursor{At: last.LastAt, ID: parsed}
	}
	out := make([]OwnSession, len(rows))
	for i, r := range rows {
		out[i] = OwnSession{SessionID: r.SessionID, ClientApp: r.ClientApp, Currency: r.Currency, Calls: r.Calls, PriceNano: r.Price, MainModel: r.MainModel, FirstAt: r.FirstAt, LastAt: r.LastAt}
	}
	return out, next, nil
}
