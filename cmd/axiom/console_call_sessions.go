package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"

	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func (c consoleAPI) ListCallSessions(ctx context.Context, request consoleapi.ListCallSessionsRequestObject) (consoleapi.ListCallSessionsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, callReportTimeout)
	defer cancel()
	cl := callOf(ctx)
	q, err := ownCallFilters(cl)
	if err != nil {
		return nil, err
	}
	q, err = boundedCallWindow(q, request.Params.Since, request.Params.Until, ownCallReportWindow)
	if err != nil {
		return nil, err
	}
	q.PageSize = pageSize(request.Params.Limit)
	q.FellBack = request.Params.FellBack != nil && *request.Params.FellBack
	if request.Params.Cursor != nil {
		q.After, err = c.parseSessionCursor(*request.Params.Cursor, q)
		if err != nil {
			return nil, err
		}
	}
	var rows []observability.OwnSession
	var next *observability.Cursor
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		rows, next, err = observability.ListOwnSessions(ctx, tx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	data := make([]consoleapi.CallSession, len(rows))
	for i, row := range rows {
		data[i] = consoleapi.CallSession{
			SessionId: &row.SessionID, ClientApp: &row.ClientApp, Calls: &row.Calls,
			Price: &consoleapi.CallAggregateMoney{Amount: &row.PriceNano, Currency: &row.Currency}, MainModel: &row.MainModel, FirstAt: &row.FirstAt, LastAt: &row.LastAt,
		}
	}
	body := consoleapi.CallSessionList{Data: &data, Since: &q.Since, Until: &q.Until}
	if next != nil {
		cursor, err := c.sessionCursor(*next, q)
		if err != nil {
			return nil, err
		}
		body.NextCursor = &cursor
	}
	return consoleapi.ListCallSessions200JSONResponse(body), nil
}

// Bind the cursor to the complete filter set, including account and fixed time bounds. The identity key is shared
// across nodes; domain separation keeps these signatures independent of upstream identifier replacement.
func (c consoleAPI) sessionCursorMAC(position string, q observability.OwnQuery) ([]byte, error) {
	filters, err := jsonx.MarshalCanonical([]any{
		q.Account, q.Since.UTC(), q.Until.UTC(), q.Model, q.ClientApp,
		q.Key, q.Workspace, q.RequestID, q.Status, q.FellBack,
	})
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, c.sessionCursorKey)
	mac.Write([]byte("axiom/console/call-sessions/v1\x00"))
	mac.Write(filters)
	mac.Write([]byte(position))
	return mac.Sum(nil), nil
}

func (c consoleAPI) sessionCursor(position observability.Cursor, q observability.OwnQuery) (string, error) {
	text := position.String()
	signature, err := c.sessionCursorMAC(text, q)
	if err != nil {
		return "", err
	}
	return text + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c consoleAPI) parseSessionCursor(raw string, q observability.OwnQuery) (*observability.Cursor, error) {
	if len(raw) != 75 {
		return nil, observability.ErrBadCursor
	}
	position, err := observability.ParseCursor(raw[:32])
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(raw[32:])
	if err != nil {
		return nil, observability.ErrBadCursor
	}
	expected, err := c.sessionCursorMAC(raw[:32], q)
	if err != nil || !hmac.Equal(signature, expected) {
		return nil, observability.ErrBadCursor
	}
	return &position, nil
}
