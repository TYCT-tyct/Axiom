package observability

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestUsage90DayPlanReadsOnlyRollups(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	if err := pgtest.Run(t.Context(), database.Superuser, `
        INSERT INTO observability.usage_hourly
            (site_id,account_id,hour,workspace_id,key_id,model,currency,requests,successes,
            input,output,cache_read,cache_write,cache_rate_requests,cache_rate_read,cache_rate_input,spend)
        SELECT '00000000-0000-4000-8000-0000000000ff',
            CASE WHEN n/2160=0 THEN '`+heavyAccount+`'::uuid
                ELSE ('00000000-0000-4000-8000-'||lpad((n/2160+2)::text,12,'0'))::uuid END,
            '2026-10-01T00:00:00Z'::timestamptz + (n%2160)*interval '1 hour',
            '00000000-0000-4000-8000-000000000111','00000000-0000-4000-8000-000000000121',
            CASE WHEN n%2=0 THEN 'model-a' ELSE 'model-b' END,'USD',100,90,12000,3000,8000,0,100,8000,20000,500000000
        FROM generate_series(0,215999) n`, `ANALYZE observability.usage_hourly`); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var plan string
	if err := db.ReadTx(t.Context(), accountScope(uuid.MustParse(heavyAccount)), func(tx pg.Tx) error {
		if err := planOwnQuery(t.Context(), tx); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+store.UsageQuery,
			maxUsageGroups+1, since, "day", "model", heavyAccount, since, since.Add(90*24*time.Hour), "", "", "")
		if err != nil {
			return err
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		plan = strings.Join(lines, "\n")
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	t.Log("90-day daily model-grouped query, 216000 hourly rows / 100 accounts:\n" + plan)
	if strings.Contains(plan, "call_log") || strings.Contains(plan, "Seq Scan") || !strings.Contains(plan, "usage_hourly_pkey") {
		t.Fatalf("expected rollup index only:\n%s", plan)
	}
	blocks := buffers(t, plan)
	if blocks > 1500 {
		t.Fatalf("%d blocks exceeds 1500-block statistics budget:\n%s", blocks, plan)
	}
}

func TestUsageRejectsPartialTotals(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	if err := pgtest.Run(t.Context(), database.Superuser, `
        INSERT INTO observability.usage_hourly
            (site_id,account_id,hour,workspace_id,key_id,model,currency,requests,successes,
            cache_rate_requests,cache_rate_read,cache_rate_input,spend)
        SELECT '00000000-0000-4000-8000-0000000000ff','`+heavyAccount+`',
            '2026-11-01T00:00:00Z','00000000-0000-4000-8000-000000000111',
            '00000000-0000-4000-8000-000000000121','model-'||n,'USD',1,1,0,0,0,1
        FROM generate_series(1,10001) n`); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	err := db.ReadTx(t.Context(), accountScope(uuid.MustParse(heavyAccount)), func(tx pg.Tx) error {
		_, err := ReadUsageStatistics(t.Context(), tx, UsageQuery{
			Account: uuid.MustParse(heavyAccount), Since: since, Until: since.Add(time.Hour), Granularity: "hour", GroupBy: "model",
		})
		return err
	})
	if !errors.Is(err, ErrAggregateSize) {
		t.Fatalf("partial totals: %v", err)
	}
}
