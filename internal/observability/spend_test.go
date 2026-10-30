package observability

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func openSpendDB(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t, Module(), accounts.Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func seedSpendSite(t *testing.T, db *pg.DB) (site, tier uuid.UUID) {
	t.Helper()
	site, tier = uuid.NewV7(), uuid.NewV7()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts.sites(id,name,status,key_prefix,registration,default_tier_id) VALUES($1,'Spend site','active','ax-','invite',$2)`, []any{site, tier}},
			{`INSERT INTO accounts.tiers(id,site_id,name) VALUES($1,$2,'Default')`, []any{tier, site}},
		} {
			if _, err := tx.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site, tier
}

// seedSpendAccount gives one personal account and its only user. Accounts share a site because a site is the tenant
// boundary a user belongs to, and each personal account has exactly one user.
func seedSpendAccount(t *testing.T, db *pg.DB, site, tier uuid.UUID, n int) (account uuid.UUID) {
	t.Helper()
	account, user, workspace := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts.accounts(id,site_id,kind,name,status,tier_id) VALUES($1,$2,'personal','Spend user','active',$3)`, []any{account, site, tier}},
			{`INSERT INTO accounts.workspaces(id,site_id,account_id,name,status,is_default) VALUES($1,$2,$3,'Default','active',true)`, []any{workspace, site, account}},
			{`INSERT INTO accounts.users(id,site_id,account_id,email,password_hash,factor_version,status,invite_code) VALUES($1,$2,$3,$4,'$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA',1,'active',$5)`, []any{user, site, account, fmt.Sprintf("spend%d@example.com", n), fmt.Sprintf("spend%03d", n)}},
		} {
			if _, err := tx.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return account
}

// keepSpend writes records through the real recorder in batches smaller than its queue.
func keepSpend(t *testing.T, db *pg.DB, records ...CallRecord) {
	t.Helper()
	for len(records) > 0 {
		n := min(8, len(records))
		keep(t, db, records[:n]...)
		records = records[n:]
	}
}

func spendRecord(account uuid.UUID, at time.Time, ip, model string, price int64) CallRecord {
	r := record(account, at)
	r.IP = netip.MustParseAddr(ip)
	r.Model = model
	r.PriceNano = price
	r.CostNano = price / 2
	r.Free = false
	return r
}

func configureSpendDetection(t *testing.T, db *pg.DB, d SpendDetection) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.SpendDetection = &d
		return SetAlertConfiguration(t.Context(), tx, c)
	})
}

func runSpendDetection(t *testing.T, db *pg.DB, now time.Time) (fired, resolved int) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) (err error) {
		fired, resolved, err = detectSpendAnomalies(t.Context(), tx, now)
		return err
	})
	return fired, resolved
}

