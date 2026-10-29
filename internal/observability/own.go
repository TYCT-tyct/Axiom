package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// OwnCall is a call as its own account sees it: what the user did and was charged, and not what it cost us, which
// endpoint served it, or how it was routed (docs/domains/gateway.md「用量与费用」).
type OwnCall struct {
	PriceVersion                     string
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

// OwnQuery selects the calls of one account, newest first, from those the transaction's scope can see. Account is
// required: it is what the index is read by. Since is inclusive and Until exclusive; Model
// and ClientApp, when not empty, must match exactly; After continues from a previous page.
type OwnQuery struct {
	Account                   uuid.UUID
	Since, Until              time.Time
	Model, ClientApp          string
	Key, Workspace, RequestID *uuid.UUID
	Status                    string // empty, success, or error; error means error_kind is nonempty
	After                     *Cursor
	PageSize                  int // 1 to MaxPageSize; 0 is DefaultPageSize
}

// The page sizes of ListOwn.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// cursorOf uses the tighter of the exclusive window end and the page cursor as one composite index bound.
func cursorOf(q OwnQuery) (at time.Time, id string) {
	if q.After != nil && q.After.At.Before(q.Until) {
		return q.After.At, q.After.ID.String()
	}
	return q.Until, uuid.Nil().String()
}

// listRows plans for the actual filter values so optional predicates do not hide the ordered indexes.
func listRows(ctx context.Context, tx pg.Tx, q OwnQuery, size int) ([]store.ListOwnCallsRow, error) {
	if err := planOwnPage(ctx, tx); err != nil {
		return nil, err
	}
	at, id := cursorOf(q)
	return store.New().ListOwnCalls(ctx, tx, store.ListOwnCallsParams{
		AccountID: q.Account.String(), Since: q.Since, Until: q.Until, AfterAt: at, AfterID: id,
		Model: q.Model, ClientApp: q.ClientApp, KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace),
		Status: q.Status, RequestID: uuidFilter(q.RequestID),
		PageSize: int32(size) + 1, //nolint:gosec // ListOwn checks size is 1 to MaxPageSize
	})
}

func planOwnPage(ctx context.Context, tx pg.Tx) error {
	if err := planOwnQuery(ctx, tx); err != nil {
		return err
	}
	// Correlated sparse filters can be estimated below LIMIT and otherwise chosen as bitmap + sort.
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort = off"); err != nil {
		return fmt.Errorf("read the page in index order: %w", err)
	}
	return nil
}

func planOwnQuery(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
		return fmt.Errorf("plan calls for their filters: %w", err)
	}
	return nil
}

func uuidFilter(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

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
	rows, err := listRows(ctx, tx, q, size)
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
	r, err := store.New().GetOwnCall(ctx, tx, id.String())
	if err != nil {
		return OwnCall{}, fmt.Errorf("get call %s: %w", id, err)
	}
	return ownCall(store.ListOwnCallsRow(r))
}

func ownCall(r store.ListOwnCallsRow) (OwnCall, error) {
	c := OwnCall{
		At: r.At, IP: r.Ip, ClientApp: r.ClientApp, SessionID: r.SessionID, SubagentID: r.SubagentID,
		PriceVersion: r.PriceVersion, Class: r.RequestClass, Model: r.Model, ServiceTier: r.ServiceTier, PriceNano: r.PriceNano,
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
