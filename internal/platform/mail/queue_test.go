package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// syncBuffer collects the mailer's log lines. The worker runs on the queue's goroutines, so the test may read the
// buffer while a line is being written to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// queueWorld is one node's queue over a private copy of the database.
type queueWorld struct {
	db    *pg.DB
	queue *pg.Queue
	logs  *syncBuffer
}

func newQueueWorld(t *testing.T) *queueWorld {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return &queueWorld{
		db:    db,
		queue: db.NewQueue(slog.New(slog.DiscardHandler), pg.NewScope(true, nil, nil), nil),
		logs:  &syncBuffer{},
	}
}

// start runs the queue and stops it when the test ends, with a context of its own because the test's is cancelled by
// then.
func (w *queueWorld) start(t *testing.T) {
	t.Helper()
	if err := w.queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := w.queue.Stop(ctx); err != nil {
			t.Errorf("stop queue: %v", err)
		}
	})
}

// enqueue queues one message for its tenant, in a transaction of that tenant's scope, as a flow does.
func (w *queueWorld) enqueue(t *testing.T, mailer *Mailer, tenant Tenant, msg Message) {
	t.Helper()
	scope := pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)
	if err := w.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return mailer.Enqueue(t.Context(), tx, tenant, msg)
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

// job is the node's most recent mail job: its state, as River spells it, its attempt, and the arguments it carries.
func (w *queueWorld) job(t *testing.T) (state string, attempt int, args string) {
	t.Helper()
	err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state::text, attempt, args::text FROM river.river_job
			WHERE kind IN ($1,'mail.send_tracked') ORDER BY id DESC LIMIT 1`, jobKind).Scan(&state, &attempt, &args)
	})
	if err != nil {
		t.Fatalf("read the mail job: %v", err)
	}
	return state, attempt, args
}

// jobErrors is what river.river_job.errors holds for the node's most recent mail job: the error text River recorded for
// each attempt, in a table no tenant policy covers.
func (w *queueWorld) jobErrors(t *testing.T) string {
	t.Helper()
	var errs string
	err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce(errors::text, '') FROM river.river_job
			WHERE kind IN ($1,'mail.send_tracked') ORDER BY id DESC LIMIT 1`, jobKind).Scan(&errs)
	})
	if err != nil {
		t.Fatalf("read the mail job's recorded errors: %v", err)
	}
	return errs
}

// waitForJob waits until the node's latest mail job reaches one of the given states, and returns how many attempts it
// took. River leaves a job that is to be retried as retryable, or scheduled when the retry is nearer than its scheduler's
// run, so a retried job is either.
func (w *queueWorld) waitForJob(t *testing.T, want ...string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, attempt, _ := w.job(t)
		if slices.Contains(want, state) {
			return attempt
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the job stayed %q; want %q", state, want)
		case <-ticker.C:
		}
	}
}

// queued reads the outbox in scope: what a transaction of that scope can see of the queued messages.
func (w *queueWorld) queued(t *testing.T, scope pg.Scope) []string {
	t.Helper()
	var recipients []string
	err := w.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT recipient FROM platform.mail_outbox ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var recipient string
			if err := rows.Scan(&recipient); err != nil {
				return err
			}
			recipients = append(recipients, recipient)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	return recipients
}

// register registers a mailer that talks to the fake server instead of a real SMTP host, and returns it.
func (w *queueWorld) register(t *testing.T, server *fakeServer) *Mailer {
	t.Helper()
	mailer, err := Register(t.Context(), w.db, w.queue, Config{
		Host: "127.0.0.1", Port: 587, From: netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
	}, slog.New(slog.NewTextHandler(w.logs, nil)))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	mailer.sender.addr = server.ln.Addr().String()
	mailer.sender.roots = server.pool
	return mailer
}

var welcome = Message{To: "user@example.com", Subject: "Welcome", Text: "Hello from Axiom.", HTML: "<p>Hello from Axiom.</p>"}

