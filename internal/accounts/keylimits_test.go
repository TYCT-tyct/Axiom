package accounts_test

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// tryKey makes a call key of a1 the way the console does and gives back its refusal instead of failing: for the tests
// that expect one.
func tryKey(t *testing.T, db *pg.DB, s *accounts.Service, name string, workspace accounts.WorkspaceID) error {
	t.Helper()
	return db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error {
		_, err := s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), Workspace: workspace,
			CreatedBy: accounts.UserID(u1), Name: name,
		})
		return err
	})
}

// keySettingsOf sets s1's limits on the keys of an account, and leaves the rest of its settings at their defaults.
func keySettingsOf(t *testing.T, database pgtest.Database, held, perHour int) {
	t.Helper()
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(fmt.Sprintf(
		"INSERT INTO accounts.site_settings (site_id, max_keys_per_account, key_creations_per_hour) VALUES (:s1, %d, %d)", held, perHour))); err != nil {
		t.Fatal(err)
	}
}

// createRacing makes a call key of a1 and, once the checks have passed, keeps its transaction open until every other
// create has either passed them too or is waiting on a lock: so creates that the checks do not order are all in the
// transaction together, and creates that they do order are seen waiting. Nothing here waits for time to pass.
func createRacing(t *testing.T, db *pg.DB, s *accounts.Service, arrived *atomic.Int32, creates int) error {
	t.Helper()
	return db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error {
		if _, err := s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: accounts.AccountID(a1), CreatedBy: accounts.UserID(u1), Name: "At once",
		}); err != nil {
			return err
		}
		arrived.Add(1)
		for t.Context().Err() == nil {
			waiting, err := strconv.Atoi(texts(t, db, platform,
				"SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")[0])
			if err != nil {
				return err
			}
			if int(arrived.Load())+waiting >= creates {
				return nil
			}
			runtime.Gosched()
		}
		return t.Context().Err()
	})
}

// TestKeyCapHoldsUnderConcurrentCreates: a site that allows two call keys an account, whose account holds one, makes
// one more when four creates arrive at once: the count that admits a key is read in the account's row's turn, in the
// transaction that inserts, so the other three wait and then see the key the first one made.
func TestKeyCapHoldsUnderConcurrentCreates(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 2, 100)
	s := node(t, db, hashKeys(t, 1), nil)
	const at = 4
	errs := make([]error, at)
	var arrived atomic.Int32
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = createRacing(t, db, s, &arrived, at)
		})
	}
	wg.Wait()
	made := 0
	for _, err := range errs {
		switch {
		case err == nil:
			made++
		case !errors.Is(err, accounts.ErrTooManyKeys):
			t.Errorf("a create beside the others: %v", err)
		}
	}
	if made != 1 {
		t.Errorf("%d of %d creates at once made a key, want one", made, at)
	}
	// The seed's key and the one create that was admitted: no other got past the count.
	if got := texts(t, db, platform, fmt.Sprintf(
		`SELECT count(*)::text FROM accounts.api_keys WHERE account_id = '%s' AND kind = 'call' AND deleted_at IS NULL`, a1)); len(got) != 1 || got[0] != "2" {
		t.Errorf("the account holds %v call keys, want the 2 its site allows", got)
	}
	if got := texts(t, db, platform, `SELECT count(*)::text FROM platform.audit_log WHERE action = 'api_key.create'`); len(got) != 1 || got[0] != "1" {
		t.Errorf("%v audit entries of a created key, want 1", got)
	}
}

// madeAgo adds a call key of a1 that was made the span ago, and deleted if it was.
func madeAgo(t *testing.T, database pgtest.Database, ago time.Duration, deleted bool) {
	t.Helper()
	id := "'" + uuid.NewV7().String() + "'"
	sql := apiKey(id, ":s1", ":a1", ":w1", ":u1", "call") + fmt.Sprintf(
		";\nUPDATE accounts.api_keys SET created_at = now() - make_interval(secs => %d) WHERE id = %s", int(ago.Seconds()), id)
	if deleted {
		sql += fmt.Sprintf(";\nUPDATE accounts.api_keys SET deleted_at = now() WHERE id = %s", id)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(sql)); err != nil {
		t.Fatal(err)
	}
}

