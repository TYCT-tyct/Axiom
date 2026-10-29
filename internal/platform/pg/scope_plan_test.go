package pg_test

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

type scopePlan struct {
	Relationship string      `json:"Parent Relationship"`
	Output       []string    `json:"Output"`
	Filter       string      `json:"Filter"`
	Loops        float64     `json:"Actual Loops"`
	Plans        []scopePlan `json:"Plans"`
}

// Timings vary with load; the plan proves parsing is independent of both matching and rejected row counts.
func TestScopeSettingsAreStatementInitPlans(t *testing.T) {
	database := newDatabase(t)
	if err := pgtest.Run(t.Context(), database.Superuser, fmt.Sprintf(`INSERT INTO rlstest.items
		SELECT '%s', CASE WHEN n %% 2 = 0 THEN '%s'::uuid ELSE '%s'::uuid END, 'seed'
		FROM generate_series(1, 2000000) n; ANALYZE rlstest.items`, site1, account1, account2)); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	for _, scope := range []pg.Scope{pg.PlatformScope(), pg.AccountScope(account1), {}} {
		var raw []byte
		if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) SELECT count(*) FROM rlstest.items").Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		var plans []struct{ Plan scopePlan }
		if err := jsonx.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		reads := checkScopePlans(t, plans[0].Plan, false)
		if reads != 3 {
			t.Errorf("%d scope expressions, want three InitPlan readers:\n%s", reads, raw)
		}
	}
}

func checkScopePlans(t *testing.T, p scopePlan, init bool) int {
	t.Helper()
	init = init || p.Relationship == "InitPlan"
	reads := 0
	for _, expression := range append(p.Output, p.Filter) {
		if strings.Contains(expression, "current_setting(") {
			reads++
			if !init || p.Loops > 1 {
				t.Errorf("scope parsed outside a once-executed InitPlan (loops %v): %s", p.Loops, expression)
			}
		}
		if strings.Contains(expression, "platform.in_scope(") {
			t.Errorf("row-dependent scope call: %s", expression)
		}
	}
	for _, child := range p.Plans {
		reads += checkScopePlans(t, child, init)
	}
	return reads
}
