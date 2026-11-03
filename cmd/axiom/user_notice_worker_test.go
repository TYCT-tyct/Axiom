package main

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"testing"
	"time"

	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

func TestUserNoticeRunDrainsWithoutBalanceJournal(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.owner()
	logger := slog.New(slog.DiscardHandler)
	q := f.db.NewQueue(logger, pg.PlatformScope(), nil)
	mailer, err := axmail.Register(t.Context(), f.db, q, axmail.Config{Host: "localhost", Port: 1, From: mail.Address{Address: "notices@example.com"}}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	f.run(`INSERT INTO river.river_queue(name,paused_at) VALUES('notifications',now()) ON CONFLICT(name) DO UPDATE SET paused_at=now()`)
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	f.run(`INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value)
 SELECT site_id,account_id,id,'backlog-'||n,'factor_changed',1 FROM accounts.users CROSS JOIN generate_series(1,260) n WHERE email='owner@example.com'`)
	if err := runUserNotices(t.Context(), f.db, q, mailer, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := f.text(`SELECT count(*)::text FROM accounts.user_notices WHERE NOT dispatched`); got != "0" {
		t.Fatalf("undrained backlog: %s", got)
	}
	if got := f.text(`SELECT count(*)::text FROM platform.mail_outbox`); got != "260" {
		t.Fatalf("mail backlog: %s", got)
	}
}

func TestUserNoticeBudgetIsNormalCompletion(t *testing.T) {
	t.Parallel()
	if err := runUserNotices(t.Context(), nil, nil, nil, 0); err != nil {
		t.Fatalf("exhausted before starting: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runUserNotices(ctx, nil, nil, nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation: %v", err)
	}
	f := newConsoleFixture(t)
	f.owner()
	f.run(`INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value,dispatched,webhook_state)
 SELECT site_id,account_id,id,'budget','factor_changed',1,true,'queued' FROM accounts.users`)
	locked, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE accounts.user_notices SET webhook_state='queued' WHERE source='budget'`); err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	}()
	select {
	case <-locked:
	case err := <-finished:
		t.Fatalf("lock: %v", err)
	}
	err := runUserNotices(t.Context(), f.db, nil, nil, 1500*time.Millisecond)
	close(release)
	lockErr := <-finished
	if err != nil || lockErr != nil {
		t.Fatalf("budget during transaction: %v; lock: %v", err, lockErr)
	}
}
