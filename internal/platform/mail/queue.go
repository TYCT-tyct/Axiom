package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	sentEvent     = "mail sent"
	failedEvent   = "mail send failed"
	disabledEvent = "email disabled: AXIOM_SMTP_HOST and AXIOM_SMTP_FROM are unset"
	enabledEvent  = "email enabled"
)

// Mailer queues the delivery of one message per call and works those jobs. Registration happens for every node, so a
// flow can call Enqueue wherever it runs; a node without SMTP settings says so once at startup and refuses at
// enqueue time.
type Mailer struct {
	queue  *pg.Queue
	sender *sender // nil when email is disabled
	logger *slog.Logger
}

// Register registers the send worker on the queue, which must not have started yet, and returns the mailer the flows
// enqueue through. With no SMTP settings it logs that email is disabled and returns a mailer whose Enqueue fails with
// ErrNotConfigured: email costs no configuration for a node that never sends it.
func Register(ctx context.Context, queue *pg.Queue, cfg Config, logger *slog.Logger) (*Mailer, error) {
	m := &Mailer{queue: queue, logger: logger}
	if !cfg.Configured() {
		logger.InfoContext(ctx, disabledEvent)
		return m, nil
	}
	m.sender = newSender(cfg)
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(m.work)); err != nil {
		return nil, fmt.Errorf("mail: register the send worker: %w", err)
	}
	logger.InfoContext(ctx, enabledEvent, "host", cfg.Host, "port", cfg.Port, "tls", m.sender.mode)
	return m, nil
}

// Enqueue queues one message for delivery in the transaction of the write that causes it, so the message exists if and
// only if that write does. What the caller got wrong (an address that does not parse, an empty body) is reported here,
// before the write commits, rather than from the worker. Delivery is at least once: a node that crashes after the
// message is accepted but before the job is completed sends it again, so a flow must tolerate a repeated email.
func (m *Mailer) Enqueue(ctx context.Context, tx pg.Tx, msg Message) error {
	if m.sender == nil {
		return ErrNotConfigured
	}
	if _, err := msg.validate(); err != nil {
		return err
	}
	_, err := m.queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: sendArgs(msg)}})
	if err != nil {
		return fmt.Errorf("mail: enqueue: %w", err)
	}
	return nil
}

// sendArgs is one message as the queue stores it: what the caller wrote, and no credentials. The row keeps it until
// the job is done and River's retention has passed, so a body holding a one-time link keeps it only that long.
type sendArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

// Kind implements river.JobArgs.
func (sendArgs) Kind() string { return jobKind }

// InsertOpts implements river.JobArgsWithInsertOpts. The job stays on the default queue; the queue split gives
// notifications a queue of their own later.
func (sendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: river.QueueDefault, MaxAttempts: maxAttempts}
}

// work delivers one message and records the result. River's job row is the durable record (state, attempts and last
// error), and the log line adds the recipient and the Message-ID for the provider's log. A rate-limited recipient
// snoozes the job instead of failing it: snoozing spends no attempt, and River's first retry would come back inside
// the same interval.
func (m *Mailer) work(ctx context.Context, job *river.Job[sendArgs]) error {
	msg := Message{To: job.Args.To, Subject: job.Args.Subject, Text: job.Args.Text, HTML: job.Args.HTML}
	id, err := m.sender.Send(ctx, msg)
	if errors.Is(err, ErrRateLimited) {
		return river.JobSnooze(rateInterval)
	}
	if err == nil {
		m.logger.InfoContext(ctx, sentEvent, "job_id", job.ID, "attempt", job.Attempt, "to", msg.To, "message_id", id)
		return nil
	}
	permanent := errors.Is(err, ErrRejected) || errors.Is(err, ErrInvalidMessage)
	m.logger.WarnContext(ctx, failedEvent, "job_id", job.ID, "attempt", job.Attempt, "to", msg.To,
		"message_id", id, "permanent", permanent, "err", err)
	if permanent {
		return river.JobCancel(err)
	}
	return err
}
