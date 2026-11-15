package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

type passwordResetCleanupArgs struct{}

func (passwordResetCleanupArgs) Kind() string { return "accounts.password_reset_cleanup" }
func (passwordResetCleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, MaxAttempts: 5}
}

func (r *PasswordRecovery) cleanup(ctx context.Context, job *river.Job[passwordResetCleanupArgs]) error {
	if err := r.purge(ctx); err != nil {
		r.logFailure(ctx, job.ID, job.Attempt, err)
		return errPasswordResetJob
	}
	return nil
}

func (r *PasswordRecovery) purge(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return r.db.Tx(ctx, r.scope, func(tx pg.Tx) error {
		if err := New().PurgePasswordResetTokens(ctx, tx); err != nil {
			return err
		}
		if err := New().PurgePasswordResetRequests(ctx, tx); err != nil {
			return err
		}
		return New().PurgeEmailChanges(ctx, tx)
	})
}

// Only categories and SQLSTATE may cross into logs; SQL details can quote personal data or credentials.
func (r *PasswordRecovery) logFailure(ctx context.Context, jobID int64, attempt int, err error) {
	category := "processing"
	switch {
	case errors.Is(err, errNoResetDomain):
		category = "no_https_console_domain"
	case errors.Is(err, ErrBadEmail):
		category = "invalid_email"
	case errors.Is(err, mail.ErrInvalidMessage):
		category = "invalid_message"
	case errors.Is(err, mail.ErrNotConfigured):
		category = "email_disabled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "timeout"
	case errors.Is(err, context.Canceled):
		category = "canceled"
	case pg.SQLState(err) != "":
		category = "database"
	}
	r.logger.ErrorContext(ctx, "accounts.password_reset.failed", "job_id", jobID, "attempt", attempt,
		"category", category, "sqlstate", pg.SQLState(err))
}
