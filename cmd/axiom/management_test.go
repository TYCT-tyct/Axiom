package main

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/gateway"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

const managementHost = "api.main.example"

func (f consoleFixture) managementKey(s session, settings string) consoleapi.CreatedKey {
	f.t.Helper()
	return f.createKey(s, `{"kind":"management","expires_at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`",`+settings+`}`)
}

func (f consoleFixture) managementRequest(method, path, body, token string, mods ...mod) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequestWithContext(f.t.Context(), method, "https://"+managementHost+"/api/v1/keys"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:4000"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	for _, modify := range mods {
		modify(r)
	}
	rec := httptest.NewRecorder()
	withKeyManagement(f.routes, managementAPI{accounts: f.svc, keyLookup: gateway.DefaultConfig().KeyLookup}).ServeHTTP(rec, r)
	return rec
}

// managedData is the wire contract, with nullable limits and timestamps distinct from omitted values.
type managedData struct {
	Hash      string     `json:"hash"`
	Name      string     `json:"name"`
	Limit     *float64   `json:"limit"`
	Remaining *float64   `json:"limit_remaining"`
	Reset     *string    `json:"limit_reset"`
	Expires   *time.Time `json:"expires_at"`
	Updated   *time.Time `json:"updated_at"`
	Disabled  bool       `json:"disabled"`
	Workspace string     `json:"workspace_id"`
	Usage     float64    `json:"usage"`
}

func readManaged(t *testing.T, rec *httptest.ResponseRecorder, status int) (managedData, string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("response %d: %s, want %d", rec.Code, rec.Body, status)
	}
	var reply struct {
		Data managedData `json:"data"`
		Key  string      `json:"key"`
	}
	decode(t, rec, &reply)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable key response")
	}
	return reply.Data, reply.Key
}

func checkManagedCreation(t *testing.T, created managedData, expires *time.Time) {
	t.Helper()
	want := managedData{
		Hash: created.Hash, Workspace: created.Workspace, Name: "child", Limit: new(1.000000001),
		Remaining: new(1.000000001), Reset: new("daily"), Expires: expires,
	}
	if diff := cmp.Diff(want, created); diff != "" {
		t.Fatal(diff)
	}
}

func checkManagedPatch(t *testing.T, patched, before managedData) {
	t.Helper()
	want := before
	want.Name, want.Disabled, want.Updated = "renamed", true, patched.Updated
	if diff := cmp.Diff(want, patched); diff != "" {
		t.Fatal(diff)
	}
	if patched.Updated == nil {
		t.Fatal("PATCH did not timestamp update")
	}
}

