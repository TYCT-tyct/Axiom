package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// Consumers and delivery-receipt callbacks must share the mailer and be registered before the queue starts.
func registerNotifications(ctx context.Context, db *pg.DB, queue *pg.Queue, smtp mail.Config,
	cache *kv.Client, master *secret.MasterKeys, keys *secret.Keyring, exit *egress.Client, logger *slog.Logger,
) (*accounts.PasswordRecovery, error) {
	mailer, err := mail.Register(ctx, db, queue, smtp, cache, master, logger)
	if err != nil {
		return nil, err
	}
	recovery, err := accounts.RegisterPasswordRecovery(db, queue, mailer, pg.PlatformScope(), logger)
	if err != nil {
		return nil, err
	}
	if err := observability.RegisterAlerts(db, pg.PlatformScope(), queue, mailer, keys, exit); err != nil {
		return nil, err
	}
	if err := accounts.RegisterUserNotices(db, pg.PlatformScope(), queue, exit); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[userNotificationArgs]) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for {
			consumed, err := userNotificationBatch(ctx, db, queue, mailer)
			if err != nil {
				logger.ErrorContext(ctx, "user notification processing failed", "err", err)
				return errors.New("user notification processing failed")
			}
			if consumed < 100 {
				return nil
			}
		}
	})); err != nil {
		return nil, err
	}
	queue.Every(time.Minute, func() river.JobArgs { return userNotificationArgs{} })
	return recovery, nil
}

type userNotificationArgs struct{}

func (userNotificationArgs) Kind() string { return "accounts.user_notices" }
func (userNotificationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

// A batch owns one bounded transaction; successful earlier batches survive a later timeout.
func userNotificationBatch(ctx context.Context, db *pg.DB, queue *pg.Queue, mailer *mail.Mailer) (consumed int, err error) {
	err = db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		if err := billing.ConsumeBalanceNotices(ctx, tx, func(n billing.BalanceNotice) error {
			consumed++
			return accounts.RecordBalanceNotice(ctx, tx, n.ID, n.Site, n.Account, n.Previous, n.Balance, n.TopUp)
		}); err != nil {
			return err
		}
		return accounts.DispatchUserNotices(ctx, tx, queue, mailer)
	})
	return consumed, err
}
