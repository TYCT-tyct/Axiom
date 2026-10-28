package pg_test

import (
	"context"
	"errors"
	"testing"

	"axiom/internal/platform/pg"
)

func TestBatchKeepsTransactionAndScope(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	ctx := t.Context()
	rejected := errors.New("reject after batch")
	checkBatchEnding(t, db, rejected)
	checkBatchEnding(t, db, nil)
	if n := count(t, db, pg.AccountScope(account1)); n != 1 {
		t.Errorf("committed %d rows, want 1", n)
	}
	if n := count(t, db, pg.AccountScope(account2)); n != 0 {
		t.Errorf("other account read %d rows", n)
	}
	err := db.Tx(ctx, pg.AccountScope(account2), func(tx pg.Tx) error {
		return tx.Batch(ctx, []pg.Statement{{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'foreign')", Args: []any{site1, account1}}}, func(pg.BatchResults) error { return nil })
	})
	if pg.SQLState(err) != "42501" {
		t.Errorf("out-of-scope write: %v", err)
	}
}

func checkBatchEnding(t *testing.T, db *pg.DB, ending error) {
	t.Helper()
	ctx := t.Context()
	err := db.Tx(ctx, pg.AccountScope(account1), func(tx pg.Tx) error {
		return tx.Batch(ctx, []pg.Statement{
			{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'one')", Args: []any{site1, account1}},
			{SQL: "SELECT count(*) FROM rlstest.items"},
		}, func(batch pg.BatchResults) error {
			if _, err := batch.Exec(); err != nil {
				return err
			}
			var n int
			if err := batch.QueryRow().Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("batch sees %d rows, want its own write", n)
			}
			return ending
		})
	})
	if !errors.Is(err, ending) {
		t.Fatalf("batch result: %v, want %v", err, ending)
	}
}

// An unconsumed late error overrides a business refusal, rolls back earlier writes and leaves the next call usable.
func TestBatchCloseReportsLateFailure(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	ctx := t.Context()
	rejected := errors.New("business refusal")
	err := db.Tx(ctx, pg.AccountScope(account1), func(tx pg.Tx) error {
		return tx.Batch(ctx, []pg.Statement{
			{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'rolled back')", Args: []any{site1, account1}},
			{SQL: "SELECT 1/0"},
		}, func(pg.BatchResults) error { return rejected })
	})
	if pg.SQLState(err) != "22012" || errors.Is(err, rejected) {
		t.Errorf("late SQL error: %v", err)
	}
	if n := count(t, db, pg.AccountScope(account1)); n != 0 {
		t.Errorf("failed transaction left %d rows", n)
	}
	if err := db.Tx(ctx, pg.AccountScope(account1), func(tx pg.Tx) error {
		return tx.Batch(ctx, []pg.Statement{{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'independent')", Args: []any{site1, account1}}}, func(pg.BatchResults) error { return nil })
	}); err != nil {
		t.Fatalf("independent transaction after failure: %v", err)
	}
	if n := count(t, db, pg.AccountScope(account1)); n != 1 {
		t.Errorf("independent transaction left %d rows", n)
	}
}

func TestBatchHidesTheConnection(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	for _, rewrite := range []bool{false, true} {
		spy := new(connSpy)
		if err := db.Tx(t.Context(), pg.AccountScope(account1), func(tx pg.Tx) error {
			statement := pg.Statement{SQL: "SELECT 'hidden'::text"}
			if rewrite {
				statement.Args = []any{spy}
			}
			return tx.Batch(t.Context(), []pg.Statement{statement}, func(batch pg.BatchResults) error {
				if rewrite {
					var text string
					return batch.QueryRow().Scan(&text)
				}
				return batch.QueryRow().Scan(spy)
			})
		}); err != nil {
			t.Errorf("rewrite %v: %v", rewrite, err)
		}
		if !spy.called || spy.conn != nil {
			t.Errorf("rewrite %v: called %v, connection %v", rewrite, spy.called, spy.conn)
		}
	}
}

func TestBatchDrainsAfterNoRows(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	ctx := t.Context()
	if err := db.Tx(ctx, pg.AccountScope(account1), func(tx pg.Tx) error {
		if err := tx.Batch(ctx, []pg.Statement{{SQL: "SELECT 1 WHERE false"}, {SQL: "SELECT 2"}}, func(batch pg.BatchResults) error {
			var n int
			if err := batch.QueryRow().Scan(&n); !errors.Is(err, pg.ErrNoRows) {
				t.Errorf("empty row: %v", err)
			}
			return nil
		}); err != nil {
			return err
		}
		var n int
		return tx.QueryRow(ctx, "SELECT 3").Scan(&n)
	}); err != nil {
		t.Fatalf("query after drained batch: %v", err)
	}
}

func TestBatchCancellationRollsBack(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	ctx, cancel := context.WithCancel(t.Context())
	err := db.Tx(ctx, pg.AccountScope(account1), func(tx pg.Tx) error {
		if err := tx.Batch(ctx, []pg.Statement{{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'cancelled')", Args: []any{site1, account1}}}, func(pg.BatchResults) error { return nil }); err != nil {
			return err
		}
		cancel()
		return nil
	})
	cancel()
	if err == nil {
		t.Error("cancelled batch committed")
	}
	if n := count(t, db, pg.AccountScope(account1)); n != 0 {
		t.Errorf("cancelled batch left %d rows", n)
	}
}

func TestBatchPanicRollsBack(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	func() {
		defer func() {
			if value := recover(); value != "rejected" {
				t.Errorf("panic: %v", value)
			}
		}()
		if err := db.Tx(t.Context(), pg.AccountScope(account1), func(tx pg.Tx) error {
			return tx.Batch(t.Context(), []pg.Statement{{SQL: "INSERT INTO rlstest.items VALUES ($1,$2,'panicked')", Args: []any{site1, account1}}}, func(pg.BatchResults) error { panic("rejected") })
		}); err != nil {
			t.Error(err)
		}
	}()
	if n := count(t, db, pg.AccountScope(account1)); n != 0 {
		t.Errorf("panicked batch left %d rows", n)
	}
}
