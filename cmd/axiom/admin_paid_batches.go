package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// paidBatchWindow bounds the paid-batch list. Unlike the cash list, the retention drill starts at the previous UTC
// month's first day, so the window can span two calendar months and is allowed 63 days.
func paidBatchWindow(since, until *time.Time) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	if until != nil {
		end = *until
	}
	start := end.Add(-24 * time.Hour)
	if since != nil {
		start = *since
	}
	if !start.Before(end) || end.Sub(start) > 63*24*time.Hour {
		return time.Time{}, time.Time{}, invalidField("since", "choose a nonempty window of at most 63 days")
	}
	return start, end, nil
}

// ListPaidBatches lists the paid credit batches behind the dashboard's paid users and monthly revenue. The time is the
// batch's created_at, the same time those figures count, so an order created before the window and credited inside it
// is in the list. A retention drill spans the cohort month and the current month, hence the wider window bound.
func (a adminAPI) ListPaidBatches(ctx context.Context, r adminapi.ListPaidBatchesRequestObject) (adminapi.ListPaidBatchesResponseObject, error) {
	p := r.Params
	siteID, site, err := a.siteOf(p.SiteId)
	if err != nil {
		return nil, err
	}
	since, until, err := paidBatchWindow(p.Since, p.Until)
	if err != nil {
		return nil, err
	}
	after, err := pageCursorOf(p.Cursor)
	if err != nil {
		return nil, err
	}
	start := page.UpperBound(until, after)
	var rows []billing.PaidBatch
	var next *page.Cursor
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rows, next, err = billing.ListPaidBatches(ctx, tx, billing.PaidBatchQuery{
			Site: uuid.UUID(siteID), Since: since, Until: until, After: start, Limit: pageSize(p.Limit),
		})
		return err
	}); err != nil {
		return nil, err
	}
	data := make([]adminapi.PaidBatch, len(rows))
	for i, batch := range rows {
		id := batch.ID.String()
		account := accounts.AccountID(batch.Account).TypeID()
		amount := adminapi.Money{Amount: batch.Amount, Currency: site.Currency.String()}
		body := adminapi.PaidBatch{
			Id: id, AccountId: account, Amount: amount, CreatedAt: batch.CreatedAt, Online: batch.Online,
		}
		if batch.OfflineChannel != "" {
			channel := batch.OfflineChannel
			body.OfflineChannel = &channel
		}
		data[i] = body
	}
	list := adminapi.PaidBatchList{Data: data}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return adminapi.ListPaidBatches200JSONResponse(list), nil
}