// seedKeyIsOld makes the key the seed gave a1 three hours old, so that the keys of the hour are the ones a test adds.
func seedKeyIsOld(t *testing.T, database pgtest.Database) {
	t.Helper()
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(
		"UPDATE accounts.api_keys SET created_at = now() - interval '3 hours' WHERE id = :k1")); err != nil {
		t.Fatal(err)
	}
}

// tooManyKeys is the refusal err is, or fails the test.
func tooManyKeys(t *testing.T, err error) accounts.TooManyKeys {
	t.Helper()
	keys, ok := errors.AsType[*accounts.TooManyKeys](err)
	if !ok || !errors.Is(err, accounts.ErrTooManyKeys) {
		t.Fatalf("got %v, want a refusal of too many keys", err)
	}
	return *keys
}

// TestKeyCap: the refusal of the cap names the limit and the keys held, a deleted key is no longer held, and a site
// that lowers its cap below what an account holds leaves those keys and refuses more.
func TestKeyCap(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 2, 100)
	s := node(t, db, hashKeys(t, 1), nil)
	if err := tryKey(t, db, s, "Second", accounts.WorkspaceID(w1)); err != nil {
		t.Fatalf("the second key, of two: %v", err)
	}
	want := accounts.TooManyKeys{Limit: 2, Count: 2}
	if diff := cmp.Diff(want, tooManyKeys(t, tryKey(t, db, s, "Third", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("the third key (-want +got):\n%s", diff)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, "UPDATE accounts.api_keys SET deleted_at = now() WHERE name = 'Second'"); err != nil {
		t.Fatal(err)
	}
	if err := tryKey(t, db, s, "Third", accounts.WorkspaceID(w1)); err != nil {
		t.Errorf("a key after one was deleted: %v", err)
	}
}

// TestKeyCreationWindow: the keys made in the last hour are counted on the database's clock, deleted ones too, and
// the refusal reports how many there are, whatever the limit now is, and how long until the key that must age out for
// a place to be free is an hour old. The keys are made at times before now, and the waits are checked to a minute,
// which is more than the test takes.
func TestKeyCreationWindow(t *testing.T) {
	t.Parallel()
	minutes := func(n ...int) []time.Duration {
		ago := make([]time.Duration, len(n))
		for i, m := range n {
			ago[i] = time.Duration(m) * time.Minute
		}
		return ago
	}
	for name, c := range map[string]struct {
		limit   int
		ago     []time.Duration // when the keys were made, the first one deleted
		refused bool
		count   int
		wait    time.Duration
	}{
		"a deleted key counts":                   {2, minutes(40, 10), true, 2, 20 * time.Minute},
		"a key past the hour does not count":     {2, minutes(61, 10), false, 0, 0},
		"a key just inside the hour counts":      {2, minutes(59, 10), true, 2, time.Minute},
		"a lowered limit reports the true count": {2, minutes(50, 40, 30, 20, 10), true, 5, 40 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, database := open(t)
			keySettingsOf(t, database, 100, c.limit)
			seedKeyIsOld(t, database)
			for i, ago := range c.ago {
				madeAgo(t, database, ago, i == 0)
			}
			s := node(t, db, hashKeys(t, 1), nil)
			err := tryKey(t, db, s, "Over", accounts.WorkspaceID(w1))
			if !c.refused {
				if err != nil {
					t.Errorf("a key with the hour's keys %v: %v", c.ago, err)
				}
				return
			}
			got := tooManyKeys(t, err)
			if got.Limit != c.limit || got.Count != c.count || got.RetryAfter > c.wait || got.RetryAfter <= c.wait-time.Minute {
				t.Errorf("the refusal is %+v, want %d of %d and a wait of up to %v and more than a minute less", got, c.count, c.limit, c.wait)
			}
		})
	}
}

