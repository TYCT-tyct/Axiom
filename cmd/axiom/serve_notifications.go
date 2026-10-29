package main

import (
	"context"
	"log/slog"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// Consumers and delivery-receipt callbacks must share the mailer and be registered before the queue starts.
func registerNotifications(ctx context.Context, db *pg.DB, queue *pg.Queue, smtp mail.Config,
	cache *kv.Client, master *secret.MasterKeys, logger *slog.Logger,
) (*accounts.PasswordRecovery, error) {
	mailer, err := mail.Register(ctx, db, queue, smtp, cache, master, logger)
	if err != nil {
		return nil, err
	}
	recovery, err := accounts.RegisterPasswordRecovery(db, queue, mailer, pg.PlatformScope(), logger)
	if err != nil {
		return nil, err
	}
	if err := observability.RegisterAlerts(db, pg.PlatformScope(), queue, mailer); err != nil {
		return nil, err
	}
	return recovery, nil
}
