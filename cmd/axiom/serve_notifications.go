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
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(func(ctx context.Context, _ *river.Job[userNoticeArgs]) error {
		if err := runUserNotices(ctx, db, queue, mailer, 30*time.Second); err != nil {
			logger.ErrorContext(ctx, "user notice processing failed", "err", err)
			return errors.New("user notice processing failed")
		}
		return nil
	})); err != nil {
		return nil, err
	}
	queue.Every(time.Minute, func() river.JobArgs { return userNoticeArgs{} })
	return recovery, nil
}

type userNoticeArgs struct{}

func (userNoticeArgs) Kind() string { return "accounts.user_notices" }
func (userNoticeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

// A budget limits each evaluation normally; only unexpected failures consume River retries.
func runUserNotices(ctx context.Context, db *pg.DB, queue *pg.Queue, mailer *mail.Mailer, budget time.Duration) (err error) {
	workCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		if errors.Is(err, context.DeadlineExceeded) && workCtx.Err() == context.DeadlineExceeded {
			err = nil
		}
	}()
	// Avoid starting a fresh transaction in the final second; a slower batch can still exhaust the budget safely.
	const batchReserve = time.Second
	deadline, _ := workCtx.Deadline()
	if time.Until(deadline) < batchReserve {
		return ctx.Err()
	}
	if err = db.Tx(workCtx, pg.PlatformScope(), func(tx pg.Tx) error {
		return accounts.MaintainUserNotices(workCtx, tx)
	}); err != nil {
		return err
	}
	for time.Until(deadline) >= batchReserve {
		if err = ctx.Err(); err != nil {
			return err
		}
		progressed, batchErr := userNoticeBatch(workCtx, db, queue, mailer)
		if batchErr != nil {
			return batchErr
		}
		if progressed == 0 {
			return nil
		}
	}
	return ctx.Err()
}

// A batch owns one bounded transaction; successful earlier batches survive a later timeout.
func userNoticeBatch(ctx context.Context, db *pg.DB, queue *pg.Queue, mailer *mail.Mailer) (progressed int, err error) {
	err = db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		if err := billing.ConsumeBalanceNotices(ctx, tx, func(notices []accounts.BalanceNotice) error {
			progressed += len(notices)
			return accounts.RecordBalanceNotices(ctx, tx, notices)
		}); err != nil {
			return err
		}
		dispatched, err := accounts.DispatchUserNotices(ctx, tx, queue, mailer)
		progressed += dispatched
		return err
	})
	return progressed, err
}