// TestKeyCreationsCountTheKeysMade: the keys the account makes through CreateKey count toward the hour, the one the
// seed made too, since all were made in it.
func TestKeyCreationsCountTheKeysMade(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 100, 2)
	s := node(t, db, hashKeys(t, 1), nil)
	if err := tryKey(t, db, s, "Second", accounts.WorkspaceID(w1)); err != nil {
		t.Fatalf("the second key of the hour: %v", err)
	}
	keys := tooManyKeys(t, tryKey(t, db, s, "Third", accounts.WorkspaceID(w1)))
	if keys.Limit != 2 || keys.Count != 2 || keys.RetryAfter <= 0 || keys.RetryAfter > time.Hour {
		t.Errorf("the third key of the hour: %+v, want 2 of 2 and a wait of up to an hour", keys)
	}
}

// TestKeyCreationsIgnoreTheNodesClocks: nodes whose clocks are hours apart, making keys of one account in turn, are held
// to the hour's limit as one: a key is stamped and the hour judged by the database, not by the node that made it.
func TestKeyCreationsIgnoreTheNodesClocks(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 100, 3)
	seedKeyIsOld(t, database)
	nodes := make([]*accounts.Service, 0, 3)
	for _, skew := range []time.Duration{0, 2 * time.Hour, -2 * time.Hour} {
		nodes = append(nodes, node(t, db, hashKeys(t, 1), func() time.Time { return time.Now().Add(skew) }))
	}
	for i := range 3 {
		if err := tryKey(t, db, nodes[i], "Skewed", accounts.WorkspaceID(w1)); err != nil {
			t.Fatalf("key %d of the hour, made on a node %d of 3: %v", i+1, i+1, err)
		}
	}
	for i, s := range nodes {
		if keys := tooManyKeys(t, tryKey(t, db, s, "Over", accounts.WorkspaceID(w1))); keys.Count != 3 || keys.RetryAfter <= 0 || keys.RetryAfter > time.Hour {
			t.Errorf("a fourth key from node %d: %+v, want 3 of 3 and a wait of up to an hour", i, keys)
		}
	}
}

// TestKeyCreationsUseTheIndex: the hour's count of an account that made none reads no more than its own keys however
// many keys other accounts made in the hour: the plan is a scan of api_keys_made_by_account with no row dropped by a
// filter. The statement is RecentKeys's, spelled with the literals it takes, and must stay in step with it.
func TestKeyCreationsUseTheIndex(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(`
		INSERT INTO accounts.api_keys (id, site_id, account_id, workspace_id, kind, name, prefix, last4, secret_hash,
			secret_version, created_by, status)
		SELECT gen_random_uuid(), :s2, :a2, :w2, 'call', 'Bulk', 'sk-ax-v1-', 'abcd', sha256(convert_to(n::text, 'UTF8')), 1, :u2, 'active'
		FROM generate_series(1, 30000) AS n;
		ANALYZE accounts.api_keys`)); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(texts(t, db, platform, ids.Replace(`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF)
		WITH recent AS (
			SELECT created_at FROM accounts.api_keys
			WHERE account_id = :a1 AND kind = 'call' AND created_at > statement_timestamp() - make_interval(secs => 3600::bigint)
		)
		SELECT (SELECT count(*) FROM recent)::bigint AS made`)), "\n")
	if !strings.Contains(plan, "api_keys_made_by_account") || strings.Contains(plan, "Seq Scan") || strings.Contains(plan, "Rows Removed by Filter") {
		t.Errorf("the plan of the hour's count:\n%s", plan)
	}
}

