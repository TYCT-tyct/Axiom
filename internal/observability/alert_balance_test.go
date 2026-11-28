package observability

import (
	"errors"
	"maps"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestLowBalanceThresholds: the suppliers' alert lines are saved with the rules, read back by suppliers, kept by a
// replacement that does not send them, and refused when an amount is not a non-negative decimal or a supplier repeats.
func TestLowBalanceThresholds(t *testing.T) {
	db := openDB(t)
	relay, wallet := uuid.NewV7(), uuid.NewV7()
	set := func(lines *[]LowBalanceThreshold) error {
		var err error
		alertTx(t, db, func(tx pg.Tx) error {
			c, readErr := GetAlertConfiguration(t.Context(), tx)
			if readErr != nil {
				return readErr
			}
			c.LowBalanceThresholds = lines
			err = SetAlertConfiguration(t.Context(), tx, c)
			return nil
		})
		return err
	}
	saved := func() map[uuid.UUID]string {
		var lines map[uuid.UUID]string
		alertTx(t, db, func(tx pg.Tx) (err error) { lines, err = LowBalanceLines(t.Context(), tx); return err })
		return lines
	}
	if got := saved(); len(got) != 0 {
		t.Fatalf("lines before any were set: %v", got)
	}
	want := map[uuid.UUID]string{relay: "2500000", wallet: "12.5"}
	if err := set(&[]LowBalanceThreshold{{relay, "2500000"}, {wallet, "12.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := set(nil); err != nil {
		t.Fatal(err)
	}
	if got := saved(); !maps.Equal(got, want) {
		t.Fatalf("lines after a replacement without them: %v, want %v", got, want)
	}
	for _, bad := range [][]LowBalanceThreshold{
		{{relay, "-1"}},
		{{relay, "1e3"}},
		{{relay, ""}},
		{{relay, "0.0000000000000001"}},
		{{uuid.Nil(), "1"}},
		{{relay, "1"}, {relay, "2"}},
	} {
		if err := set(&bad); !errors.Is(err, ErrAlertConfiguration) {
			t.Errorf("lines %v: %v, want refused", bad, err)
		}
	}
	if err := set(&[]LowBalanceThreshold{}); err != nil || len(saved()) != 0 {
		t.Fatalf("clearing the lines: %v, %v", err, saved())
	}
}
