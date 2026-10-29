package pg_test

import (
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestSitePolicyReadsTheHomeSite: authorization grants access to the home site's public configuration, without
// exposing another site's configuration.
func TestSitePolicyReadsTheHomeSite(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	const site = "00000000-0000-4000-8000-000000000001"
	const other = "00000000-0000-4000-8000-000000000002"
	if err := pgtest.Run(t.Context(), database.Superuser, `SET ROLE axiom_owner;
CREATE TABLE rlstest.site_config (site_id uuid PRIMARY KEY);
ALTER TABLE rlstest.site_config ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON rlstest.site_config FOR SELECT USING (
    (SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR site_id = (SELECT platform.home_site()));
GRANT SELECT ON rlstest.site_config TO axiom_app;
RESET ROLE;
INSERT INTO rlstest.site_config VALUES ('`+site+`'), ('`+other+`');`); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	siteID, account := uuid.MustParse(site), uuid.NewV7()
	for name, c := range map[string]struct {
		scope       pg.Scope
		site, other bool
	}{
		"no scope":               {pg.Scope{}, false, false},
		"an account in its site": {pg.AccountScopeIn(siteID, account), true, false},
		"an account alone":       {pg.AccountScope(account), false, false},
		"the site":               {pg.SiteScope(siteID), true, false},
		"the platform":           {pg.PlatformScope(), true, true},
	} {
		var sees, elsewhere bool
		err := db.Tx(t.Context(), c.scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT
                EXISTS (SELECT FROM rlstest.site_config WHERE site_id = $1),
                EXISTS (SELECT FROM rlstest.site_config WHERE site_id = $2)`, site, other).Scan(&sees, &elsewhere)
		})
		if err != nil || sees != c.site || elsewhere != c.other {
			t.Errorf("%s: sees the site %v, another %v, %v; want %v, %v", name, sees, elsewhere, err, c.site, c.other)
		}
	}
}