// TestManagementKeyLookupHasTheGatewaysBudget: a management request's lookup of a key not yet cached has the gateway's
// KeyLookup, not the five seconds the whole request has. The deadline is read where the lookup takes its slot, so the
// test compares it with the budget instead of timing the lookup.
func TestManagementKeyLookupHasTheGatewaysBudget(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	// The lookup outlives a request whose budget ran out, so it hands over what it saw instead of writing a variable.
	// The request takes a slot again for the key manager after the lookup; only the first, the lookup's, is kept.
	type seen struct {
		remaining time.Duration
		set       bool
	}
	lookups := make(chan seen, 1)
	cold, err := accounts.NewService(f.db, accounts.Config{
		Platform: pg.PlatformScope(), Hashes: macKeys(t), Keyring: masterKeys(t), Now: f.clock.Now,
		LookupSlot: func(ctx context.Context) (func(), error) {
			deadline, set := ctx.Deadline()
			select {
			case lookups <- seen{time.Until(deadline), set}:
			default:
			}
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cold.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	const budget = 100 * time.Millisecond
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+managementHost+"/api/v1/keys", nil)
	r.Header.Set("Authorization", "Bearer "+manager.Secret)
	withKeyManagement(f.routes, managementAPI{accounts: cold, keyLookup: budget}).ServeHTTP(httptest.NewRecorder(), r)
	if got := <-lookups; !got.set || got.remaining > budget {
		t.Fatalf("the lookup had %v left (deadline set %v), want at most the %v budget", got.remaining, got.set, budget)
	}
}

func TestManagementAPIProvisioningLifecycle(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager","spend_limit":{"amount":"2000000000","currency":"USD"},"limit_reset":"daily"`)
	if diff := cmp.Diff(new(consoleapi.KeyKind(accounts.ManagementKey)), manager.Key.Kind); diff != "" {
		t.Fatal(diff)
	}
	created, secret := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"child","limit":1.000000001,"limit_reset":"daily","include_byok_in_limit":true}`, manager.Secret), http.StatusCreated)
	checkManagedCreation(t, created, manager.Key.ExpiresAt)
	if secret == "" {
		t.Fatal("create did not return secret")
	}
	if got := f.text(`SELECT spend_limit::text FROM accounts.api_keys WHERE name='child'`); got != "1000000001" {
		t.Fatal("inexact spend limit", got)
	}
	read, noSecret := readManaged(t, f.managementRequest(http.MethodGet, "/"+created.Hash, "", manager.Secret), http.StatusOK)
	if noSecret != "" || read.Hash != created.Hash {
		t.Fatal("read disclosed text or changed hash")
	}
	patched, _ := readManaged(t, f.managementRequest(http.MethodPatch, "/"+created.Hash, `{"name":"renamed","disabled":true}`, manager.Secret), http.StatusOK)
	checkManagedPatch(t, patched, created)
	var list struct {
		Data []managedData `json:"data"`
	}
	decode(t, f.managementRequest(http.MethodGet, "", "", manager.Secret), &list)
	if len(list.Data) != 0 {
		t.Fatal("default list included disabled key", list)
	}
	decode(t, f.managementRequest(http.MethodGet, "?include_disabled=true", "", manager.Secret), &list)
	if diff := cmp.Diff([]managedData{patched}, list.Data); diff != "" {
		t.Fatal(diff)
	}
	decode(t, f.managementRequest(http.MethodGet, "?include_disabled=true&offset=1", "", manager.Secret), &list)
	if len(list.Data) != 0 {
		t.Fatal("offset ignored")
	}
	rec := f.managementRequest(http.MethodDelete, "/"+created.Hash, "", manager.Secret)
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	decode(t, rec, &deleted)
	if diff := cmp.Diff(struct {
		Code    int
		Deleted bool
	}{http.StatusOK, true}, struct {
		Code    int
		Deleted bool
	}{rec.Code, deleted.Deleted}); diff != "" {
		t.Fatal("delete", rec.Code, rec.Body)
	}
	if rec := f.managementRequest(http.MethodGet, "/"+created.Hash, "", manager.Secret); rec.Code != http.StatusNotFound {
		t.Fatal("deleted read", rec.Code)
	}
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE actor_kind='management_key'`); got != "3" {
		t.Fatal("audit", got)
	}
}

