package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
)

// runSummary is a page of sync runs as version: kind@group of each event, the group's ref when the event names a group.
func runSummary(list adminapi.SupplierSyncRunList, groups map[string]string) []string {
	var out []string
	for _, r := range list.Data {
		line := fmt.Sprint(r.Version, ":")
		for _, e := range r.Events {
			line += " " + string(e.Kind) + "@" + groups[valueOf(e.GroupId)]
		}
		out = append(out, line)
	}
	return out
}

// TestSupplierSyncRuns: the sync's commits that recorded a difference, newest first, a page at a time, each with its
// differences and the group they are about; a kind this node does not know is left out.
func TestSupplierSyncRuns(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	groups := map[string]string{f.group(supplier, "vip"): "vip", "": "-"}
	f.run(fmt.Sprintf(`INSERT INTO suppliers.sync_events (supplier_id, version, seq, kind, subject, old_value, new_value, why) VALUES
		('%[1]s', 3, 0, 'ratio_changed', 'vip', '1', '1.2', ''),
		('%[1]s', 3, 1, 'model_added', 'vip/gpt-6', '', '', ''),
		('%[1]s', 3, 2, 'from_a_newer_node', 'vip', '', '', ''),
		('%[1]s', 5, 0, 'balance_low', '', '10', '4', 'below the line'),
		('%[1]s', 7, 0, 'group_removed', 'gone', '', '', '')`, supplier))
	var first, rest adminapi.SupplierSyncRunList
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier+"/sync-runs?limit=2", "", as(f.s)), &first)
	expectSame(t, "the first page", runSummary(first, groups), []string{"7: group_removed@-", "5: balance_low@-"})
	expectSame(t, "an event's words", valueOf(first.Data[1].Events[0].Why), "below the line")
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier+"/sync-runs?limit=2&cursor="+valueOf(first.NextCursor), "", as(f.s)), &rest)
	expectSame(t, "the last page", []any{runSummary(rest, groups), rest.NextCursor}, []any{[]string{"3: ratio_changed@vip model_added@vip"}, (*string)(nil)})
	expectAdmin(t, "a bad cursor", f.do(http.MethodGet, "/suppliers/"+supplier+"/sync-runs?cursor=x", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "no supplier", f.do(http.MethodGet, "/suppliers/"+uuid.NewV7().String()+"/sync-runs", "", as(f.s)), http.StatusNotFound, codeNotFound)
}

