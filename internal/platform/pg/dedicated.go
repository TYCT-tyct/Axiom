package pg

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Dedicated is one connection of its own, outside both pools, for work whose deadline must not wait for a pooled
// connection: the holder's heartbeat and reap (internal/billing/README.md「持有者与截止时间」). It connects with its
// first transaction and, after any failure, replaces the connection before the next one, so a beat after an outage
// runs on a fresh session. Each transaction takes its deadline from ctx.
type Dedicated struct {
	config *pgx.ConnConfig
	tracer trace.Tracer
	// A beat and a reap may arrive at once, and the two run one after another on the one connection: a second
	// connection would be another session outside the pool, which is what this type exists to avoid.
	mu   sync.Mutex
	conn *pgx.Conn
}

// Dedicated returns a connection of its own. It connects with the first transaction, not here, so a database that is
// away at this moment fails that call and is retried by the next beat rather than failing the start of the loop.
func (db *DB) Dedicated() *Dedicated {
	return &Dedicated{config: db.config, tracer: db.tracer}
}

// Tx runs fn in a transaction limited to scope and commits if fn returns nil, as DB.Tx does with a pooled connection.
//
// A failed transaction also drops the connection, whatever failed: a beat whose deadline ran out in the middle of its
// statement may leave the session busy with it, and only a new connection is known to be clean.
func (d *Dedicated) Tx(ctx context.Context, scope Scope, fn func(Tx) error) (err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		conn, err := pgx.ConnectConfig(ctx, d.config)
		if err != nil {
			return fmt.Errorf("connect the dedicated connection: %w", err)
		}
		d.conn = conn
	}
	ctx, span := startSpan(ctx, d.tracer)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "transaction failed")
		}
		span.End()
	}()
	err = pgx.BeginTxFunc(ctx, d.conn, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return fmt.Errorf("set the scope: %w", err)
		}
		return fn(Tx{tx})
	})
	if err != nil {
		d.close(ctx)
	}
	return err
}

// Close closes the connection it holds; the next transaction connects anew, so only the loop that is done with it
// calls this. A Close with no connection is free.
func (d *Dedicated) Close(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.close(ctx)
}

// close drops the connection. The close itself is not the error to report, and the caller's context may be the one
// that has already run out.
func (d *Dedicated) close(ctx context.Context) {
	if d.conn == nil {
		return
	}
	_ = d.conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the transaction's error is the one to report
	d.conn = nil
}
