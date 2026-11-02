package main

import (
	"context"
	"errors"
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
	account := uuid.UUID(cl.principal.AccountID())
	// The batches need the allocation replay advanced to say what is left in each exactly. That is bounded so the page
	// does not wait on an account with a long history; if it cannot finish, the field is absent and the page shows this
	// part as unavailable instead of a figure that trails the ledger.
	batchCtx, cancel := context.WithTimeout(ctx, batchReplayBudget)
	advanced, err := billing.AdvanceCreditBatches(batchCtx, c.db, cl.scope, account, batchReplayPages)
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		advanced = false
	default:
		return nil, err
	}
	// Balance and batches come from one snapshot. Reading them separately could show a balance that already includes a
	// charge which the batches, replayed a moment earlier, do not.
	var view billing.BalanceView
	var batches []billing.CreditBatchView
	var ready bool
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		ids, err := c.accounts.AccountWorkspaces(ctx, tx, cl.principal.AccountID())
		if err != nil {
			return err
		}
		if view, err = billing.ReadBalance(ctx, tx, account, ids); err != nil {
			return err
		}
		batches, ready, err = billing.ReadCreditBatches(ctx, tx, account, advanced)
		return err
	})
	if err != nil {
		return nil, err
	}
	// The balance less reservations and freezes can leave 64 bits when the figures are at their limits; text does not.
	available := view.Available()
	currency := cl.site.Currency
	body := consoleapi.GetBalance200JSONResponse{
		Balance: moneyOf(view.Balance, currency), Reserved: moneyOf(view.Reserved, currency), Floor: moneyOf(view.Floor, currency),
		Available:      consoleapi.Money{Amount: available.String(), Currency: currency.String()},
		SpentThisMonth: moneyOf(view.SpentThisMonth, currency),
	}
	if ready {
		list := make([]consoleapi.CreditBatch, len(batches))
		for i, b := range batches {
			list[i] = creditBatchBody(b, currency)
		}
		body.Batches = &list
	}
	return body, nil
}

// How far a balance read may chase the allocation and how long it may spend doing it. A page after the first is usually
// already caught up; the bound only matters for an account whose whole history has never been replayed.
const (
	batchReplayPages  = 8
	batchReplayBudget = 2 * time.Second
)

// creditBatchBody is a batch as the contract shows it. Money is the site's accounting currency.
func creditBatchBody(b billing.CreditBatchView, currency platform.Currency) consoleapi.CreditBatch {
	return consoleapi.CreditBatch{
		Id: b.ID.String(), Source: consoleapi.CreditBatchSource(b.Source), Amount: moneyOf(b.Amount, currency),
		Remaining: moneyOf(b.Remaining, currency), Frozen: moneyOf(b.Frozen, currency), CreatedAt: b.CreatedAt,
		ExpiresAt: b.ExpiresAt,
	}
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
		FinishReason: c.FinishReason, ErrorKind: c.ErrorKind, CacheHit: &c.CacheHit,
	}
}

// ListCalls lists the account's calls, newest first, a page at a time.
func (c consoleAPI) ListCalls(ctx context.Context, request consoleapi.ListCallsRequestObject) (consoleapi.ListCallsResponseObject, error) {
	cl := callOf(ctx)
	q, err := ownCallQuery(cl)
	if err != nil {
		return nil, err
	}
	q.PageSize = pageSize(request.Params.Limit)
	var calls []observability.OwnCall
	var next *observability.Cursor
	var charges map[uuid.UUID]billing.ChargeDetails
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		calls, next, err = observability.ListOwn(ctx, tx, q)
		if err == nil {
			charges, err = c.readCallCharges(ctx, tx, calls)
		}
		return err
	}); err != nil {
		return nil, err
	}
	list := consoleapi.CallList{Data: make([]consoleapi.Call, len(calls)), StatusFilter: request.Params.Status}
	for i, call := range calls {
		list.Data[i] = callBody(call)
		if d, ok := charges[call.ID]; ok {
			list.Data[i].ChargeDetails = chargeBody(d)
		}
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
	var charges map[uuid.UUID]billing.ChargeDetails
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		call, err = observability.GetOwn(ctx, tx, id)
		if err == nil {
			charges, err = c.charges.ReadCharges(ctx, tx, []uuid.UUID{id})
		}
		return err
	}); err != nil {
		return nil, err
	}
	body := callBody(call)
	if d, ok := charges[id]; ok {
		body.ChargeDetails = chargeBody(d)
	}
	return consoleapi.GetCall200JSONResponse(body), nil
}

func (c consoleAPI) readCallCharges(ctx context.Context, tx pg.Tx, calls []observability.OwnCall) (map[uuid.UUID]billing.ChargeDetails, error) {
	if len(calls) == 0 {
		return map[uuid.UUID]billing.ChargeDetails{}, nil
	}
	ids := make([]uuid.UUID, len(calls))
	for i, call := range calls {
		ids[i] = call.ID
	}
	return c.charges.ReadCharges(ctx, tx, ids)
}

func chargeBody(d billing.ChargeDetails) *consoleapi.ChargeDetails {
	items := make([]consoleapi.ChargeItem, len(d.Items))
	for i, item := range d.Items {
		// Request charges are one per attempt, not one per sampling; sampling is optional for exactly that case.
		var sampling *int
		if item.Sampling > 0 {
			sampling = new(item.Sampling)
		}
		items[i] = consoleapi.ChargeItem{
			Item: new(item.Item), Quantity: new(item.Quantity), Unit: new(consoleapi.ChargeItemUnit(item.Unit)), UnitPrice: new(item.UnitPrice),
			Amount: new(item.Amount), Sampling: sampling, ServiceTier: new(item.ServiceTier), ContextBand: new(item.ContextBand),
			PriceVersion: optionalText(item.PriceVersion), Estimated: new(item.Estimated), Fallback: new(item.Fallback),
		}
	}
	return &consoleapi.ChargeDetails{
		Currency: new(d.Currency), Items: &items, Subtotal: new(d.Subtotal), Rounding: new(d.Rounding),
		BalanceAdjustment: new(d.BalanceAdjustment), Charged: new(d.Charged), Estimated: new(d.Estimated),
	}
}

// optionalText is a contract field that is absent when the call has nothing to say for it, which a page shows as
// unavailable rather than as an empty value.
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
