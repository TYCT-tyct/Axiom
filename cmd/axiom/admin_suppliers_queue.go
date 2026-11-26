package main

import (
	"context"
	"strconv"
	"time"
	"uuid"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// A supplier's sync history, reconciliation and intake rule, and the suppliers' to-do queue (docs/domains/suppliers.md
// 「同步记录」「对账」「接入规则」「待处理队列」).

// syncEventKinds are the kinds of difference the contract names; one a newer node wrote is left out of an older one's
// answer rather than breaking it.
var syncEventKinds = map[string]bool{
	"group_added": true, "group_removed": true, "ratio_changed": true, "model_added": true, "model_removed": true,
	"plan_changed": true, "key_changed": true, "balance_low": true, "needs_attention": true, "task_halted": true,
	"group_changed": true, "model_changed": true, "funding_changed": true, "balance_crossed": true, "balance_recovered": true,
}

// ListSupplierSyncRuns lists the sync's commits of a supplier that changed something, newest first.
func (a adminAPI) ListSupplierSyncRuns(ctx context.Context, request adminapi.ListSupplierSyncRunsRequestObject) (adminapi.ListSupplierSyncRunsResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	var before int64
	if c := request.Params.Cursor; c != nil {
		if before, err = strconv.ParseInt(*c, 10, 64); err != nil || before < 1 {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
	}
	var runs []suppliers.SyncRun
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		runs, more, err = a.suppliers.SyncRuns(ctx, tx, supplier, before, pageSize(request.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.SupplierSyncRunList{Data: make([]adminapi.SupplierSyncRun, len(runs))}
	for i, r := range runs {
		list.Data[i] = syncRunBody(r)
	}
	if more {
		list.NextCursor = new(strconv.FormatInt(runs[len(runs)-1].Version, 10))
	}
	return adminapi.ListSupplierSyncRuns200JSONResponse(list), nil
}

// syncRunBody is a run as the contract gives it, without the differences of a kind it does not name.
func syncRunBody(r suppliers.SyncRun) adminapi.SupplierSyncRun {
	run := adminapi.SupplierSyncRun{Version: r.Version, At: r.At, Events: []adminapi.SupplierSyncEvent{}}
	for _, e := range r.Events {
		if !syncEventKinds[e.Kind] {
			continue
		}
		event := adminapi.SupplierSyncEvent{Kind: adminapi.SyncEventKind(e.Kind), Subject: e.Subject, Old: text(e.Old), New: text(e.New), Why: text(e.Why)}
		if e.Group != nil {
			event.GroupId = new(e.Group.String())
		}
		run.Events = append(run.Events, event)
	}
	return run
}

// RequestSupplierSync makes every task of a supplier due now.
func (a adminAPI) RequestSupplierSync(ctx context.Context, request adminapi.RequestSupplierSyncRequestObject) (adminapi.RequestSupplierSyncResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	tasks, replayed, err := idempotently(ctx, a.db, "requestSupplierSync", request.Params.IdempotencyKey, []any{request.SupplierId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierSyncTaskList, error) {
			t, err := a.suppliers.RequestSync(ctx, tx, actor(ctx), supplier, request.Body.Reason)
			return adminapi.SupplierSyncTaskList{Data: syncTasksBody(t)}, err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.RequestSupplierSync202JSONResponse{Body: tasks, Headers: adminapi.RequestSupplierSync202ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// syncTasksBody is how the sync's tasks stand, as the contract gives it.
func syncTasksBody(tasks []suppliers.SyncTask) []adminapi.SupplierSyncTask {
	out := make([]adminapi.SupplierSyncTask, len(tasks))
	for i, t := range tasks {
		out[i] = adminapi.SupplierSyncTask{Task: adminapi.SyncTask(t.Task), Failures: t.Failures, Halted: t.Halted}
		if !t.Due.IsZero() {
			out[i].DueAt = new(t.Due)
		}
		if !t.LastOK.IsZero() {
			out[i].LastOkAt = new(t.LastOK.UTC())
		}
	}
	return out
}

// GetSupplierReconciliation reads a supplier's reconciliation by UTC day.
func (a adminAPI) GetSupplierReconciliation(ctx context.Context, request adminapi.GetSupplierReconciliationRequestObject) (adminapi.GetSupplierReconciliationResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	days := 7
	if request.Params.Days != nil {
		days = *request.Params.Days
	}
	if days < 1 || days > suppliers.MaxReconciliationDays {
		return nil, invalidField("days", "1 to 31")
	}
	var r suppliers.Reconciliation
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		r, err = a.suppliers.Reconciliation(ctx, tx, supplier, days)
		return err
	}); err != nil {
		return nil, err
	}
	body := adminapi.SupplierReconciliation{
		SupplierId: r.Supplier.String(), NativeUnit: adminapi.SupplierReconciliationNativeUnit(r.NativeUnit),
		UnitValue: decimal(r.UnitValue), SettlementCurrency: text(r.SettlementCurrency),
		Days: make([]adminapi.SupplierReconciliationDay, len(r.Days)),
	}
	for i, d := range r.Days {
		date, err := time.Parse(time.DateOnly, d.Date)
		if err != nil {
			return nil, err
		}
		day := adminapi.SupplierReconciliationDay{
			Date: openapi_types.Date{Time: date}, EstimatedNative: d.EstimatedNative, ChargedNative: d.ChargedNative,
			Charges: d.Charges, Matched: d.Matched, Held: d.Held, ChargedAbove: d.ChargedAbove, ChargedBelow: d.ChargedBelow,
			UnmatchedAttempts: new(d.UnmatchedAttempts),
		}
		if d.DifferenceBP != nil {
			day.DifferenceBp = new(int(*d.DifferenceBP))
		}
		body.Days[i] = day
	}
	return adminapi.GetSupplierReconciliation200JSONResponse(body), nil
}

// intakeRuleBody is a supplier's intake rule as the contract gives it.
func intakeRuleBody(r suppliers.IntakeRule) adminapi.SupplierIntakeRule {
	return adminapi.SupplierIntakeRule{Mode: adminapi.SupplierIntakeMode(r.Mode), Include: r.Include, Exclude: r.Exclude, Rev: r.Rev}
}

// SetSupplierIntakeRule replaces the rule of which of a supplier's names become endpoints, decided against its rev.
func (a adminAPI) SetSupplierIntakeRule(ctx context.Context, request adminapi.SetSupplierIntakeRuleRequestObject) (adminapi.SetSupplierIntakeRuleResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	rule := suppliers.IntakeRule{Mode: string(b.Mode), Rev: b.Rev}
	if b.Include != nil {
		rule.Include = *b.Include
	}
	if b.Exclude != nil {
		rule.Exclude = *b.Exclude
	}
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierIntakeRule", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.SupplierIntakeRule, error) {
			r, err := a.suppliers.SetIntakeRule(ctx, tx, actor(ctx), supplier, rule, b.Reason)
			return intakeRuleBody(r), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSupplierIntakeRule200JSONResponse{Body: changed, Headers: adminapi.SetSupplierIntakeRule200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// ListSupplierInbox lists what across the suppliers waits for a person, most recently seen first.
func (a adminAPI) ListSupplierInbox(ctx context.Context, request adminapi.ListSupplierInboxRequestObject) (adminapi.ListSupplierInboxResponseObject, error) {
	p := request.Params
	q := suppliers.InboxQuery{PageSize: pageSize(p.Limit)}
	if p.Kind != nil {
		q.Kind = string(*p.Kind)
	}
	if p.SupplierId != nil {
		var err error
		if q.Supplier, err = idIn("supplier_id", *p.SupplierId); err != nil {
			return nil, err
		}
	}
	after, err := pageCursorOf(p.Cursor)
	if err != nil {
		return nil, err
	}
	if after != nil {
		q.After = &suppliers.InboxCursor{LastSeen: after.At, ID: after.ID}
	}
	var items []suppliers.InboxItem
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		items, more, err = a.suppliers.Inbox(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.SupplierInboxList{Data: make([]adminapi.SupplierInboxItem, len(items))}
	for i, item := range items {
		if list.Data[i], err = inboxItemBody(item); err != nil {
			return nil, err
		}
	}
	if more {
		last := items[len(items)-1]
		list.NextCursor = new(page.Cursor{At: last.LastSeen, ID: last.ID}.String())
	}
	return adminapi.ListSupplierInbox200JSONResponse(list), nil
}

// inboxItemBody is an item as the contract gives it: the members its kind has.
func inboxItemBody(item suppliers.InboxItem) (adminapi.SupplierInboxItem, error) {
	body := adminapi.SupplierInboxItem{
		Id: item.ID.String(), Kind: adminapi.SupplierInboxKind(item.Kind), SupplierId: item.Supplier.String(),
		FirstSeenAt: item.FirstSeen, LastSeenAt: item.LastSeen, IgnoredAt: item.Ignored,
		GroupId: text(item.Group), CredentialId: text(item.Credential), UpstreamName: text(item.UpstreamName),
		BalanceNative: decimal(item.BalanceNative), PreviousRatio: decimal(item.PreviousRatio), Ratio: decimal(item.Ratio),
		EstimatedNative: decimal(item.EstimatedNative), ChargedNative: decimal(item.ChargedNative),
	}
	if item.BalanceDays != nil {
		body.BalanceDays = new(int(*item.BalanceDays))
	}
	if item.Date != "" {
		date, err := time.Parse(time.DateOnly, item.Date)
		if err != nil {
			return adminapi.SupplierInboxItem{}, err
		}
		body.Date = &openapi_types.Date{Time: date}
	}
	return body, nil
}

// GetSupplierInboxSummary counts the items that show, by kind.
func (a adminAPI) GetSupplierInboxSummary(ctx context.Context, _ adminapi.GetSupplierInboxSummaryRequestObject) (adminapi.GetSupplierInboxSummaryResponseObject, error) {
	var counts map[string]int64
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		counts, err = a.suppliers.InboxCounts(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	var body adminapi.SupplierInboxSummary
	k := &body.Kinds
	k.LowBalance, k.RatioChanged, k.CostMismatch = counts["low_balance"], counts["ratio_changed"], counts["cost_mismatch"]
	k.GroupAdded, k.UnlistedModelCalled = counts["group_added"], counts["unlisted_model_called"]
	k.KeyInvalid, k.UnknownModel = counts["key_invalid"], counts["unknown_model"]
	for _, n := range counts {
		body.Total += n
	}
	return adminapi.GetSupplierInboxSummary200JSONResponse(body), nil
}

// IgnoreSupplierInboxItem hides an item until the next observation still finds its condition.
func (a adminAPI) IgnoreSupplierInboxItem(ctx context.Context, request adminapi.IgnoreSupplierInboxItemRequestObject) (adminapi.IgnoreSupplierInboxItemResponseObject, error) {
	id, err := uuid.Parse(request.ItemId)
	if err != nil || id.String() != request.ItemId {
		return nil, suppliers.ErrNoInboxItem
	}
	item, replayed, err := idempotently(ctx, a.db, "ignoreSupplierInboxItem", request.Params.IdempotencyKey, []any{request.ItemId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierInboxItem, error) {
			ignored, err := a.suppliers.IgnoreInboxItem(ctx, tx, actor(ctx), id, request.Body.Reason)
			if err != nil {
				return adminapi.SupplierInboxItem{}, err
			}
			return inboxItemBody(ignored)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.IgnoreSupplierInboxItem200JSONResponse{Body: item, Headers: adminapi.IgnoreSupplierInboxItem200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
