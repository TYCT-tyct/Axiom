package pg_test

import (
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
	}{
		"the program's size":    {"", pg.PoolSize},
		"the connection string": {"&pool_max_conns=7", 7},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, err := pg.Open(t.Context(), app+c.poolMaxConns)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			if got := db.PoolMaxConns(); got != c.want {
				t.Errorf("pool size %d, want %d", got, c.want)
			}
		})
	}
}
