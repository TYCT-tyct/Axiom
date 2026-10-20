package pg_test

import (
	"math"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestOpenSizesThePool: the pool is PoolSize whatever the machine's cores, and a connection string that sets pgx's
// pool_max_conns sizes it instead.
func TestOpenSizesThePool(t *testing.T) {
	t.Parallel()
	app := pgtest.New(t).App
	cases := map[string]struct {
		poolMaxConns string
		want         int32
		reserve      int
	}{
		"the program's size":           {"", pg.PoolSize, pg.CompletionReserve},
		"the connection string":        {"&pool_max_conns=7", 7, 2},
		"minimum fills ordinary share": {"&pool_max_conns=7&pool_min_conns=5&pool_min_idle_conns=5", 7, 2},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opts []pg.Option
			if c.poolMaxConns != "" {
				opts = append(opts, pg.WithCompletionReserve(c.reserve))
			}
			db, err := pg.Open(t.Context(), app+c.poolMaxConns, opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if got := db.MaxConns(); got != c.want {
				t.Errorf("pool size %d, want %d", got, c.want)
			}
			if got := db.CompletionConns(); got != c.reserve {
				t.Errorf("completion reserve %d, want %d", got, c.reserve)
			}
		})
	}
}

// Bad budgets fail before opening a connection, without quoting the URL.
func TestOpenRefusesInvalidConnectionBudgets(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params  string
		reserve int
		want    []string
	}{
		"zero reserve": {"", 0, nil}, "negative reserve": {"", -1, nil},
		"no ordinary share": {"", pg.PoolSize, nil}, "overflow": {"", math.MaxInt, nil},
		"URL too small":                       {"&pool_max_conns=2", 2, nil},
		"minimum exceeds ordinary share":      {"&pool_max_conns=4&pool_min_conns=4", 1, []string{"pool_min_conns 4", "pool_min_idle_conns 0", "ordinary pool capacity 3"}},
		"idle minimum exceeds ordinary share": {"&pool_max_conns=4&pool_min_idle_conns=4", 1, []string{"pool_min_conns 0", "pool_min_idle_conns 4", "ordinary pool capacity 3"}},
	} {
		t.Run(name, func(t *testing.T) {
			db, err := pg.Open(t.Context(), "postgres://planted:planted@127.0.0.1:1/planted?sslmode=disable"+tc.params, pg.WithCompletionReserve(tc.reserve))
			if err == nil {
				db.Close()
				t.Fatal("invalid budget accepted")
			}
			if !strings.Contains(err.Error(), pg.DatabaseURLVar) || strings.Contains(err.Error(), "planted") {
				t.Fatalf("unsafe or unhelpful error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("budget error %q omits %q", err, want)
				}
			}
		})
	}
}

func TestCompletionPoolStillEnforcesTenantScope(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	ctx := pg.CompletionContext(t.Context())
	for _, tc := range []struct {
		scope pg.Scope
		want  int
	}{
		{pg.NewScope(true, nil, nil), 4},
		{pg.AccountScope(account1), 1},
		{pg.Scope{}, 0},
	} {
		var count int
		if err := db.Tx(ctx, tc.scope, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM rlstest.items").Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		if count != tc.want {
			t.Fatalf("completion saw %d rows, want %d", count, tc.want)
		}
	}
}
