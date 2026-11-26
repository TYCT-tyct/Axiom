package suppliers

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// The suppliers' to-do queue (docs/domains/suppliers.md「待处理队列」): one item per condition of a supplier that waits for
// a person, kept while the condition holds. The sync's reads of the offer observe the conditions (persist.ObserveInbox);
// a person's change that ends one takes its item out in the same transaction.

// ErrNoInboxItem is an item the queue does not have.
var ErrNoInboxItem = errors.New("suppliers: no such item in the queue")

// InboxKinds are the kinds of item, in the contract's order.
var InboxKinds = []string{"low_balance", "ratio_changed", "cost_mismatch", "group_added", "unlisted_model_called", "key_invalid", "unknown_model"}

// InboxItem is one item. The detail members are those of its kind, as text, and empty when the kind has none.
type InboxItem struct {
	ID, Supplier                   uuid.UUID
	Kind                           string
	FirstSeen, LastSeen            time.Time
	Ignored                        *time.Time
	Group, Credential              string
	UpstreamName                   string
	BalanceNative, PreviousRatio   string
	Ratio, Date                    string
	EstimatedNative, ChargedNative string
	BalanceDays                    *int64
}

// inboxDetail is the detail column, whose members are the kind's.
type inboxDetail struct {
	Group           string `json:"group_id"`
	Credential      string `json:"credential_id"`
	UpstreamName    string `json:"upstream_name"`
	BalanceNative   string `json:"balance_native"`
	BalanceDays     *int64 `json:"balance_days"`
	PreviousRatio   string `json:"previous_ratio"`
	Ratio           string `json:"ratio"`
	Date            string `json:"date"`
	EstimatedNative string `json:"estimated_native"`
	ChargedNative   string `json:"charged_native"`
}

func inboxItemOf(id, supplier uuid.UUID, kind string, detail []byte, first, last time.Time, ignored *time.Time) (InboxItem, error) {
	var d inboxDetail
	if err := jsonx.Unmarshal(detail, &d); err != nil {
		return InboxItem{}, fmt.Errorf("the detail of item %s: %w", id, err)
	}
	return InboxItem{
		ID: id, Supplier: supplier, Kind: kind, FirstSeen: first, LastSeen: last, Ignored: ignored,
		Group: d.Group, Credential: d.Credential, UpstreamName: d.UpstreamName, BalanceNative: d.BalanceNative,
		BalanceDays: d.BalanceDays, PreviousRatio: d.PreviousRatio, Ratio: d.Ratio, Date: d.Date,
		EstimatedNative: d.EstimatedNative, ChargedNative: d.ChargedNative,
	}, nil
}

// InboxQuery chooses the items to list, most recently seen first. Zero values are no filter; After is the last item of
// the page before.
type InboxQuery struct {
	Kind     string
	Supplier uuid.UUID
	After    *InboxCursor
	PageSize int
}

// InboxCursor is where a page of the queue ends.
type InboxCursor struct {
	LastSeen time.Time
	ID       uuid.UUID
}

// Inbox lists a page of the items that show; more tells whether some remain beyond it.
func (*Admin) Inbox(ctx context.Context, tx pg.Tx, q InboxQuery) (items []InboxItem, more bool, err error) {
	params := store.InboxPageParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Kind != "" {
		params.Kind = &q.Kind
	}
	if q.Supplier != (uuid.UUID{}) {
		params.SupplierID = &q.Supplier
	}
	if q.After != nil {
		params.AfterSeen, params.AfterID = &q.After.LastSeen, &q.After.ID
	}
	rows, err := store.New().InboxPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the queue: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	items = make([]InboxItem, len(rows))
	for i, r := range rows {
		if items[i], err = inboxItemOf(r.ID, r.SupplierID, r.Kind, r.Detail, r.FirstSeenAt, r.LastSeenAt, r.IgnoredAt); err != nil {
			return nil, false, err
		}
	}
	return items, more, nil
}

// InboxCounts is how many items of each kind show; a kind with none is 0.
func (*Admin) InboxCounts(ctx context.Context, tx pg.Tx) (map[string]int64, error) {
	rows, err := store.New().InboxCounts(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("count the queue: %w", err)
	}
	counts := make(map[string]int64, len(InboxKinds))
	for _, k := range InboxKinds {
		counts[k] = 0
	}
	for _, r := range rows {
		counts[r.Kind] = r.Items
	}
	return counts, nil
}

// IgnoreInboxItem hides an item until the next observation that still finds its condition. An item ignored already is
// returned as it is, and records nothing again.
func (a *Admin) IgnoreInboxItem(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, reason string) (InboxItem, error) {
	q := store.New()
	rows, err := q.LockInboxItem(ctx, tx, id)
	if err != nil {
		return InboxItem{}, fmt.Errorf("read the item: %w", err)
	}
	if len(rows) == 0 {
		return InboxItem{}, ErrNoInboxItem
	}
	r := rows[0]
	item, err := inboxItemOf(r.ID, r.SupplierID, r.Kind, r.Detail, r.FirstSeenAt, r.LastSeenAt, r.IgnoredAt)
	if err != nil || item.Ignored != nil {
		return item, err
	}
	now := a.now().UTC().Truncate(time.Microsecond) // as the column keeps it, so the answer is what a read gives back
	if err := q.IgnoreInboxItem(ctx, tx, store.IgnoreInboxItemParams{ID: id, Now: &now}); err != nil {
		return InboxItem{}, fmt.Errorf("ignore the item: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "supplier.inbox.ignore", Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "inbox_item", ID: id.String()},
		Detail: map[string]any{"supplier": r.SupplierID.String(), "kind": r.Kind},
	}); err != nil {
		return InboxItem{}, err
	}
	item.Ignored = &now
	return item, nil
}
