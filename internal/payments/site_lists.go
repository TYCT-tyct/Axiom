package payments

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"axiom/internal/platform"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// OrderQuery narrows one site's orders. A zero field does not narrow; Site is required.
type OrderQuery struct {
	Site     uuid.UUID
	Account  *uuid.UUID
	User     *uuid.UUID
	Merchant *uuid.UUID
	State    State
	Since    time.Time
	Until    time.Time
}

// SiteOrder is one of a site's orders with what its operator's list shows beside the order: the merchant account's
// name as the channel, and what the channel actually took and credited, which a partial or mismatched payment makes
// different from the quote.
type SiteOrder struct {
	Order
	MerchantName string
	PaidAt       *time.Time
	Credited     *int64
	PaidTotal    *int64
	PaidFee      *int64
}

// ListSiteOrders is one page of a site's orders, newest first, and the cursor of the next page, nil at the end. The
// scope is the access control: a site or platform scope sees its site, and row-level security shows no other.
func (p *Payments) ListSiteOrders(ctx context.Context, s pg.Scope, q OrderQuery, after *page.Cursor, size int) ([]SiteOrder, *page.Cursor, error) {
	if q.Site == (uuid.UUID{}) {
		return nil, nil, fmt.Errorf("payments: list site orders: no site")
	}
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list site orders: page size %d, want 1 to 200", size)
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && !q.Until.After(q.Since) {
		return nil, nil, fmt.Errorf("payments: list site orders: until is not after since")
	}
	// The first page starts above every order; an empty ID in the bound excludes nothing at its time.
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	params := ListSiteOrdersParams{
		SiteID: q.Site, AccountID: q.Account, UserID: q.User, MerchantID: q.Merchant, AfterAt: at, AfterID: id,
		Max: int32(size + 1), //nolint:gosec // size is 1 to 200
	}
	if q.State != "" {
		state := string(q.State)
		params.State = &state
	}
	if !q.Since.IsZero() {
		params.Since = pgtype.Timestamptz{Time: q.Since, Valid: true}
	}
	if !q.Until.IsZero() {
		params.Until = pgtype.Timestamptz{Time: q.Until, Valid: true}
	}
	var orders []SiteOrder
	var next *page.Cursor
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) error {
		rows, err := queries.ListSiteOrders(ctx, tx, params)
		if err != nil {
			return fmt.Errorf("payments: list site orders: %w", err)
		}
		if len(rows) > size {
			rows = rows[:size]
			last := rows[size-1]
			next = &page.Cursor{At: last.CreatedAt, ID: last.ID}
		}
		orders = make([]SiteOrder, len(rows))
		for i, r := range rows {
			orders[i], err = siteOrderOf(r)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return orders, next, err
}

func siteOrderOf(r ListSiteOrdersRow) (SiteOrder, error) {
	pay, err := platform.ParseCurrency(r.PayCurrency)
	if err != nil {
		return SiteOrder{}, fmt.Errorf("payments: order %s is paid in %q: %w", OrderID(r.ID).TypeID(), r.PayCurrency, err)
	}
	o := SiteOrder{
		Order: Order{
			ID: OrderID(r.ID), SiteID: r.SiteID, AccountID: r.AccountID, UserID: r.UserID,
			MerchantID: MerchantID(r.MerchantID), Method: Method(r.Method), State: State(r.State),
			Credits: r.Credits, Total: Money{Currency: pay, Minor: r.Total}, Fee: r.Fee, TradeNo: r.TradeNo,
			ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RefundAllowed: r.RefundAllowed,
		},
		MerchantName: r.MerchantName, Credited: r.Credited, PaidTotal: r.PaidTotal, PaidFee: r.PaidFee,
	}
	if r.PaidAt.Valid {
		at := r.PaidAt.Time
		o.PaidAt = &at
	}
	return o, nil
}

// RefundQuery narrows one site's refunds. State is an internal state; empty does not narrow, and Site is required.
type RefundQuery struct {
	Site  uuid.UUID
	State string
}

// ListSiteRefunds is one page of a site's refunds, newest first, and the cursor of the next page, nil at the end. The
// scope sees only its site, as for every read here.
func (p *Payments) ListSiteRefunds(ctx context.Context, s pg.Scope, q RefundQuery, after *page.Cursor, size int) ([]Refund, *page.Cursor, error) {
	if q.Site == (uuid.UUID{}) {
		return nil, nil, fmt.Errorf("payments: list site refunds: no site")
	}
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list site refunds: page size %d, want 1 to 200", size)
	}
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	params := ListSiteRefundsParams{SiteID: q.Site, AfterAt: at, AfterID: id, Max: int32(size + 1)} //nolint:gosec // size is 1 to 200
	if q.State != "" {
		state := q.State
		params.State = &state
	}
	var refunds []Refund
	var next *page.Cursor
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) error {
		rows, err := queries.ListSiteRefunds(ctx, tx, params)
		if err != nil {
			return fmt.Errorf("payments: list site refunds: %w", err)
		}
		if len(rows) > size {
			rows = rows[:size]
			last := rows[size-1]
			next = &page.Cursor{At: last.PaymentsRefund.CreatedAt, ID: last.PaymentsRefund.ID}
		}
		refunds = make([]Refund, len(rows))
		for i, r := range rows {
			pay, err := platform.ParseCurrency(r.PayCurrency)
			if err != nil {
				return fmt.Errorf("payments: refund %s is paid in %q: %w", RefundID(r.PaymentsRefund.ID).TypeID(), r.PayCurrency, err)
			}
			refunds[i] = refundOf(r.PaymentsRefund, pay)
		}
		return nil
	})
	return refunds, next, err
}