// TestRequestSupplierSync: asking for a sync makes every task due and answers how they stand, recorded once; a paused
// supplier is not synced, nor is one that is not a relay; the detail gives the tasks and the default rule.
func TestRequestSupplierSync(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/sync-requests", `{"reason":"look now"}`, key)
	expectAdmin(t, "request", rec, http.StatusAccepted, "")
	var tasks adminapi.SupplierSyncTaskList
	decode(t, rec, &tasks)
	expectSame(t, "tasks", []any{len(tasks.Data), tasks.Data[0].Task, tasks.Data[0].DueAt}, []any{4, adminapi.SyncTask("balance"), (*time.Time)(nil)})
	expectAdmin(t, "replay", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/sync-requests", `{"reason":"look now"}`, key), http.StatusAccepted, "")
	expectSame(t, "requests recorded", f.auditCount("supplier.sync_request"), "1")
	f.run("UPDATE suppliers.suppliers SET status = 'paused' WHERE id = '" + supplier + "'")
	expectAdmin(t, "paused", f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/sync-requests", `{"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	for _, mode := range []string{"direct", "cloud"} {
		f.run("UPDATE suppliers.suppliers SET status = 'active', mode = '" + mode + "' WHERE id = '" + supplier + "'")
		expectAdmin(t, mode, f.change(f.s, http.MethodPost, "/suppliers/"+supplier+"/sync-requests", `{"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	}
	expectSame(t, "requests recorded at last", f.auditCount("supplier.sync_request"), "1")
	var detail adminapi.SupplierDetail
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier, "", as(f.s)), &detail)
	expectSame(t, "the detail's tasks and rule", []any{len(valueOf(detail.SyncTasks)), valueOf(detail.IntakeRule)},
		[]any{4, adminapi.SupplierIntakeRule{Mode: "listed", Include: []string{}, Exclude: []string{}}})
}

// reconciliationRows are a supplier's group, credential, endpoint and schedule, which an attempt and a charge can name.
type reconciliationRows struct {
	supplier, group, credential, endpoint, schedule string
}

func (r reconciliationRows) seed() []string {
	return []string{
		fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
			VALUES ('%s', '%s', '%s', 'k', 'binding', 1)`, r.credential, r.supplier, r.group),
		fmt.Sprintf(`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
			VALUES ('%s', '%s', '%s', gen_random_uuid(), 'gpt', '{chat}')`, r.endpoint, r.supplier, r.group),
		fmt.Sprintf(`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
			VALUES ('%s', '%s', '%s', 'quota', '{}', 'published', 'v1')`, r.schedule, r.supplier, r.endpoint),
	}
}

func (r reconciliationRows) attempt(finished time.Time, estimate, ref string) string {
	at := finished.Format(time.RFC3339)
	return fmt.Sprintf(`INSERT INTO suppliers.attempt_costs (request_id, attempt_seq, supplier_id, credential_id, endpoint_id,
		cost_schedule_id, charge_ref, usage, priced_at, finished_at, cost_flag, estimated_native, state)
		VALUES ('%s', 1, '%s', '%s', '%s', '%s', %s, '{}', '%s', '%s', 'complete', %s, 'pending')`,
		uuid.NewV7(), r.supplier, r.credential, r.endpoint, r.schedule, ref, at, at, estimate)
}

func (r reconciliationRows) charge(ref string, occurred time.Time, amount, state string) string {
	return fmt.Sprintf(`INSERT INTO suppliers.charge_records (supplier_id, charge_ref, occurred_at, amount_native, usage, funding, upstream_model, state)
		VALUES ('%s', '%s', '%s', %s, '{}', 'wallet', 'gpt', '%s')`, r.supplier, ref, occurred.Format(time.RFC3339), amount, state)
}

// reconciliationDay is a day of a reconciliation as the test compares it.
type reconciliationDay struct {
	Date                                       string
	Estimated, Charged                         string
	DifferenceBP                               *int
	Charges, Matched, Held, Above, Below, Late int64
}

func reconciliationDays(r adminapi.SupplierReconciliation) []reconciliationDay {
	out := make([]reconciliationDay, len(r.Days))
	for i, d := range r.Days {
		out[i] = reconciliationDay{
			Date: d.Date.Format(time.DateOnly), Estimated: d.EstimatedNative, Charged: d.ChargedNative, DifferenceBP: d.DifferenceBp,
			Charges: d.Charges, Matched: d.Matched, Held: d.Held, Above: d.ChargedAbove, Below: d.ChargedBelow, Late: valueOf(d.UnmatchedAttempts),
		}
	}
	return out
}

// TestSupplierReconciliation: each UTC day of the window, newest first, with what its attempts were estimated at and
// what the supplier's records of the day charged, the difference in basis points rounded half away from zero, and the
// counts the matching reached; a supplier with no charge records has no days.
func TestSupplierReconciliation(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	var r adminapi.SupplierReconciliation
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier+"/reconciliation", "", as(f.s)), &r)
	expectSame(t, "no records", []any{len(r.Days), r.NativeUnit}, []any{0, adminapi.SupplierReconciliationNativeUnit("quota")})
	rows := reconciliationRows{supplier, f.group(supplier, "1"), uuid.NewV7().String(), uuid.NewV7().String(), uuid.NewV7().String()}
	// The admin's suppliers side reads the node's clock, which holds still: the days are its days, also across midnight.
	today := f.clock.Now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	f.run(append(rows.seed(),
		rows.attempt(yesterday.Add(time.Hour), "100", "'r1'"),
		rows.attempt(yesterday.Add(2*time.Hour), "33.335", "NULL"),
		rows.attempt(today, "0", "NULL"),
		rows.charge("c1", yesterday.Add(time.Hour), "140", "new"),
		rows.charge("c2", yesterday.Add(3*time.Hour), "0.0018", "held"),
	)...)
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier+"/reconciliation?days=3", "", as(f.s)), &r)
	// (140.0018 − 133.335) / 133.335 = 499.996 bp, which rounds to 500; the attempt with a reference no charge claimed
	// is past the grace.
	expectSame(t, "the days", reconciliationDays(r), []reconciliationDay{
		{Date: today.Format(time.DateOnly), Estimated: "0", Charged: "0"},
		{Date: yesterday.Format(time.DateOnly), Estimated: "133.335", Charged: "140.0018", DifferenceBP: new(500), Charges: 2, Held: 1, Late: 1},
		{Date: today.AddDate(0, 0, -2).Format(time.DateOnly), Estimated: "0", Charged: "0"},
	})
	expectAdmin(t, "too many days", f.do(http.MethodGet, "/suppliers/"+supplier+"/reconciliation?days=32", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)
}