func spendSignalKinds(t *testing.T, db *pg.DB) []string {
	t.Helper()
	var kinds []string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT body->>'key' FROM platform.alert_signals WHERE body->>'kind'=$1 ORDER BY body->>'key'`, spendAnomalyKind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			kinds = append(kinds, key)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return kinds
}

// TestSpendAnomalyDetectsRaisesNotifiesAndResolves: one burst produces one incident per signal kind and one user
// notice, a second run while the burst is visible raises nothing again, and once the burst leaves the window all three
// incidents resolve. The clock is explicit, so no sleep or wall clock is involved.
//
//nolint:cyclop,gocognit // one burst through raise, persist, dedupe and resolve is the point.
func TestSpendAnomalyDetectsRaisesNotifiesAndResolves(t *testing.T) {
	db := openSpendDB(t)
	site, tier := seedSpendSite(t, db)
	account := seedSpendAccount(t, db, site, tier, 1)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var records []CallRecord
	for i := range 23 {
		records = append(records, spendRecord(account, now.Add(-24*time.Hour+time.Duration(i)*time.Hour+30*time.Minute), "203.0.113.7", "claude-opus", 100_000_000))
	}
	for i := range 5 {
		records = append(records, spendRecord(account, now.Add(-50*time.Minute+time.Duration(i)*time.Minute), "198.51.100.7", "gpt-5", 2_000_000_000))
	}
	keepSpend(t, db, records...)
	d := defaultSpendDetection()
	d.NotifyUsers = true
	configureSpendDetection(t, db, d)

	fired, resolved := runSpendDetection(t, db, now)
	if fired != 3 || resolved != 0 {
		t.Fatalf("first run fired=%d resolved=%d, want 3 and 0", fired, resolved)
	}
	want := []string{account.String() + "/model", account.String() + "/region", account.String() + "/spend/USD"}
	if got := spendSignalKinds(t, db); !slices.Equal(got, want) {
		t.Fatalf("signals %v, want %v", got, want)
	}
	if n := noticeCount(t, db, account); n != 1 {
		t.Fatalf("user notices %d, want 1", n)
	}
	var noticeValue int64
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT value FROM accounts.user_notices WHERE kind='spend_anomaly' AND account_id=$1`, account).Scan(&noticeValue)
	}); err != nil {
		t.Fatal(err)
	}
	if noticeValue != 10_000_000_000 {
		t.Fatalf("combined notice value %d, want the spend total 10000000000", noticeValue)
	}

	fired, resolved = runSpendDetection(t, db, now)
	if fired != 0 || resolved != 0 {
		t.Fatalf("second run fired=%d resolved=%d, want 0 and 0", fired, resolved)
	}
	if got := spendSignalKinds(t, db); len(got) != 3 {
		t.Fatalf("a persistent anomaly raised again: %v", got)
	}
	if n := noticeCount(t, db, account); n != 1 {
		t.Fatalf("a persistent anomaly notified %d times", n)
	}

	fired, resolved = runSpendDetection(t, db, now.Add(2*time.Hour))
	if fired != 0 || resolved != 3 {
		t.Fatalf("resolution fired=%d resolved=%d, want 0 and 3", fired, resolved)
	}
	if got := spendSignalKinds(t, db); len(got) != 6 {
		t.Fatalf("signals after resolution %v, want three raises and three resolutions", got)
	}
	var active int
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM observability.spend_anomalies`).Scan(&active)
	}); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("detector state left %d rows", active)
	}
}

// TestSpendRegionNetworksAreNormalised: the host bits never split one network into several, so an address change inside
// the account's own network is not foreign, and several addresses inside one new network count together.
//
//nolint:cyclop,gocognit // the table and its assertions read better in one place
func TestSpendRegionNetworksAreNormalised(t *testing.T) {
	db := openSpendDB(t)
	site, tier := seedSpendSite(t, db)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	types := []struct {
		name             string
		baselineIP       string
		recentIPs        []string
		wantRegion       bool
		wantNetworkField string
	}{
		{"IPv4 host change in the account's own /16", "203.0.113.7", []string{"203.0.113.8"}, false, ""},
		{"IPv4 addresses merged in one new /16", "203.0.113.7", []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5"}, true, "198.51.0.0/16"},
		{"IPv6 host change in the account's own /32", "2001:db8:1::1", []string{"2001:db8:1::2"}, false, ""},
		{"IPv6 addresses merged in one new /32", "2001:db8:1::1", []string{"2001:db9::1", "2001:db9::2", "2001:db9::3", "2001:db9::4", "2001:db9::5"}, true, "2001:db9::/32"},
	}
	var records []CallRecord
	ids := make([]uuid.UUID, len(types))
	for i, c := range types {
		account := seedSpendAccount(t, db, site, tier, i+1)
		ids[i] = account
		for j := range 10 {
			records = append(records, spendRecord(account, now.Add(-24*time.Hour+30*time.Minute+time.Duration(j)*2*time.Hour), c.baselineIP, "claude-opus", 0))
		}
		for j, ip := range c.recentIPs {
			records = append(records, spendRecord(account, now.Add(-50*time.Minute+time.Duration(j)*time.Minute), ip, "claude-opus", 0))
		}
	}
	keepSpend(t, db, records...)
	configureSpendDetection(t, db, defaultSpendDetection())
	runSpendDetection(t, db, now)

	regions := map[uuid.UUID]string{}
	for _, key := range spendSignalKinds(t, db) {
		account, kind, ok := splitSignalKey(key)
		if !ok || kind != "region" {
			continue
		}
		regions[account] = signalField(t, db, key, "networks")
	}
	for i, c := range types {
		got, ok := regions[ids[i]]
		if ok != c.wantRegion {
			t.Errorf("%s: region signal %v, want %v", c.name, ok, c.wantRegion)
			continue
		}
		if c.wantRegion && !strings.Contains(got, c.wantNetworkField) {
			t.Errorf("%s: networks %q, want %q", c.name, got, c.wantNetworkField)
		}
		if !c.wantRegion && got != "" {
			t.Errorf("%s: unexpected network field %q", c.name, got)
		}
	}
}

// signalField reads one field of a persisted signal.
func signalField(t *testing.T, db *pg.DB, key, field string) string {
	t.Helper()
	var value string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT body->'fields'->>$2 FROM platform.alert_signals WHERE body->>'kind'=$1 AND body->>'key'=$3`, spendAnomalyKind, field, key).Scan(&value)
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