func TestMailerDelivers(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	server := startTLSServer(t, nil)
	mailer := w.register(t, server)
	w.start(t)

	tenant := Tenant{Site: uuid.NewV7()}
	w.enqueue(t, mailer, tenant, welcome)
	attempt := w.waitForJob(t, "completed")
	if attempt != 1 {
		t.Errorf("the completed job took %d attempts; want 1", attempt)
	}
	if got := waitDelivery(t, server); !strings.Contains(string(got.payload), "Subject: Welcome") {
		t.Errorf("the server received %q; want the message's headers", got.payload)
	}
	for _, want := range []string{"email enabled", "mail sent", "message_id"} {
		if !strings.Contains(w.logs.String(), want) {
			t.Errorf("the log does not contain %q:\n%s", want, w.logs.String())
		}
	}
	// The message is deleted once it is sent: nothing keeps a one-time link after it has been used.
	if got := w.queued(t, pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)); len(got) != 0 {
		t.Errorf("the outbox still holds %v after delivery", got)
	}
}

// TestMailerLeavesTheMessageOnFailure: a refused or rate-limited send keeps the queued message, because the job is
// retried and must find it.
func TestMailerRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	// 451 is a temporary server failure (RFC 5321 section 4.2.1): the job goes back to scheduled and River retries it.
	// The reply quotes the mailbox it refused, as a real server's does.
	const reply = "try again later"
	server := startTLSServer(t, func(s *fakeServer) {
		s.rejectStep, s.rejectCode = "rcpt", 451
		s.rejectText = "4.2.1 <" + welcome.To + ">: " + reply
	})
	mailer := w.register(t, server)
	w.start(t)

	tenant := Tenant{Site: uuid.NewV7()}
	w.enqueue(t, mailer, tenant, welcome)
	attempt := w.waitForJob(t, "retryable", "scheduled")
	if attempt != 1 {
		t.Errorf("the retryable job took %d attempts; want 1", attempt)
	}
	if !strings.Contains(w.logs.String(), "permanent=false") {
		t.Errorf("the log does not record a transient failure:\n%s", w.logs.String())
	}
	if got := w.queued(t, pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)); len(got) != 1 {
		t.Errorf("the outbox holds %v; want the message kept for the retry", got)
	}
	checkJobKeepsTheMessageOut(t, w, reply)
}

func TestMailerCancelsPermanentFailure(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	// 550 is a permanent mailbox failure: another attempt sends the same message to the same refusal.
	const reply = "no such user here"
	server := startTLSServer(t, func(s *fakeServer) {
		s.rejectStep, s.rejectCode = "rcpt", 550
		s.rejectText = "5.1.1 <" + welcome.To + ">: " + reply
	})
	mailer := w.register(t, server)
	w.start(t)

	tenant := Tenant{Site: uuid.NewV7()}
	w.enqueue(t, mailer, tenant, welcome)
	attempt := w.waitForJob(t, "cancelled")
	if attempt != 1 {
		t.Errorf("the cancelled job took %d attempts; want 1", attempt)
	}
	if !strings.Contains(w.logs.String(), "permanent=true") {
		t.Errorf("the log does not record a permanent failure:\n%s", w.logs.String())
	}
	if got := w.queued(t, pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)); len(got) != 0 {
		t.Errorf("the outbox still holds %v after a permanent refusal", got)
	}
	checkJobKeepsTheMessageOut(t, w, reply)
}

// checkJobKeepsTheMessageOut checks the job row against the message it delivers. The row is the one place the queue
// writes that no tenant policy covers, so neither the arguments nor the recorded error may quote the recipient, the
// subject or a body, and the server's own reply — which names the mailbox it refused — must stay in the worker's log
// rather than in the column every scope can read.
func checkJobKeepsTheMessageOut(t *testing.T, w *queueWorld, reply string) {
	t.Helper()
	_, _, args := w.job(t)
	errs := w.jobErrors(t)
	if errs == "" {
		t.Error("the job recorded no error; want the failure the worker classified")
	}
	for _, value := range []string{welcome.To, welcome.Subject, welcome.Text, welcome.HTML, reply} {
		if strings.Contains(args, value) {
			t.Errorf("the job's arguments hold %q:\n%s", value, args)
		}
		if strings.Contains(errs, value) {
			t.Errorf("the job's recorded error holds %q:\n%s", value, errs)
		}
	}
	// The recipient is still recorded, where the tenant's own operator reads it.
	if !strings.Contains(w.logs.String(), welcome.To) {
		t.Errorf("the log does not name the recipient:\n%s", w.logs.String())
	}
}

