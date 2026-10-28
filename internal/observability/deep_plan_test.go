package observability

import (
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestListDeepPagesReadInIndexOrder(t *testing.T) {
	db, database := openDBWithURL(t)
	seedForPlans(t, database, 2000000)
	account := uuid.MustParse(heavyAccount)
	since, until := window()
	var deep Cursor
	if err := db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT at, id FROM observability.call_log WHERE account_id = $1
			ORDER BY at DESC, id DESC OFFSET 750000 LIMIT 1`, account).Scan(&deep.At, &deep.ID)
	}); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]pg.Scope{"platform": pg.NewScope(true, nil, nil), "account": accountScope(account)} {
		for _, filter := range []struct{ name, model, app string }{
			{"unfiltered", "", ""}, {"model", "m", ""}, {"app", "", "cli"},
		} {
			t.Run(name+"/"+filter.name, func(t *testing.T) {
				q := OwnQuery{Account: account, Since: since, Until: until, Model: filter.model, ClientApp: filter.app}
				firstPlan := explain(t, db, q, scope)
				q.After = &deep
				checkListPagePlans(t, firstPlan, explain(t, db, q, scope))
			})
		}
	}
}

func checkListPagePlans(t *testing.T, firstPlan, deepPlan string) {
	t.Helper()
	first, later := buffers(t, firstPlan), buffers(t, deepPlan)
	t.Logf("blocks first=%d deep=%d", first, later)
	for _, plan := range []string{firstPlan, deepPlan} {
		if strings.Contains(plan, "Sort") || !strings.Contains(plan, "Index Scan using") || strings.Contains(plan, "Seq Scan") {
			t.Errorf("not read in index order:\n%s", plan)
		}
	}
	if later > first+first/2+20 || later > 100 || first > 100 {
		t.Errorf("a deep page touched %d blocks against first %d:\n%s", later, first, deepPlan)
	}
}