// TestSpendScheduleLeavesNoGapBetweenRuns: a burst that happens between two scheduled runs still falls inside the next
// run's window, because the job runs more often than the shortest window an administrator may set.
func TestSpendScheduleLeavesNoGapBetweenRuns(t *testing.T) {
	if spendEvery >= minSpendWindow {
		t.Fatalf("the job runs every %s but the shortest window is %s: a burst can fall between runs", spendEvery, minSpendWindow)
	}
	db := openSpendDB(t)
	site, tier := seedSpendSite(t, db)
	account := seedSpendAccount(t, db, site, tier, 1)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var records []CallRecord
	for i := range 23 {
		records = append(records, spendRecord(account, now.Add(-24*time.Hour+time.Duration(i)*time.Hour+30*time.Minute), "203.0.113.7", "claude-opus", 100_000_000))
	}
	keepSpend(t, db, records...)
	d := defaultSpendDetection()
	d.WindowSeconds = int(minSpendWindow / time.Second)
	d.NotifyUsers = true
	configureSpendDetection(t, db, d)
	if fired, _ := runSpendDetection(t, db, now); fired != 0 {
		t.Fatalf("the calm run fired %d signals", fired)
	}

	// The burst starts a minute after the run and is over before the next one begins.
	var burst []CallRecord
	for i := range 5 {
		burst = append(burst, spendRecord(account, now.Add(time.Minute+time.Duration(i)*time.Minute), "198.51.100.7", "gpt-5", 2_000_000_000))
	}
	keepSpend(t, db, burst...)
	fired, _ := runSpendDetection(t, db, now.Add(spendEvery))
	if fired != 3 {
		t.Fatalf("the next run fired %d signals, want 3; the burst fell between windows", fired)
	}
}

// TestSpendNoticeWaitsForNotifyUsers: detection and its signal do not depend on the user notice, which stays off until
// an administrator enables it after the rollout; enabling it later notifies the next incident.
func TestSpendNoticeWaitsForNotifyUsers(t *testing.T) {
	db := openSpendDB(t)
	site, tier := seedSpendSite(t, db)
	account := seedSpendAccount(t, db, site, tier, 1)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var records []CallRecord
	for i := range 23 {
		records = append(records, spendRecord(account, now.Add(-24*time.Hour+time.Duration(i)*time.Hour+30*time.Minute), "203.0.113.7", "claude-opus", 100_000_000))
	}
	for i := range 5 {
		records = append(records, spendRecord(account, now.Add(-50*time.Minute+time.Duration(i)*time.Minute), "198.51.100.7", "gpt-5", 2_000_000_000))
	}
	keepSpend(t, db, records...)
	configureSpendDetection(t, db, defaultSpendDetection())
	if fired, _ := runSpendDetection(t, db, now); fired != 3 {
		t.Fatalf("fired %d signals", fired)
	}
	if n := noticeCount(t, db, account); n != 0 {
		t.Fatalf("the reminder was produced while notify_users was off: %d", n)
	}

	d := defaultSpendDetection()
	d.NotifyUsers = true
	configureSpendDetection(t, db, d)
	runSpendDetection(t, db, now.Add(2*time.Hour)) // resolves the first incident
	var second []CallRecord
	for i := range 5 {
		second = append(second, spendRecord(account, now.Add(2*time.Hour+10*time.Minute+time.Duration(i)*time.Minute), "192.0.2.7", "gpt-6", 2_000_000_000))
	}
	keepSpend(t, db, second...)
	if fired, _ := runSpendDetection(t, db, now.Add(3*time.Hour)); fired != 3 {
		t.Fatalf("the next incident fired %d signals", fired)
	}
	if n := noticeCount(t, db, account); n != 1 {
		t.Fatalf("notices after enabling: %d, want 1", n)
	}
}

