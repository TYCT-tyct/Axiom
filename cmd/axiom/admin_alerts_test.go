package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
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

func TestAdminWebhookSecretIsSealedAuditedAndIdempotent(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		c, err := observability.GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels = []observability.AlertChannel{{ID: "hook", WebhookURL: "https://receiver.example/hook"}}
		return observability.SetAlertConfiguration(t.Context(), tx, c)
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"rev":2,"secret":"` + strings.Repeat("s", 32) + `","reason":"rotate receiver secret"}`
	key := newKey()
	path := "/alerts/channels/hook/secret"
	rec := f.change(s, http.MethodPut, path, body, key)
	expectAdmin(t, "rotateAlertWebhookSecret", rec, http.StatusCreated, "")
	if strings.Contains(rec.Body.String(), strings.Repeat("s", 32)) {
		t.Fatal("secret in response")
	}
	replay := f.change(s, http.MethodPut, path, body, key)
	if replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != rec.Body.String() {
		t.Fatalf("rotation replay: %s", replay.Body)
	}
	expectAdmin(t, "stale secret rotation", f.change(s, http.MethodPut, path, body, newKey()), http.StatusConflict, codeStaleRevision)
	f.expectAudit("alert_channel.rotate_secret", map[string]string{"reason": "rotate receiver secret"})
	if n := f.text(`SELECT count(*)::text FROM observability.webhook_keys`); n != "1" {
		t.Fatal("rotation repeated", n)
	}
	rec = f.do(http.MethodGet, "/alerts/configuration", "", as(s))
	if strings.Contains(rec.Body.String(), strings.Repeat("s", 32)) || strings.Contains(rec.Body.String(), "ciphertext") {
		t.Fatal("secret in configuration")
	}
	// The production egress client refuses a literal private address before it can be saved.
	body = `{"rev":3,"rules":[],"channels":[{"id":"private","webhook_url":"https://127.0.0.1/hook"}],"reason":"invalid target"}`
	expectAdmin(t, "private webhook", f.change(s, http.MethodPut, "/alerts/configuration", body, newKey()), http.StatusUnprocessableEntity, codeUnprocessable)
}

func TestAdminWebhookPreflightUsesDomainValidation(t *testing.T) {
	for _, raw := range []string{
		"http://receiver.example/hook",
		"https://user@receiver.example/hook",
		"https://receiver.example/hook#fragment",
		"https://receiver.example/" + strings.Repeat("x", 2048),
	} {
		body, err := alertJSON[adminapi.AlertConfigurationRequest](observability.AlertConfiguration{Channels: []observability.AlertChannel{{ID: "hook", WebhookURL: raw}}})
		if err != nil {
			t.Fatal(err)
		}
		// No egress client: every invalid URL must fail before network preflight.
		if err := (adminAPI{}).checkAlertDestinations(t.Context(), &body); !errors.Is(err, observability.ErrAlertConfiguration) {
			t.Fatalf("invalid webhook reached egress: %v", err)
		}
	}
}
