package audit_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgconn"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var (
	site1, site2       = uuid.NewV7(), uuid.NewV7()
	account1, account2 = uuid.NewV7(), uuid.NewV7()
	platform           = pg.NewScope(true, nil, nil)
)

// open returns the application's pool on a fresh database that also holds public.changes, a table standing in for
// a module's own data.
func open(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t)
	if err := pgtest.Run(t.Context(), database.Superuser, "CREATE TABLE public.changes (note text NOT NULL)",
		"GRANT SELECT, INSERT ON public.changes TO axiom_app"); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// event is a valid event about a target of the given tenant, named by targetID.
func event(targetID string, site, account uuid.UUID) audit.Event {
	return audit.Event{
		Actor:  audit.System("catalog.sync"),
		Action: "model.retire",
		Target: audit.Target{Module: "catalog", Kind: "model", ID: targetID, Site: site, Account: account},
	}
}

// count reads the number of rows of table that the platform scope sees.
func count(t *testing.T, db *pg.DB, table string) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRefusedEntryFailsTheChange: an entry is written in the transaction of the change it describes, so when the
// entry is refused the change is not kept either.
func TestRefusedEntryFailsTheChange(t *testing.T) {
	t.Parallel()
	db := open(t)
	refused := event("m", uuid.Nil(), uuid.Nil())
	refused.Action = ""
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), "INSERT INTO public.changes VALUES ('retired')"); err != nil {
			return err
		}
		return audit.Record(t.Context(), tx, refused)
	})
	if err == nil {
		t.Error("the transaction committed")
	}
	if changes := count(t, db, "public.changes"); changes != 0 {
		t.Errorf("kept %d changes", changes)
	}
}

// TestEntriesCannotBeRewritten: the application may not change, remove or backdate an entry, even in the platform
// scope; the database refuses the privilege, not just the rows.
func TestEntriesCannotBeRewritten(t *testing.T) {
	t.Parallel()
	db := open(t)
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return audit.Record(t.Context(), tx, event("m", uuid.Nil(), uuid.Nil()))
	}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"UPDATE platform.audit_log SET reason = 'rewritten'",
		"DELETE FROM platform.audit_log",
		"TRUNCATE platform.audit_log",
		`INSERT INTO platform.audit_log (at, actor_kind, actor_id, action, module, target_kind, target_id, detail)
			VALUES ('2020-01-01', 'system', 'catalog.sync', 'model.retire', 'catalog', 'model', 'm', '{}')`,
		`INSERT INTO platform.audit_log (id, actor_kind, actor_id, action, module, target_kind, target_id, detail)
			VALUES (uuidv7(), 'system', 'catalog.sync', 'model.retire', 'catalog', 'model', 'm', '{}')`,
	} {
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := tx.Exec(t.Context(), statement)
			return err
		})
		if code(err) != "42501" { // insufficient_privilege
			t.Errorf("%s: %v, want permission denied", statement, err)
		}
	}
}

