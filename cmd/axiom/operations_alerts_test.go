package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axiom/internal/platform/operationsapi"
)

// alertAs asks, as the agent, to raise the alert of body under key, or to resolve it when path is /alerts/resolve.
func (f adminFixture) alertAs(token, path, body, key string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodPost, path, body, idempotent(key))
}

// alerter has the owner make a credential that may raise alerts and read the platform's.
func (f adminFixture) alerter(owner session) string {
	f.t.Helper()
	_, token := f.makeCredential(owner, `{"name":"hermes","permissions":["operations.write","calls.search"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"tell the owner"}`)
	return token
}

// raise is the body of an alert under key.
func raise(key, severity, summary string) string {
	return fmt.Sprintf(`{"key":%q,"severity":%q,"summary":%q,"reason":"the patrol saw it"}`, key, severity, summary)
}

// TestOperationsAlertRaise: the agent's alert is a signal of its own kind under its key, written with the operation and
// audited as the agent's; a retry under the key raises nothing more and the key with another alert is refused. A
// resolution is a recovery signal for the same key.
func TestOperationsAlertRaise(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	token, key := f.alerter(f.owner()), newKey()
	body := raise("supplier/acme/balance", "warning", "Acme 余额只够 2 小时")
	rec := f.alertAs(token, "/alerts", body, key)
	raised := agentOperation(t, rec)
	if rec.Code != http.StatusOK || raised.State != operationsapi.OperationStateSucceeded || raised.Target.Kind != "alert" ||
		!strings.Contains(rec.Body.String(), `"kind":"operations.agent_alert"`) {
		t.Fatalf("raising: %d %s", rec.Code, rec.Body)
	}
	signal := `SELECT concat_ws('|', body->>'key', body->>'severity', body->'fields'->>'summary', body->'fields'->>'operation', coalesce(body->>'resolved', 'false'))
 FROM platform.alert_signals WHERE body->>'kind' = 'operations.agent_alert' ORDER BY seq`
	want := "supplier/acme/balance|warning|Acme 余额只够 2 小时|" + *raised.OperationId + "|false"
	if got := f.text(signal); got != want {
		t.Errorf("the signal is %q, want %q", got, want)
	}
	rec = f.alertAs(token, "/alerts", body, key)
	if again := agentOperation(t, rec); rec.Header().Get("Idempotency-Replayed") != "true" || *again.OperationId != *raised.OperationId {
		t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "the key with another alert", f.alertAs(token, "/alerts", raise("supplier/acme/balance", "critical", "Acme 没钱了"), key),
		http.StatusConflict, codeIdempotencyReuse)
	f.expectResolved(token, "supplier/acme/balance")
}

// expectResolved has the agent resolve the alert it raised under key, which is a recovery signal for the key; the
// raise and the resolution are both audited as the agent's.
func (f adminFixture) expectResolved(token, key string) {
	f.t.Helper()
	rec := f.alertAs(token, "/alerts/resolve", `{"key":"`+key+`","reason":"the owner topped it up"}`, newKey())
	if resolved := agentOperation(f.t, rec); rec.Code != http.StatusOK || resolved.State != operationsapi.OperationStateSucceeded {
		f.t.Fatalf("resolving: %d %s", rec.Code, rec.Body)
	}
	resolution := `SELECT body->>'key' FROM platform.alert_signals WHERE body->>'kind' = 'operations.agent_alert' AND (body->>'resolved')::bool`
	if got, n := f.text(resolution), f.text(`SELECT count(*)::text FROM platform.alert_signals`); got != key || n != "2" {
		f.t.Errorf("the resolution is of %q among %s signals", got, n)
	}
	if got := f.text(`SELECT string_agg(actor_kind || ':' || reason, ',' ORDER BY at, id) FROM platform.audit_log WHERE action = 'operation.succeed'`); got != "operations_agent:the patrol saw it,operations_agent:the owner topped it up" {
		f.t.Errorf("the audit entries: %s", got)
	}
}

