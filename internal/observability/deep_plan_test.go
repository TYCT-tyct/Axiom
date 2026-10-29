package observability

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func checkListDeepPagesReadInIndexOrder(t *testing.T, db *pg.DB) {
	t.Helper()
	account := uuid.MustParse(heavyAccount)
	since, until := window()
	var deep Cursor
	if err := db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT at, id FROM observability.call_log WHERE account_id = $1
			ORDER BY at DESC, id DESC OFFSET 45000 LIMIT 1`, account).Scan(&deep.At, &deep.ID)
	}); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]pg.Scope{"platform": pg.NewScope(true, nil, nil), "account": accountScope(account)} {
		for _, filter := range []struct{ name, model, app string }{
			{"unfiltered", "", ""}, {"model", "m", ""}, {"app", "", "cli"},
		} {
			for _, size := range []int{pageSize, MaxPageSize} {
				t.Run(fmt.Sprintf("deep/%s/%s/%d", name, filter.name, size), func(t *testing.T) {
					q := OwnQuery{Account: account, Since: since, Until: until, Model: filter.model, ClientApp: filter.app, PageSize: size}
					firstPlan := explain(t, db, q, scope)
					q.After = &deep
					checkListPagePlans(t, firstPlan, explain(t, db, q, scope))
					q.Until = deep.At
					for _, after := range []*Cursor{nil, &deep, {At: until, ID: uuid.Max()}} {
						q.After = after
						t.Logf("historical window, cursor=%v", after)
						checkListPagePlans(t, firstPlan, explain(t, db, q, scope))
					}
					q.Until, q.After = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), nil
					widePlan := explain(t, db, q, scope)
					q.After = &deep
					t.Log("window includes future partitions")
					checkListPagePlans(t, widePlan, explain(t, db, q, scope))
				})
			}
		}
	}
}

func checkListPagePlans(t *testing.T, firstPlan, deepPlan string) {
	t.Helper()
	first, later := buffers(t, firstPlan), buffers(t, deepPlan)
	t.Logf("blocks first=%d deep=%d\nfirst:\n%s\ndeep:\n%s", first, later, firstPlan, deepPlan)
	for _, plan := range []string{firstPlan, deepPlan} {
		if strings.Contains(plan, "Sort  (") || strings.Contains(plan, "Bitmap") || (!strings.Contains(plan, "Index Scan using") && !strings.Contains(plan, "Index Scan Backward using")) || strings.Contains(plan, "Seq Scan") {
			t.Errorf("not read in index order:\n%s", plan)
		}
	}
	if later > first+first/2+20 || later > 100 || first > 100 {
		t.Errorf("a deep page touched %d blocks against first %d:\n%s", later, first, deepPlan)
	}
}
