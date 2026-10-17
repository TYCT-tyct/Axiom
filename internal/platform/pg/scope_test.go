package pg_test

import (
	"strings"
	"testing"

	"axiom/internal/platform/pg"
)

// TestScopeIsInlinedIntoPolicies: PostgreSQL substitutes the function's body into the policy, so filtering a row costs
// the expression itself and no call. A body holding a subquery stops that substitution, and every row then re-runs the
// body's plan: issue #63 measured 53s for the same scan that takes 1.9s inlined, on 2M rows. Timings cannot gate, so
// this test reads the plan, where the body must appear and the function's name must not.
func TestScopeIsInlinedIntoPolicies(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	var plan strings.Builder
	if err := db.Tx(t.Context(), pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "EXPLAIN (VERBOSE, COSTS OFF) SELECT count(*) FROM rlstest.items")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line + "\n")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	// The body reads both arrays; the call it was inlined from is gone.
	for _, want := range []string{"current_setting('axiom.scope_sites'", "current_setting('axiom.scope_accounts'"} {
		if !strings.Contains(plan.String(), want) {
			t.Errorf("the plan does not read the settings once per row: %s is missing from\n%s", want, plan.String())
		}
	}
	if strings.Contains(plan.String(), "platform.in_scope(") {
		t.Errorf("the policy calls platform.in_scope per row instead of inlining it:\n%s", plan.String())
	}
}
