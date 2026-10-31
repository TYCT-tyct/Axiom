package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// CallSummary is the support projection. It cannot carry supplier costs or routing details.
type CallSummary struct {
	ID, Site, Account uuid.UUID
	At                time.Time
	Model, ErrorKind  string
	LatencyMS         int
	PriceNano         int64
	Currency          string
}

// AdminQuery filters the platform's call summaries. The HTTP boundary bounds time and page size.
type AdminQuery struct {
	Since, Until       time.Time
	Account, RequestID *uuid.UUID
	Model              string // exact model name; empty narrows nothing
	Status             string // "error" or "ok"; empty narrows nothing
	After              *Cursor
	PageSize           int
}

const adminCallsSQL = `SELECT id, at, site_id, account_id, model, error_kind, latency_ms, price_nano, currency
 FROM observability.call_log
 WHERE (%[1]s,id) < (%[2]s,$2::uuid) AND at >= $3 AND at < $4
 AND ($5::text = '' OR account_id = nullif($5,'')::uuid)
 AND ($6::text = '' OR id = nullif($6,'')::uuid)
 AND ($7::text = '' OR model = $7)
 AND ($8::text = '' OR (error_kind IS DISTINCT FROM '') = ($8 = 'error'))
 ORDER BY %[1]s DESC,call_log.id DESC LIMIT $9`

// The UTC expression isolates the global index from account queries: PostgreSQL otherwise prefers
// chronological scans for correlated sparse account filters, reading thousands of blocks before LIMIT.
func adminListSQL(filtered bool) string {
	if filtered {
		return fmt.Sprintf(adminCallsSQL, "call_log.at", "$1::timestamptz")
	}
	return fmt.Sprintf(adminCallsSQL, "(call_log.at AT TIME ZONE 'UTC')", "($1::timestamptz AT TIME ZONE 'UTC')")
}

// ListAdmin reads only the columns a support operator may see, in index order.
func ListAdmin(ctx context.Context, tx pg.Tx, q AdminQuery) ([]CallSummary, *Cursor, error) {
	if err := planOwnPage(ctx, tx); err != nil {
		return nil, nil, err
	}
	at, id := cursorOf(OwnQuery{Until: q.Until, After: q.After})
	rows, err := tx.Query(ctx, adminListSQL(q.Account != nil || q.RequestID != nil), at, id, q.Since, q.Until, uuidFilter(q.Account), uuidFilter(q.RequestID), q.Model, q.Status, q.PageSize+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make([]CallSummary, 0, q.PageSize+1)
	for rows.Next() {
		var c CallSummary
		if err := rows.Scan(&c.ID, &c.At, &c.Site, &c.Account, &c.Model, &c.ErrorKind, &c.LatencyMS, &c.PriceNano, &c.Currency); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *Cursor
	if len(out) > q.PageSize {
		out = out[:q.PageSize]
		c := out[len(out)-1]
		next = &Cursor{At: c.At, ID: c.ID}
	}
	return out, next, nil
}
