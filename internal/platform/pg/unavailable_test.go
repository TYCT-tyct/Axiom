package pg_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"axiom/internal/platform/pg"
)

// TestUnavailable: a database that cannot be reached, a connection exception, a server going down and one out of
// connections are outages; a refused row, a missing table and a plain error are not. The reason never repeats the
// error's own text, which names the host and user.
func TestUnavailable(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), "postgres://secretuser:x@127.0.0.1:1/secretdb?sslmode=disable") // the pool connects lazily
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	unreachable := db.Tx(t.Context(), pg.Scope{}, func(pg.Tx) error { return nil })
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"unreachable", fmt.Errorf("admit: %w", unreachable), true},
		{"connection exception", &pgconn.PgError{Code: "08006"}, true},
		{"shutting down", &pgconn.PgError{Code: "57P01"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"undefined table", &pgconn.PgError{Code: "42P01"}, false},
		{"other", fmt.Errorf("a bug"), false},
	} {
		reason, got := pg.Unavailable(tc.err)
		if got != tc.want {
			t.Errorf("%s: Unavailable = %v, want %v", tc.name, got, tc.want)
		}
		if got && (strings.Contains(reason, "secretuser") || strings.Contains(reason, "secretdb") || strings.Contains(reason, "127.0.0.1")) {
			t.Errorf("%s: the reason %q names the connection", tc.name, reason)
		}
	}
}
