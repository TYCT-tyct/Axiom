package main

import (
	"context"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func preferencesJSON(p accounts.NotificationPreferences) consoleapi.NotificationPreferences {
	return consoleapi.NotificationPreferences{LowBalance: new(p.LowBalance), TopUp: new(p.TopUp), FactorChanged: new(p.FactorChanged), Threshold: new(strconv.FormatInt(p.Threshold, 10)), WebhookUrl: new(p.WebhookURL), Language: new(consoleapi.NotificationPreferencesLanguage(p.Language))}
}

func (c consoleAPI) GetNotificationPreferences(ctx context.Context, _ consoleapi.GetNotificationPreferencesRequestObject) (consoleapi.GetNotificationPreferencesResponseObject, error) {
	p, err := c.accounts.NotificationPreferences(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	return consoleapi.GetNotificationPreferences200JSONResponse(preferencesJSON(p)), nil
}

func (c consoleAPI) SetNotificationPreferences(ctx context.Context, r consoleapi.SetNotificationPreferencesRequestObject) (consoleapi.SetNotificationPreferencesResponseObject, error) {
	threshold, err := strconv.ParseInt(r.Body.Threshold, 10, 64)
	if err != nil {
		return nil, accounts.ErrNotificationPreferences
	}
	p := accounts.NotificationPreferences{LowBalance: r.Body.LowBalance, TopUp: r.Body.TopUp, FactorChanged: r.Body.FactorChanged, Threshold: threshold, WebhookURL: r.Body.WebhookUrl, Language: string(r.Body.Language)}
	if p.WebhookURL != "" {
		host, err := accounts.ValidateNotificationWebhookURL(p.WebhookURL)
		if err != nil {
			return nil, err
		}
		if err = c.exit.Check(ctx, host); err != nil {
			return nil, accounts.ErrNotificationPreferences
		}
	}
	if err = c.accounts.UpdateNotificationPreferences(ctx, callOf(ctx).principal, p); err != nil {
		return nil, err
	}
	return consoleapi.SetNotificationPreferences200JSONResponse(preferencesJSON(p)), nil
}

func (c consoleAPI) ListAnnouncements(ctx context.Context, _ consoleapi.ListAnnouncementsRequestObject) (consoleapi.ListAnnouncementsResponseObject, error) {
	data, err := c.accounts.Announcements(ctx, callOf(ctx).principal, time.Now())
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, len(data))
	for i, n := range data {
		items[i] = announcementJSON(n)
	}
	return adminJSON[consoleapi.ListAnnouncements200JSONResponse](map[string]any{"data": items})
}

func (a adminAPI) PublishAnnouncement(ctx context.Context, r adminapi.PublishAnnouncementRequestObject) (adminapi.PublishAnnouncementResponseObject, error) {
	site, err := accounts.ParseSiteID(r.Body.SiteId)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	if _, ok := a.accounts.Site(site); !ok {
		return nil, accounts.ErrNotFound
	}
	body, replayed, err := idempotently(ctx, a.db, "publishAnnouncement", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.Announcement, error) {
		n := accounts.Announcement{ID: uuid.NewV7(), Site: uuid.UUID(site), Title: r.Body.Title, Body: r.Body.Body, StartsAt: r.Body.StartsAt, EndsAt: r.Body.EndsAt}
		if err := accounts.PublishAnnouncement(ctx, tx, n); err != nil {
			return adminapi.Announcement{}, err
		}
		if err := announcementAudit(ctx, tx, "announcement.publish", n.ID, r.Body.Reason); err != nil {
			return adminapi.Announcement{}, err
		}
		return adminJSON[adminapi.Announcement](announcementJSON(n))
	})
	if err != nil {
		return nil, err
	}
	return adminapi.PublishAnnouncement201JSONResponse{Body: body, Headers: adminapi.PublishAnnouncement201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) WithdrawAnnouncement(ctx context.Context, r adminapi.WithdrawAnnouncementRequestObject) (adminapi.WithdrawAnnouncementResponseObject, error) {
	id := uuid.UUID(r.AnnouncementId)
	input := struct {
		ID     uuid.UUID
		Reason string
	}{id, r.Body.Reason}
	body, replayed, err := idempotently(ctx, a.db, "withdrawAnnouncement", r.Params.IdempotencyKey, input, func(tx pg.Tx) (adminapi.AnnouncementResult, error) {
		if err := accounts.WithdrawAnnouncement(ctx, tx, id); err != nil {
			return adminapi.AnnouncementResult{}, err
		}
		if err := announcementAudit(ctx, tx, "announcement.withdraw", id, r.Body.Reason); err != nil {
			return adminapi.AnnouncementResult{}, err
		}
		return adminJSON[adminapi.AnnouncementResult](map[string]string{"id": id.String()})
	})
	if err != nil {
		return nil, err
	}
	return adminapi.WithdrawAnnouncement201JSONResponse{Body: body, Headers: adminapi.WithdrawAnnouncement201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func announcementAudit(ctx context.Context, tx pg.Tx, action string, id uuid.UUID, reason string) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: action, Target: audit.Target{Module: "accounts", Kind: "announcement", ID: id.String()}, Reason: reason})
}

func announcementJSON(n accounts.Announcement) map[string]any {
	return map[string]any{"id": n.ID.String(), "site_id": accounts.SiteID(n.Site).TypeID(), "title": n.Title, "body": n.Body, "starts_at": n.StartsAt, "ends_at": n.EndsAt}
}