// TestAccountKeyLimits: an account's own limits come over its site's, one limit at a time, in either direction; a limit
// the account does not set is the site's, and clearing them gives the site's back; the change is audited with the
// values it replaced, bounds are checked before anything is written, and the account's key being made meets them.
func TestAccountKeyLimits(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 3, 100)
	seedKeyIsOld(t, database)
	s := node(t, db, hashKeys(t, 1), nil)
	scope := pg.NewScope(true, nil, nil)
	set := func(limits accounts.KeyLimits) error {
		return db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			return s.SetAccountKeyLimits(t.Context(), tx, operatorActor, accounts.AccountID(a1), limits, "reason")
		})
	}
	read := func() (accounts.KeyLimits, accounts.EffectiveKeyLimits) {
		var own accounts.KeyLimits
		var effective accounts.EffectiveKeyLimits
		if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
			own, effective, err = s.AccountKeyLimits(t.Context(), tx, accounts.AccountID(a1))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return own, effective
	}
	if own, effective := read(); own != (accounts.KeyLimits{}) || effective != (accounts.EffectiveKeyLimits{MaxKeys: 3, CreationsPerHour: 100}) {
		t.Errorf("an account that sets none: %+v, %+v", own, effective)
	}
	// Below the site's: the seed's key and one more are the two it may hold.
	if err := set(accounts.KeyLimits{MaxKeys: new(int32(2))}); err != nil {
		t.Fatal(err)
	}
	if own, effective := read(); *own.MaxKeys != 2 || own.CreationsPerHour != nil || effective != (accounts.EffectiveKeyLimits{MaxKeys: 2, CreationsPerHour: 100}) {
		t.Errorf("an account with a cap of its own: %+v, %+v", own, effective)
	}
	if err := tryKey(t, db, s, "Second", accounts.WorkspaceID(w1)); err != nil {
		t.Fatalf("the second key, of two: %v", err)
	}
	if diff := cmp.Diff(accounts.TooManyKeys{Limit: 2, Count: 2}, tooManyKeys(t, tryKey(t, db, s, "Third", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("the account's cap of 2 under its site's 3 (-want +got):\n%s", diff)
	}
	// Over the site's: its own hourly limit is lower and its cap higher, and each wins.
	if err := set(accounts.KeyLimits{MaxKeys: new(int32(10)), CreationsPerHour: new(int32(1))}); err != nil {
		t.Fatal(err)
	}
	if keys := tooManyKeys(t, tryKey(t, db, s, "Third", accounts.WorkspaceID(w1))); keys.Limit != 1 || keys.Count != 1 {
		t.Errorf("the account's hour of 1 and cap of 10: %+v, want 1 of 1", keys)
	}
	// Cleared, it is the site's again: three held, so the cap of 3 refuses.
	if err := set(accounts.KeyLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := tryKey(t, db, s, "Third", accounts.WorkspaceID(w1)); err != nil {
		t.Fatalf("the third key under the site's 3: %v", err)
	}
	if diff := cmp.Diff(accounts.TooManyKeys{Limit: 3, Count: 3}, tooManyKeys(t, tryKey(t, db, s, "Fourth", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("the site's cap of 3 (-want +got):\n%s", diff)
	}
	for name, limits := range map[string]accounts.KeyLimits{
		"no keys":                      {MaxKeys: new(int32(0))},
		"too many keys":                {MaxKeys: new(int32(10_001))},
		"none an hour":                 {CreationsPerHour: new(int32(0))},
		"ten thousand and one an hour": {CreationsPerHour: new(int32(10_001))},
	} {
		var bad *accounts.SettingError
		if err := set(limits); !errors.As(err, &bad) {
			t.Errorf("%s: %v, want a setting error", name, err)
		}
	}
	if got := texts(t, db, platform, `SELECT coalesce(detail->'before'->>'max_keys', 'none') || ',' || coalesce(detail->'after'->>'max_keys', 'none') || ',' ||
		coalesce(detail->'after'->>'creations_per_hour', 'none') FROM platform.audit_log WHERE action = 'account_key_limits.update' ORDER BY id`); !slices.Equal(got, []string{"none,2,none", "2,10,1", "10,none,none"}) {
		t.Errorf("the audit entries of the account's limits (before max, after max, after hour): %v", got)
	}
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return s.SetAccountKeyLimits(t.Context(), tx, operatorActor, accounts.AccountID(x), accounts.KeyLimits{}, "r")
	}); !errors.Is(err, accounts.ErrNotFound) {
		t.Errorf("limits of an account that is not there: %v", err)
	}
}

// TestSiteKeyLimits: a site's own limits are read and replaced without touching its other settings, and the
// replacement of its other settings leaves them: the older shape of that request has no key limits in it. Another node
// applies them when it reloads the sites.
func TestSiteKeyLimits(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	seedKeyIsOld(t, database)
	s := node(t, db, hashKeys(t, 1), nil)
	scope := pg.NewScope(true, nil, nil)
	site := accounts.SiteID(s1)
	readLimits := func() (accounts.KeyLimits, accounts.EffectiveKeyLimits) {
		var own accounts.KeyLimits
		var effective accounts.EffectiveKeyLimits
		if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
			own, effective, err = s.SiteKeyLimits(t.Context(), tx, site)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return own, effective
	}
	if own, effective := readLimits(); own != (accounts.KeyLimits{}) || effective != accounts.DefaultKeyLimits() {
		t.Errorf("a site that sets none: %+v, %+v", own, effective)
	}
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return s.SetSiteKeyLimits(t.Context(), tx, operatorActor, site, accounts.KeyLimits{MaxKeys: new(int32(2))}, "tighten")
	}); err != nil {
		t.Fatal(err)
	}
	// The settings are replaced whole, as the request of an older page does; the site's key limits stay.
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return s.SetSiteSettings(t.Context(), tx, operatorActor, site, accounts.SiteSettings{AdminIdle: time.Hour}, "older page")
	}); err != nil {
		t.Fatal(err)
	}
	if own, effective := readLimits(); *own.MaxKeys != 2 || effective != (accounts.EffectiveKeyLimits{MaxKeys: 2, CreationsPerHour: 50}) {
		t.Errorf("the site's limits after its settings were replaced: %+v, %+v", own, effective)
	}
	// This node has not reloaded the sites yet, another that has applies the limit.
	other := node(t, db, hashKeys(t, 1), nil)
	if err := tryKey(t, db, other, "Second", accounts.WorkspaceID(w1)); err != nil {
		t.Fatalf("the second key, of two: %v", err)
	}
	if diff := cmp.Diff(accounts.TooManyKeys{Limit: 2, Count: 2}, tooManyKeys(t, tryKey(t, db, other, "Third", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("the site's cap of 2 (-want +got):\n%s", diff)
	}
	var bad *accounts.SettingError
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return s.SetSiteKeyLimits(t.Context(), tx, operatorActor, site, accounts.KeyLimits{MaxKeys: new(int32(0))}, "r")
	}); !errors.As(err, &bad) {
		t.Errorf("a cap of none: %v, want a setting error", err)
	}
	if got := texts(t, db, platform, `SELECT count(*)::text FROM platform.audit_log WHERE action = 'site_key_limits.update'`); got[0] != "1" {
		t.Errorf("%s audit entries of the site's key limits, want 1", got[0])
	}
}