// TestScopeDecidesWhichEntries: a transaction reads and records the entries of the tenants in its scope; only the
// platform scope reads and records those of no tenant.
func TestScopeDecidesWhichEntries(t *testing.T) {
	t.Parallel()
	db := open(t)
	targets := []struct {
		id            string
		site, account uuid.UUID
	}{{"a1", site1, account1}, {"s1", site1, uuid.Nil()}, {"a2", site2, account2}, {"platform", uuid.Nil(), uuid.Nil()}}
	scopes := []struct {
		name  string
		scope pg.Scope
		sees  string // the targets whose entries the scope may record and read
	}{
		{"no scope", pg.Scope{}, ""},
		{"account 1", pg.NewScope(false, nil, []uuid.UUID{account1}), "a1"},
		{"site 1", pg.NewScope(false, []uuid.UUID{site1}, nil), "a1,s1"},
		{"platform", platform, "a1,a2,platform,s1"},
	}
	for _, s := range scopes {
		var recorded []string
		for _, target := range targets {
			if err := db.Tx(t.Context(), s.scope, func(tx pg.Tx) error {
				return audit.Record(t.Context(), tx, event(target.id, target.site, target.account))
			}); err == nil {
				recorded = append(recorded, target.id)
			} else if code(err) != "42501" { // the policy's refusal
				t.Errorf("%s recording %s: %v", s.name, target.id, err)
			}
		}
		slices.Sort(recorded)
		if got := strings.Join(recorded, ","); got != s.sees {
			t.Errorf("%s recorded %q, want %q", s.name, got, s.sees)
		}
	}
	for _, s := range scopes {
		var read string
		if err := db.Tx(t.Context(), s.scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(DISTINCT target_id, ',' ORDER BY target_id), '')
				FROM platform.audit_log`).Scan(&read)
		}); err != nil {
			t.Fatal(err)
		}
		if read != s.sees {
			t.Errorf("%s read %q, want %q", s.name, read, s.sees)
		}
	}
}

// TestRecordRefusesIncompleteEvents: an event without an actor, an action or a target, attributed to an account
// without its site, or carrying more than a small detail, is refused.
func TestRecordRefusesIncompleteEvents(t *testing.T) {
	t.Parallel()
	db := open(t)
	cases := map[string]func(*audit.Event){
		"no actor":                func(e *audit.Event) { e.Actor = audit.Actor{} },
		"no action":               func(e *audit.Event) { e.Action = "" },
		"an action of one word":   func(e *audit.Event) { e.Action = "retire" },
		"no module":               func(e *audit.Event) { e.Target.Module = "" },
		"no target kind":          func(e *audit.Event) { e.Target.Kind = "" },
		"no target ID":            func(e *audit.Event) { e.Target.ID = "" },
		"an account without site": func(e *audit.Event) { e.Target.Account = account1 },
		"a large detail":          func(e *audit.Event) { e.Detail = map[string]any{"text": strings.Repeat("x", 8192)} },
	}
	for name, change := range cases {
		e := event("m", uuid.Nil(), uuid.Nil())
		change(&e)
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return audit.Record(t.Context(), tx, e) })
		if code(err) != "23514" { // check_violation
			t.Errorf("%s: %v, want a check violation", name, err)
		}
	}
}

// TestRecordStoresTheEvent: an entry holds the event as given, with the request's ID when there is a request and
// none otherwise.
func TestRecordStoresTheEvent(t *testing.T) {
	t.Parallel()
	db := open(t)
	user := uuid.NewV7()
	byUser := audit.Event{
		Actor:  audit.User(user),
		Action: "api_key.revoke",
		Target: audit.Target{Module: "accounts", Kind: "api_key", ID: "k1", Site: site1, Account: account1},
		Reason: "leaked in a public repository",
		Detail: map[string]any{"name": "ci", "was": "active"},
	}
	handler := httpserver.Public(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if err := db.Tx(r.Context(), platform, func(tx pg.Tx) error { return audit.Record(r.Context(), tx, byUser) }); err != nil {
			t.Error(err)
		}
	}), slog.New(slog.DiscardHandler), time.Minute)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody))
	requestID := response.Header().Get("X-Request-ID")
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return audit.Record(t.Context(), tx, event("m", uuid.Nil(), uuid.Nil()))
	}); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT concat_ws(' | ', actor_kind, actor_id, coalesce(request_id::text, '-'),
			action, module, target_kind, target_id, coalesce(site_id::text, '-'), coalesce(account_id::text, '-'),
			coalesce(reason, '-'), detail::text) FROM platform.audit_log ORDER BY actor_kind DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				return err
			}
			got = append(got, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"user | " + user.String() + " | " + requestID + " | api_key.revoke | accounts | api_key | k1 | " + site1.String() +
			" | " + account1.String() + ` | leaked in a public repository | {"was": "active", "name": "ci"}`,
		"system | catalog.sync | - | model.retire | catalog | model | m | - | - | - | {}",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("entries (-want +got):\n%s", diff)
	}
}

// code is the SQLSTATE of err, or "" when the database did not refuse anything.
func code(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}
