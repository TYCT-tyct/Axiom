package observability

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
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
		if err := r.Enqueue(rec); err != nil {
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

// attemptsOf is how many attempt rows the account has visible to the platform scope.
func attemptsOf(t *testing.T, db *pg.DB, account uuid.UUID) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM observability.call_attempt WHERE account_id = $1", account).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestTenantsReadOnlyTheirOwnCalls: an account's scope shows its own calls and attempts, in full, and nobody else's; an
// empty scope shows nothing; a call of another account cannot be fetched by its ID.
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
	for _, tc := range []struct {
		scope pg.Scope
		want  int
	}{{accountScope(a), 2}, {accountScope(b), 2}, {accountScope(a, b), 4}, {pg.Scope{}, 0}} {
		var n int
		if err := db.ReadTx(t.Context(), tc.scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT count(*) FROM observability.call_attempt").Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		if n != tc.want {
			t.Errorf("a scope sees %d attempts, want %d", n, tc.want)
		}
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
// another, and the application can neither change nor delete a row of either table.
func TestTenantsCannotWriteOthersCalls(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a, b := uuid.NewV7(), uuid.NewV7()
	callOfB, attemptsOfB, err := newRows(record(b, now))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := jsonx.MarshalCanonical([]row{callOfB})
	if err != nil {
		t.Fatal(err)
	}
	attemptPayload, err := jsonx.MarshalCanonical(attemptsOfB)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(scope pg.Scope) error {
		return db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			_, err := store.New().InsertCallLogs(t.Context(), tx, payload)
			return err
		})
	}
	insertAttempts := func(scope pg.Scope) error {
		return db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			_, err := store.New().InsertCallAttempts(t.Context(), tx, attemptPayload)
			return err
		})
	}
	if err := insert(accountScope(a)); err == nil {
		t.Error("account A's scope inserted a call of account B")
	}
	if err := insert(accountScope(b)); err != nil {
		t.Errorf("account B's scope could not insert its own call: %v", err)
	}
	if err := insertAttempts(accountScope(a)); err == nil {
		t.Error("account A's scope inserted attempts of account B")
	}
	if err := insertAttempts(accountScope(b)); err != nil {
		t.Errorf("account B's scope could not insert its own attempts: %v", err)
	}
	for _, statement := range []string{
		"UPDATE observability.call_log SET price_nano = 0", "DELETE FROM observability.call_log",
		"TRUNCATE observability.call_log", "UPDATE observability.call_attempt SET outcome = 'committed'",
		"DELETE FROM observability.call_attempt", "TRUNCATE observability.call_attempt",
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
// attempt had succeeded, is one call row and one row per attempt, and records outside the prepared months land in the
// default partition of both tables.
func TestRecordsAreStoredOnceAndNeverRefusedForLackOfAPartition(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	rec := record(a, now)
	early := record(a, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	late := record(a, time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC))
	keep(t, db, rec, rec, early, late)

	partitionCounts := func(table, defaultPartition string) (inDefault, total int) {
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT count(*) FILTER (WHERE tableoid = $1::regclass), count(*) FROM observability."+table,
				"observability."+defaultPartition).Scan(&inDefault, &total)
		}); err != nil {
			t.Fatal(err)
		}
		return inDefault, total
	}
	if inDefault, total := partitionCounts("call_log", "call_log_default"); total != 3 || inDefault != 2 {
		t.Errorf("calls: %d rows, %d in the default partition; want 3 and 2", total, inDefault)
	}
	if inDefault, total := partitionCounts("call_attempt", "call_attempt_default"); total != 6 || inDefault != 4 {
		t.Errorf("attempts: %d rows, %d in the default partition; want 6 and 4", total, inDefault)
	}
}