// TestOutboxKeepsMailFromOtherTenants: the queue carries the outbox row's id and nothing else, and the row is readable
// only in the scope of the tenant it was queued for — neither another site's scope nor the empty one sees any of it
// (internal/platform/pg/migrations/00008_mail_outbox.sql).
func TestOutboxKeepsMailFromOtherTenants(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	// 451 keeps the queued message for the retry, so the row is still there once the attempt has finished.
	server := startTLSServer(t, func(s *fakeServer) { s.rejectStep, s.rejectCode = "rcpt", 451 })
	mailer := w.register(t, server)
	w.start(t)

	tenant := Tenant{Site: uuid.NewV7()}
	w.enqueue(t, mailer, tenant, welcome)
	w.waitForJob(t, "retryable", "scheduled")
	checkJobCarriesOnlyTheID(t, w)
	checkOutboxIsInOneScope(t, w, tenant)
}

// checkJobCarriesOnlyTheID checks that the job's arguments hold the outbox row's id and no part of the message: the job
// row belongs to no tenant, so no policy keeps another tenant's mail out of it.
func checkJobCarriesOnlyTheID(t *testing.T, w *queueWorld) {
	t.Helper()
	_, _, args := w.job(t)
	for _, secret := range []string{welcome.To, welcome.Subject, welcome.Text, welcome.HTML} {
		if strings.Contains(args, secret) {
			t.Errorf("the job's arguments hold %q:\n%s", secret, args)
		}
	}
	var id uuid.UUID
	if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM platform.mail_outbox`).Scan(&id)
	}); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if !strings.Contains(args, id.String()) {
		t.Errorf("the job's arguments are %q; want them to hold the outbox id %s", args, id)
	}
}

// checkOutboxIsInOneScope checks that the queued message is readable in its own tenant's scope and in no other.
func checkOutboxIsInOneScope(t *testing.T, w *queueWorld, tenant Tenant) {
	t.Helper()
	if own := w.queued(t, pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)); len(own) != 1 {
		t.Errorf("the tenant's scope reads %v; want its one queued message", own)
	}
	for name, scope := range map[string]pg.Scope{
		"another site":    pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, nil),
		"another account": pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}),
		"the empty":       pg.NewScope(false, nil, nil),
	} {
		if got := w.queued(t, scope); len(got) != 0 {
			t.Errorf("%s scope read %d queued messages (%v); want none", name, len(got), got)
		}
	}
}

// TestEnqueueRefusesAnotherTenant: the policy's WITH CHECK refuses a row for a site outside the caller's scope, so a flow
// cannot queue mail for another tenant. The queue runs, so nothing else could fail the transaction: what refuses the
// write is the policy.
func TestEnqueueRefusesAnotherTenant(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	mailer := w.register(t, startTLSServer(t, nil))
	w.start(t)
	own, other := uuid.NewV7(), uuid.NewV7()
	err := w.db.Tx(t.Context(), pg.NewScope(false, []uuid.UUID{own}, nil), func(tx pg.Tx) error {
		return mailer.Enqueue(t.Context(), tx, Tenant{Site: other}, welcome)
	})
	if got := pg.SQLState(err); got != "42501" {
		t.Errorf("Enqueue() error = %v (SQLSTATE %q); want the policy's refusal", err, got)
	}
	if got := w.queued(t, pg.NewScope(true, nil, nil)); len(got) != 0 {
		t.Errorf("the outbox holds %v after a refused enqueue", got)
	}
}

func TestRegisterWithoutSettings(t *testing.T) {
	t.Parallel()
	var logs syncBuffer
	// A node without SMTP settings has no queue to register on: email is disabled before the queue is touched.
	mailer, err := Register(t.Context(), nil, nil, Config{}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !strings.Contains(logs.String(), "email disabled") {
		t.Errorf("the log does not say email is disabled:\n%s", logs.String())
	}
	if err := mailer.Enqueue(t.Context(), pg.Tx{}, Tenant{Site: uuid.NewV7()}, welcome); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Enqueue() error = %v; want ErrNotConfigured", err)
	}
}

// TestEnqueueRejectsInvalidMessage: a message the send would refuse is refused here, before the outbox row and the job
// exist, so a caller learns at the call it got wrong instead of from a job cancelled later. The recipient and the
// subject that no header line can carry are part of this: they are decided by the same renderer the message goes
// through, so what Enqueue accepts is what Send can write.
func TestEnqueueRejectsInvalidMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*Message)
	}{
		{name: "an empty subject", edit: func(m *Message) { m.Subject = "" }},
		// A local part and a domain SMTPUTF8 would carry, which this node never offers.
		{name: "a recipient SMTP cannot carry", edit: func(m *Message) { m.To = "用户@example.com" }},
		{name: "a recipient no header line can carry", edit: func(m *Message) {
			m.To = strings.Repeat("x", 250) + "@example.com"
		}},
		{name: "a subject no header line can carry", edit: func(m *Message) { m.Subject = strings.Repeat("x", 1000) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newQueueWorld(t)
			mailer := w.register(t, startTLSServer(t, nil))

			msg := welcome
			c.edit(&msg)
			tenant := Tenant{Site: uuid.NewV7()}
			err := w.db.Tx(t.Context(), pg.NewScope(false, []uuid.UUID{tenant.Site}, nil), func(tx pg.Tx) error {
				return mailer.Enqueue(t.Context(), tx, tenant, msg)
			})
			if !errors.Is(err, ErrInvalidMessage) {
				t.Errorf("Enqueue() error = %v; want ErrInvalidMessage", err)
			}
			var jobs int
			if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM river.river_job WHERE kind = $1`, jobKind).Scan(&jobs)
			}); err != nil {
				t.Fatal(err)
			}
			if jobs != 0 {
				t.Errorf("an invalid message queued %d jobs", jobs)
			}
			if got := w.queued(t, pg.NewScope(false, []uuid.UUID{tenant.Site}, nil)); len(got) != 0 {
				t.Errorf("an invalid message left %v in the outbox", got)
			}
		})
	}
}

