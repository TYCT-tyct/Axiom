package accounts

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const (
	planSite      = "00000000-0000-4000-8000-0000000000a1"
	planTier      = "00000000-0000-4000-8000-0000000000a2"
	planHeavy     = "00000000-0000-4000-8000-0000000000a3"
	planLight     = "00000000-0000-4000-8000-0000000000a4"
	planHeavyWS   = "00000000-0000-4000-8000-0000000000a5"
	planLightWS   = "00000000-0000-4000-8000-0000000000a6"
	planUser      = "00000000-0000-4000-8000-0000000000a7"
	planOther     = "00000000-0000-4000-8000-0000000000a8"
	planOtherWS   = "00000000-0000-4000-8000-0000000000a9"
	planPage      = 50
	planHeavyRows = 20000
)

// TestKeyPagePlansFollowTheIndex explains the key list for a heavy account and a light one: the first page, a page
// deep in the same account, a dense name filter and one that matches nothing. Every plan is an ordered index scan
// under the limit and never sorts; the first page reads api_keys_live_of_account rather than every newer key of other
// accounts. A page costs about a page whatever its depth, because the cursor is a condition on the index rather than
// an offset (Issue #408).
func TestKeyPagePlansFollowTheIndex(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t, Module())
	if err := pgtest.Run(t.Context(), database.Superuser, fmt.Sprintf(`
		INSERT INTO accounts.sites (id, name, status, key_prefix, registration, default_tier_id)
			VALUES ('%[1]s', 'Main', 'active', 'sk-ax-v1-', 'invite', '%[2]s');
		INSERT INTO accounts.tiers VALUES ('%[2]s', '%[1]s', 'Standard');
		INSERT INTO accounts.accounts VALUES ('%[3]s', '%[1]s', 'personal', 'Heavy', 'active', '%[2]s'),
			('%[4]s', '%[1]s', 'personal', 'Light', 'active', '%[2]s'),
			('%[9]s', '%[1]s', 'personal', 'Other', 'active', '%[2]s');
		INSERT INTO accounts.workspaces (id, site_id, account_id, name, status, is_default) VALUES
			('%[5]s', '%[1]s', '%[3]s', 'Default', 'active', true), ('%[6]s', '%[1]s', '%[4]s', 'Default', 'active', true),
			('%[10]s', '%[1]s', '%[9]s', 'Default', 'active', true);
		INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code)
			VALUES ('%[7]s', '%[1]s', '%[3]s', 'plan@example.com', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA', 1, 'active', 'invitecode');
		INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
			secret_version, created_by, status, created_at)
			SELECT uuidv7(), '%[1]s', '%[3]s', '%[5]s', 'call', 'key' || n, 'sk-ax-v1-', lpad((n %% 10000)::text, 4, '0'),
				sha256(convert_to('heavy' || n, 'UTF8')), 1, '%[7]s', 'active', now() - make_interval(secs => n)
			FROM generate_series(1, %[8]d) n;
		INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
			secret_version, created_by, status, created_at)
			SELECT uuidv7(), '%[1]s', '%[4]s', '%[6]s', 'call', 'key' || n, 'sk-ax-v1-', lpad(n::text, 4, '0'),
				sha256(convert_to('light' || n, 'UTF8')), 1, '%[7]s', 'active', now() - make_interval(secs => n)
			FROM generate_series(1, 100) n;
		INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
			secret_version, created_by, status, created_at)
			SELECT uuidv7(), '%[1]s', '%[9]s', '%[10]s', 'call', 'other' || n, 'sk-ax-v1-', lpad((n %% 10000)::text, 4, '0'),
				sha256(convert_to('other' || n, 'UTF8')), 1, '%[7]s', 'active', now() - make_interval(secs => n)
			FROM generate_series(1, %[8]d * 10) n;
		ANALYZE accounts.api_keys`, planSite, planTier, planHeavy, planLight, planHeavyWS, planLightWS, planUser, planHeavyRows, planOther, planOtherWS)); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	heavy, light := uuid.MustParse(planHeavy), uuid.MustParse(planLight)
	var deep uuid.UUID
	if err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id FROM accounts.api_keys WHERE account_id = $1 ORDER BY id DESC OFFSET $2 LIMIT 1`,
			heavy, planHeavyRows/2).Scan(&deep)
	}); err != nil {
		t.Fatal(err)
	}
	dense, absent := "key1", "absent"
	base := func(account uuid.UUID, before uuid.UUID) ListKeysParams {
		return ListKeysParams{AccountID: account, Before: before, PageSize: planPage}
	}
	for _, c := range []struct {
		name string
		q    ListKeysParams
		max  int
		live bool // the first page must read the live-key index, not every newer key of other accounts
	}{
		{"heavy, first page", base(heavy, uuid.Max()), 100, true},
		{"heavy, a deep page", base(heavy, deep), 100, false},
		{"light, first page", base(light, uuid.Max()), 100, true},
		{"a dense name", ListKeysParams{AccountID: heavy, Before: uuid.Max(), PageSize: planPage, Name: &dense}, 100, true},
		{"a name that matches nothing", ListKeysParams{AccountID: heavy, Before: uuid.Max(), PageSize: planPage, Name: &absent}, 1500, true},
	} {
		plan := explainKeys(t, db, c.q)
		t.Logf("%s: %d buffers\n%s", c.name, keyBuffers(t, plan), plan)
		if strings.Contains(plan, "Seq Scan") || (c.live && !strings.Contains(plan, "api_keys_live_of_account")) {
			t.Errorf("%s: not read through the account's live-key index:\n%s", c.name, plan)
		}
		if strings.Contains(plan, "Sort") || !strings.Contains(plan, "Index Scan") {
			t.Errorf("%s: not an ordered index scan under the limit:\n%s", c.name, plan)
		}
		if n := keyBuffers(t, plan); n > c.max {
			t.Errorf("%s: touched %d buffers, want at most %d:\n%s", c.name, n, c.max, plan)
		}
	}
}

// explainKeys runs the list query as the application role in the account's scope, planned for its values, with the
// session settings ListKeys makes.
func explainKeys(t *testing.T, db *pg.DB, q ListKeysParams) string {
	t.Helper()
	var lines []string
	if err := db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{q.AccountID}), func(tx pg.Tx) error {
		for _, setting := range []string{"SET LOCAL plan_cache_mode = force_custom_plan", "SET LOCAL enable_sort = off"} {
			if _, err := tx.Exec(t.Context(), setting); err != nil {
				return err
			}
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+listKeys,
			q.AccountID, q.Before, q.Name, q.Prefix, q.Status, q.CreatedAfter, q.CreatedBefore, q.PageSize)
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

var (
	keyBuffersLine = regexp.MustCompile(`Buffers: shared((?: \w+=\d+)+)`)
	keyBufferCount = regexp.MustCompile(` (hit|read)=(\d+)`)
)

// keyBuffers is the buffers the plan touched, from cache or disk: the first Buffers line is the top node's, which
// counts everything below.
func keyBuffers(t *testing.T, plan string) int {
	t.Helper()
	m := keyBuffersLine.FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("no buffers in the plan:\n%s", plan)
	}
	var n int
	for _, c := range keyBufferCount.FindAllStringSubmatch(m[1], -1) {
		count, err := strconv.Atoi(c[2])
		if err != nil {
			t.Fatal(err)
		}
		n += count
	}
	return n
}
