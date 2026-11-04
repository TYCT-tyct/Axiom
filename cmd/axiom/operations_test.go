package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
)

const operationsBase = "/api/operations/v1"

// makeCredential has the owner make an operations credential through the admin and returns its ID and token.
func (f adminFixture) makeCredential(s session, body string) (id, token string) {
	f.t.Helper()
	rec := f.change(s, http.MethodPost, "/operations/credentials", body, newKey())
	expectAdmin(f.t, "createOperationsCredential", rec, http.StatusCreated, "")
	var made adminapi.OperationsCredentialToken
	decode(f.t, rec, &made)
	if made.Token == nil {
		f.t.Fatal("a new credential came without its token")
	}
	return made.CredentialId, *made.Token
}

// agent calls the operations API with token, as the MCP bridge does: a GET with the reason in the query.
func (f adminFixture) agent(token, path string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.agentSend(token, http.MethodGet, path, "", mods...)
}

// agentSend calls the operations API with token and a JSON body, which a write carries its reason in.
func (f adminFixture) agentSend(token, method, path, body string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequestWithContext(f.t.Context(), method, "https://core.internal:8084"+operationsBase+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = "10.0.0.9:4000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for _, m := range mods {
		m(r)
	}
	rec := httptest.NewRecorder()
	httpserver.Public(f.ops, slog.New(slog.DiscardHandler), bodyTimeout).ServeHTTP(rec, r) // as the listener wraps it
	return rec
}

func because(path, reason string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "reason=" + url.QueryEscape(reason)
}

