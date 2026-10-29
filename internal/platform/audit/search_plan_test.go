package audit

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestAuditPagesFollowIndexOrder(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	err = pgtest.Run(t.Context(), database.Superuser, `INSERT INTO platform.audit_log(id,at,actor_kind,actor_id,action,module,target_kind,target_id,request_id,detail)
 SELECT ('00000000-0000-4000-8000-'||lpad(n::text,12,'0'))::uuid,'2026-10-29'::timestamptz+n*interval '1 second','system',
 CASE WHEN n%1000=0 THEN 'rare.actor' ELSE 'ordinary.actor' END,'config.update','platform','config','mail',
 '00000000-0000-4000-8000-000000000001','{}' FROM generate_series(1,20000)n`, "ANALYZE platform.audit_log")
	if err != nil {
		t.Fatal(err)
	}
	q := Search{Since: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), Limit: 50}
	for _, tc := range []struct {
		name, actor string
		deep        bool
	}{{"first", "", false}, {"deep", "", true}, {"sparse", "rare.actor", false}, {"missing", "absent.actor", false}} {
		t.Run(tc.name, func(t *testing.T) {
			v := q
			v.ActorID = tc.actor
			if tc.deep {
				v.After = &page.Cursor{At: v.Since.Add(5000 * time.Second)}
			}
			plan := explainAuditSearch(t, db, v)

			assertAuditPlan(t, plan)
		})
	}
}

func explainAuditSearch(t *testing.T, db *pg.DB, v Search) string {
	t.Helper()
	var lines []string
	err := db.ReadTx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		if _, _, err := List(t.Context(), tx, v); err != nil {
			return err
		}
		upper := page.UpperBound(v.Until, v.After)
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+searchSQL, upper.At, upper.ID, v.Since, v.Until, v.Site, v.Account, v.RequestID, v.ActorKind, v.ActorID, v.Action, v.Module, v.TargetKind, v.TargetID, v.Limit+1)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func assertAuditPlan(t *testing.T, plan string) {
	t.Helper()
	if strings.Contains(plan, "Sort") || strings.Contains(plan, "Bitmap") || strings.Contains(plan, "Seq Scan") || !strings.Contains(plan, "Index Scan") {
		t.Fatal(plan)
	}
	match := regexp.MustCompile(`Buffers: shared((?: \w+=\d+)+)`).FindStringSubmatch(plan)
	if len(match) == 0 {
		t.Fatal(plan)
	}
	blocks := 0
	for _, m := range regexp.MustCompile(`(?:hit|read)=(\d+)`).FindAllStringSubmatch(match[1], -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		blocks += n
	}
	if blocks > 120 {
		t.Fatalf("%d blocks for one page:\n%s", blocks, plan)
	}
}
