package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
)

const (
	// jobKind is the River kind, stable for alerts and monitoring: a discarded job is reported by the queue under it.
	jobKind = "mail.send"
	// maxAttempts covers a provider outage of a few minutes. River's default backoff for v0.49 retries after about
	// 16 seconds, then 81, 256 and 625, so five tries span about sixteen minutes; a job discarded after them is
	// reported by the queue's scan, which is the failure signal an operator acts on.
	maxAttempts = 5

	// The stable event names of the delivery log. The success line carries the recipient and Message-ID so it can be
	// matched against the provider's log; neither body, subject nor credential is ever logged.
	sentEvent         = "mail sent"
	failedEvent       = "mail send failed"
	goneEvent         = "mail skipped: the queued message is gone"
	forgetFailedEvent = "mail not deleted from the outbox"
	disabledEvent     = "email disabled: AXIOM_SMTP_HOST and AXIOM_SMTP_FROM are unset"
	enabledEvent      = "email enabled"
)

// The errors a job hands River stand in for the error the send produced. River writes what it is given into
// river.river_job.errors, a column no tenant policy covers, so the recipient, the subject, the bodies and the server's
// reply — which quotes the recipient when it refuses one mailbox — stay out of it and live in the log line beside it
// instead. What River needs is the kind of failure, and that is all these say.
var (
	errSendFailed  = errors.New("mail: the send failed and may pass on the next attempt")
	errSendRefused = errors.New("mail: the send was refused for good")
)

// Mailer queues the delivery of one message per call and works those jobs. Registration happens for every node, so a
// flow can call Enqueue wherever it runs; a node without SMTP settings says so once at startup and refuses at
// enqueue time.
type Mailer struct {
	db     *pg.DB
	queue  *pg.Queue
	sender *sender // nil when email is disabled
	logger *slog.Logger
	// Receipt is installed before queue.Start for tracked messages; true means SMTP accepted.
	Receipt func(context.Context, uuid.UUID, bool) error
}

// Register registers the send worker on the queue, which must not have started yet, and returns the mailer the flows
// enqueue through. With no SMTP settings it logs that email is disabled and returns a mailer whose Enqueue fails with
// ErrNotConfigured: email costs no configuration for a node that never sends it.
func Register(ctx context.Context, db *pg.DB, queue *pg.Queue, cfg Config, logger *slog.Logger) (*Mailer, error) {
	m := &Mailer{db: db, queue: queue, logger: logger}
	if !cfg.Configured() {
		logger.InfoContext(ctx, disabledEvent)
		return m, nil
	}
	m.sender = newSender(cfg)
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(m.work)); err != nil {
		return nil, fmt.Errorf("mail: register the send worker: %w", err)
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(func(ctx context.Context, job *river.Job[trackedSendArgs]) error {
		return m.work(ctx, &river.Job[sendArgs]{JobRow: job.JobRow, Args: job.Args.sendArgs})
	})); err != nil {
		return nil, err
	}
	logger.InfoContext(ctx, enabledEvent, "host", cfg.Host, "port", cfg.Port, "tls", m.sender.mode)
	return m, nil
}

// Enqueue queues one message for the tenant, in the transaction of the write that causes it, so the message exists if
// and only if that write does. The message goes to platform.mail_outbox in the tenant's scope, where row-level security
// refuses a row for another tenant, and the queue carries its id alone: a job row belongs to no tenant and no policy
// covers it, so the address and the bodies must not travel there. What the caller got wrong (an address that does not
// parse, an empty body) is reported here, before the write commits, rather than from the worker. Delivery is at least
// once: a node that crashes after the message is accepted but before the job is completed sends it again, so a flow
// must tolerate a repeated email.
func (m *Mailer) Enqueue(ctx context.Context, tx pg.Tx, tenant Tenant, msg Message) error {
	return m.EnqueueTracked(ctx, tx, tenant, msg, uuid.Nil())
}

// EnqueueTracked links a message to a caller-owned durable receipt without putting content in River.
func (m *Mailer) EnqueueTracked(ctx context.Context, tx pg.Tx, tenant Tenant, msg Message, receipt uuid.UUID) error {
	if m.sender == nil {
		return ErrNotConfigured
	}
	to, err := msg.validate()
	if err != nil {
		return err
	}
	var account *uuid.UUID
	if tenant.Account != uuid.Nil() {
		account = &tenant.Account
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, insertOutbox, tenant.Site, account, to.String(), msg.Subject, msg.Text, msg.HTML).Scan(&id)
	if err != nil {
		return fmt.Errorf("mail: enqueue: %w", err)
	}
	var args river.JobArgs = sendArgs{ID: id}
	if receipt != uuid.Nil() {
		args = trackedSendArgs{sendArgs{ID: id, Receipt: receipt}}
	}
	if _, err := m.queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: args}}); err != nil {
		return fmt.Errorf("mail: enqueue: %w", err)
	}
	return nil
}

