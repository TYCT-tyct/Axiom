package observability

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var now = time.Now().UTC().Truncate(time.Millisecond)

// keep writes records through a real Recorder and waits for them to be stored.
func keep(t *testing.T, db *pg.DB, records ...CallRecord) {
	t.Helper()
	r := NewRecorder(t.Context(), db, platformScope, Config{
		QueueSize: 16, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, quiet)
	for _, rec := range records {
		if err := r.Enqueue(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, db *pg.DB, scope pg.Scope, q OwnQuery) []OwnCall {
	t.Helper()
	var calls []OwnCall
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
		calls, _, err = ListOwn(t.Context(), tx, q)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return calls
}

// day is every call of account in the day around now.
func day(account uuid.UUID) OwnQuery {
	return OwnQuery{Account: account, Since: now.Add(-24 * time.Hour), Until: now.Add(24 * time.Hour)}
}

// TestTenantsReadOnlyTheirOwnCalls: an account's scope shows its own calls, in full, and nobody else's; an empty
// scope shows nothing; a call of another account cannot be fetched by its ID.
func TestTenantsReadOnlyTheirOwnCalls(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a, b := uuid.NewV7(), uuid.NewV7()
	mine, theirs := record(a, now), record(b, now)
	keep(t, db, mine, theirs)

	got := list(t, db, accountScope(a), day(a))
	if len(got) != 1 || got[0].ID != mine.ID || got[0].Usage["input"] != 120 || got[0].PriceNano != mine.PriceNano ||
		got[0].Latency != mine.Latency || got[0].Key != mine.Key || got[0].IP != "203.0.113.7" {
		t.Errorf("account A reads %+v, want exactly its own call %s", got, mine.ID)
	}
	if got := list(t, db, accountScope(a), day(b)); len(got) != 0 {
		t.Errorf("account A's scope asking for account B reads %d calls, want 0: the condition is not the access control", len(got))
	}
	if got := list(t, db, accountScope(a, b), day(b)); len(got) != 1 {
		t.Errorf("a scope of both accounts reads %d calls of B, want 1", len(got))
	}
	if got := list(t, db, pg.Scope{}, day(a)); len(got) != 0 {
		t.Errorf("an empty scope reads %d calls, want 0", len(got))
	}
	err := db.ReadTx(t.Context(), accountScope(a), func(tx pg.Tx) error {
		_, err := GetOwn(t.Context(), tx, theirs.ID)
		return err
	})
	if err == nil {
		t.Error("account A fetched account B's call by ID")
	}
}

// TestTenantsCannotWriteOthersCalls: the policy binds writes too, so a scope of one account cannot insert a row of
// another, and the application can neither change nor delete a row.
func TestTenantsCannotWriteOthersCalls(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a, b := uuid.NewV7(), uuid.NewV7()
	rowOfB, err := newRow(record(b, now))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalRows([]row{rowOfB})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(scope pg.Scope) error {
		return db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			_, err := store.New().InsertCallLogs(t.Context(), tx, payload)
			return err
		})
	}
	if err := insert(accountScope(a)); err == nil {
		t.Error("account A's scope inserted a call of account B")
	}
	if err := insert(accountScope(b)); err != nil {
		t.Errorf("account B's scope could not insert its own call: %v", err)
	}
	for _, statement := range []string{
		"UPDATE observability.call_log SET price_nano = 0", "DELETE FROM observability.call_log",
		"TRUNCATE observability.call_log",
	} {
		err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), statement)
			return err
		})
		if err == nil {
			t.Errorf("%s was allowed", statement)
		}
	}
}

