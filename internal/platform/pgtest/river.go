package pgtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"
	"github.com/riverqueue/river/rivertype"
)

// WorkJob commits one attempt through River's test executor. Only River metadata uses the raw transaction;
// the worker's business queries still use its scoped pg.DB. A previous row retries the same durable job.
func WorkJob[T river.JobArgs](t testing.TB, database Database, worker river.Worker[T], args T, previous *rivertype.JobRow, clock rivertype.TimeGenerator) (*rivertest.WorkResult, error) {
	t.Helper()
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, database.App)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // The committed attempt already reports its outcome.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // Commit or the worker already reports its outcome.
	executor := rivertest.NewWorker(t, riverpgxv5.New(nil), &river.Config{Schema: "river", Logger: slog.New(slog.DiscardHandler), Test: river.TestConfig{Time: clock}}, worker)
	var result *rivertest.WorkResult
	if previous == nil {
		result, err = executor.Work(ctx, t, tx, args, nil)
	} else {
		result, err = executor.WorkJob(ctx, t, tx, previous)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		t.Fatal(commitErr)
	}
	return result, err
}
