package main

import (
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
)

const callReportTimeout = 10 * time.Second

// ownCallQuery decodes the shared filters after the guard has validated the operation's query schema.
func ownCallQuery(cl call) (observability.OwnQuery, error) {
	p := cl.r.URL.Query()
	q := observability.OwnQuery{
		Account: uuid.UUID(cl.principal.AccountID()), Until: endOfTime,
		Model: p.Get("model"), ClientApp: p.Get("client_app"), Status: p.Get("status"),
	}
	for _, field := range []struct {
		name string
		dst  *time.Time
	}{{"since", &q.Since}, {"until", &q.Until}} {
		raw := p.Get(field.name)
		if raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, invalidField(field.name, "expected an RFC3339 instant")
		}
		*field.dst = value
	}
	for _, field := range []struct {
		name  string
		dst   **uuid.UUID
		parse func(string) (uuid.UUID, error)
	}{
		{"key_id", &q.Key, func(s string) (uuid.UUID, error) { id, err := accounts.ParseKeyID(s); return uuid.UUID(id), err }},
		{"workspace_id", &q.Workspace, func(s string) (uuid.UUID, error) { id, err := accounts.ParseWorkspaceID(s); return uuid.UUID(id), err }},
		{"request_id", &q.RequestID, uuid.Parse},
	} {
		raw := p.Get(field.name)
		if raw == "" {
			continue
		}
		id, err := field.parse(raw)
		if err != nil {
			return q, invalidField(field.name, "invalid identifier")
		}
		*field.dst = &id
	}
	if raw := p.Get("cursor"); raw != "" {
		after, err := observability.ParseCursor(raw)
		if err != nil {
			return q, err
		}
		q.After = &after
	}
	return q, nil
}

func boundedCallWindow(q observability.OwnQuery, since, until *time.Time) (observability.OwnQuery, error) {
	if until == nil {
		q.Until = time.Now().UTC()
	}
	if since == nil {
		q.Since = q.Until.Add(-30 * 24 * time.Hour)
	}
	if !q.Since.Before(q.Until) || q.Until.Sub(q.Since) > 31*24*time.Hour {
		return q, invalidField("since", "choose a nonempty window of at most 31 days")
	}
	return q, nil
}
