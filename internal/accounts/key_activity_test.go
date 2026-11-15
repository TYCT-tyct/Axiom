package accounts_test

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestKeyActivityMetadata(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	id := x.makeKey(t, p, accounts.KeySettings{Name: "activity"})
	read := func() accounts.Key {
		t.Helper()
		var key accounts.Key
		x.inAccount(t, p, func(tx pg.Tx) (err error) { key, err = x.s.GetKey(t.Context(), tx, id); return err })
		return key
	}
	initial := read()
	if initial.LastUsedAt != nil || initial.CreatorName == "" || initial.CreatedBy != p.UserID() {
		t.Fatalf("unused key: %+v", initial)
	}
	writer, err := accounts.NewService(x.db, accounts.Config{Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t)})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() { writer.QueueKeyUse(id, at.Add(time.Duration(i)*time.Second)) })
	}
	wg.Wait()
	if err := writer.FlushKeyUses(t.Context()); err != nil {
		t.Fatal(err)
	}
	latest := at.Add(49 * time.Second)
	if got := read().LastUsedAt; got == nil || !got.Equal(latest) {
		t.Fatalf("concurrent use: %v", got)
	}
	writer.QueueKeyUse(id, at)
	if err := writer.FlushKeyUses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := read().LastUsedAt; got == nil || !got.Equal(latest) {
		t.Fatalf("late older observation moved time: %v", got)
	}
}

func TestKeyMetadataPagePlan(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	ids := make([]uuid.UUID, 50)
	for i := range ids {
		ids[i] = uuid.UUID(x.makeKey(t, p, accounts.KeySettings{Name: "page"}))
	}
	x.inAccount(t, p, func(tx pg.Tx) error {
		keys, _, err := x.s.ListKeys(t.Context(), tx, p.AccountID(), accounts.KeyQuery{Limit: 50})
		if err != nil {
			return err
		}
		if len(keys) != 50 {
			t.Fatalf("got %d keys", len(keys))
		}
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+accounts.ReadKeyMetadataQuery, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		text := strings.Join(plan, "\n")
		for _, table := range []string{"api_keys", "users", "accounts"} {
			if strings.Contains(text, "Seq Scan on "+table+" ") {
				t.Fatalf("metadata scanned all %s rows:\n%s", table, text)
			}
		}
		indexed := false
		for _, line := range plan {
			if strings.Contains(line, "Index Scan using ") && strings.Contains(line, " on api_keys ") {
				indexed = true
			}
		}
		if !indexed {
			t.Fatalf("metadata did not use the key index:\n%s", text)
		}
		return rows.Err()
	})
}

func TestKeyUseAdmissionSignal(t *testing.T) {
	t.Parallel()
	db, _, auth, text := callSetup(t)
	caller, err := auth.AuthenticateKey(t.Context(), accounts.SiteID(s1), text)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	writer, err := accounts.NewService(db, accounts.Config{Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Admit(t.Context(), caller, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client}, func(pg.Tx, accounts.Admission) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := writer.FlushKeyUses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.ReadTx(t.Context(), pg.AccountScope(uuid.UUID(caller.AccountID())), func(tx pg.Tx) error {
		key, err := auth.GetKey(t.Context(), tx, caller.KeyID())
		if err == nil && (key.LastUsedAt == nil || !key.LastUsedAt.Equal(at)) {
			t.Fatalf("admission activity: %+v", key)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyUseConcurrentNodes(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	writers := make([]*accounts.Service, 2)
	for i := range writers {
		var err error
		writers[i], err = accounts.NewService(db, accounts.Config{Platform: platform, Hashes: hashKeys(t, 1), Keyring: keyring(t)})
		if err != nil {
			t.Fatal(err)
		}
		keys := []uuid.UUID{k1, k2}
		if i == 1 {
			keys = []uuid.UUID{k2, k1, k2}
		}
		for _, key := range keys {
			writers[i].QueueKeyUse(accounts.KeyID(key), at.Add(time.Duration(i)*time.Second))
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, writer := range writers {
		go func() { <-start; results <- writer.FlushKeyUses(t.Context()) }()
	}
	close(start)
	for range writers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := texts(t, db, platform, fmt.Sprintf("SELECT (last_used_at = '%s')::text FROM accounts.api_keys WHERE id IN ('%s', '%s')", at.Add(time.Second).Format(time.RFC3339), k1, k2)); len(got) != 2 || got[0] != "true" || got[1] != "true" {
		t.Fatal(got)
	}
}

func TestKeyCreatorUnavailable(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p := x.signedIn(t)
	id := x.makeKey(t, p, accounts.KeySettings{Name: "departed"})
	// A creator outside the account remains identifiable even when their name is no longer visible.
	if err := pgtest.Run(t.Context(), x.database.Superuser, fmt.Sprintf("UPDATE accounts.api_keys SET created_by = '%s' WHERE id = '%s'", u1, uuid.UUID(id))); err != nil {
		t.Fatal(err)
	}
	var got accounts.Key
	x.inAccount(t, p, func(tx pg.Tx) (err error) { got, err = x.s.GetKey(t.Context(), tx, id); return err })
	if got.CreatorName != "Unknown user" || got.CreatedBy != accounts.UserID(u1) {
		t.Fatalf("departed creator: %+v", got)
	}
}

// Holding the first ID must stop both batches before either can lock the second ID.
func TestKeyUseLockOrder(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	results := make(chan error, 2)
	pending := 0
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "SELECT id FROM accounts.api_keys WHERE id = $1 FOR NO KEY UPDATE", k1); err != nil {
			return err
		}
		for _, keys := range [][]uuid.UUID{{k1, k2}, {k2, k1, k2}} {
			pending++
			go func() {
				times := make([]string, len(keys))
				for i := range times {
					times[i] = at
				}
				results <- db.Tx(t.Context(), platform, func(writer pg.Tx) error {
					return accounts.New().TouchKeys(t.Context(), writer, accounts.TouchKeysParams{Keys: keys, UsedAt: times})
				})
			}()
		}
		for t.Context().Err() == nil {
			select {
			case err := <-results:
				pending--
				return fmt.Errorf("batch returned before both writers waited: %v", err)
			default:
			}
			waiting := texts(t, db, platform, `SELECT count(*)::text FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '-- name: TouchKeys%'`)
			if waiting[0] == "2" {
				_, err := tx.Exec(t.Context(), "SELECT id FROM accounts.api_keys WHERE id = $1 FOR NO KEY UPDATE NOWAIT", k2)
				return err
			}
			runtime.Gosched()
		}
		return t.Context().Err()
	})
	if err != nil {
		t.Error(err)
	}
	for range pending {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
