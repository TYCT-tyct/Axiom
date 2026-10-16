package pg_test

import (
	"os"
	"testing"

	"axiom/internal/platform/pgtest"
)

// TestMain gives this package a PostgreSQL cluster of its own, whatever cluster the run shares: its tests create, alter
// and rename cluster-wide roles, and count the sessions of the whole server, which would break the tests of the other
// packages running at the same time.
func TestMain(m *testing.M) {
	if err := os.Unsetenv(pgtest.ClusterEnv); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