func noticeCount(t *testing.T, db *pg.DB, account uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM accounts.user_notices WHERE kind='spend_anomaly' AND account_id=$1`, account).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSpendAnomalyThresholds: each threshold and the baseline requirement gates exactly the signal it governs, and an
// account without history is never called anomalous.
func TestSpendAnomalyThresholds(t *testing.T) {
	db := openSpendDB(t)
	site, tier := seedSpendSite(t, db)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	d := defaultSpendDetection()
	configureSpendDetection(t, db, d)

	type testCase struct {
		name                     string
		baselineCount            int
		baselineEach, recentEach int64
		recentCount              int
		recentIP, recentModel    string
		want                     []string
	}
	cases := []testCase{
		{"spike above both floors with a new region and model", 10, 100_000_000, 2_000_000_000, 5, "198.51.100.7", "gpt-5", []string{"model", "region", "spend/USD"}},
		{"below the absolute spend floor", 10, 100_000_000, 200_000_000, 5, "203.0.113.7", "claude-opus", nil},
		{"below the ratio", 10, 50_000_000_000, 1_000_000_000, 5, "203.0.113.7", "claude-opus", nil},
		{"no baseline history", 0, 0, 2_000_000_000, 5, "198.51.100.7", "gpt-5", nil},
		{"new region below its count", 10, 100_000_000, 2_000_000_000, 4, "198.51.100.7", "claude-opus", []string{"spend/USD"}},
		{"new model below its count", 10, 100_000_000, 2_000_000_000, 4, "203.0.113.7", "gpt-5", []string{"spend/USD"}},
		{"the baseline's own region and model", 10, 100_000_000, 2_000_000_000, 5, "203.0.113.7", "claude-opus", []string{"spend/USD"}},
		{"spend beyond int64", 10, 100_000_000, maxInt64 / 2, 5, "203.0.113.7", "claude-opus", []string{"spend/USD"}},
	}
	var records []CallRecord
	ids := make([]uuid.UUID, len(cases))
	for i, c := range cases {
		account := seedSpendAccount(t, db, site, tier, i+2)
		ids[i] = account
		for j := range c.baselineCount {
			records = append(records, spendRecord(account, now.Add(-24*time.Hour+time.Duration(j)*time.Hour+30*time.Minute), "203.0.113.7", "claude-opus", c.baselineEach))
		}
		for j := range c.recentCount {
			records = append(records, spendRecord(account, now.Add(-50*time.Minute+time.Duration(j)*time.Minute), c.recentIP, c.recentModel, c.recentEach))
		}
	}
	keepSpend(t, db, records...)

	fired, _ := runSpendDetection(t, db, now)
	signals := spendSignalKinds(t, db)
	byAccount := map[uuid.UUID][]string{}
	for _, key := range signals {
		account, kind, _ := splitSignalKey(key)
		byAccount[account] = append(byAccount[account], kind)
	}
	wantFired := 0
	for i, c := range cases {
		if !slices.Equal(byAccount[ids[i]], c.want) {
			t.Errorf("%s: signals %v, want %v", c.name, byAccount[ids[i]], c.want)
		}
		wantFired += len(c.want)
	}
	if fired != wantFired {
		t.Fatalf("fired=%d, want %d", fired, wantFired)
	}
}

// TestSpendDetectorDisabledResolvesActive: turning the detector off closes what is still firing instead of leaving
// events nobody watches; the disabled path touches no accounts table, so this test needs only this module.
func TestSpendDetectorDisabledResolvesActive(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	d := defaultSpendDetection()
	d.Enabled = false
	configureSpendDetection(t, db, d)
	account := uuid.NewV7()
	alertTx(t, db, func(tx pg.Tx) error {
		for _, kind := range []string{"spend/USD", "region"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO observability.spend_anomalies(account_id,kind) VALUES($1,$2)`, account, kind); err != nil {
				return err
			}
		}
		return nil
	})
	if fired, resolved := runSpendDetection(t, db, now); fired != 0 || resolved != 0 {
		t.Fatalf("disabled detector fired=%d resolved=%d", fired, resolved)
	}
	var active int
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM observability.spend_anomalies`).Scan(&active)
	}); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("disabled detector left %d incidents", active)
	}
	if got := spendSignalKinds(t, db); len(got) != 2 {
		t.Fatalf("resolutions %v, want two", got)
	}
}

func splitSignalKey(key string) (uuid.UUID, string, bool) {
	for i := 36; i < len(key); i++ {
		if key[i] == '/' {
			account, err := uuid.Parse(key[:i])
			return account, key[i+1:], err == nil
		}
	}
	return uuid.UUID{}, "", false
}

// TestSpendDetectionConfiguration: code defaults are served before the first save, invalid bounds and a stale revision
// are refused, and a replacement that omits the object (an older node) leaves the saved thresholds alone.
//
//nolint:cyclop // defaults, save, invalid bounds, stale revision and an older node's replacement.
func TestSpendDetectionConfiguration(t *testing.T) {
	db := openDB(t)
	var c AlertConfiguration
	alertTx(t, db, func(tx pg.Tx) (err error) { c, err = GetAlertConfiguration(t.Context(), tx); return err })
	if c.SpendDetection == nil || *c.SpendDetection != defaultSpendDetection() {
		t.Fatalf("defaults: %+v", c.SpendDetection)
	}
	if c.Rev != 2 || len(c.Rules) != 5 || c.Rules[4].Kind != spendAnomalyKind {
		t.Fatalf("migration did not add the spend rule: rev %d rules %+v", c.Rev, c.Rules)
	}

	saved := defaultSpendDetection()
	saved.SpendRatio = 5
	saved.WindowSeconds = int(minSpendWindow / time.Second)
	c.SpendDetection = &saved
	alertTx(t, db, func(tx pg.Tx) error { return SetAlertConfiguration(t.Context(), tx, c) })

	var updated AlertConfiguration
	alertTx(t, db, func(tx pg.Tx) (err error) { updated, err = GetAlertConfiguration(t.Context(), tx); return err })
	if updated.SpendDetection == nil || updated.SpendDetection.SpendRatio != 5 || updated.Rev != c.Rev+1 {
		t.Fatalf("saved: %+v rev %d", updated.SpendDetection, updated.Rev)
	}

	updated.SpendDetection = &saved
	for _, window := range []int{1, int(minSpendWindow/time.Second) - 1, 86401} {
		invalid := saved
		invalid.WindowSeconds = window
		updated.SpendDetection = &invalid
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return SetAlertConfiguration(t.Context(), tx, updated) }); !errors.Is(err, ErrAlertConfiguration) {
			t.Fatalf("window %d: %v", window, err)
		}
	}
	updated.SpendDetection = &saved
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return SetAlertConfiguration(t.Context(), tx, c) }); !errors.Is(err, ErrAlertRevision) {
		t.Fatalf("stale revision: %v", err)
	}

	updated.SpendDetection = nil
	alertTx(t, db, func(tx pg.Tx) error { return SetAlertConfiguration(t.Context(), tx, updated) })
	var after AlertConfiguration
	alertTx(t, db, func(tx pg.Tx) (err error) { after, err = GetAlertConfiguration(t.Context(), tx); return err })
	if after.SpendDetection == nil || after.SpendDetection.SpendRatio != 5 {
		t.Fatalf("an older node dropped the thresholds: %+v", after.SpendDetection)
	}

	// A damaged row is reported instead of reaching a ratio computation that cannot represent it.
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE observability.spend_detection SET body='{"enabled":true}'`)
		return err
	})
	alertTx(t, db, func(tx pg.Tx) error {
		if _, err := GetAlertConfiguration(t.Context(), tx); !errors.Is(err, ErrAlertConfiguration) {
			t.Fatalf("a damaged saved detection was accepted: %v", err)
		}
		return nil
	})
}
