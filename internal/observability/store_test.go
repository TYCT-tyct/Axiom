package observability

import (
	"errors"
	"net/netip"
	"regexp"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

var now = time.Now().UTC().Truncate(time.Millisecond)

// keep writes records through a real Recorder and waits for them to be stored.
func keep(t *testing.T, db *pg.DB, records ...CallRecord) {
	t.Helper()
	r, err := NewRecorder(t.Context(), db, platformScope, Config{
		Dir: t.TempDir(), Capacity: 64, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, quiet)
	if err != nil {
		t.Fatal(err)
	}
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
	checkAttemptScopes(t, db, a, b)
	err := db.ReadTx(t.Context(), accountScope(a), func(tx pg.Tx) error {
		_, err := GetOwn(t.Context(), tx, theirs.ID)
		return err
	})
	if err == nil {
		t.Error("account A fetched account B's call by ID")
	}
}

// checkAttemptScopes: the attempt table follows the same account scopes as the call table.
func checkAttemptScopes(t *testing.T, db *pg.DB, a, b uuid.UUID) {
	t.Helper()
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

// TestCallAndAttemptsRoundTrip: a call written twice stores one call and one row per attempt, its endpoint, source
// class, egress and attempt count are the last attempt's, and GetTrace reads the whole record back — every field, its
// attempts in order and routing's route — by the request ID alone.
func TestCallAndAttemptsRoundTrip(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	rec := record(uuid.NewV7(), now)
	rec.Route = []byte(`{"excluded":[{"endpoint":"p1","reason":"context"}],"route":["a","b"]}`)
	keep(t, db, rec)
	keep(t, db, rec) // a retry whose first write had committed
	checkCallRow(t, db, rec)
	got := trace(t, db, rec.ID)
	var wantRoute, gotRoute any
	if err := jsonx.Unmarshal(rec.Route, &wantRoute); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal(got.Route, &gotRoute); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(wantRoute, gotRoute); diff != "" {
		t.Errorf("route (-want +got):\n%s", diff)
	}
	rec.Route, got.Route = nil, nil
	if diff := cmp.Diff(rec, got, cmp.Comparer(func(a, b EndUserHash) bool { return a == b }),
		cmp.Comparer(func(a, b platform.Currency) bool { return a == b }),
		cmp.Comparer(func(a, b netip.Addr) bool { return a == b })); diff != "" {
		t.Errorf("the call read back (-want +got):\n%s", diff)
	}
}

// withAttempts is rec with n attempts numbered in order, the later ones copies of its second.
func withAttempts(rec CallRecord, n int) CallRecord {
	attempts := rec.Attempts[:2:2]
	for len(attempts) < n {
		a := rec.Attempts[1]
		a.Seq = platform.AttemptSeq(len(attempts) + 1) //nolint:gosec // G115: a test's few attempts
		attempts = append(attempts, a)
	}
	rec.Attempts = attempts
	return rec
}

// TestALoopsAttemptsAreKept: every sampling of a model-side loop is an attempt, so a call keeps up to
// platform.MaxAttemptSeq of them, which the table's widened check (00016) accepts.
func TestALoopsAttemptsAreKept(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	rec := withAttempts(record(uuid.NewV7(), now), platform.MaxAttemptSeq)
	keep(t, db, rec)
	if got := trace(t, db, rec.ID); len(got.Attempts) != platform.MaxAttemptSeq || got.Attempts[platform.MaxAttemptSeq-1].Seq != platform.MaxAttemptSeq {
		t.Fatalf("read back %d attempts, want %d in order", len(got.Attempts), platform.MaxAttemptSeq)
	}
}

// trace reads a call's chain in the platform scope.
func trace(t *testing.T, db *pg.DB, id uuid.UUID) CallRecord {
	t.Helper()
	var rec CallRecord
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		rec, err = GetTrace(t.Context(), tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func checkCallRow(t *testing.T, db *pg.DB, rec CallRecord) {
	t.Helper()
	var count, attempts int
	var endpoint, source, egress string
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), max(attempt_count), max(endpoint_id::text), max(source_class),
			max(egress)
			FROM observability.call_log WHERE id = $1`, rec.ID).
			Scan(&count, &attempts, &endpoint, &source, &egress)
	}); err != nil {
		t.Fatal(err)
	}
	last := rec.Attempts[len(rec.Attempts)-1]
	if count != 1 || attempts != len(rec.Attempts) || endpoint != last.Endpoint.String() || source != last.SourceClass ||
		egress != last.Egress {
		t.Errorf("call: %d rows, %d attempts, endpoint %q, source %q, egress %q",
			count, attempts, endpoint, source, egress)
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

// TestCallLogCoversTheWriteTimeFields: every field of docs/domains/observability.md「写入时记全」has a column, on the
// call or on its attempt, under the group the document puts it in. A removed or renamed column fails here, so the
// acceptance item is checked field by field rather than by reading the schema by eye.
func TestCallLogCoversTheWriteTimeFields(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	calls, _ := columns(t, db, "call_log")
	attempts, _ := columns(t, db, "call_attempt")
	for _, group := range []struct {
		name   string
		table  map[string]string
		fields []string
	}{
		{"who", calls, []string{"site_id", "account_id", "workspace_id", "key_id", "end_user_hash", "ip"}},
		{"with what", calls, []string{
			"client_app", "app_referer", "app_title", "app_categories", "session_id", "subagent_id", "request_class",
		}},
		{"where", calls, []string{"model", "endpoint_id", "source_class", "egress", "route"}},
		{"result", calls, []string{
			"usage", "price_nano", "cost_nano", "latency_ms", "first_token_ms", "finish_reason", "error_kind", "free_of_charge",
		}},
		{"rights", calls, []string{"consent_version", "training_allowed"}},
		{"write labels", calls, []string{"client_app", "model", "source_class", "language", "used_tools", "has_image", "has_file"}},
		{"each attempt", attempts, []string{
			"request_id", "attempt_seq", "endpoint_id", "supplier_id", "source_class", "egress", "started_at", "finished_at",
			"first_token_ms", "outcome", "error_kind",
		}},
	} {
		for _, field := range group.fields {
			if _, ok := group.table[field]; !ok {
				t.Errorf("%s: no column %q", group.name, field)
			}
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

func TestOwnCallPriceVersionAndLegacy(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	fresh := record(account, now)
	legacy := record(account, now.Add(-time.Second))
	legacy.PriceVersion = ""
	keep(t, db, fresh, legacy)
	got := list(t, db, accountScope(account), day(account))
	if len(got) != 2 {
		t.Fatalf("calls: %v", got)
	}
	if got[0].PriceVersion != fresh.PriceVersion || got[1].PriceVersion != "" {
		t.Fatalf("versions: %s / %s", got[0].PriceVersion, got[1].PriceVersion)
	}
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		call, err := GetOwn(t.Context(), tx, fresh.ID)
		if err != nil {
			return err
		}
		if call.PriceVersion != fresh.PriceVersion {
			t.Fatalf("get own version: %s", call.PriceVersion)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAPendingCallIsNotReadAsZero: the log never knew a pending call's usage or price, so the reads an account makes
// skip it (the list the CSV export pages through, the aggregates, the activity and the call by ID) rather than show
// zeros, while the operator's trace and list name it pending and the list gives it no price.
func TestAPendingCallIsNotReadAsZero(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	a := uuid.NewV7()
	settled, pending := record(a, now), pendingOf(record(a, now.Add(-time.Second)))
	keep(t, db, settled, pending)
	if got := list(t, db, accountScope(a), day(a)); len(got) != 1 || got[0].ID != settled.ID {
		t.Errorf("the account lists %d calls, want only the settled one", len(got))
	}
	if err := db.ReadTx(t.Context(), accountScope(a), func(tx pg.Tx) error {
		if _, err := GetOwn(t.Context(), tx, pending.ID); !errors.Is(err, pg.ErrNoRows) {
			t.Errorf("GetOwn of the pending call = %v, want no rows", err)
		}
		groups, err := AggregateOwn(t.Context(), tx, day(a), "model")
		if err != nil || len(groups) != 1 || groups[0].Calls != "1" {
			t.Errorf("aggregates %+v (%v), want the settled call alone", groups, err)
		}
		items, err := ActivityOwn(t.Context(), tx, ActivityQuery{Account: a, Since: day(a).Since, Until: day(a).Until})
		if err != nil || len(items) != 1 || items[0].Requests != 1 {
			t.Errorf("activity %+v (%v), want the settled call alone", items, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkOperatorSeesPending(t, db, a, pending.ID)
}

// checkOperatorSeesPending: the operator's trace of the pending call says so, and the operator's list of account a
// has the settled call and then the pending one, marked.
func checkOperatorSeesPending(t *testing.T, db *pg.DB, a, pending uuid.UUID) {
	t.Helper()
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		trace, err := GetTrace(t.Context(), tx, pending)
		if err != nil || !trace.BillingPending {
			t.Errorf("the trace of the pending call says pending %v (%v), want true", trace.BillingPending, err)
		}
		calls, _, _, err := ListAdmin(t.Context(), tx, AdminQuery{Since: day(a).Since, Until: day(a).Until, Account: &a, PageSize: 10})
		if err != nil || len(calls) != 2 || calls[0].Pending || !calls[1].Pending {
			t.Errorf("the operator lists %+v (%v), want the settled call, then the pending one", calls, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
