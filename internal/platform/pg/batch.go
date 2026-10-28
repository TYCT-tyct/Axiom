package pg

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Statement is one SQL statement of a batch. Modules use sqlc's query constants so their schema and SQL remain
// checked at generation time. Inside a READ COMMITTED transaction each statement takes its own snapshot.
type Statement struct {
	SQL  string
	Args []any
}

// BatchResults hides the underlying connection, just as Tx does. Tx.Batch closes it before another query can run.
type BatchResults struct{ results pgx.BatchResults }

type statementBatch struct {
	batch   pgx.Batch
	queries []pgx.QueuedQuery
}

var statementBatches = sync.Pool{New: func() any { return new(statementBatch) }}

// Batch sends statements together, reads their results through fn, then drains every remaining result even after an
// error or panic. A database error found while draining takes precedence over a business refusal. The batch stays
// in this scoped transaction with its original commit boundary. A row lock in an earlier statement is granted before
// a later statement starts; merging them into one SQL query would not preserve that snapshot boundary.
func (t Tx) Batch(ctx context.Context, statements []Statement, fn func(BatchResults) error) (err error) {
	b := statementBatches.Get().(*statementBatch)
	if cap(b.queries) < len(statements) {
		b.queries = make([]pgx.QueuedQuery, len(statements))
		b.batch.QueuedQueries = make([]*pgx.QueuedQuery, len(statements))
	}
	b.queries = b.queries[:len(statements)]
	b.batch.QueuedQueries = b.batch.QueuedQueries[:len(statements)]
	for i, s := range statements {
		// pgx mutates query metadata while sending; a fresh value must replace every reused entry, including that metadata.
		b.queries[i] = pgx.QueuedQuery{SQL: s.SQL, Arguments: hideFromRewriters(s.Args)}
		b.batch.QueuedQueries[i] = &b.queries[i]
	}
	results := t.tx.SendBatch(ctx, &b.batch)
	defer func() {
		if closeErr := results.Close(); closeErr != nil {
			err = closeErr
		}
		clear(b.queries)
		statementBatches.Put(b)
	}()
	return fn(BatchResults{results})
}

// Exec consumes the next command result without exposing a connection.
func (b BatchResults) Exec() (pgconn.CommandTag, error) { return b.results.Exec() }

// QueryRow consumes the next row with the same connection guards as Tx.QueryRow.
func (b BatchResults) QueryRow() pgx.Row { return row{b.results.QueryRow()} }
