package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

const maxOwnSessionCalls = 100000

// ErrSessionSize refuses a window before grouping more calls than its fixed read budget.
var ErrSessionSize = fmt.Errorf("session window exceeds %d matching calls; narrow the window or filters", maxOwnSessionCalls)

// OwnSession contains only visible call metadata, grouped within the requested window and currency.
type OwnSession struct {
	SessionID, ClientApp, Currency, Calls, PriceNano, MainModel string
	FirstAt, LastAt                                             time.Time
}

// ListOwnSessions groups after applying the same scope and filters as ListOwn. The newest call identifies a group
// uniquely, so the shared time/UUID cursor also orders sessions whose last timestamps are equal.
func ListOwnSessions(ctx context.Context, tx pg.Tx, q OwnQuery) ([]OwnSession, *Cursor, error) {
	// This exported read also guards callers outside HTTP: the index budget assumes a bounded window.
	if !q.Since.Before(q.Until) || q.Until.Sub(q.Since) > 31*24*time.Hour {
		return nil, nil, fmt.Errorf("list sessions: require a nonempty window of at most 31 days")
	}
	size, err := ownPageSize(q.PageSize)
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}
	if err := planOwnPage(ctx, tx); err != nil {
		return nil, nil, err
	}
	at, id := cursorOf(q)
	rows, err := store.New().ListOwnSessions(ctx, tx, store.ListOwnSessionsParams{
		AccountID: q.Account.String(), Since: q.Since, Until: q.Until, Model: q.Model, ClientApp: q.ClientApp,
		KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace), Status: q.Status, RequestID: uuidFilter(q.RequestID),
		FellBack: q.FellBack, AfterAt: at, AfterID: id, PageSize: int64(size) + 1, CallLimit: maxOwnSessionCalls,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list sessions: %w", err)
	}
	if len(rows) > 0 && rows[0].OverLimit {
		return nil, nil, ErrSessionSize
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
