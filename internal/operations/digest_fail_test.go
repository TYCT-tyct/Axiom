package operations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestDigestStepsFail: each step of the digest hands a failure of its database to its caller.
func TestDigestStepsFail(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	scope := pg.NewScope(true, nil, nil)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, step := range map[string]func(tx pg.Tx) error{
		"digestOf":      func(tx pg.Tx) error { _, err := digestOf(canceled, tx, time.Now(), time.Now()); return err },
		"digestMoney":   func(tx pg.Tx) error { _, err := digestMoney(canceled, tx, time.Now()); return err },
		"digestChain":   func(tx pg.Tx) error { _, err := digestChain(canceled, tx); return err },
		"AuditDigests":  func(tx pg.Tx) error { _, _, err := AuditDigests(canceled, tx, nil, 1); return err },
		"databaseClock": func(tx pg.Tx) error { _, _, err := databaseClock(0)(canceled, tx); return err },
	} {
		if err := db.Tx(t.Context(), scope, step); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}
	for name, run := range map[string]func() error{
		"nextDigestDay":    func() error { _, _, _, err := nextDigestDay(canceled, db, scope, databaseClock(0)); return err },
		"saveDigest":       func() error { _, err := saveDigest(canceled, db, scope, time.Now(), AuditDigest{}); return err },
		"makeAuditDigests": func() error { _, err := makeAuditDigests(canceled, db, scope, databaseClock(0)); return err },
	} {
		if err := run(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}
}

// TestDigestWork: a failing run of the job is logged and not retried, and a successful one logs nothing.
func TestDigestWork(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	var logs bytes.Buffer
	work := digestWork(db, pg.NewScope(true, nil, nil), slog.New(slog.NewTextHandler(&logs, nil)))
	if err := work(t.Context(), nil); err != nil || logs.Len() != 0 {
		t.Errorf("a run that makes yesterday's digest: %v %s", err, logs.String())
	}
	if err := work(canceled, nil); err != nil || !strings.Contains(logs.String(), "the audit digest stopped") {
		t.Errorf("a failing run: %v %s", err, logs.String())
	}
	if opts := (digestArgs{}).InsertOpts(); (digestArgs{}).Kind() != "operations.audit_digest" || opts.Queue != pg.QueueArchive || !opts.UniqueOpts.ByArgs {
		t.Errorf("the digest's job: %+v", opts)
	}
}
