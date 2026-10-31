package main

import (
	"context"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func adminSearchWindow(since, until *time.Time, cursor *string) (time.Time, time.Time, *observability.Cursor, error) {
	q, err := boundedCallWindow(observability.OwnQuery{}, since, until, 24*time.Hour)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	if cursor != nil {
		c, err := observability.ParseCursor(*cursor)
		if err != nil {
			return time.Time{}, time.Time{}, nil, invalidField("cursor", "not a cursor this API gave out")
		}
		q.After = &c
	}
	return q.Since, q.Until, q.After, nil
}

func summaryBody(c observability.CallSummary) (adminapi.AdminCallSummary, error) {
	return adminJSON[adminapi.AdminCallSummary](map[string]any{"id": c.ID.String(), "site_id": accounts.SiteID(c.Site).TypeID(), "account_id": accounts.AccountID(c.Account).TypeID(), "at": c.At, "model": c.Model, "error_kind": c.ErrorKind, "latency_ms": c.LatencyMS, "price": map[string]string{"amount": strconv.FormatInt(c.PriceNano, 10), "currency": c.Currency}})
}

func (a adminAPI) ListAdminCalls(ctx context.Context, r adminapi.ListAdminCallsRequestObject) (adminapi.ListAdminCallsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	since, until, after, err := adminSearchWindow(r.Params.Since, r.Params.Until, r.Params.Cursor)
	if err != nil {
		return nil, err
	}
	q := observability.AdminQuery{Since: since, Until: until, After: after, PageSize: pageSize(r.Params.Limit)}
	if r.Params.AccountId != nil {
		id, e := accounts.ParseAccountID(*r.Params.AccountId)
		if e != nil {
			return nil, invalidField("account_id", "not an account ID")
		}
		v := uuid.UUID(id)
		q.Account = &v
	}
	if r.Params.RequestId != nil {
		id := uuid.UUID(*r.Params.RequestId)
		q.RequestID = &id
	}
	if r.Params.Model != nil {
		q.Model = *r.Params.Model
	}
	if r.Params.Status != nil {
		q.Status = string(*r.Params.Status)
	}

	var calls []observability.CallSummary
	var next *observability.Cursor
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { calls, next, err = observability.ListAdmin(ctx, tx, q); return err })
	if err != nil {
		return nil, err
	}
	data := make([]adminapi.AdminCallSummary, len(calls))
	for i, c := range calls {
		data[i], err = summaryBody(c)
		if err != nil {
			return nil, err
		}
	}
	result := adminapi.ListAdminCalls200JSONResponse{
		Data: &data, Since: &since, Until: &until, ModelFilter: r.Params.Model, StatusFilter: r.Params.Status,
	}
	if next != nil {
		v := next.String()
		result.NextCursor = &v
	}
	return result, nil
}

func (a adminAPI) GetAdminCallTrace(ctx context.Context, r adminapi.GetAdminCallTraceRequestObject) (adminapi.GetAdminCallTraceResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id, err := uuid.Parse(r.CallId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	var c observability.CallRecord
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { c, err = observability.GetTrace(ctx, tx, id); return err })
	if err != nil {
		return nil, err
	}
	summary, err := summaryBody(observability.CallSummary{ID: c.ID, Site: c.Site, Account: c.Account, At: c.At, Model: c.Model, ErrorKind: c.ErrorKind, LatencyMS: int(c.Latency.Milliseconds()), PriceNano: c.PriceNano, Currency: c.Currency.String()})
	if err != nil {
		return nil, err
	}
	attempts := make([]map[string]any, len(c.Attempts))
	for i, v := range c.Attempts {
		attempts[i] = map[string]any{"seq": v.Seq, "endpoint_id": v.Endpoint.String(), "supplier_id": v.Supplier.String(), "source_class": v.SourceClass, "egress": v.Egress, "outcome": v.Outcome, "error_kind": v.ErrorKind, "started_at": v.StartedAt, "finished_at": v.FinishedAt, "first_token_ms": v.FirstToken.Milliseconds()}
	}
	return adminJSON[adminapi.GetAdminCallTrace200JSONResponse](map[string]any{"summary": summary, "cost": adminMoney(c.CostNano, c.Currency), "route": jsonx.Raw(c.Route), "attempts": attempts})
}

func auditBody(e audit.Entry) (adminapi.AuditEntry, error) {
	body, err := adminJSON[adminapi.AuditEntry](e)
	if err != nil {
		return body, err
	}
	if e.Site != nil {
		v := accounts.SiteID(*e.Site).TypeID()
		body.SiteId = &v
	}
	if e.Account != nil {
		v := accounts.AccountID(*e.Account).TypeID()
		body.AccountId = &v
	}
	return body, nil
}

func (a adminAPI) ListAuditEntries(ctx context.Context, r adminapi.ListAuditEntriesRequestObject) (adminapi.ListAuditEntriesResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	since, until, after, err := adminSearchWindow(r.Params.Since, r.Params.Until, r.Params.Cursor)
	if err != nil {
		return nil, err
	}
	p := r.Params
	q := audit.Search{Since: since, Until: until, After: after, Limit: pageSize(p.Limit), ActorKind: textValue(p.ActorKind), ActorID: textValue(p.ActorId), Action: textValue(p.Action), Module: textValue(p.Module), TargetKind: textValue(p.TargetKind), TargetID: textValue(p.TargetId)}
	if p.RequestId != nil {
		q.RequestID = p.RequestId.String()
	}

	if p.SiteId != nil {
		id, e := accounts.ParseSiteID(*p.SiteId)
		if e != nil {
			return nil, invalidField("site_id", "not a site ID")
		}
		q.Site = uuid.UUID(id).String()
	}
	if p.AccountId != nil {
		id, e := accounts.ParseAccountID(*p.AccountId)
		if e != nil {
			return nil, invalidField("account_id", "not an account ID")
		}
		q.Account = uuid.UUID(id).String()
	}
	var rows []audit.Entry
	var next *observability.Cursor
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { rows, next, err = audit.List(ctx, tx, q); return err })
	if err != nil {
		return nil, err
	}
	var cursor *string
	if next != nil {
		v := next.String()
		cursor = &v
	}

	data := make([]adminapi.AuditEntry, len(rows))
	for i, e := range rows {
		data[i], err = auditBody(e)
		if err != nil {
			return nil, err
		}
	}
	return adminapi.ListAuditEntries200JSONResponse{Data: &data, NextCursor: cursor, Since: &since, Until: &until}, nil
}

func (a adminAPI) GetAuditEntry(ctx context.Context, r adminapi.GetAuditEntryRequestObject) (adminapi.GetAuditEntryResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id, err := uuid.Parse(r.EntryId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	var e audit.Entry
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { e, err = audit.Get(ctx, tx, id); return err })
	if err != nil {
		return nil, err
	}
	body, err := auditBody(e)
	return adminapi.GetAuditEntry200JSONResponse(body), err
}
