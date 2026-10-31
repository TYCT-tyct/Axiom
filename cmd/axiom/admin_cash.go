package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// ListCashMovements lists the cash behind the dashboard's figure: paid credits in and refunds out, by the ledger
// transaction's time, so a refund of an older order or an offline top-up is in the window the dashboard counted
// (docs/domains/operations.md「运营看板」).
func (a adminAPI) ListCashMovements(ctx context.Context, r adminapi.ListCashMovementsRequestObject) (adminapi.ListCashMovementsResponseObject, error) {
	p := r.Params
	siteID, site, err := a.siteOf(p.SiteId)
	if err != nil {
		return nil, err
	}
	w, err := boundedCallWindow(observability.OwnQuery{}, p.Since, p.Until, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	account, err := parseFilterID(p.AccountId, accounts.ParseAccountID, "account_id", "not an account ID")
	if err != nil {
		return nil, err
	}
	after := billing.CashCursor{At: w.Until}
	if p.Cursor != nil {
		after, err = billing.ParseCashCursor(string(*p.Cursor))
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
	}
	var rows []billing.CashMovement
	var next *billing.CashCursor
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rows, next, err = billing.ListCashMovements(ctx, tx, billing.CashQuery{
			Site: uuid.UUID(siteID), Account: account, Since: w.Since, Until: w.Until, After: after, Limit: pageSize(p.Limit),
		})
		return err
	}); err != nil {
		return nil, err
	}
	data := make([]adminapi.CashMovement, len(rows))
	for i, m := range rows {
		id, batch := adminapi.Uuid(m.ID.String()), adminapi.Uuid(m.Batch.String())
		account := adminapi.AccountId(accounts.AccountID(m.Account).TypeID())
		kind := adminapi.CashMovementKind(m.Kind)
		at := m.At
		currency := m.Currency
		if currency == "" {
			currency = site.Currency.String()
		}
		amount := adminapi.Money{Amount: m.Amount, Currency: currency}
		body := adminapi.CashMovement{Id: &id, Kind: &kind, At: &at, AccountId: &account, BatchId: &batch, Amount: &amount}
		if m.Online {
			order := adminapi.OrderId(payments.OrderID(m.Batch).TypeID())
			body.OrderId = &order
		} else if m.OfflineChannel != "" {
			body.OfflineChannel = &m.OfflineChannel
		}
		data[i] = body
	}
	list := adminapi.CashMovementList{Data: data}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return adminapi.ListCashMovements200JSONResponse(list), nil
}