// TestCallAndAttemptsRoundTrip: a call written twice stores one row per attempt, and the call's endpoint, source
// class, egress and attempt count are the last attempt's, so the record links its attempts, route and ledger entries.
func TestCallAndAttemptsRoundTrip(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	rec := record(a, now)
	keep(t, db, rec)
	keep(t, db, rec) // a retry whose first write had committed

	var count, attempts int
	var endpoint, source, egress, route string
	var ledger int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), max(attempt_count), max(endpoint_id::text), max(source_class),
			max(egress), max(route::text), max(cardinality(ledger_entry_ids))
			FROM observability.call_log WHERE id = $1`, rec.ID).
			Scan(&count, &attempts, &endpoint, &source, &egress, &route, &ledger)
	}); err != nil {
		t.Fatal(err)
	}
	last := rec.Attempts[len(rec.Attempts)-1]
	if count != 1 || attempts != len(rec.Attempts) || endpoint != last.Endpoint.String() || source != last.SourceClass ||
		egress != last.Egress || ledger != len(rec.Ledger) || !strings.Contains(route, `"preset"`) {
		t.Errorf("call: %d rows, %d attempts, endpoint %q, source %q, egress %q, route %q, %d ledger entries",
			count, attempts, endpoint, source, egress, route, ledger)
	}

	var seqs []int
	var outcomes, endpoints, errors []string
	var firstTokens []int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT attempt_seq, outcome, error_kind, endpoint_id::text, first_token_ms
			FROM observability.call_attempt WHERE request_id = $1 ORDER BY attempt_seq`, rec.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seq int
			var outcome, errKind, endpoint string
			var first int
			if err := rows.Scan(&seq, &outcome, &errKind, &endpoint, &first); err != nil {
				return err
			}
			seqs, outcomes, errors, endpoints, firstTokens = append(seqs, seq), append(outcomes, outcome),
				append(errors, errKind), append(endpoints, endpoint), append(firstTokens, first)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	wantSeqs, wantOutcomes := []int{1, 2}, []string{string(AttemptFailed), string(AttemptCommitted)}
	if !slices.Equal(seqs, wantSeqs) || !slices.Equal(outcomes, wantOutcomes) ||
		!slices.Equal(errors, []string{"rate_limited", ""}) ||
		!slices.Equal(endpoints, []string{rec.Attempts[0].Endpoint.String(), rec.Attempts[1].Endpoint.String()}) ||
		!slices.Equal(firstTokens, []int{0, 300}) {
		t.Errorf("attempts: seqs %v, outcomes %v, errors %v, endpoints %v, first tokens %v", seqs, outcomes, errors, endpoints, firstTokens)
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

// columns lists the names and types of a table's columns, and those of its text and JSON columns that no check bounds
// in size or format.
func columns(t *testing.T, db *pg.DB, table string) (all map[string]string, unbounded []string) {
	t.Helper()
	all = map[string]string{}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT column_name, data_type, NOT EXISTS (
				SELECT FROM pg_constraint k
				WHERE k.conrelid = format('observability.%I', $1::text)::regclass AND k.contype = 'c'
				AND pg_get_constraintdef(k.oid) LIKE '%' || column_name || '%'
				AND pg_get_constraintdef(k.oid) ~ '(char_length|octet_length|~|ANY)')
			FROM information_schema.columns WHERE table_schema = 'observability' AND table_name = $1::text`, table)
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

// TestCallLogHoldsNoContentOrSecret: no column of either table is named like conversation text or a credential, none
// holds raw bytes, and every text and JSON column is bounded by a check, so content cannot be added by accident.
func TestCallLogHoldsNoContentOrSecret(t *testing.T) {
	t.Parallel()
	forbidden := regexp.MustCompile(`(?i)body|content|prompt|message|completion|reply|response|secret|(access|auth|api|bearer|refresh)_?token|^token|password|credential|authorization|cookie|header|api_key|raw|plaintext|email`)
	db := openDB(t)
	for _, table := range []string{"call_log", "call_attempt"} {
		all, unbounded := columns(t, db, table)
		if len(all) < 10 {
			t.Fatalf("%s: only %d columns found: the check reads nothing", table, len(all))
		}
		for name, kind := range all {
			if forbidden.MatchString(name) || kind == "bytea" {
				t.Errorf("%s.%s (%s) may hold content or a secret", table, name, kind)
			}
		}
		if len(unbounded) > 0 {
			t.Errorf("%s: text and JSON columns without a size or format check: %v", table, unbounded)
		}
	}
}

// TestStartupCheckAcceptsTheModule: the startup audit finds forced row-level security and a policy on both tables and
// on every partition.
func TestStartupCheckAcceptsTheModule(t *testing.T) {
	t.Parallel()
	if err := openDB(t).Check(t.Context(), Module()); err != nil {
		t.Fatal(err)
	}
}
