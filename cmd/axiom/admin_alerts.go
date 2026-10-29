package main

import (
	"context"
	"net/url"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// alertJSON bridges the contract's optional response fields and the domain's complete records.
func alertJSON[T any](value any) (out T, err error) {
	raw, err := jsonx.MarshalCanonical(value)
	if err != nil {
		return out, err
	}
	err = jsonx.Unmarshal(raw, &out)
	return out, err
}

func (a adminAPI) GetAlertConfiguration(ctx context.Context, _ adminapi.GetAlertConfigurationRequestObject) (adminapi.GetAlertConfigurationResponseObject, error) {
	var c observability.AlertConfiguration
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { c, err = observability.GetAlertConfiguration(ctx, tx); return err })
	if err != nil {
		return nil, err
	}
	return alertJSON[adminapi.GetAlertConfiguration200JSONResponse](c)
}

func (a adminAPI) SetAlertConfiguration(ctx context.Context, r adminapi.SetAlertConfigurationRequestObject) (adminapi.SetAlertConfigurationResponseObject, error) {
	if err := a.checkAlertDestinations(ctx, r.Body); err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "setAlertConfiguration", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.AlertConfiguration, error) {
		c, err := alertJSON[observability.AlertConfiguration](r.Body)
		if err != nil {
			return adminapi.AlertConfiguration{}, err
		}
		if err = observability.SetAlertConfiguration(ctx, tx, c); err != nil {
			return adminapi.AlertConfiguration{}, err
		}
		if err = alertAudit(ctx, tx, "alert_configuration.update", "configuration", r.Body.Reason); err != nil {
			return adminapi.AlertConfiguration{}, err
		}
		c.Rev++
		return alertJSON[adminapi.AlertConfiguration](c)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetAlertConfiguration201JSONResponse{Body: body, Headers: adminapi.SetAlertConfiguration201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) checkAlertDestinations(ctx context.Context, body *adminapi.AlertConfigurationRequest) error {
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, ch := range body.Channels {
		if ch.WebhookUrl == nil {
			continue
		}
		u, err := url.Parse(*ch.WebhookUrl)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return observability.ErrAlertConfiguration
		}
		if err = a.exit.Check(check, u.Hostname()); err != nil {
			return observability.ErrAlertConfiguration
		}
	}
	return nil
}

func (a adminAPI) RotateAlertWebhookSecret(ctx context.Context, r adminapi.RotateAlertWebhookSecretRequestObject) (adminapi.RotateAlertWebhookSecretResponseObject, error) {
	if r.Body.Secret == nil {
		return nil, observability.ErrAlertConfiguration
	}
	request := struct {
		Channel string
		Body    *adminapi.AlertWebhookSecretRequest
	}{r.ChannelId, r.Body}
	body, replayed, err := idempotently(ctx, a.db, "rotateAlertWebhookSecret", r.Params.IdempotencyKey, request, func(tx pg.Tx) (adminapi.AlertTestResult, error) {
		id, err := observability.RotateAlertWebhookSecret(ctx, tx, a.keys, r.ChannelId, r.Body.Rev, secret.New(*r.Body.Secret), time.Now().UTC())
		if err != nil {
			return adminapi.AlertTestResult{}, err
		}
		if err = alertAudit(ctx, tx, "alert_channel.rotate_secret", r.ChannelId, r.Body.Reason); err != nil {
			return adminapi.AlertTestResult{}, err
		}
		return alertJSON[adminapi.AlertTestResult](map[string]string{"id": id.String()})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.RotateAlertWebhookSecret201JSONResponse{Body: body, Headers: adminapi.RotateAlertWebhookSecret201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) ListAlertEvents(ctx context.Context, r adminapi.ListAlertEventsRequestObject) (adminapi.ListAlertEventsResponseObject, error) {
	var before uuid.UUID
	var err error
	if r.Params.Before != nil {
		before, err = uuid.Parse(*r.Params.Before)
		if err != nil {
			return nil, invalidField("before", "must be a UUID")
		}
	}
	var events []observability.AlertEvent
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { events, err = observability.ListAlertEvents(ctx, tx, before); return err })
	if err != nil {
		return nil, err
	}
	return alertJSON[adminapi.ListAlertEvents200JSONResponse](map[string]any{"data": events})
}

func (a adminAPI) SendAlertTest(ctx context.Context, r adminapi.SendAlertTestRequestObject) (adminapi.SendAlertTestResponseObject, error) {
	body, replayed, err := idempotently(ctx, a.db, "sendAlertTest", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.AlertTestResult, error) {
		id, err := observability.SendAlertTest(ctx, tx, r.Body.Channel, time.Now().UTC())
		if err != nil {
			return adminapi.AlertTestResult{}, err
		}
		if err = alertAudit(ctx, tx, "alert_channel.test", r.Body.Channel, r.Body.Reason); err != nil {
			return adminapi.AlertTestResult{}, err
		}
		return alertJSON[adminapi.AlertTestResult](map[string]string{"id": id.String()})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SendAlertTest201JSONResponse{Body: body, Headers: adminapi.SendAlertTest201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) ResolveAlertEvent(ctx context.Context, r adminapi.ResolveAlertEventRequestObject) (adminapi.ResolveAlertEventResponseObject, error) {
	id, err := uuid.Parse(r.EventId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	input := struct {
		ID   string
		Body *adminapi.ReasonRequest
	}{r.EventId, r.Body}
	body, replayed, err := idempotently(ctx, a.db, "resolveAlertEvent", r.Params.IdempotencyKey, input, func(tx pg.Tx) (adminapi.AlertTestResult, error) {
		if err := observability.ResolveAlertEvent(ctx, tx, id, time.Now().UTC()); err != nil {
			return adminapi.AlertTestResult{}, err
		}
		if err := alertAudit(ctx, tx, "alert_event.resolve", r.EventId, r.Body.Reason); err != nil {
			return adminapi.AlertTestResult{}, err
		}
		return alertJSON[adminapi.AlertTestResult](map[string]string{"id": id.String()})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.ResolveAlertEvent201JSONResponse{Body: body, Headers: adminapi.ResolveAlertEvent201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func alertAudit(ctx context.Context, tx pg.Tx, action, id, reason string) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: action, Target: audit.Target{Module: "observability", Kind: "alert", ID: id}, Reason: reason, Detail: map[string]any{}})
}
