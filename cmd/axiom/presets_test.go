package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

func TestPresetAPIs(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	owner := f.owner()
	manager := f.managementKey(owner, `"name":"manager"`)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, "https://"+managementHost+"/api/v1/presets"+path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:4000"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		withKeyManagement(f.routes, managementAPI{accounts: f.svc}).ServeHTTP(rec, req)
		return rec
	}
	check := func(rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("got %d: %s, want %d", rec.Code, rec.Body, status)
		}
	}
	check(request("POST", "", `{"slug":"support","name":"Support","config":{"model":"m","temperature":0.5}}`, manager.Secret), 201)
	check(request("GET", "/support", "", manager.Secret), 200)
	check(request("PATCH", "/support", `{"name":"Updated"}`, manager.Secret), 200)
	rec := f.do("GET", "/presets/support", "", as(owner))
	check(rec, 200)
	var data accounts.Preset
	decode(t, rec, &data)
	if data.Name != "Updated" || !strings.Contains(string(data.Config), "temperature") {
		t.Fatalf("PATCH lost config: %+v", data)
	}
	check(f.do("PUT", "/presets/support", `{"name":"Console","config":{"model":"new","seed":9007199254740993}}`, as(owner)), 200)
	var seed string
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT config->>'seed' FROM accounts.presets WHERE slug='support' AND deleted_at IS NULL`).Scan(&seed)
	}); err != nil {
		t.Fatal(err)
	}
	if seed != "9007199254740993" {
		t.Fatalf("console rounded preset seed: %s", seed)
	}
	check(f.do("GET", "/presets?limit=1&offset=0", "", as(owner)), 200)
	check(request("POST", "", `{"slug":"support","name":"Duplicate","config":{}}`, manager.Secret), 409)
	check(request("GET", "?limit=0", "", manager.Secret), 400)
	check(request("POST", "", `{"slug":"bad","name":"Bad","config":{"messages":[]}}`, manager.Secret), 400)
	invalid := `{"slug":"nul","name":"Invalid","config":{"system":"a\u0000b"}}`
	check(request("POST", "", invalid, manager.Secret), 400)
	check(f.do("POST", "/presets", invalid, as(owner)), 422)
	check(f.do("POST", "/presets", `{"slug":"console","name":"Console","config":{}}`, as(owner)), 201)
	check(f.do("DELETE", "/presets/console", "", as(owner)), 204)
	check(request("DELETE", "/support", "", manager.Secret), 200)
	check(request("GET", "/support", "", manager.Secret), 404)
	callKey := f.createKey(owner, `{"name":"call"}`)
	check(request("GET", "", "", callKey.Secret), 401)
	check(f.do("POST", "/presets", `{"slug":"csrf","name":"CSRF","config":{}}`, header("Cookie", owner.cookie.String())), 403)
}
