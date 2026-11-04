package audit_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var (
	site, account = uuid.NewV7(), uuid.NewV7()
	platform      = pg.NewScope(true, nil, nil)
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

// TestActorUserID: only a user actor names a row's user, and the UUID survives the round trip through the actor.
func TestActorUserID(t *testing.T) {
	t.Parallel()
	id := uuid.NewV7()
	if got, ok := audit.User(id).UserID(); !ok || got != id {
		t.Fatalf("User(%s).UserID() = (%s, %t), want (%s, true)", id, got, ok, id)
	}
	for name, actor := range map[string]audit.Actor{
		"system":         audit.System("catalog.sync"),
		"management key": audit.ManagementKey(id),
		"zero":           {},
	} {
		if _, ok := actor.UserID(); ok {
			t.Errorf("%s actor reported a user ID", name)
		}
	}
}

// event is a valid event.
func event() audit.Event {
	return audit.Event{
		Actor:  audit.System("catalog.sync"),
		Action: "model.retire",
		Target: audit.Target{Module: "catalog", Kind: "model", ID: "m"},
	}
}

// count reads the number of rows of table that scope sees.
func count(t *testing.T, db *pg.DB, scope pg.Scope, table string) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// exec runs one statement in its own platform transaction.
func exec(t *testing.T, db *pg.DB, sql string, args ...any) error {
	t.Helper()
	return db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), sql, args...)
		return err
	})
}

// TestRefusedEntryFailsTheChange: an entry is written in the transaction of the change it describes, so when the
// entry is refused the change is not kept either.
func TestRefusedEntryFailsTheChange(t *testing.T) {
	t.Parallel()
	db := open(t)
	refused := event()
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
	if changes := count(t, db, platform, "public.changes"); changes != 0 {
		t.Errorf("kept %d changes", changes)
	}
}

// TestEntriesCannotBeRewritten: the application may not change, remove or backdate an entry, even in the platform
// scope; the database refuses the privilege, not just the rows.
func TestEntriesCannotBeRewritten(t *testing.T) {
	t.Parallel()
	db := open(t)
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return audit.Record(t.Context(), tx, event())
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
		if err := exec(t, db, statement); code(err) != "42501" { // insufficient_privilege
			t.Errorf("%s: %v, want permission denied", statement, err)
		}
	}
}

// TestOnlyThePlatformScopeRecordsAndReads: an entry that names no tenant only the platform scope records or reads.
func TestOnlyThePlatformScopeRecordsAndReads(t *testing.T) {
	t.Parallel()
	db := open(t)
	for _, s := range []struct {
		name    string
		scope   pg.Scope
		refusal string // SQLSTATE of recording; 42501 is the policy's refusal
		sees    int
	}{ // platform first, so the others have an entry they must not read
		{"platform", platform, "", 1},
		{"no scope", pg.Scope{}, "42501", 0},
		{"account", pg.NewScope(false, nil, []uuid.UUID{account}), "42501", 0},
		{"site", pg.NewScope(false, []uuid.UUID{site}, nil), "42501", 0},
	} {
		err := db.Tx(t.Context(), s.scope, func(tx pg.Tx) error { return audit.Record(t.Context(), tx, event()) })
		if code(err) != s.refusal {
			t.Errorf("%s recording: %v, want SQLSTATE %q", s.name, err, s.refusal)
		}
		if read := count(t, db, s.scope, "platform.audit_log"); read != s.sees {
			t.Errorf("%s read %d entries, want %d", s.name, read, s.sees)
		}
	}
}

// TestTenantEntriesNeedARegisteredPair: an entry may name a site, or an account with its site, only as registered;
// the two mismatched pairs of two registered accounts, an unregistered site and an account without a site are
// refused. The tenant's own scope records its entries and registers itself, and reads its entries but not the
// registry.
func TestTenantEntriesNeedARegisteredPair(t *testing.T) {
	t.Parallel()
	db := open(t)
	otherSite, otherAccount := uuid.NewV7(), uuid.NewV7()
	ownAccount := pg.NewScope(false, nil, []uuid.UUID{account})
	if err := db.Tx(t.Context(), ownAccount, func(tx pg.Tx) error {
		return audit.RegisterAccount(t.Context(), tx, site, account)
	}); err != nil {
		t.Fatalf("an account registering itself: %v", err)
	}
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return errors.Join(audit.RegisterSite(t.Context(), tx, site), audit.RegisterSite(t.Context(), tx, otherSite),
			audit.RegisterAccount(t.Context(), tx, otherSite, otherAccount))
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name          string
		site, account uuid.UUID
		want          string
	}{
		{"a registered site", site, uuid.UUID{}, ""},
		{"a registered account", site, account, ""},
		{"an account with another registered site", otherSite, account, "23503"},
		{"another registered account with the site", site, otherAccount, "23503"},
		{"an unregistered site", uuid.NewV7(), uuid.UUID{}, "23503"},
		{"an account without its site", uuid.UUID{}, account, "23514"},
	} {
		e := event()
		e.Target.Site, e.Target.Account = c.site, c.account
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return audit.Record(t.Context(), tx, e) })
		if code(err) != c.want {
			t.Errorf("%s: %v, want SQLSTATE %q", c.name, err, c.want)
		}
	}
	e := event()
	e.Target.Site, e.Target.Account = site, account
	if err := db.Tx(t.Context(), ownAccount, func(tx pg.Tx) error { return audit.Record(t.Context(), tx, e) }); err != nil {
		t.Errorf("the account recording its own entry: %v", err)
	}
	if read, registry := count(t, db, ownAccount, "platform.audit_log"), count(t, db, ownAccount, "platform.tenants"); read != 2 || registry != 0 {
		t.Errorf("the account reads %d entries and %d registrations, want its 2 and none", read, registry)
	}
}

// TestRecordRefusesIncompleteEvents: an event without an actor, an action or a target, or carrying more than a small
// detail, is refused.
func TestRecordRefusesIncompleteEvents(t *testing.T) {
	t.Parallel()
	db := open(t)
	cases := map[string]func(*audit.Event){
		"no actor":              func(e *audit.Event) { e.Actor = audit.Actor{} },
		"no action":             func(e *audit.Event) { e.Action = "" },
		"an action of one word": func(e *audit.Event) { e.Action = "retire" },
		"no module":             func(e *audit.Event) { e.Target.Module = "" },
		"no target kind":        func(e *audit.Event) { e.Target.Kind = "" },
		"no target ID":          func(e *audit.Event) { e.Target.ID = "" },
		"a large detail":        func(e *audit.Event) { e.Detail = map[string]any{"text": strings.Repeat("x", 8192)} },
	}
	for name, change := range cases {
		e := event()
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
		Target: audit.Target{Module: "accounts", Kind: "api_key", ID: "k1"},
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
		return audit.Record(t.Context(), tx, event())
	}); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT concat_ws(' | ', actor_kind, actor_id, coalesce(request_id::text, '-'),
			action, module, target_kind, target_id, coalesce(reason, '-'), detail::text) FROM platform.audit_log ORDER BY actor_kind DESC`)
		if err != nil {
			return err
		}
		got, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"user | " + user.String() + " | " + requestID +
			` | api_key.revoke | accounts | api_key | k1 | leaked in a public repository | {"was": "active", "name": "ci"}`,
		"system | catalog.sync | - | model.retire | catalog | model | m | - | {}",
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
