package accounts_test

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestPresetKeys: the keys bound to a routing preset are counted across the scope or for one account, deleted keys
// and the unbound zero ID left out, and an account's count reads the index's entries of that preset and account
// alone, not the many keys of the site bound to nothing (docs/domains/routing.md「接口、权限与审计」).
func TestPresetKeys(t *testing.T) {
	t.Parallel()
	const (
		site, tier, user = "00000000-0000-4000-8000-0000000000b1", "00000000-0000-4000-8000-0000000000b2", "00000000-0000-4000-8000-0000000000b3"
		mine, mineWS     = "00000000-0000-4000-8000-0000000000b4", "00000000-0000-4000-8000-0000000000b5"
		theirs, theirsWS = "00000000-0000-4000-8000-0000000000b6", "00000000-0000-4000-8000-0000000000b7"
	)
	strict, cheap, unused, zero := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.UUID{}
	// keys inserts n keys of an account bound to preset (SQL NULL when preset is "NULL"), deleted ones when deleted.
	keys := func(account, workspace, preset string, n int, deleted bool) string {
		gone := "NULL"
		if deleted {
			gone = "now()"
		}
		return fmt.Sprintf(`INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4,
			secret_hash, secret_version, created_by, status, preset_id, deleted_at)
			SELECT uuidv7(), '%s', '%s', '%s', 'call', 'k', 'sk-ax-v1-', 'abcd', sha256(convert_to(gen_random_uuid()::text, 'UTF8')),
				1, '%s', 'active', %s, %s FROM generate_series(1, %d)`, site, account, workspace, user, preset, gone, n)
	}
	quoted := func(id uuid.UUID) string { return "'" + id.String() + "'" }
	database := pgtest.New(t, accounts.Module())
	if err := pgtest.Run(t.Context(), database.Superuser, fmt.Sprintf(`
		INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id)
			VALUES ('%[1]s', 'Main', 'active', 'sk-ax-v1-', 'invite', '%[2]s');
		INSERT INTO accounts.tiers VALUES ('%[2]s', '%[1]s', 'Standard');
		INSERT INTO accounts.accounts VALUES ('%[4]s', '%[1]s', 'personal', 'Mine', 'active', '%[2]s'),
			('%[6]s', '%[1]s', 'personal', 'Theirs', 'active', '%[2]s');
		INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES
			('%[5]s', '%[1]s', '%[4]s', 'Default', 'active', true), ('%[7]s', '%[1]s', '%[6]s', 'Default', 'active', true);
		INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code)
			VALUES ('%[3]s', '%[1]s', '%[4]s', 'count@example.com', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA', 1, 'active', 'invitecode')`,
		site, tier, user, mine, mineWS, theirs, theirsWS),
		keys(mine, mineWS, quoted(strict), 3, false), keys(mine, mineWS, quoted(strict), 2, true),
		keys(mine, mineWS, quoted(cheap), 1, false), keys(theirs, theirsWS, quoted(strict), 4, false),
		keys(mine, mineWS, quoted(zero), 20000, false), keys(theirs, theirsWS, "NULL", 20000, false),
		"ANALYZE accounts.api_keys"); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	service := node(t, db, hashKeys(t, 1), time.Now)
	own := accounts.AccountID(uuid.MustParse(mine))
	asked := []uuid.UUID{strict, cheap, unused, zero}
	for _, c := range []struct {
		what    string
		scope   pg.Scope
		account *accounts.AccountID
		want    map[uuid.UUID]int64
	}{
		{"the platform's", platform, nil, map[uuid.UUID]int64{strict: 7, cheap: 1}},
		{"one account's", platform, &own, map[uuid.UUID]int64{strict: 3, cheap: 1}},
		{"in an account's scope", pg.NewScope(false, nil, []uuid.UUID{uuid.UUID(own)}), nil, map[uuid.UUID]int64{strict: 3, cheap: 1}},
	} {
		var got map[uuid.UUID]int64
		if err := db.ReadTx(t.Context(), c.scope, func(tx pg.Tx) (err error) {
			got, err = service.PresetKeys(t.Context(), tx, c.account, asked)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got, c.want) {
			t.Errorf("%s keys by preset %v, want %v", c.what, got, c.want)
		}
	}

	plan := explain(t, db, pg.NewScope(false, nil, []uuid.UUID{uuid.UUID(own)}), accounts.AccountKeysByPresetQuery, asked, uuid.UUID(own))
	if !strings.Contains(plan, "Index Only Scan using api_keys_by_preset") {
		t.Errorf("an account's count is not an index-only scan of api_keys_by_preset:\n%s", plan)
	}
}

// explain is the plan of query with args, run as the application role in scope.
func explain(t *testing.T, db *pg.DB, scope pg.Scope, query string, args ...any) string {
	t.Helper()
	var lines []string
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "EXPLAIN "+query, args...)
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
