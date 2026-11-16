package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

// PasswordRecovery owns the durable reset requests and their mail outbox. It is shared by HTTP flows and the worker.
type PasswordRecovery struct {
	db     *pg.DB
	queue  *pg.Queue
	mailer *mail.Mailer
	scope  pg.Scope
	logger *slog.Logger
}

// RegisterPasswordRecovery registers before the queue starts. A node without SMTP cannot take mail-dependent jobs.
func RegisterPasswordRecovery(db *pg.DB, queue *pg.Queue, mailer *mail.Mailer, scope pg.Scope, logger *slog.Logger) (*PasswordRecovery, error) {
	r := &PasswordRecovery{db: db, queue: queue, mailer: mailer, scope: scope, logger: logger}
	tick := func() river.JobArgs { return passwordResetCleanupArgs{} }
	if err := registerMailWorkers(queue, mailer, tick, r.cleanup, r.work); err != nil {
		return nil, err
	}
	return r, nil
}

// registerMailWorkers adds the cleanup every node runs and, when SMTP is configured, the worker that sends the mail.
// Recovery and open registration are wired alike: a node without SMTP still removes expired rows, so rows a node that
// once sent mail leaves behind do not stay after SMTP is turned off.
func registerMailWorkers[C, W river.JobArgs](queue *pg.Queue, mailer *mail.Mailer, tick func() river.JobArgs,
	cleanup func(context.Context, *river.Job[C]) error, work func(context.Context, *river.Job[W]) error,
) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(cleanup)); err != nil {
		return err
	}
	queue.Every(time.Minute, tick)
	if !mailer.Configured() {
		return nil
	}
	return river.AddWorkerSafely(queue.Workers(), river.WorkFunc(work))
}

type passwordResetArgs struct {
	ID uuid.UUID `json:"id"`
}

func (passwordResetArgs) Kind() string { return "accounts.password_reset" }
func (passwordResetArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5}
}

// Errors recorded by River must not carry SQL values, addresses or tokens.
var (
	errPasswordResetJob = errors.New("accounts: password reset processing failed")
	errNoResetDomain    = errors.New("accounts: no HTTPS console domain")
)

func (r *PasswordRecovery) work(ctx context.Context, job *river.Job[passwordResetArgs]) error {
	return runMailJob(ctx, job, r.process, r.logFailure, errNoResetDomain, errPasswordResetJob)
}

func runMailJob[A river.JobArgs](ctx context.Context, job *river.Job[A], process func(context.Context, A) error,
	logFailure func(context.Context, int64, int, error), noDomain, failure error,
) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := process(ctx, job.Args); err != nil {
		logFailure(ctx, job.ID, job.Attempt, err)
		if errors.Is(err, noDomain) || errors.Is(err, ErrBadEmail) {
			return river.JobCancel(failure)
		}
		return failure
	}
	return nil
}

func (r *PasswordRecovery) process(ctx context.Context, args passwordResetArgs) error {
	q := New()
	var permanent error
	_, err := r.db.TenantTx(ctx, "accounts.password_reset_request_tenant", args.ID, func(tx pg.Tx) error {
		requests, err := q.LockPasswordResetRequest(ctx, tx, args.ID)
		if err != nil || len(requests) == 0 {
			return err
		}
		request := requests[0]
		if request.Email == nil {
			return nil
		}
		if !request.Unexpired {
			return q.FinishPasswordResetRequest(ctx, tx, args.ID)
		}
		err = r.issueReset(ctx, tx, args, request)
		if errors.Is(err, errNoResetDomain) || errors.Is(err, ErrBadEmail) {
			permanent = err
			return q.FinishPasswordResetRequest(ctx, tx, args.ID)
		}
		return err
	})
	if err != nil {
		return err
	}
	return permanent
}

func (s *Service) recoveryReady() error {
	if s.recovery == nil {
		return fmt.Errorf("%w: password recovery", ErrBusy)
	}
	if !s.recovery.mailer.Configured() {
		return fmt.Errorf("%w: email disabled", ErrBusy)
	}
	return nil
}

func (r *PasswordRecovery) issueReset(ctx context.Context, tx pg.Tx, args passwordResetArgs, request LockPasswordResetRequestRow) error {
	q := New()
	if !emailAllowed(*request.Email) {
		return ErrBadEmail
	}
	sites, err := q.ConsoleSite(ctx, tx, ConsoleSiteParams{SiteID: request.SiteID, Domain: request.Domain})
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		return errNoResetDomain
	}
	users, err := q.ResetUser(ctx, tx, ResetUserParams{SiteID: request.SiteID, Email: *request.Email})
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return q.FinishPasswordResetRequest(ctx, tx, args.ID)
	}
	user := users[0]
	locked, err := q.LockUser(ctx, tx, user.ID)
	if err != nil {
		return err
	}
	if len(locked) == 0 || !locked[0].Active {
		return q.FinishPasswordResetRequest(ctx, tx, args.ID)
	}
	token := randomToken()
	hash := sha256.Sum256([]byte(token))
	if err := q.InsertPasswordResetToken(ctx, tx, InsertPasswordResetTokenParams{
		ID: args.ID, AccountID: user.AccountID, UserID: user.ID, TokenHash: hash[:], FactorVersion: locked[0].FactorVersion,
	}); err != nil {
		return err
	}
	link := "https://" + sites[0].Domain + "/console/reset-password#token=" + token
	if err := r.mailer.Enqueue(ctx, tx, mail.Tenant{Site: request.SiteID, Account: user.AccountID},
		passwordMail(*request.Email, sites[0].Name, request.Language, link)); err != nil {
		return err
	}
	return q.FinishPasswordResetRequest(ctx, tx, args.ID)
}
