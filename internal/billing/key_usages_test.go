package billing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestReadKeyUsagesMatchesSingleKey(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedKeyDays(b, today, today.AddDate(0, 0, -(int(today.Weekday())+6)%7), today.AddDate(0, 0, 1-today.Day()))
	idle := uuid.NewV7()
	for _, scope := range []pg.Scope{b.admitted.Scope, pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})} {
		for _, reset := range []Reset{ResetNever, ResetDaily, ResetWeekly, ResetMonthly} {
			windows := []KeyWindow{{b.admitted.Key, reset}, {idle, reset}}
			var batch map[uuid.UUID]KeyUsage
			err := b.app.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) {
				batch, err = ReadKeyUsages(t.Context(), tx, windows)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) != len(windows) || batch[b.admitted.Key] != b.keyUsage(scope, reset) || batch[idle] != (KeyUsage{}) {
				t.Fatalf("reset %q: batch differs from single-key usage: %+v", reset, batch)
			}
		}
	}
	if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		batch, err := ReadKeyUsages(t.Context(), tx, nil)
		if len(batch) != 0 {
			t.Error("empty input returned counters", batch)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
