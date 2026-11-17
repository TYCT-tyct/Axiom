package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func (c consoleAPI) ListStatementDaily(ctx context.Context, request consoleapi.ListStatementDailyRequestObject) (consoleapi.ListStatementDailyResponseObject, error) {
	cl := callOf(ctx)
	zone := "UTC"
	if request.Params.TimeZone != nil {
		zone = *request.Params.TimeZone
	}
	if _, err := time.LoadLocation(zone); err != nil || zone == "Local" {
		return nil, invalidField("time_zone", "must name an IANA time zone")
	}
	q := billing.StatementDailyQuery{
		Account: uuid.UUID(cl.principal.AccountID()), TimeZone: zone,
		PageSize: min(pageSize(request.Params.Limit), billing.MaxStatementDays),
	}
	if request.Params.Cursor != nil {
		before, err := billing.ParseStatementDailyCursor(*request.Params.Cursor, zone)
		if err != nil {
			return nil, err
		}
		q.Before = before
	}
	var rows []billing.StatementDay
	var next *string
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		rows, next, err = billing.ListStatementDaily(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	page := consoleapi.StatementDaily{Data: make([]consoleapi.StatementDay, len(rows)), NextCursor: next}
	for i, row := range rows {
		page.Data[i] = consoleapi.StatementDay{
			Day:      row.Day,
			Spent:    consoleapi.Money{Amount: row.Spent, Currency: cl.site.Currency.String()},
			ToppedUp: consoleapi.Money{Amount: row.ToppedUp, Currency: cl.site.Currency.String()},
			Gifted:   consoleapi.Money{Amount: row.Gifted, Currency: cl.site.Currency.String()},
			Expired:  consoleapi.Money{Amount: row.Expired, Currency: cl.site.Currency.String()},
		}
		if row.ClosingBalance != nil {
			balance := moneyOf(*row.ClosingBalance, cl.site.Currency)
			page.Data[i].ClosingBalance = &balance
		}
	}
	return consoleapi.ListStatementDaily200JSONResponse(page), nil
}