// keyChange is an entry of api_key.update as the audit log holds it: the settings the change names, and the ones whose
// old and new values it records. A field is nil where the entry does not record it.
type keyChange struct {
	Changed []string  `json:"changed"`
	Before  keyValues `json:"before"`
	After   keyValues `json:"after"`
}

// keyValues is the part of a change that may disclose values: the two lists are named in Changed and kept only as
// written here, so Models and IPAllow must stay nil.
type keyValues struct {
	Name       *string   `json:"name"`
	Disabled   *bool     `json:"disabled"`
	SpendLimit *int64    `json:"spend_limit"`
	LimitReset *string   `json:"limit_reset"`
	Models     jsonx.Raw `json:"models"`
	IPAllow    jsonx.Raw `json:"ip_allow"`
}

// lastKeyChange is the newest entry of a change to a key.
func lastKeyChange(t *testing.T, db *pg.DB) keyChange {
	t.Helper()
	got := texts(t, db, platform, `SELECT detail::text FROM platform.audit_log WHERE action = 'api_key.update' ORDER BY id DESC LIMIT 1`)
	if len(got) != 1 {
		t.Fatalf("%d entries of a changed key, want one", len(got))
	}
	var change keyChange
	if err := jsonx.Unmarshal([]byte(got[0]), &change); err != nil {
		t.Fatal(err)
	}
	return change
}