// sendArgs is what the queue carries: the id of the outbox row, and nothing of the message itself. River keeps a job
// row until it is done and its retention has passed; the message it points at is deleted as soon as it is sent or
// refused for good.
type sendArgs struct {
	ID      uuid.UUID `json:"id"`
	Receipt uuid.UUID `json:"receipt,omitempty"`
}

// Kind implements river.JobArgs.
func (sendArgs) Kind() string { return jobKind }

// InsertOpts implements river.JobArgsWithInsertOpts. The job rides the notifications queue, so a burst of mail waits
// behind that queue's workers alone.
func (sendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: maxAttempts}
}

// work delivers one message and records the result. The job row is the durable record of the attempt (state, attempts
// and last error), and the log line adds the recipient and the Message-ID for the provider's log. A rate-limited
// recipient snoozes the job instead of failing it: snoozing spends no attempt, and River's first retry would come back
// inside the same interval.
func (m *Mailer) work(ctx context.Context, job *river.Job[sendArgs]) error {
	msg, found, err := m.queued(ctx, job.Args.ID)
	if err != nil {
		// The database's error can name the row it failed on, so it stays in the log and River is told only that the
		// attempt failed: an unreadable row is worth another attempt, unlike the message itself.
		m.logger.WarnContext(ctx, failedEvent, "job_id", job.ID, "attempt", job.Attempt, "err", err)
		return errSendFailed
	}
	if !found {
		if err := m.recordReceipt(ctx, job, false); err != nil {
			return errSendFailed
		}
		m.logger.InfoContext(ctx, goneEvent, "job_id", job.ID, "attempt", job.Attempt)
		return nil
	}
	id, err := m.sender.Send(ctx, msg)
	if errors.Is(err, ErrRateLimited) {
		return river.JobSnooze(rateInterval)
	}
	if err == nil {
		m.logger.InfoContext(ctx, sentEvent, "job_id", job.ID, "attempt", job.Attempt, "to", msg.To, "message_id", id)
		if err := m.recordReceipt(ctx, job, true); err != nil {
			return errSendFailed
		}
		m.forget(ctx, job, msg.To)
		return nil
	}
	permanent := errors.Is(err, ErrRejected) || errors.Is(err, ErrInvalidMessage)
	m.logger.WarnContext(ctx, failedEvent, "job_id", job.ID, "attempt", job.Attempt, "to", msg.To,
		"message_id", id, "permanent", permanent, "err", err)
	if permanent || job.Attempt >= job.MaxAttempts {
		if err := m.recordReceipt(ctx, job, false); err != nil {
			return errSendFailed
		}
	}
	if !permanent {
		return errSendFailed
	}
	m.forget(ctx, job, msg.To)
	return river.JobCancel(errSendRefused)
}

// queued reads the message the job carries the id of, in the scope of the tenant the outbox lookup names. A message
// that is no longer there was already sent or refused, or deleted by hand: there is nothing left to deliver.
func (m *Mailer) queued(ctx context.Context, id uuid.UUID) (Message, bool, error) {
	var msg Message
	found, err := m.db.TenantTx(ctx, outboxLookup, id, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, readOutbox, id).Scan(&msg.To, &msg.Subject, &msg.Text, &msg.HTML)
	})
	if err != nil {
		return Message{}, false, err
	}
	return msg, found, nil
}

// forget deletes the queued message once it has been sent or refused for good. A failure is logged and changes nothing
// else: the message is delivered or refused either way, and retrying would send it a second time.
func (m *Mailer) forget(ctx context.Context, job *river.Job[sendArgs], to string) {
	_, err := m.db.TenantTx(ctx, outboxLookup, job.Args.ID, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, deleteOutbox, job.Args.ID)
		return err
	})
	if err != nil {
		m.logger.ErrorContext(ctx, forgetFailedEvent, "job_id", job.ID, "attempt", job.Attempt, "to", to, "err", err)
	}
}

func (m *Mailer) recordReceipt(ctx context.Context, job *river.Job[sendArgs], delivered bool) error {
	if job.Args.Receipt == uuid.Nil() {
		return nil
	}
	if m.Receipt == nil {
		return errSendFailed
	}
	return m.Receipt(ctx, job.Args.Receipt, delivered)
}

type trackedSendArgs struct{ sendArgs }

func (trackedSendArgs) Kind() string { return "mail.send_tracked" }