// TestOperationsAlertRefusals: a preview, a summary over 512 bytes, a malformed key and a credential without
// operations.write leave no operation and no signal; once the alerts the hour starts with are spent, previews
// included, the agent is held back, but may still resolve.
func TestOperationsAlertRefusals(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	token := f.alerter(owner)
	rec := f.alertAs(token, "/alerts", strings.TrimSuffix(raise("k", "info", "x"), "}")+`,"dry_run":true}`, newKey())
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStateApproved {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	expectAdmin(t, "171 Chinese characters", f.alertAs(token, "/alerts", raise("k", "info", strings.Repeat("钱", 171)), newKey()),
		http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "a key with a space", f.alertAs(token, "/alerts", raise("a key", "info", "x"), newKey()), http.StatusBadRequest, codeInvalidRequest)
	_, reader := f.makeCredential(owner, `{"name":"reader","permissions":["calls.search"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"patrol only"}`)
	expectAdmin(t, "a credential without operations.write", f.alertAs(reader, "/alerts", raise("k", "info", "x"), newKey()),
		http.StatusForbidden, codeForbidden)
	if ops, signals := f.text(`SELECT count(*)::text FROM operations.operations`), f.text(`SELECT count(*)::text FROM platform.alert_signals`); ops != "0" || signals != "0" {
		t.Errorf("%s operations and %s signals after the refusals and the preview", ops, signals)
	}

	for i := range 29 { // the preview spent one of the 30 the bucket starts with; the refused requests none
		expectAdmin(t, fmt.Sprintf("alert %d", i), f.alertAs(token, "/alerts", raise(fmt.Sprintf("k%d", i), "info", "x"), newKey()), http.StatusOK, "")
	}
	rec = f.alertAs(token, "/alerts", raise("one-more", "info", "x"), newKey())
	expectAdmin(t, "an alert past the limit", rec, http.StatusTooManyRequests, codeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the refusal does not say when to come back")
	}
	expectAdmin(t, "a resolution past it", f.alertAs(token, "/alerts/resolve", `{"key":"k0","reason":"it passed"}`, newKey()), http.StatusOK, "")
}

// TestOperationsAlertsList: the platform's incidents newest first, page by page, only the open ones when asked; a
// credential without the platform grant finds none.
func TestOperationsAlertsList(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.run(`INSERT INTO observability.alert_events(id, kind, key, severity, fields, state, fired_at, resolved_at) VALUES
 ('01900000-0000-7000-8000-000000000001', 'suppliers.low_balance', 'acme', 'warning', '{}', 'resolved', now(), now()),
 ('01900000-0000-7000-8000-000000000002', 'operations.agent_alert', 'supplier/acme/balance', 'warning', '{"summary":"low"}', 'delivered', now(), NULL),
 ('01900000-0000-7000-8000-000000000003', 'queue.discarded', 'mail.send', 'critical', '{}', 'firing', now(), NULL)`)
	owner := f.owner()
	token := f.alerter(owner)
	keys := func(path string) (string, *string) {
		t.Helper()
		rec := f.agent(token, because(path, "what is wrong"))
		var list operationsapi.OperationsAlertList
		decode(t, rec, &list)
		var got []string
		for _, e := range list.Data {
			got = append(got, *e.Key)
		}
		return strings.Join(got, ","), list.NextCursor
	}
	if got, next := keys("/alerts"); got != "mail.send,supplier/acme/balance,acme" || next != nil {
		t.Errorf("every incident: %s, next %v", got, next)
	}
	if got, _ := keys("/alerts?open=true"); got != "mail.send,supplier/acme/balance" {
		t.Errorf("the open ones: %s", got)
	}
	got, next := keys("/alerts?limit=2")
	if got != "mail.send,supplier/acme/balance" || next == nil {
		t.Fatalf("the first page: %s, next %v", got, next)
	}
	if got, next := keys("/alerts?limit=2&cursor=" + *next); got != "acme" || next != nil {
		t.Errorf("the second page: %s, next %v", got, next)
	}
	_, sites := f.makeCredential(owner, `{"name":"sites","permissions":["calls.search"],"platform":false,
		"sites":["`+f.siteID()+`"],"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	expectAdmin(t, "the platform's alerts without the grant", f.agent(sites, because("/alerts", "x")), http.StatusNotFound, codeNotFound)
}
