package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/platform/adminapi"
)

func TestAdminAlertConfigurationTestAndResolve(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.do(http.MethodGet, "/alerts/configuration", "", as(s))
	expectAdmin(t, "getAlertConfiguration", rec, http.StatusOK, "")
	var config adminapi.AlertConfiguration
	decode(t, rec, &config)
	if config.Rev == nil || *config.Rev != 1 || config.Rules == nil || len(*config.Rules) != 4 {
		t.Fatalf("defaults: %s", rec.Body)
	}
	key := newKey()
	body := `{"rev":1,"rules":[],"channels":[{"id":"ops","email":"ops@example.com"}],"reason":"enable alerts"}`
	expectAdmin(t, "setAlertConfiguration", f.change(s, http.MethodPut, "/alerts/configuration", body, key), http.StatusCreated, "")
	rec = f.change(s, http.MethodPut, "/alerts/configuration", body, key)
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("configuration did not replay")
	}
	expectAdmin(t, "stale configuration", f.change(s, http.MethodPut, "/alerts/configuration", body, newKey()), http.StatusConflict, codeStaleRevision)
	f.expectAudit("alert_configuration.update", map[string]string{"reason": "enable alerts"})
	expectAdmin(t, "sendAlertTest", f.change(s, http.MethodPost, "/alerts/tests", `{"channel":"ops","reason":"check delivery"}`, newKey()), http.StatusCreated, "")
	rec = f.do(http.MethodGet, "/alerts/events", "", as(s))
	expectAdmin(t, "listAlertEvents", rec, http.StatusOK, "")
	var events adminapi.AlertEventList
	decode(t, rec, &events)
	if events.Data == nil || len(*events.Data) != 1 {
		t.Fatalf("events: %s", rec.Body)
	}
	event := (*events.Data)[0]
	if event.Id == nil || event.Receipts == nil || len(*event.Receipts) != 1 {
		t.Fatal("test did not persist receipt")
	}
	path := "/alerts/events/" + *event.Id + "/resolve"
	key = newKey()
	expectAdmin(t, "resolveAlertEvent", f.change(s, http.MethodPost, path, `{"reason":"investigated"}`, key), http.StatusCreated, "")
	rec = f.change(s, http.MethodPost, path, `{"reason":"investigated"}`, key)
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("resolution did not replay")
	}
	expectAdmin(t, "missing incident", f.change(s, http.MethodPost, "/alerts/events/"+uuid.NewV7().String()+"/resolve", `{"reason":"investigated"}`, newKey()), http.StatusNotFound, codeNotFound)
}
