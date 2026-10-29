package main

import (
	"context"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// moneyOf is an amount in billionths of a unit of the site's currency, as the contract writes money.
func moneyOf(amount int64, currency platform.Currency) consoleapi.Money {
	return consoleapi.Money{Amount: strconv.FormatInt(amount, 10), Currency: currency.String()}
}

// GetBalance reads the figures a call is admitted by.
func (c consoleAPI) GetBalance(ctx context.Context, _ consoleapi.GetBalanceRequestObject) (consoleapi.GetBalanceResponseObject, error) {
	cl := callOf(ctx)
	var view billing.BalanceView
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		ids, err := c.accounts.AccountWorkspaces(ctx, tx, cl.principal.AccountID())
		if err != nil {
			return err
		}
		view, err = billing.ReadBalance(ctx, tx, uuid.UUID(cl.principal.AccountID()), ids)
		return err
	})
	if err != nil {
		return nil, err
	}
	// The balance less reservations and freezes can leave 64 bits when the figures are at their limits; text does not.
	available := view.Available()
	currency := cl.site.Currency
	return consoleapi.GetBalance200JSONResponse{
		Balance: moneyOf(view.Balance, currency), Reserved: moneyOf(view.Reserved, currency), Floor: moneyOf(view.Floor, currency),
		Available:      consoleapi.Money{Amount: available.String(), Currency: currency.String()},
		SpentThisMonth: moneyOf(view.SpentThisMonth, currency),
	}, nil
}

// endOfTime is the upper bound of a list that gives none: later than any call.
var endOfTime = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// callBody is a call as the contract gives it. The usage is an object even when empty.
func callBody(c observability.OwnCall) consoleapi.Call {
	usage := c.Usage
	if usage == nil {
		usage = map[string]int64{}
	}
	return consoleapi.Call{
		Id: c.ID.String(), At: c.At, WorkspaceId: accounts.WorkspaceID(c.Workspace).TypeID(), KeyId: accounts.KeyID(c.Key).TypeID(),
		Ip: c.IP, ClientApp: c.ClientApp, SessionId: c.SessionID, SubagentId: c.SubagentID, RequestClass: c.Class,
		AppReferer: optionalText(c.AppReferer), AppTitle: optionalText(c.AppTitle), AppCategories: optionalText(c.AppCategories),
		Model: c.Model, Usage: usage, ServiceTier: c.ServiceTier,
		PriceVersion: optionalText(c.PriceVersion), Price: consoleapi.Money{Amount: strconv.FormatInt(c.PriceNano, 10), Currency: c.Currency},
		Free: c.Free, LatencyMs: int(c.Latency.Milliseconds()), FirstTokenMs: int(c.FirstToken.Milliseconds()),
		FinishReason: c.FinishReason, ErrorKind: c.ErrorKind,
	}
}

// ListCalls lists the account's calls, newest first, a page at a time.
func (c consoleAPI) ListCalls(ctx context.Context, request consoleapi.ListCallsRequestObject) (consoleapi.ListCallsResponseObject, error) {
	cl := callOf(ctx)
	p := request.Params
	q := observability.OwnQuery{Account: uuid.UUID(cl.principal.AccountID()), Until: endOfTime, PageSize: pageSize(p.Limit)}
	if p.Since != nil {
		q.Since = *p.Since
	}
	if p.Until != nil {
		q.Until = *p.Until
	}
	if p.Model != nil {
		q.Model = *p.Model
	}
	if p.ClientApp != nil {
		q.ClientApp = *p.ClientApp
	}
	if p.Cursor != nil {
		after, err := observability.ParseCursor(*p.Cursor)
		if err != nil {
			return nil, err
		}
		q.After = &after
	}
	var calls []observability.OwnCall
	var next *observability.Cursor
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		calls, next, err = observability.ListOwn(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := consoleapi.CallList{Data: make([]consoleapi.Call, len(calls))}
	for i, call := range calls {
		list.Data[i] = callBody(call)
	}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return consoleapi.ListCalls200JSONResponse(list), nil
}

// GetCall reads one call of the account. A malformed ID and an unknown one are the same 404, and so is another
// account's call, which the scope cannot see.
func (c consoleAPI) GetCall(ctx context.Context, request consoleapi.GetCallRequestObject) (consoleapi.GetCallResponseObject, error) {
	cl := callOf(ctx)
	id, err := uuid.Parse(request.CallId)
	if err != nil || id.String() != request.CallId {
		return nil, accounts.ErrNotFound
	}
	var call observability.OwnCall
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		call, err = observability.GetOwn(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.GetCall200JSONResponse(callBody(call)), nil
}

// optionalText is a contract field that is absent when the call has nothing to say for it, which a page shows as
// unavailable rather than as an empty value.
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