func TestManagementAPIRejectsEscalationAndMalformedInput(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	manager := f.managementKey(s, `"name":"manager","spend_limit":{"amount":"2000000000","currency":"USD"},"limit_reset":"daily","models":["allowed"]`)
	child, _ := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"child"}`, manager.Secret), http.StatusCreated)
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"self", http.MethodPatch, "/" + managementHash(t, manager.Key.Id), `{"limit":999}`, http.StatusNotFound},
		{"mint manager", http.MethodPost, "", `{"name":"x","kind":"management"}`, http.StatusBadRequest},
		{"forge account", http.MethodPost, "", `{"name":"x","account_id":"other"}`, http.StatusBadRequest},
		{"forge creator", http.MethodPost, "", `{"name":"x","creator_user_id":"other"}`, http.StatusForbidden},
		{"higher limit", http.MethodPatch, "/" + child.Hash, `{"limit":3}`, http.StatusForbidden},
		{"remove limit alone", http.MethodPatch, "/" + child.Hash, `{"limit":null}`, http.StatusForbidden},
		{"remove limit", http.MethodPatch, "/" + child.Hash, `{"limit":null,"limit_reset":null}`, http.StatusForbidden},
		{"change reset", http.MethodPatch, "/" + child.Hash, `{"limit_reset":"monthly"}`, http.StatusForbidden},
		{"remove model scope", http.MethodPatch, "/" + child.Hash, `{"models":null}`, http.StatusForbidden},
		{"budget", http.MethodPatch, "/" + child.Hash, `{"budget":999}`, http.StatusBadRequest},
		{"change expiry", http.MethodPatch, "/" + child.Hash, `{"expires_at":"2099-01-01T00:00:00Z"}`, http.StatusBadRequest},
		{"overflow", http.MethodPost, "", `{"name":"x","limit":9223372037}`, http.StatusBadRequest},
		{"huge exponent", http.MethodPost, "", `{"name":"x","limit":1e999999999}`, http.StatusBadRequest},
		{"sub nano", http.MethodPost, "", `{"name":"x","limit":1e-10}`, http.StatusBadRequest},
		{"negative", http.MethodPost, "", `{"name":"x","limit":-1}`, http.StatusBadRequest},
		{"string limit", http.MethodPost, "", `{"name":"x","limit":"1"}`, http.StatusBadRequest},
		{"duplicate", http.MethodPost, "", `{"name":"x","limit":1,"limit":0}`, http.StatusBadRequest},
		{"trailing JSON", http.MethodPost, "", `{"name":"x"}{}`, http.StatusBadRequest},
		{"null name", http.MethodPost, "", `{"name":null}`, http.StatusBadRequest},
		{"empty reset", http.MethodPatch, "/" + child.Hash, `{"limit_reset":""}`, http.StatusBadRequest},
		{"empty patch", http.MethodPatch, "/" + child.Hash, `{}`, http.StatusBadRequest},
		{"offset", http.MethodGet, "?offset=-1", "", http.StatusBadRequest},
		{"duplicate query", http.MethodGet, "?offset=0&offset=1", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.managementRequest(tc.method, tc.path, tc.body, manager.Secret)
			if rec.Code != tc.status {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, tc.status)
			}
			var failure struct {
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			decode(t, rec, &failure)
			if failure.Error.Code != rec.Code {
				t.Fatal("not an OpenRouter error")
			}
		})
	}
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE actor_kind='management_key'`); got != "1" {
		t.Fatal("refused mutations changed audit", got)
	}
}

func managementHash(t *testing.T, raw string) string {
	t.Helper()
	id, err := accounts.ParseKeyID(raw)
	if err != nil {
		t.Fatal(err)
	}
	u := uuid.UUID(id)
	return hex.EncodeToString(u[:])
}

func TestManagementAPIAuthenticationAndRate(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	manager := f.managementKey(s, `"name":"manager"`)
	call := f.createKey(s, `{"name":"call"}`)
	for name, modify := range map[string]mod{
		"call key":       header("Authorization", "Bearer "+call.Secret),
		"cookie alone":   func(r *http.Request) { r.Header.Del("Authorization"); r.AddCookie(s.cookie) },
		"console domain": atHost(consoleHost),
		"unknown domain": atHost("unknown.example"),
	} {
		rec := f.managementRequest(http.MethodGet, "", "", manager.Secret, modify)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	f.run(`UPDATE accounts.api_keys SET management_window=statement_timestamp()+interval '1 day', management_requests=60 WHERE kind='management'`)
	rec := f.managementRequest(http.MethodGet, "", "", manager.Secret)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatal("rate", rec.Code, rec.Body)
	}
	f.run(`UPDATE accounts.api_keys SET status='disabled' WHERE kind='management'`)
	rec = f.managementRequest(http.MethodGet, "", "", manager.Secret)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("cached manager was admitted", rec.Code)
	}
}

