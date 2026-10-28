package pg_test

import (
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestSeesSiteReadsTheHomeSite: an account scope sees the public configuration of the site its authorization put in
// the scope, and of no other; the other scopes see what in_scope gives them.
func TestSeesSiteReadsTheHomeSite(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	site, other, account := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	for name, c := range map[string]struct {
		scope       pg.Scope
		site, other bool
	}{
		"no scope":               {pg.Scope{}, false, false},
		"an account in its site": {pg.AccountScopeIn(site, account), true, false},
		"an account alone":       {pg.AccountScope(account), false, false},
		"the site":               {pg.SiteScope(site), true, false},
		"the platform":           {pg.PlatformScope(), true, true},
	} {
		var sees, elsewhere bool
		err := db.Tx(t.Context(), c.scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT platform.sees_site($1), platform.sees_site($2)", site, other).Scan(&sees, &elsewhere)
		})
		if err != nil || sees != c.site || elsewhere != c.other {
			t.Errorf("%s: sees the site %v, another %v, %v; want %v, %v", name, sees, elsewhere, err, c.site, c.other)
		}
	}
}
