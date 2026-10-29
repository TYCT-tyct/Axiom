package pg_test

import (
	"slices"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// A prepared policy must read the execution's scope, even on the connection that planned it in a wider scope.
func TestScopePlansDoNotCacheAnEarlierScope(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	cases := []struct {
		scope pg.Scope
		want  []string
	}{
		{pg.PlatformScope(), sorted(account1, account2, account3, account4)},
		{pg.SiteScope(site1), sorted(account1, account2)},
		{pg.AccountScope(account3), sorted(account3)},
		{pg.Scope{}, nil},
		{pg.AccountScopeIn(site1, account1), sorted(account1)},
		{pg.NewScope(false, []uuid.UUID{site1}, []uuid.UUID{account3}), sorted(account1, account2, account3)},
	}
	for _, mode := range []string{"force_generic_plan", "force_custom_plan"} {
		db := open(t, database.App+"&plan_cache_mode="+mode)
		for range 3 {
			for _, c := range cases {
				if got := visible(t, db, c.scope); !slices.Equal(got, c.want) {
					t.Errorf("%s: visible %v, want %v", mode, got, c.want)
				}
			}
		}
	}
}

func TestMalformedScopeArraysFailClosed(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	for _, setting := range []string{"axiom.scope_sites", "axiom.scope_accounts"} {
		err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), "SELECT set_config($1, '{broken}', true)", setting); err != nil {
				return err
			}
			var n int
			return tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&n)
		})
		if pg.SQLState(err) != "22P02" {
			t.Errorf("%s: %v; want invalid UUID rejected", setting, err)
		}
	}
}