func TestManagementAPIUnlimitedAndPageFilters(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	child, _ := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"child","limit":5,"limit_reset":"daily"}`, manager.Secret), http.StatusCreated)
	cleared, _ := readManaged(t, f.managementRequest(http.MethodPatch, "/"+child.Hash, `{"limit":null}`, manager.Secret), http.StatusOK)
	if cleared.Limit != nil || cleared.Reset != nil || cleared.Remaining != nil {
		t.Fatalf("null did not clear: %+v", cleared)
	}
	f.run(`UPDATE accounts.api_keys SET expires_at=statement_timestamp()-interval '1 second' WHERE kind='call'`)
	workspace := uuid.NewV7()
	f.run(`INSERT INTO accounts.workspaces(id,site_id,account_id,name,status,is_default)
        SELECT '` + workspace.String() + `',site_id,account_id,'other','active',false FROM accounts.workspaces WHERE is_default`)
	readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"other workspace","workspace_id":"`+workspace.String()+`"}`, manager.Secret), http.StatusCreated)
	var list struct {
		Data []managedData `json:"data"`
	}
	decode(t, f.managementRequest(http.MethodGet, "", "", manager.Secret), &list)
	if len(list.Data) != 0 {
		t.Fatal("included expired key")
	}
	decode(t, f.managementRequest(http.MethodGet, "?include_expired=true&workspace_id="+child.Workspace, "", manager.Secret), &list)
	if len(list.Data) != 1 {
		t.Fatal("expiry/workspace filter", list)
	}
	decode(t, f.managementRequest(http.MethodGet, "?workspace_id="+workspace.String(), "", manager.Secret), &list)
	if len(list.Data) != 1 || list.Data[0].Name != "other workspace" {
		t.Fatal("explicit workspace filter", list)
	}
}

func TestManagementAPICannotCrossAccounts(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	if _, err := f.user(secondPW, "create", "--email", "other@example.com"); err != nil {
		t.Fatal(err)
	}
	other := f.createKey(f.signIn("other@example.com", secondPW, atHost(strings.ToUpper(consoleHost))), `{"name":"other"}`)
	hash := managementHash(t, other.Key.Id)
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		rec := f.managementRequest(method, "/"+hash, `{"name":"stolen"}`, manager.Secret)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s crossed account: %d %s", method, rec.Code, rec.Body)
		}
	}
	workspace, err := accounts.ParseWorkspaceID(other.Key.WorkspaceId)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.managementRequest(http.MethodPost, "", `{"name":"stolen","workspace_id":"`+uuid.UUID(workspace).String()+`"}`, manager.Secret)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("created across account: %d %s", rec.Code, rec.Body)
	}
}

func TestManagementIssuanceNeedsEveryGrantedPermission(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	owner := f.owner()
	expect(t, "expiry required", f.do(http.MethodPost, "/keys", `{"name":"manager","kind":"management"}`, as(owner)), http.StatusBadRequest, "invalid_request")
	f.run(`UPDATE accounts.roles SET permissions='{api_keys.manage}', additional_permissions='{}' WHERE name='owner'`)
	f = f.restarted()
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec := f.do(http.MethodPost, "/keys", `{"name":"manager","kind":"management","expires_at":"`+expiry+`"}`, as(owner))
	expect(t, "cannot grant view", rec, http.StatusForbidden, "forbidden")
}

type managementBodyProbe struct {
	io.Reader
	check func()
}

func (p *managementBodyProbe) Read(b []byte) (int, error) {
	if p.check != nil {
		p.check()
		p.check = nil
	}
	return p.Reader.Read(b)
}

