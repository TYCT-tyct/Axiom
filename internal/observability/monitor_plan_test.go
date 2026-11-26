package observability

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// explainPlan runs EXPLAIN (ANALYZE, BUFFERS) on sql in the platform scope, planned as ListAdmin plans its page.
func explainPlan(t *testing.T, db *pg.DB, sql string, args ...any) string {
	t.Helper()
	var lines []string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := planOwnPage(t.Context(), tx); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// TestMonitorAndDashboardPlansReadTheirWindow: each read stays within a fixed number of blocks whatever the call volume.
// The monitor's rollups keep two hours of minutes and two days of hours for 60 endpoints, so a window is a large part
// of them and PostgreSQL may scan the table, whose size the pruning bounds; the site's usage is thirty days among other
// sites' and is read through its index; the web calls are one in a hundred and come from the plugin index
// (docs/domains/observability.md「监控汇总」).
func TestMonitorAndDashboardPlansReadTheirWindow(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database, false)
	site := "00000000-0000-4000-8000-0000000000ff"
	if err := pgtest.Run(t.Context(), database.Superuser,
		`INSERT INTO observability.monitor_counts
		 SELECT w, date_bin(make_interval(secs => w), now() - make_interval(secs => b * w), '1970-01-01T00:00:00Z'::timestamptz),
		   ('00000000-0000-4000-8000-' || lpad(e::text, 12, '0'))::uuid, '', 10, 0, 1000
		 FROM unnest(ARRAY[60, 3600]) w, generate_series(0, 47) b, generate_series(1, 60) e WHERE w = 3600 OR b < 120`,
		`INSERT INTO observability.monitor_counts
		 SELECT 60, date_bin('60 seconds', now() - make_interval(mins => b), '1970-01-01T00:00:00Z'::timestamptz),
		   ('00000000-0000-4000-8000-' || lpad(e::text, 12, '0'))::uuid, '', 10, 0, 1000
		 FROM generate_series(48, 119) b, generate_series(1, 60) e`,
		`INSERT INTO observability.monitor_latency
		 SELECT c.width, c.bucket, m, c.endpoint_id, bin, 1 FROM observability.monitor_counts c, generate_series(1, 2) m, generate_series(10, 20) bin`,
		`INSERT INTO observability.usage_hourly
		 SELECT CASE WHEN s = 0 THEN '`+site+`'::uuid ELSE ('00000000-0000-4000-8000-' || lpad((s + 900)::text, 12, '0'))::uuid END,
		   ('00000000-0000-4000-8000-' || lpad(a::text, 12, '0'))::uuid, date_trunc('hour', now()) - make_interval(hours => h),
		   '00000000-0000-4000-8000-000000000111', '00000000-0000-4000-8000-000000000121', 'm' || (a % 5)::text, 'USD',
		   2, 2, 100, 10, 0, 0, 0, 0, 0, 1000
		 FROM generate_series(0, 3) s, generate_series(1, 100) a, generate_series(0, 719) h`,
		`INSERT INTO observability.first_token_hourly
		 SELECT '`+site+`', date_trunc('hour', now()) - make_interval(hours => h), bin, 3 FROM generate_series(0, 719) h, generate_series(10, 30) bin`,
		`UPDATE observability.call_log SET plugins = '[{"plugin":"web","ok":true,"uses":1,"ms":5}]' WHERE (extract(epoch FROM at) * 4)::bigint % 100 = 0`,
		`ANALYZE observability.monitor_counts`, `ANALYZE observability.monitor_latency`, `ANALYZE observability.usage_hourly`,
		`ANALYZE observability.first_token_hourly`, `ANALYZE observability.call_log`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	type read struct {
		name, sql, index string
		args             []any
		blocks           int
	}
	var reads []read
	for _, width := range []time.Duration{MonitorMinute, MonitorHour} {
		since, until := MonitorWindow(width, now)
		args := []any{int(width / time.Second), since, until}
		reads = append(reads,
			read{"counts " + width.String(), monitorCountsSQL, "monitor_counts", args, 200},
			read{"latency " + width.String(), monitorLatencySQL, "monitor_latency", args, 2500},
			read{"line latency " + width.String(), monitorEndpointLatencySQL, "monitor_latency", args, 2500})
	}
	first, until := FirstHour(now.Add(-24*time.Hour)), now
	usage := []any{site, first, until}
	money := []any{site, first, until, "USD"}
	reads = append(reads,
		read{"site tokens", siteUsageTokensSQL, "usage_hourly_site_hour", usage, 400},
		read{"site spenders", siteSpendersSQL, "usage_hourly_site_hour", append(money, adminTop), 400},
		read{"site models", siteModelsSQL, "usage_hourly_site_hour", append(money, []string{"m1", "m2"}), 400},
		read{"site first tokens", siteFirstOutputSQL, "first_token_hourly_pkey", usage, 100},
		read{
			"web calls", adminListSQL(false, true), "Index Scan",
			[]any{now, uuid.Nil().String(), now.Add(-48 * time.Hour), now, "", "", "", "", 51, "", PluginWeb},
			50,
		},
	)
	for _, r := range reads {
		plan := explainPlan(t, db, r.sql, r.args...)
		scanned := strings.Contains(plan, "call_log ")
		if !strings.HasPrefix(r.index, "monitor_") {
			scanned = strings.Contains(plan, "Seq Scan")
		}
		if scanned || !strings.Contains(plan, r.index) {
			t.Errorf("%s does not read its window through %s:\n%s", r.name, r.index, plan)
		}
		if n := buffers(t, plan); n > r.blocks {
			t.Errorf("%s touched %d blocks, want at most %d:\n%s", r.name, n, r.blocks, plan)
		}
	}
}