// TestSupplierIntakeRule: the rule is replaced as of its revision; a rule that does not fit its mode is refused, a stale
// revision is a conflict, and the same rule changes nothing. A supplier the sync does not run has no rule to set.
func TestSupplierIntakeRule(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	path := "/suppliers/" + supplier + "/intake-rule"
	key := newKey()
	body := `{"mode":"names","include":["gpt-*"],"exclude":["*-mini"],"rev":0,"reason":"only these"}`
	var rule adminapi.SupplierIntakeRule
	rec := f.change(f.s, http.MethodPut, path, body, key)
	expectAdmin(t, "set", rec, http.StatusOK, "")
	decode(t, rec, &rule)
	expectSame(t, "the rule", rule, adminapi.SupplierIntakeRule{Mode: "names", Include: []string{"gpt-*"}, Exclude: []string{"*-mini"}, Rev: 1})
	expectAdmin(t, "replay", f.change(f.s, http.MethodPut, path, body, key), http.StatusOK, "")
	expectAdmin(t, "stale", f.change(f.s, http.MethodPut, path, `{"mode":"known","rev":0,"reason":"r"}`, newKey()), http.StatusConflict, codeStaleRevision)
	for name, bad := range map[string]string{
		"names without include": `{"mode":"names","rev":1,"reason":"r"}`,
		"listed with include":   `{"mode":"listed","include":["gpt-*"],"rev":1,"reason":"r"}`,
		"known with exclude":    `{"mode":"known","exclude":["gpt-*"],"rev":1,"reason":"r"}`,
	} {
		expectAdmin(t, name, f.change(f.s, http.MethodPut, path, bad, newKey()), http.StatusUnprocessableEntity, codeUnprocessable)
	}
	f.send(http.MethodPut, path, `{"mode":"names","include":["gpt-*"],"exclude":["*-mini"],"rev":1,"reason":"same"}`, http.StatusOK, &rule)
	expectSame(t, "the same rule", []any{rule.Rev, f.auditCount("supplier.intake_rule")}, []any{int64(1), "1"})
	f.send(http.MethodPut, path, `{"mode":"known","rev":1,"reason":"all we know"}`, http.StatusOK, &rule)
	expectSame(t, "known", rule, adminapi.SupplierIntakeRule{Mode: "known", Include: []string{}, Exclude: []string{}, Rev: 2})
	expectAdmin(t, "no supplier", f.change(f.s, http.MethodPut, "/suppliers/"+uuid.NewV7().String()+"/intake-rule", `{"mode":"known","rev":0,"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	for _, mode := range []string{"direct", "cloud"} {
		f.run("UPDATE suppliers.suppliers SET mode = '" + mode + "' WHERE id = '" + supplier + "'")
		expectAdmin(t, mode, f.change(f.s, http.MethodPut, path, `{"mode":"listed","rev":2,"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	}
	expectSame(t, "rules recorded at last", f.auditCount("supplier.intake_rule"), "2")
}

// inboxShows is what the queue lists and counts now: kind:credential@group of each key_invalid item, the total and the
// key_invalid count.
func (f relayFixture) inboxShows() []any {
	f.t.Helper()
	var list adminapi.SupplierInboxList
	decode(f.t, f.do(http.MethodGet, "/supplier-inbox?kind=key_invalid&supplier_id="+f.supplier, "", as(f.s)), &list)
	var summary adminapi.SupplierInboxSummary
	decode(f.t, f.do(http.MethodGet, "/supplier-inbox/summary", "", as(f.s)), &summary)
	items := []string{}
	for _, item := range list.Data {
		items = append(items, string(item.Kind)+":"+valueOf(item.CredentialId)+"@"+valueOf(item.GroupId))
	}
	return []any{items, summary.Total, summary.Kinds.KeyInvalid}
}

// TestSupplierInbox: a key the supplier refused at a check is in the queue at once, counted by kind; ignoring it hides
// it, and ignoring it again changes nothing; a check that passes takes it out. A key already rejected that a check
// finds refused again is in the queue too.
func TestSupplierInbox(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.relay.add("default", "sk-inbox-key-4444")
	var checks adminapi.CredentialCheckList
	f.send(http.MethodPost, f.path("/keys"), `{"group_id":"`+f.def+`","keys":["sk-inbox-key-4444"],"max_concurrency":1,"reason":"r"}`, http.StatusCreated, &checks)
	id := valueOf(checks.Data[0].CredentialId)
	f.relay.setRefuse(true)
	var check adminapi.CredentialCheck
	f.send(http.MethodPost, f.path("/credentials/"+id+"/tests"), `{"reason":"r"}`, http.StatusCreated, &check)
	expectSame(t, "the queue", f.inboxShows(), []any{[]string{"key_invalid:" + id + "@" + f.def}, int64(1), int64(1)})

	item := "/supplier-inbox/" + f.text("SELECT id::text FROM suppliers.inbox_items") + "/ignore"
	key := newKey()
	var ignored, again adminapi.SupplierInboxItem
	rec := f.change(f.s, http.MethodPost, item, `{"reason":"known, replacing it tomorrow"}`, key)
	expectAdmin(t, "ignore", rec, http.StatusOK, "")
	decode(t, rec, &ignored)
	expectAdmin(t, "replay", f.change(f.s, http.MethodPost, item, `{"reason":"known, replacing it tomorrow"}`, key), http.StatusOK, "")
	f.send(http.MethodPost, item, `{"reason":"again"}`, http.StatusOK, &again)
	expectSame(t, "ignored again", []any{again.IgnoredAt != nil, valueOf(again.IgnoredAt).Equal(valueOf(ignored.IgnoredAt)), f.auditCount("supplier.inbox.ignore")}, []any{true, true, "1"})
	expectSame(t, "the queue ignoring it", f.inboxShows(), []any{[]string{}, int64(0), int64(0)})
	expectAdmin(t, "no item", f.change(f.s, http.MethodPost, "/supplier-inbox/"+uuid.NewV7().String()+"/ignore", `{"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)

	f.relay.setRefuse(false)
	f.send(http.MethodPost, f.path("/credentials/"+id+"/tests"), `{"reason":"r"}`, http.StatusCreated, &check)
	expectSame(t, "items after the key passed", f.text("SELECT count(*)::text FROM suppliers.inbox_items"), "0")

	f.run("UPDATE suppliers.credentials SET status = 'rejected' WHERE id = '" + id + "'")
	f.relay.setRefuse(true)
	f.send(http.MethodPost, f.path("/credentials/"+id+"/tests"), `{"reason":"r"}`, http.StatusCreated, &check)
	expectSame(t, "the queue for a key rejected already", f.inboxShows(), []any{[]string{"key_invalid:" + id + "@" + f.def}, int64(1), int64(1)})
}