func TestTrackedMailReportsSMTPResult(t *testing.T) {
	for _, code := range []int{0, 550} {
		t.Run(fmt.Sprint(code), func(t *testing.T) { checkTrackedMail(t, code) })
	}
}

func checkTrackedMail(t *testing.T, code int) {
	t.Helper()

	w := newQueueWorld(t)
	server := startTLSServer(t, func(s *fakeServer) {
		if code != 0 {
			s.rejectStep, s.rejectCode = "rcpt", code
		}
	})
	mailer := w.register(t, server)
	receipt := uuid.NewV7()
	reported := make(chan bool, 1)
	mailer.Receipt = func(_ context.Context, id uuid.UUID, sent bool) error {
		if id != receipt {
			return errors.New("wrong receipt")
		}
		reported <- sent
		return nil
	}
	w.start(t)
	if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error { return mailer.EnqueueTracked(t.Context(), tx, Tenant{}, welcome, receipt) }); err != nil {
		t.Fatal(err)
	}
	select {
	case sent := <-reported:
		if sent != (code == 0) {
			t.Fatalf("sent=%v for SMTP %d", sent, code)
		}
	case <-t.Context().Done():
		t.Fatal("no SMTP receipt")
	}
	if code == 0 {
		w.waitForJob(t, "completed")
	} else {
		w.waitForJob(t, "cancelled")
	}
	if got := w.queued(t, pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, nil)); len(got) != 0 {
		t.Fatal("site read platform mail")
	}
}
