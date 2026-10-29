package main

import (
	"context"
	"net/url"
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

func adminSearchWindow(values url.Values) (since, until time.Time, after *observability.Cursor, err error) {
	until = time.Now().UTC()
	since = until.Add(-24 * time.Hour)
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"since", &since}, {"until", &until}} {
		if raw := values.Get(p.name); raw != "" {
			*p.dst, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return since, until, nil, invalidField(p.name, "must be an RFC3339 time")
			}
		}
	}
	if values.Get("since") == "" {
		since = until.Add(-24 * time.Hour)
	}
	if !since.Before(until) || until.Sub(since) > 31*24*time.Hour {
		return since, until, nil, invalidField("since", "window must be positive and no longer than 31 days")
	}
	if raw := values.Get("cursor"); raw != "" {
		c, e := observability.ParseCursor(raw)
		if e != nil || c.At.Year() < 1 || c.At.Year() > 9999 {
			return since, until, nil, invalidField("cursor", "not a cursor this API gave out")
		}
		after = &c
	}
	return since, until, after, nil
}

func summaryBody(c observability.CallSummary) (adminapi.AdminCallSummary, error) {
	return adminJSON[adminapi.AdminCallSummary](map[string]any{"id": c.ID.String(), "site_id": accounts.SiteID(c.Site).TypeID(), "account_id": accounts.AccountID(c.Account).TypeID(), "at": c.At, "model": c.Model, "error_kind": c.ErrorKind, "latency_ms": c.LatencyMS, "price": map[string]string{"amount": strconv.FormatInt(c.PriceNano, 10), "currency": c.Currency}})
}

func (a adminAPI) ListAdminCalls(ctx context.Context, r adminapi.ListAdminCallsRequestObject) (adminapi.ListAdminCallsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	values := callOf(ctx).r.URL.Query()
	since, until, after, err := adminSearchWindow(values)
	if err != nil {
		return nil, err
	}
	q := observability.AdminQuery{Since: since, Until: until, After: after, PageSize: pageSize(r.Params.Limit)}
	if raw := values.Get("account_id"); raw != "" {
		id, e := accounts.ParseAccountID(raw)
		if e != nil {
			return nil, invalidField("account_id", "not an account ID")
		}
		v := uuid.UUID(id)
		q.Account = &v
	}
	if raw := values.Get("request_id"); raw != "" {
		id, e := uuid.Parse(raw)
		if e != nil {
			return nil, invalidField("request_id", "not a UUID")
		}
		q.RequestID = &id
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
	result := adminapi.ListAdminCalls200JSONResponse{Data: &data, Since: &since, Until: &until}
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
	v := callOf(ctx).r.URL.Query()
	since, until, after, err := adminSearchWindow(v)
	if err != nil {
		return nil, err
	}
	q := audit.Search{Since: since, Until: until, BeforeAt: until, Limit: pageSize(r.Params.Limit), ActorKind: v.Get("actor_kind"), ActorID: v.Get("actor_id"), Action: v.Get("action"), Module: v.Get("module"), TargetKind: v.Get("target_kind"), TargetID: v.Get("target_id"), RequestID: v.Get("request_id")}
	if after != nil && after.At.Before(until) {
		q.BeforeAt, q.BeforeID = after.At, after.ID
	}
	if raw := v.Get("site_id"); raw != "" {
		id, e := accounts.ParseSiteID(raw)
		if e != nil {
			return nil, invalidField("site_id", "not a site ID")
		}
		q.Site = uuid.UUID(id).String()
	}
	if raw := v.Get("account_id"); raw != "" {
		id, e := accounts.ParseAccountID(raw)
		if e != nil {
			return nil, invalidField("account_id", "not an account ID")
		}
		q.Account = uuid.UUID(id).String()
	}
	var rows []audit.Entry
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { rows, err = audit.List(ctx, tx, q); return err })
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
		last := rows[len(rows)-1]
		v := (observability.Cursor{At: last.At, ID: last.ID}).String()
		next = &v
	}
	data := make([]adminapi.AuditEntry, len(rows))
	for i, e := range rows {
		data[i], err = auditBody(e)
		if err != nil {
			return nil, err
		}
	}
	return adminapi.ListAuditEntries200JSONResponse{Data: &data, NextCursor: next, Since: &since, Until: &until}, nil
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