// updateKey replaces the settings of key id the way the console does.
func updateKey(t *testing.T, db *pg.DB, s *accounts.Service, id accounts.KeyID, k accounts.KeySettings) error {
	t.Helper()
	return db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error {
		_, err := s.UpdateKey(t.Context(), tx, system, id, k)
		return err
	})
}

// changeOf replaces the settings of key id and returns what the change recorded.
func changeOf(t *testing.T, db *pg.DB, s *accounts.Service, id accounts.KeyID, k accounts.KeySettings) keyChange {
	t.Helper()
	if err := updateKey(t, db, s, id, k); err != nil {
		t.Fatal(err)
	}
	return lastKeyChange(t, db)
}

// TestKeyUpdateRecordsWhatChanged: a change to a key records the settings whose values it may disclose, as they were
// and as they are, and names the two lists without their values; a change that changes nothing says so. The key's text
// is never in the log.
func TestKeyUpdateRecordsWhatChanged(t *testing.T) {
	t.Parallel()
	db, _, s, text := callSetup(t)
	c, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), text)
	if err != nil {
		t.Fatal(err)
	}
	// Every setting a user may change, changed at once: the new key took the site's daily limit.
	all := accounts.KeySettings{
		Name: "Renamed", Disabled: true, SpendLimit: new(int64(1_000)), LimitReset: accounts.ResetWeekly,
		Models: []string{"m-1", "m-2"}, IPAllow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}
	want := keyChange{
		Changed: []string{"name", "disabled", "spend_limit", "limit_reset", "models", "ip_allow"},
		Before: keyValues{
			Name: new("Test"), Disabled: new(false), SpendLimit: new(int64(10_000_000_000)), LimitReset: new(accounts.ResetDaily),
		},
		After: keyValues{
			Name: new("Renamed"), Disabled: new(true), SpendLimit: new(int64(1_000)), LimitReset: new(accounts.ResetWeekly),
		},
	}
	if diff := cmp.Diff(want, changeOf(t, db, s, c.KeyID(), all)); diff != "" {
		t.Errorf("what the change recorded (-want +got):\n%s", diff)
	}
	// The same settings again change nothing, and the entry says so.
	if diff := cmp.Diff(keyChange{Changed: []string{}}, changeOf(t, db, s, c.KeyID(), all)); diff != "" {
		t.Errorf("a change that changed nothing (-want +got):\n%s", diff)
	}
	// One list changed is named alone: its values are not the log's.
	all.Models = append(all.Models, "m-3")
	if diff := cmp.Diff(keyChange{Changed: []string{"models"}}, changeOf(t, db, s, c.KeyID(), all)); diff != "" {
		t.Errorf("a change of the models (-want +got):\n%s", diff)
	}
	got := texts(t, db, platform, `SELECT detail::text FROM platform.audit_log WHERE action = 'api_key.update'`)
	for _, entry := range got {
		if strings.Contains(entry, "m-3") || strings.Contains(entry, "10.0.0.0/8") {
			t.Errorf("an entry holds the value of a list: %s", entry)
		}
	}
	var holding string
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%' || $1 || '%'`, text).Scan(&holding)
	}); err != nil {
		t.Fatal(err)
	}
	if holding != "0" {
		t.Errorf("%s audit entries hold the key's text", holding)
	}
}