// TestRecordsAreStoredOnceAndNeverRefusedForLackOfAPartition: a record written twice, as after a retry whose first
// attempt had succeeded, is one row, and records outside the prepared months land in the default partition.
func TestRecordsAreStoredOnceAndNeverRefusedForLackOfAPartition(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	rec := record(a, now)
	early := record(a, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	late := record(a, time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC))
	keep(t, db, rec, rec, early, late)

	var inDefault, total int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE tableoid = 'observability.call_log_default'::regclass),
			count(*) FROM observability.call_log`).Scan(&inDefault, &total)
	}); err != nil {
		t.Fatal(err)
	}
	if total != 3 || inDefault != 2 {
		t.Errorf("%d rows, %d in the default partition; want 3 and 2", total, inDefault)
	}
}

// TestListOwnPagesAndFilters: pages follow the newest-first order without gaps or repeats, and the filters narrow.
func TestListOwnPagesAndFilters(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	var all []CallRecord
	for i := range 5 {
		rec := record(a, now.Add(-time.Duration(i)*time.Minute))
		if i == 3 {
			rec.Model, rec.ClientApp = "gpt-5", "codex"
		}
		all = append(all, rec)
	}
	keep(t, db, all...)

	var seen []uuid.UUID
	q := day(a)
	q.PageSize = 2
	for {
		var next *Cursor
		if err := db.ReadTx(t.Context(), accountScope(a), func(tx pg.Tx) (err error) {
			var page []OwnCall
			page, next, err = ListOwn(t.Context(), tx, q)
			for _, c := range page {
				seen = append(seen, c.ID)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if next == nil {
			break
		}
		q.After = next
	}
	want := make([]uuid.UUID, len(all))
	for i, rec := range all {
		want[i] = rec.ID
	}
	if !slices.Equal(seen, want) {
		t.Errorf("pages read %v, want %v", seen, want)
	}
	byModel := list(t, db, accountScope(a), OwnQuery{Account: a, Since: day(a).Since, Until: day(a).Until, Model: "gpt-5"})
	byApp := list(t, db, accountScope(a), OwnQuery{Account: a, Since: day(a).Since, Until: day(a).Until, ClientApp: "codex"})
	byTime := list(t, db, accountScope(a), OwnQuery{Account: a, Since: now.Add(-90 * time.Second), Until: day(a).Until})
	if len(byModel) != 1 || len(byApp) != 1 || len(byTime) != 2 {
		t.Errorf("filters match %d, %d, %d calls; want 1, 1, 2", len(byModel), len(byApp), len(byTime))
	}
}

// columns lists the names and types of the call log's columns, and those of its text and JSON columns that no check
// bounds in size or format.
func columns(t *testing.T, db *pg.DB) (all map[string]string, unbounded []string) {
	t.Helper()
	all = map[string]string{}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT column_name, data_type, NOT EXISTS (
				SELECT FROM pg_constraint k WHERE k.conrelid = 'observability.call_log'::regclass AND k.contype = 'c'
				AND pg_get_constraintdef(k.oid) LIKE '%' || column_name || '%'
				AND pg_get_constraintdef(k.oid) ~ '(char_length|octet_length|~|ANY)')
			FROM information_schema.columns WHERE table_schema = 'observability' AND table_name = 'call_log'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, kind string
			var noCheck bool
			if err := rows.Scan(&name, &kind, &noCheck); err != nil {
				return err
			}
			all[name] = kind
			if noCheck && (kind == "text" || kind == "jsonb") {
				unbounded = append(unbounded, name)
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return all, unbounded
}

// TestCallLogHoldsNoContentOrSecret: no column is named like conversation text or a credential, none holds raw
// bytes, and every text and JSON column is bounded by a check, so content cannot be added by accident.
func TestCallLogHoldsNoContentOrSecret(t *testing.T) {
	t.Parallel()
	forbidden := regexp.MustCompile(`(?i)body|content|prompt|message|completion|reply|response|secret|(access|auth|api|bearer|refresh)_?token|^token|password|credential|authorization|cookie|header|api_key|raw|plaintext|email`)
	all, unbounded := columns(t, openDB(t))
	if len(all) < 30 {
		t.Fatalf("only %d columns found: the check reads nothing", len(all))
	}
	for name, kind := range all {
		if forbidden.MatchString(name) || kind == "bytea" {
			t.Errorf("column %s (%s) may hold content or a secret", name, kind)
		}
	}
	if len(unbounded) > 0 {
		t.Errorf("text and JSON columns without a size or format check: %v", unbounded)
	}
}

// TestStartupCheckAcceptsTheModule: the startup audit finds forced row-level security and a policy on the table and
// on every partition.
func TestStartupCheckAcceptsTheModule(t *testing.T) {
	t.Parallel()
	if err := openDB(t).Check(t.Context(), Module()); err != nil {
		t.Fatal(err)
	}
}

// TestListFollowsTheAccountIndex: among many accounts, a page of one account's calls is read from the index by
// account, newest first, touching about a page of rows and not the table, as the application role under row-level
// security. The plan is logged for the PR record.
func TestListFollowsTheAccountIndex(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO observability.call_log (id, at, site_id, account_id,
			workspace_id, key_id, ip, request_class, used_tools, has_image, has_file, model, endpoint_id, attempt_count,
			usage, service_tier, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms,
			consent_version, training_allowed)
		SELECT uuidv7(), now() - make_interval(secs => n), '00000000-0000-4000-8000-000000000001',
			('00000000-0000-4000-8000-' || lpad((n % 300)::text, 12, '0'))::uuid, uuidv7(), uuidv7(), '203.0.113.7',
			'chat', false, false, false, 'm', uuidv7(), 1, '{}', 'standard', 1, 1, 'USD', false, 1, 0, 'v', true
		FROM generate_series(1, 90000) n`, "ANALYZE observability.call_log"); err != nil {
		t.Fatal(err)
	}
	account := uuid.MustParse("00000000-0000-4000-8000-000000000007")
	var plan []string
	if err := db.Tx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `EXPLAIN (ANALYZE, BUFFERS) SELECT id FROM observability.call_log
			WHERE account_id = $1 AND at >= now() - interval '2 days' AND at < now() + interval '1 day'
			ORDER BY at DESC, id DESC LIMIT 51`, account.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	t.Log("\n" + text)
	if !strings.Contains(text, "Index Cond: ((account_id") || strings.Contains(text, "Seq Scan") {
		t.Errorf("the page is not read through the account index:\n%s", text)
	}
}
