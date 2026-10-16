package pg_test

import (
	"context"
	"errors"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestIsDataError: a refused value is the data's fault, which no retry mends; a missing table and a cancelled
// context are not, and neither is an error that is not the database's.
func TestIsDataError(t *testing.T) {
	t.Parallel()
	db := open(t, pgtest.New(t).App)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	cases := []struct {
		name      string
		cancelled bool
		sql       string
		want      bool
	}{
		{"a malformed value", false, "SELECT 'x'::uuid", true},
		{"a division by zero", false, "SELECT 1 / 0", true},
		{"a missing table", false, "SELECT * FROM nowhere", false},
		{"a cancelled context", true, "SELECT 1", false},
	}
	for _, c := range cases {
		ctx := t.Context()
		if c.cancelled {
			ctx = cancelled
		}
		err := db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			_, err := tx.Exec(ctx, c.sql)
			return err
		})
		if err == nil || pg.IsDataError(err) != c.want {
			t.Errorf("%s: IsDataError(%v) = %v, want %v", c.name, err, pg.IsDataError(err), c.want)
		}
	}
	if pg.IsDataError(errors.New("boom")) || pg.IsDataError(nil) {
		t.Error("an error that is not the database's was taken for a data error")
	}
}
