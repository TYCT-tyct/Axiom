package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// OwnCall is a call as its own account sees it: what the user did and was charged, and not what it cost us, which
// endpoint served it, or how it was routed (docs/domains/gateway.md「用量与费用」).
type OwnCall struct {
	ID, Workspace, Key               uuid.UUID
	At                               time.Time
	IP                               string
	ClientApp, SessionID, SubagentID string
	Class, Model                     string
	Usage                            map[string]int64 // billing item name to count
	ServiceTier                      string
	PriceNano                        int64
	Currency                         string
	Free                             bool
	Latency, FirstToken              time.Duration
	FinishReason, ErrorKind          string
}

// Cursor is the place after which the next page starts: the last call of the page before.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// OwnQuery selects calls from the transaction's scope, newest first. Since is inclusive and Until exclusive; Model
// and ClientApp, when not empty, must match exactly; After continues from a previous page.
type OwnQuery struct {
	Since, Until     time.Time
	Model, ClientApp string
	After            *Cursor
	PageSize         int // 1 to MaxPageSize; 0 is DefaultPageSize
}

// The page sizes of ListOwn.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ListOwn returns one page of the calls the transaction's scope can see, and the cursor of the next page, nil when
// this was the last. The scope is the whole of the access control: row-level security shows the account's own rows
// and no others, so a caller that builds the scope from the user's authorization cannot ask for someone else's.
func ListOwn(ctx context.Context, tx pg.Tx, q OwnQuery) ([]OwnCall, *Cursor, error) {
	size := q.PageSize
	switch {
	case size == 0:
		size = DefaultPageSize
	case size < 0 || size > MaxPageSize:
		return nil, nil, fmt.Errorf("list calls: page size %d, want 1 to %d", size, MaxPageSize)
	}
	params := ListOwnCallsParams{
		Since: q.Since, Until: q.Until, Model: q.Model, ClientApp: q.ClientApp, PageSize: int32(size) + 1,
		AfterAt: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), AfterID: uuid.Max().String(), // before the newest call
	}
	if q.After != nil {
		params.AfterAt, params.AfterID = q.After.At, q.After.ID.String()
	}
	rows, err := New().ListOwnCalls(ctx, tx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list calls: %w", err)
	}
	var next *Cursor
	if len(rows) > size { // the extra row only proves there is a next page
		rows = rows[:size]
		last := rows[size-1]
		id, err := uuid.Parse(last.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("list calls: %w", err)
		}
		next = &Cursor{At: last.At, ID: id}
	}
	calls := make([]OwnCall, len(rows))
	for i, r := range rows {
		if calls[i], err = ownCall(r); err != nil {
			return nil, nil, fmt.Errorf("list calls: %w", err)
		}
	}
	return calls, next, nil
}

// GetOwn returns one call of the scope by its request ID; a call of another account is not found, as if it did not
// exist.
func GetOwn(ctx context.Context, tx pg.Tx, id uuid.UUID) (OwnCall, error) {
	r, err := New().GetOwnCall(ctx, tx, id.String())
	if err != nil {
		return OwnCall{}, fmt.Errorf("get call %s: %w", id, err)
	}
	return ownCall(ListOwnCallsRow(r))
}

func ownCall(r ListOwnCallsRow) (OwnCall, error) {
	c := OwnCall{
		At: r.At, IP: r.Ip, ClientApp: r.ClientApp, SessionID: r.SessionID, SubagentID: r.SubagentID,
		Class: r.RequestClass, Model: r.Model, ServiceTier: r.ServiceTier, PriceNano: r.PriceNano,
		Currency: r.Currency, Free: r.FreeOfCharge, Latency: time.Duration(r.LatencyMs) * time.Millisecond,
		FirstToken: time.Duration(r.FirstTokenMs) * time.Millisecond, FinishReason: r.FinishReason, ErrorKind: r.ErrorKind,
	}
	var err error
	for _, p := range []struct {
		dst *uuid.UUID
		s   string
	}{{&c.ID, r.ID}, {&c.Workspace, r.WorkspaceID}, {&c.Key, r.KeyID}} {
		if *p.dst, err = uuid.Parse(p.s); err != nil {
			return OwnCall{}, err
		}
	}
	return c, jsonx.Unmarshal([]byte(r.Usage), &c.Usage)
}
