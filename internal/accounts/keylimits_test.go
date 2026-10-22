package accounts_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
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
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = tryKey(t, db, s, "At once", accounts.WorkspaceID(w1))
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

// madeAt makes a call key of a1 whose ID carries the time at, as a key made then would have, and deletes it if it was.
// ID n is the last byte of the ID, to tell keys of one millisecond apart.
func madeAt(t *testing.T, database pgtest.Database, at time.Time, n byte, deleted bool) {
	t.Helper()
	var id uuid.UUID
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(at.UnixMilli())) // #nosec G115 -- after 1970
	copy(id[:6], ms[2:])
	id[6], id[8], id[15] = 0x70, 0x80, n
	sql := apiKey("'"+id.String()+"'", ":s1", ":a1", ":w1", ":u1", "call")
	if deleted {
		sql += ";\nUPDATE accounts.api_keys SET deleted_at = now() WHERE id = '" + id.String() + "'"
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ids.Replace(sql)); err != nil {
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

// TestKeyCreationWindow: the keys made in the last hour are counted by the time their IDs carry, deleted ones too, on
// the node's clock; the refusal names when the oldest of them is an hour old. The clock is two hours on, so that the
// key the seed made is out of the hour, and the keys of the hour are the ones the test makes.
func TestKeyCreationWindow(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	keySettingsOf(t, database, 100, 2)
	var mu sync.Mutex
	at := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	s := node(t, db, hashKeys(t, 1), func() time.Time { mu.Lock(); defer mu.Unlock(); return at })
	move := func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	madeAt(t, database, now().Add(-40*time.Minute), 1, true)
	madeAt(t, database, now().Add(-10*time.Minute), 2, false)
	madeAt(t, database, now().Add(-2*time.Hour), 3, false) // long out of the hour
	want := accounts.TooManyKeys{Limit: 2, Count: 2, RetryAfter: 20 * time.Minute}
	if diff := cmp.Diff(want, tooManyKeys(t, tryKey(t, db, s, "Over", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("two keys in the hour, one of them deleted (-want +got):\n%s", diff)
	}
	// A key exactly an hour old is out of the hour; a millisecond younger is in it.
	move(20*time.Minute - time.Millisecond)
	want.RetryAfter = time.Millisecond
	if diff := cmp.Diff(want, tooManyKeys(t, tryKey(t, db, s, "Over", accounts.WorkspaceID(w1)))); diff != "" {
		t.Errorf("a millisecond before the oldest is an hour old (-want +got):\n%s", diff)
	}
	move(time.Millisecond)
	if err := tryKey(t, db, s, "In", accounts.WorkspaceID(w1)); err != nil {
		t.Errorf("when the oldest is an hour old: %v", err)
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