// TestOperationsAPIReads: a credential the owner made reads what its permissions and scope allow, answered with the
// admin's bodies and uncached, and every call it makes, the refused ones too, is in its own operation log before the
// answer leaves, with the reason apart from the input; another credential's log is not its.
func TestOperationsAPIReads(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	id, token := f.makeCredential(owner, `{"name":"hermes","permissions":["suppliers.view","catalog.view","calls.search"],
		"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"start the agent"}`)
	f.expectAudit("operations_credential.create", map[string]string{"reason": "start the agent", "target_id": id})

	expectWhoami(t, f.agent(token, because("/me", "who am I"), header("X-Axiom-Run-ID", "run-1")), id, token)
	for _, path := range []string{"/suppliers", "/pending-mappings", "/models?status=listed", "/calls"} {
		expectAdmin(t, path, f.agent(token, because(path, "patrol")), http.StatusOK, "")
	}
	expectAdmin(t, "a read without a reason", f.agent(token, "/suppliers"), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "a model that does not exist", f.agent(token, because("/models/0198c0a8-0000-7000-8000-000000000002", "x")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "a permission it lacks", f.agent(token, because("/calls/0198c0a8-0000-7000-8000-000000000001", "x")), http.StatusForbidden, codeForbidden)

	expectReadLog(t, f.agent(token, because("/log?limit=20", "replay")))

	_, other := f.makeCredential(owner, `{"name":"codex","permissions":[],"platform":false,"proposer_vendors":["openai"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	var log operationsapi.OperationLogList
	decode(t, f.agent(other, because("/log", "mine only")), &log)
	if len(log.Data) != 0 {
		t.Errorf("another credential read %d entries", len(log.Data))
	}
}

// expectWhoami checks the credential's description: its own, never its token, and uncached.
func expectWhoami(t *testing.T, rec *httptest.ResponseRecorder, id, token string) {
	t.Helper()
	expectAdmin(t, "whoami", rec, http.StatusOK, "")
	var me operationsapi.AgentSelf
	decode(t, rec, &me)
	if me.Id != id || strings.Join(me.Permissions, ",") != "calls.search,catalog.view,operations.read,suppliers.view" ||
		!me.Platform || me.Sites != nil || strings.Contains(rec.Body.String(), token) {
		t.Errorf("whoami: %s", rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

// expectReadLog checks the log TestOperationsAPIReads leaves, newest first.
func expectReadLog(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	expectAdmin(t, "operationLogList", rec, http.StatusOK, "")
	var log operationsapi.OperationLogList
	decode(t, rec, &log)
	tools := make([]string, len(log.Data))
	for i, e := range log.Data {
		tools[i] = e.Tool + ":" + http.StatusText(e.Status)
	}
	want := []string{
		"call_get:Forbidden", "model_get:Not Found", "suppliers_list:Bad Request", "calls_list:OK", "models_list:OK",
		"pending_mappings_list:OK", "suppliers_list:OK", "whoami:OK",
	}
	if !slices.Equal(tools, want) {
		t.Fatalf("the log is %v, want %v", tools, want)
	}
	code := func(e operationsapi.OperationLogEntry) string { return deref(e.ErrorCode) }
	for _, c := range []struct {
		entry int
		what  string
		ok    func(operationsapi.OperationLogEntry) bool
	}{
		{0, "the refused call", func(e operationsapi.OperationLogEntry) bool {
			return code(e) == string(codeForbidden) && recordedInput(t, e)["path"] != nil
		}},
		{2, "the call without a reason", func(e operationsapi.OperationLogEntry) bool {
			return e.Reason == "" && code(e) == string(codeInvalidRequest)
		}},
		{4, "the reason apart from the input", func(e operationsapi.OperationLogEntry) bool {
			return e.Reason == "patrol" && recordedInput(t, e)["query"] != nil && !strings.Contains(string(e.Input), "reason")
		}},
		{7, "whoami with its run", func(e operationsapi.OperationLogEntry) bool {
			return deref(e.RunId) == "run-1" && e.RequestId != "" && !e.EndedAt.Before(e.StartedAt)
		}},
	} {
		if e := log.Data[c.entry]; !c.ok(e) {
			t.Errorf("%s is recorded as %+v", c.what, e)
		}
	}
}

// recordedInput is a log entry's input as JSON values.
func recordedInput(t *testing.T, e operationsapi.OperationLogEntry) map[string]any {
	t.Helper()
	var input map[string]any
	if err := jsonx.Unmarshal(e.Input, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

// TestOperationsAPIRefuses: no token, a malformed one, an API key and a revoked credential are unauthenticated alike; a
// credential without the platform grant finds no supplier, though it reads the calls of its sites; the control plane is
// not on the operations listener; and a call whose record cannot be written is not answered.
func TestOperationsAPIRefuses(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	revoked, revokedToken := f.makeCredential(owner, `{"name":"old","permissions":[],"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	expectAdmin(t, "revokeOperationsCredential", f.change(owner, http.MethodPost, "/operations/credentials/"+revoked+"/revoke", `{"reason":"leaked"}`, newKey()), http.StatusOK, "")
	_, sites := f.makeCredential(owner, `{"name":"sites","permissions":["suppliers.view","calls.search"],"platform":false,
		"sites":["`+f.siteID()+`"],"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)

	for name, token := range map[string]string{ // #nosec G101 -- tokens this test made or malformed on purpose
		"no token": "", "a malformed token": "op-ax-v1-nope", "an API key": "sk-ax-v1-" + revokedToken[len("op-ax-v1-"):],
		"a revoked credential": revokedToken,
	} {
		expectAdmin(t, name, f.agent(token, because("/me", "x")), http.StatusUnauthorized, codeUnauthenticated)
	}
	expectAdmin(t, "the platform without the grant", f.agent(sites, because("/suppliers", "x")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "its own site's calls", f.agent(sites, because("/calls", "x")), http.StatusOK, "")
	for _, path := range []string{"/api/admin/v1/operations/credentials", "/api/admin/v1/operations/agent/stop", "/api/operations/v1/approvals"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://core.internal:8084"+path+"?reason=x", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+sites)
		rec := httptest.NewRecorder()
		f.ops.ServeHTTP(rec, r)
		expectAdmin(t, "the control plane at "+path, rec, http.StatusNotFound, codeNotFound)
	}

	f.run(`ALTER TABLE operations.operation_log ADD CONSTRAINT refuse_all CHECK (false) NOT VALID`)
	rec := f.agent(sites, because("/me", "x"))
	expectAdmin(t, "a call that cannot be recorded", rec, http.StatusServiceUnavailable, codeUnavailable)
	if strings.Contains(rec.Body.String(), "operations.read") {
		t.Errorf("the unrecorded answer leaked: %s", rec.Body)
	}
}

// TestOperationsCredentialControlPlane: the owner makes credentials and stops them all; no credential can be given the
// control plane's permissions; a token is shown once, never on a replay nor in a stored response; and after the stop
// no credential works, a stopped one cannot be rotated back, and only a new one resumes.
func TestOperationsCredentialControlPlane(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	for _, perm := range []string{"operations.manage", "platform_grants.manage", "no.such"} {
		rec := f.change(owner, http.MethodPost, "/operations/credentials",
			`{"name":"n","permissions":["`+perm+`"],"platform":false,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`, newKey())
		expectAdmin(t, "a credential holding "+perm, rec, http.StatusBadRequest, codeInvalidRequest)
	}

	body := `{"name":"hermes","permissions":["suppliers.view"],"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`
	key := newKey()
	rec := f.change(owner, http.MethodPost, "/operations/credentials", body, key)
	expectAdmin(t, "createOperationsCredential", rec, http.StatusCreated, "")
	var made adminapi.OperationsCredentialToken
	decode(t, rec, &made)
	rec = f.change(owner, http.MethodPost, "/operations/credentials", body, key)
	var replay adminapi.OperationsCredentialToken
	decode(t, rec, &replay)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotency-Replayed") != "true" || replay.Token != nil || replay.CredentialId != made.CredentialId {
		t.Errorf("the replay: %d %s %s", rec.Code, rec.Header(), rec.Body)
	}
	if got := f.text("SELECT count(*)::text FROM platform.idempotency WHERE response::text LIKE '%op-ax-v1-%'"); got != "0" {
		t.Errorf("%s stored responses hold a token", got)
	}

	_, another := f.makeCredential(owner, `{"name":"codex","permissions":[],"platform":false,"proposer_vendors":["openai"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	rec = f.change(owner, http.MethodPost, "/operations/agent/stop", `{"reason":"it misbehaved"}`, newKey())
	expectAdmin(t, "stopOperationsAgent", rec, http.StatusOK, "")
	var stopped adminapi.OperationsAgentStopped
	decode(t, rec, &stopped)
	if stopped.Revoked != 2 {
		t.Errorf("the stop revoked %d, want 2", stopped.Revoked)
	}
	for _, token := range []string{*made.Token, another} {
		expectAdmin(t, "a credential after the stop", f.agent(token, because("/me", "x")), http.StatusUnauthorized, codeUnauthenticated)
	}
	f.expectAudit("operations_agent.stop", map[string]string{"reason": "it misbehaved", "actor_kind": "user"})
	expectAdmin(t, "rotating a stopped credential", f.change(owner, http.MethodPost, "/operations/credentials/"+made.CredentialId+"/rotate", `{"reason":"r"}`, newKey()),
		http.StatusNotFound, codeNotFound)
	_, resumed := f.makeCredential(owner, body)
	expectAdmin(t, "a credential made after the stop", f.agent(resumed, because("/me", "x")), http.StatusOK, "")
}

// TestOperationsCredentialRotation: a rotation shows the new token once and keeps the old one for its overlap, which a
// later rotation or an overlap of zero ends at once; the list shows only the last four characters.
func TestOperationsCredentialRotation(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	owner := f.owner()
	id, first := f.makeCredential(owner, `{"name":"hermes","permissions":[],"platform":false,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	rotate := func(body string) string {
		t.Helper()
		rec := f.change(owner, http.MethodPost, "/operations/credentials/"+id+"/rotate", body, newKey())
		expectAdmin(t, "rotateOperationsCredential", rec, http.StatusOK, "")
		var rotated adminapi.OperationsCredentialToken
		decode(t, rec, &rotated)
		return *rotated.Token
	}
	works := func(what, token string, ok bool) {
		t.Helper()
		if ok {
			expectAdmin(t, what, f.agent(token, because("/me", "x")), http.StatusOK, "")
		} else {
			expectAdmin(t, what, f.agent(token, because("/me", "x")), http.StatusUnauthorized, codeUnauthenticated)
		}
	}
	second := rotate(`{"reason":"scheduled"}`)
	works("the old token in its overlap", first, true)
	works("the new token", second, true)
	rec := f.do(http.MethodGet, "/operations/credentials", "", as(owner))
	var list adminapi.OperationsCredentialList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].TokenLast4 != second[len(second)-4:] || list.Data[0].PreviousTokenUntil == nil ||
		strings.Contains(rec.Body.String(), "op-ax-v1-") {
		t.Errorf("the list: %s", rec.Body)
	}
	third := rotate(`{"overlap_hours":0,"reason":"leaked"}`)
	works("a token replaced with no overlap", second, false)
	works("the earlier overlap ends with the next rotation", first, false)
	fourth := rotate(`{"reason":"scheduled"}`)
	works("a token in its overlap", third, true)
	works("the current token", fourth, true)
}
