package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
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
	Pending           bool // the call's settlement never reached the log: PriceNano is not its price (README「写入」)
	Plugins           []PluginRun
}

// AdminQuery filters the platform's call summaries. The HTTP boundary bounds time and page size.
type AdminQuery struct {
	Since, Until       time.Time
	Account, RequestID *uuid.UUID
	Site               *uuid.UUID // one site's calls; nil is every site the scope sees
	Model              string     // exact model name; empty narrows nothing
	Status             string     // "error" or "ok"; empty narrows nothing
	Plugin             string     // PluginWeb or PluginFileParser: calls that ran it; empty narrows nothing
	After              *Cursor
	PageSize           int
}

const adminCallsSQL = `SELECT id, at, site_id, account_id, model, error_kind, latency_ms, price_nano, currency, billing_pending, plugins
 FROM observability.call_log
 WHERE (%[1]s,id) < (%[2]s,$2::uuid) AND at >= $3 AND at < $4
 AND ($5::text = '' OR account_id = nullif($5,'')::uuid)
 AND ($6::text = '' OR id = nullif($6,'')::uuid)
 AND ($7::text = '' OR model = $7)
 AND ($8::text = '' OR (error_kind IS DISTINCT FROM '') = ($8 = 'error'))
 AND ($10::text = '' OR site_id = nullif($10,'')::uuid)
 AND ($11::text = '' OR plugins @> jsonb_build_array(jsonb_build_object('plugin', $11::text)))%[3]s
 ORDER BY %[1]s DESC,call_log.id DESC LIMIT $9`

// The UTC expression isolates the global index from account queries: PostgreSQL otherwise prefers
// chronological scans for correlated sparse account filters, reading thousands of blocks before LIMIT. A plugin filter
// also states the plugin index's predicate, which the planner cannot infer from the containment test.
func adminListSQL(filtered, plugin bool) string {
	used := ""
	if plugin {
		used = "\n AND plugins <> '[]'::jsonb"
	}
	if filtered {
		return fmt.Sprintf(adminCallsSQL, "call_log.at", "$1::timestamptz", used)
	}
	return fmt.Sprintf(adminCallsSQL, "(call_log.at AT TIME ZONE 'UTC')", "($1::timestamptz AT TIME ZONE 'UTC')", used)
}

// ListAdmin reads only the columns a support operator may see, in index order, and the log's coverage: a window that
// reaches archived months is incomplete, not empty.
func ListAdmin(ctx context.Context, tx pg.Tx, q AdminQuery) ([]CallSummary, *Cursor, Coverage, error) {
	coverage, err := CallCoverage(ctx, tx)
	if err != nil {
		return nil, nil, Coverage{}, err
	}
	if err := planOwnPage(ctx, tx); err != nil {
		return nil, nil, Coverage{}, err
	}
	at, id := cursorOf(OwnQuery{Until: q.Until, After: q.After})
	rows, err := tx.Query(ctx, adminListSQL(q.Account != nil || q.RequestID != nil, q.Plugin != ""), at, id, q.Since, q.Until, uuidFilter(q.Account), uuidFilter(q.RequestID), q.Model, q.Status, q.PageSize+1, uuidFilter(q.Site), q.Plugin)
	if err != nil {
		return nil, nil, Coverage{}, err
	}
	defer rows.Close()
	out := make([]CallSummary, 0, q.PageSize+1)
	for rows.Next() {
		var c CallSummary
		var plugins []byte
		if err := rows.Scan(&c.ID, &c.At, &c.Site, &c.Account, &c.Model, &c.ErrorKind, &c.LatencyMS, &c.PriceNano, &c.Currency, &c.Pending, &plugins); err != nil {
			return nil, nil, Coverage{}, err
		}
		if c.Plugins, err = readPluginRuns(plugins); err != nil {
			return nil, nil, Coverage{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, Coverage{}, err
	}
	var next *Cursor
	if len(out) > q.PageSize {
		out = out[:q.PageSize]
		c := out[len(out)-1]
		next = &Cursor{At: c.At, ID: c.ID}
	}
	return out, next, coverage, nil
}

// readPluginRuns reads the plugins column back; an empty array is no run.
func readPluginRuns(text []byte) ([]PluginRun, error) {
	var stored []pluginRow
	if err := jsonx.Unmarshal(text, &stored); err != nil {
		return nil, fmt.Errorf("observability: read a call's plugins: %w", err)
	}
	if len(stored) == 0 {
		return nil, nil
	}
	runs := make([]PluginRun, len(stored))
	for i, p := range stored {
		runs[i] = PluginRun{Plugin: p.Plugin, OK: p.OK, Engine: p.Engine, Uses: p.Uses, Duration: time.Duration(p.MS) * time.Millisecond, PriceNano: -1}
		if p.PriceNano != nil {
			runs[i].PriceNano = *p.PriceNano
		}
	}
	return runs, nil
}
