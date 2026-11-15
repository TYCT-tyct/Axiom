package billing

import (
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestKeyDailySpendWindow(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	a := b.admitted
	b.sql(fmt.Sprintf(`INSERT INTO billing.spend(subject,window_start,site_id,account_id,spent,reserved)
 SELECT '%s', date_trunc('day',clock_timestamp(),'UTC') + i * interval '24 hours', '%s','%s', i+15, 999
 FROM generate_series(-14,1) i`, a.Key, a.Site, a.Account))
	ids := make([]uuid.UUID, 50)
	ids[0] = a.Key
	for i := 1; i < len(ids); i++ {
		ids[i] = uuid.NewV7()
	}
	if err := b.app.ReadTx(t.Context(), a.Scope, func(tx pg.Tx) error {
		got, err := KeyDailySpend(t.Context(), tx, ids)
		if err != nil {
			return err
		}
		if len(got) != 1 || len(got[a.Key]) != 14 {
			t.Fatalf("window: %v", got)
		}
		checkDailySpend(t, got[a.Key])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func checkDailySpend(t *testing.T, days []DailySpend) {
	t.Helper()
	for i, d := range days {
		if d.Spent != int64(i+2) || !d.Day.Equal(days[0].Day.AddDate(0, 0, i)) {
			t.Fatalf("day %d: %+v", i, d)
		}
	}
}

func TestKeyDailySpendEmptyAndScope(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	a := b.admitted
	reservedOnly := uuid.NewV7()
	b.sql(fmt.Sprintf(`INSERT INTO billing.spend(subject,window_start,site_id,account_id,spent,reserved)
 VALUES ('%s',date_trunc('day',clock_timestamp(),'UTC'),'%s','%s',17,999),
 ('%s',date_trunc('day',clock_timestamp(),'UTC'),'%s','%s',0,999)`, a.Key, a.Site, a.Account, reservedOnly, a.Site, a.Account))
	cases := []struct {
		name  string
		scope pg.Scope
		keys  []uuid.UUID
	}{
		{"empty", a.Scope, nil},
		{"unused", a.Scope, []uuid.UUID{uuid.NewV7()}},
		{"reserved only", a.Scope, []uuid.UUID{reservedOnly}},
		{"other account", pg.AccountScope(uuid.NewV7()), []uuid.UUID{a.Key}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.app.ReadTx(t.Context(), tc.scope, func(tx pg.Tx) error {
				got, err := KeyDailySpend(t.Context(), tx, tc.keys)
				if len(got) != 0 {
					t.Fatalf("unexpected spend: %v", got)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
