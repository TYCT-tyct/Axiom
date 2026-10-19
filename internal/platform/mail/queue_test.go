package mail

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// queueWorld is one node's queue over a private copy of the database.
type queueWorld struct {
	db    *pg.DB
	queue *pg.Queue
	logs  *bytes.Buffer
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
		logs:  &bytes.Buffer{},
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

func (w *queueWorld) enqueue(t *testing.T, mailer *Mailer, msg Message) {
	t.Helper()
	if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return mailer.Enqueue(t.Context(), tx, msg)
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
}

// job is the node's most recent mail job: its state, as River spells it, and the attempt it reached.
func (w *queueWorld) job(t *testing.T) (string, int) {
	t.Helper()
	var (
		state   string
		attempt int
	)
	err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state::text, attempt FROM river.river_job
			WHERE kind = $1 ORDER BY id DESC LIMIT 1`, jobKind).Scan(&state, &attempt)
	})
	if err != nil {
		t.Fatalf("read the mail job: %v", err)
	}
	return state, attempt
}

func (w *queueWorld) waitForJob(t *testing.T, want string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, attempt := w.job(t)
		if state == want {
			return attempt
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the job stayed %q; want %q", state, want)
		case <-ticker.C:
		}
	}
}

// register registers a mailer that talks to the fake server instead of a real SMTP host, and returns it.
func (w *queueWorld) register(t *testing.T, server *fakeServer) *Mailer {
	t.Helper()
	mailer, err := Register(t.Context(), w.queue, Config{
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

	w.enqueue(t, mailer, welcome)
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
}

func TestMailerRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	// 451 is a temporary server failure (RFC 5321 section 4.2.1): the job goes back to scheduled and River retries it.
	server := startTLSServer(t, func(s *fakeServer) { s.rejectStep, s.rejectCode = "rcpt", 451 })
	mailer := w.register(t, server)
	w.start(t)

	w.enqueue(t, mailer, welcome)
	attempt := w.waitForJob(t, "scheduled")
	if attempt != 1 {
		t.Errorf("the retryable job took %d attempts; want 1", attempt)
	}
	if !strings.Contains(w.logs.String(), "permanent=false") {
		t.Errorf("the log does not record a transient failure:\n%s", w.logs.String())
	}
}

func TestMailerCancelsPermanentFailure(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	// 550 is a permanent mailbox failure: another attempt sends the same message to the same refusal.
	server := startTLSServer(t, func(s *fakeServer) { s.rejectStep, s.rejectCode = "rcpt", 550 })
	mailer := w.register(t, server)
	w.start(t)

	w.enqueue(t, mailer, welcome)
	attempt := w.waitForJob(t, "cancelled")
	if attempt != 1 {
		t.Errorf("the cancelled job took %d attempts; want 1", attempt)
	}
	if !strings.Contains(w.logs.String(), "permanent=true") {
		t.Errorf("the log does not record a permanent failure:\n%s", w.logs.String())
	}
}

func TestRegisterWithoutSettings(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	// A node without SMTP settings has no queue to register on: email is disabled before the queue is touched.
	mailer, err := Register(t.Context(), nil, Config{}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !strings.Contains(logs.String(), "email disabled") {
		t.Errorf("the log does not say email is disabled:\n%s", logs.String())
	}
	if err := mailer.Enqueue(t.Context(), pg.Tx{}, welcome); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Enqueue() error = %v; want ErrNotConfigured", err)
	}
}

func TestEnqueueRejectsInvalidMessage(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	server := startTLSServer(t, nil)
	mailer := w.register(t, server)

	invalid := welcome
	invalid.Subject = ""
	if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return mailer.Enqueue(t.Context(), tx, invalid)
	}); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("Enqueue() error = %v; want ErrInvalidMessage", err)
	}
	var count int
	if err := w.db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM river.river_job WHERE kind = $1`, jobKind).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("an invalid message queued %d jobs", count)
	}
}
