package payments

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// GetOrder reads an order the scope sees: the account's own on the console, so another account's is ErrNotFound.
func (p *Payments) GetOrder(ctx context.Context, s pg.Scope, id OrderID) (Order, error) {
	var o Order
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) { o, err = loadOrder(ctx, tx, id); return err })
	return o, err
}

// ListOrders is one page of the account's orders, newest first, and the cursor of the next page, nil at the end. The
// scope is the access control: row-level security shows the account's own rows and no others.
func (p *Payments) ListOrders(ctx context.Context, s pg.Scope, account uuid.UUID, after *page.Cursor, size int) ([]Order, *page.Cursor, error) {
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list orders: page size %d, want 1 to 200", size)
	}
	// The first page starts above every order; an empty ID in the bound excludes nothing at its time.
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	var orders []Order
	var next *page.Cursor
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) error {
		rows, err := queries.ListOrders(ctx, tx, ListOrdersParams{
			AccountID: account, AfterAt: at, AfterID: id, Max: int32(size + 1), //nolint:gosec // size is 1 to 200
		})
		if err != nil {
			return fmt.Errorf("payments: list orders: %w", err)
		}
		if len(rows) > size {
			rows = rows[:size]
			last := rows[size-1]
			next = &page.Cursor{At: last.CreatedAt, ID: last.ID}
		}
		orders = make([]Order, len(rows))
		for i, r := range rows {
			// ListOrders selects the same columns as ReadOrder, so the generated row types are the same and one
			// conversion keeps the two reads from drifting apart.
			orders[i], err = orderOf(ReadOrderRow(r))
			if err != nil {
				return err
			}
		}
		return nil
	})
	return orders, next, err
}

// endOfOrders is the upper bound of the first page: later than any order.
var endOfOrders = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
