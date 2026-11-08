package operations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestChainStepsFail: each step of the chaining hands a failure of its database to its caller instead of linking or
// halting on what it could not read, and neither a link nor a fault is written where the transaction cannot write.
func TestChainStepsFail(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	scope := pg.NewScope(true, nil, nil)
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO platform.audit_log (at, actor_kind, actor_id, action,
		module, target_kind, target_id, detail) VALUES (now() - interval '5 minutes', 'system', 'test.chain', 'thing.touch',
		'operations', 'thing', 't1', '{}')`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, step := range map[string]func(tx pg.Tx) error{
		"lockChain":   func(tx pg.Tx) error { _, err := lockChain(canceled, tx); return err },
		"checkHead":   func(tx pg.Tx) error { _, err := checkHead(canceled, tx); return err },
		"extend":      func(tx pg.Tx) error { _, err := extend(canceled, tx, 0); return err },
		"signalLag":   func(tx pg.Tx) error { return signalLag(canceled, tx, 0) },
		"verifyBatch": func(tx pg.Tx) error { _, _, err := verifyBatch(canceled, tx, 1); return err },
		"verifyBatch after a link": func(tx pg.Tx) error {
			_, _, err := verifyBatch(canceled, tx, 2)
			return err
		},
		"haltAt": func(tx pg.Tx) error { return haltAt(canceled, tx, &fault{"late", 1, uuid.NewV7()}, nil) },
	} {
		if err := db.Tx(t.Context(), scope, step); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}
	for name, step := range map[string]func(tx pg.Tx) error{
		"extend": func(tx pg.Tx) error { _, err := extend(t.Context(), tx, time.Minute); return err },
		"haltAt": func(tx pg.Tx) error { return haltAt(t.Context(), tx, &fault{"late", 1, uuid.NewV7()}, nil) },
	} {
		if err := db.ReadTx(t.Context(), scope, step); err == nil {
			t.Errorf("%s wrote in a read-only transaction", name)
		}
	}
}

// TestAuditChainWork: a run of the job that fails, the chaining or the check, is logged and not retried.
func TestAuditChainWork(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for whole, want := range map[bool]string{false: "msg=\"operations: the audit chain stopped\"", true: "the check of the audit chain stopped"} {
		var logs bytes.Buffer
		work := chainWork(db, pg.NewScope(true, nil, nil), slog.New(slog.NewTextHandler(&logs, nil)))
		if err := work(canceled, &river.Job[chainArgs]{Args: chainArgs{Whole: whole}}); err != nil || !strings.Contains(logs.String(), want) {
			t.Errorf("whole %v: %v %s", whole, err, logs.String())
		}
	}
}

func TestAuditChainJob(t *testing.T) {
	opts := chainArgs{}.InsertOpts()
	if (chainArgs{}).Kind() != "operations.audit_chain" || opts.Queue != pg.QueueArchive || !opts.UniqueOpts.ByArgs {
		t.Errorf("the audit chain's job: %+v", opts)
	}
}