func TestManagementBodyCannotHoldDatabaseLocks(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	id, err := accounts.ParseKeyID(manager.Key.Id)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	probe := &managementBodyProbe{Reader: strings.NewReader(`{"name":"child"}`), check: func() {
		checked = true
		err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			var found uuid.UUID
			return tx.QueryRow(t.Context(), `SELECT id FROM accounts.api_keys WHERE id=$1 FOR UPDATE NOWAIT`, uuid.UUID(id)).Scan(&found)
		})
		if err != nil {
			t.Errorf("request body read while holding manager row lock: %v", err)
		}
	}}
	rec := f.managementRequest(http.MethodPost, "", "", manager.Secret, func(r *http.Request) { r.Body = io.NopCloser(probe) })
	readManaged(t, rec, http.StatusCreated)
	if !checked {
		t.Fatal("request body was never read")
	}
}

func TestManagementAPIPageIsBounded(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	f.run(`INSERT INTO accounts.api_keys(id,site_id,account_id,workspace_id,kind,name,prefix,last4,secret_hash,secret_version,created_by,status)
        SELECT gen_random_uuid(),site_id,account_id,workspace_id,'call','bulk-'||n,prefix,'test',
            sha256(convert_to(n::text,'UTF8')),secret_version,created_by,'active'
        FROM accounts.api_keys CROSS JOIN generate_series(1,101) n WHERE kind='management'`)
	var list struct {
		Data []managedData `json:"data"`
	}
	decode(t, f.managementRequest(http.MethodGet, "", "", manager.Secret), &list)
	if len(list.Data) != 100 {
		t.Fatal("unbounded first page", len(list.Data))
	}
	first := list.Data[0].Hash
	decode(t, f.managementRequest(http.MethodGet, "?offset=100", "", manager.Secret), &list)
	if len(list.Data) != 1 || list.Data[0].Hash == first {
		t.Fatal("offset page", list)
	}
}

func TestManagementAPICanDisableLegacyKeys(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	owner := f.owner()
	legacy := f.createKey(owner, `{"name":"legacy"}`)
	manager := f.managementKey(owner, `"name":"manager"`)
	hash := managementHash(t, legacy.Key.Id)
	disabled, _ := readManaged(t, f.managementRequest(http.MethodPatch, "/"+hash, `{"disabled":true}`, manager.Secret), http.StatusOK)
	if !disabled.Disabled || disabled.Expires != nil {
		t.Fatal("legacy key was not disabled", disabled)
	}
	rec := f.managementRequest(http.MethodPatch, "/"+hash, `{"disabled":false}`, manager.Secret)
	if rec.Code != http.StatusForbidden {
		t.Fatal("temporary manager enabled permanent key", rec.Code, rec.Body)
	}
}

func TestManagementPATCHSkipsUnchangedSettings(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	manager := f.managementKey(f.owner(), `"name":"manager"`)
	child, _ := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"child","models":["a"]}`, manager.Secret), http.StatusCreated)
	if child.Limit != nil || manager.Key.SpendLimit != nil {
		t.Fatal("management keys inherited call-key default")
	}
	for _, body := range []string{`{"include_byok_in_limit":false}`, `{"name":"child","models":["a"]}`} {
		unchanged, _ := readManaged(t, f.managementRequest(http.MethodPatch, "/"+child.Hash, body, manager.Secret), http.StatusOK)
		if unchanged.Updated != nil {
			t.Fatal("no-op PATCH updated timestamp")
		}
	}
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE actor_kind='management_key' AND action='api_key.update'`); got != "0" {
		t.Fatal("no-op PATCH wrote audit", got)
	}
	readManaged(t, f.managementRequest(http.MethodPatch, "/"+child.Hash, `{"models":["b"]}`, manager.Secret), http.StatusOK)
	if got := f.text(`SELECT array_to_string(models,',') FROM accounts.api_keys WHERE name='child'`); got != "b" {
		t.Fatal("model patch mistaken for no-op", got)
	}
	if got := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE actor_kind='management_key' AND action='api_key.update'`); got != "1" {
		t.Fatal("model patch missing audit", got)
	}
}
