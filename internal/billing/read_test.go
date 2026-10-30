package billing

import (
	"fmt"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// seedKeyDays puts a row of the key's counters on every edge of the UTC day, week and month, each spending a different
// power of ten so that a sum says which rows it holds, and reserving 7. It returns the days, which are distinct rows
// (the edges of the windows coincide on some dates).
func seedKeyDays(b *bank, today, monday, first time.Time) (days []time.Time, spent map[time.Time]int64) {
	edges := []time.Time{today, monday, monday.AddDate(0, 0, -1), first, first.AddDate(0, 0, -1), today.AddDate(0, -13, 0)}
	days = slices.Compact(slices.SortedFunc(slices.Values(edges), time.Time.Compare))
	spent = map[time.Time]int64{}
	a, power := b.admitted, int64(1)
	for _, d := range days {
		spent[d], power = power, power*10
		b.sql(fmt.Sprintf("INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent, reserved) VALUES ('%s', '%s', '%s', '%s', %d, 7)",
			a.Key, d.Format(time.RFC3339), a.Site, a.Account, spent[d]))
	}
	// The workspace's counters, which the key's usage must not take up.
	b.sql(fmt.Sprintf("INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent) VALUES ('%s', '%s', '%s', '%s', 999999)",
		a.Workspace, today.Format(time.RFC3339), a.Site, a.Account))
	return days, spent
}

func (b *bank) keyUsage(scope pg.Scope, reset Reset) (u KeyUsage) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), scope, func(tx pg.Tx) (err error) {
		u, err = ReadKeyUsage(b.t.Context(), tx, b.admitted.Key, reset)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return u
}

// TestReadKeyUsage: usage is summed over the current UTC day, week and month and over all time, from spent alone, and the
// limit's count is spent and reserved in the window of the key's reset. Days are placed at the edges of the windows, so
// a window that starts a day early or late changes a sum.
func TestReadKeyUsage(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	today := time.Now().UTC().Truncate(24 * time.Hour)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	first := today.AddDate(0, 0, 1-today.Day())
	days, spent := seedKeyDays(b, today, monday, first)
	// since is the start of each window; the zero time is all of them.
	since := map[Reset]time.Time{ResetDaily: today, ResetWeekly: monday, ResetMonthly: first, ResetNever: {}}
	spentSince := func(reset Reset) (spentSum, reservedSum int64) {
		for _, d := range days {
			if !d.Before(since[reset]) {
				spentSum, reservedSum = spentSum+spent[d], reservedSum+7
			}
		}
		return spentSum, reservedSum
	}
	for reset := range since {
		windowSpent, windowReserved := spentSince(reset)
		if got := b.keyUsage(b.admitted.Scope, reset).Counted; got != windowSpent+windowReserved {
			t.Errorf("%q: counted %d, want %d spent and %d reserved", reset, got, windowSpent, windowReserved)
		}
	}
	got := b.keyUsage(b.admitted.Scope, ResetDaily)
	day, _ := spentSince(ResetDaily)
	week, _ := spentSince(ResetWeekly)
	month, _ := spentSince(ResetMonthly)
	total, _ := spentSince(ResetNever)
	if got.Total != total || got.Day != day || got.Week != week || got.Month != month {
		t.Errorf("usage %+v, want total %d, day %d, week %d, month %d", got, total, day, week, month)
	}
	if got := b.keyUsage(pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), ResetNever); got != (KeyUsage{}) {
		t.Errorf("another account reads %+v of the key", got)
	}
}

// TestReadFunds: the balance is the balance row's, and what was spent is summed over the workspaces given only, so the
// key's own counter of the same charges is not added; an account without a row has nothing; another account's row is
// not visible.
func TestReadFunds(t *testing.T) {
	t.Parallel()

	b := newBank(t, 700, -50, Config{})
	a := b.admitted
	old := uuid.NewV7()
	for subject, spent := range map[uuid.UUID]int64{a.Workspace: 200, old: 50, a.Key: 250} {
		b.sql(fmt.Sprintf("INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent) VALUES ('%s', now(), '%s', '%s', %d)",
			subject, a.Site, a.Account, spent))
	}
	read := func(scope pg.Scope, account uuid.UUID, workspaces ...uuid.UUID) (f Funds) {
		t.Helper()
		if err := b.app.Tx(t.Context(), scope, func(tx pg.Tx) (err error) {
			f, err = ReadFunds(t.Context(), tx, account, workspaces)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := read(a.Scope, a.Account, a.Workspace, old); f != (Funds{Balance: 700, Spent: 250}) {
		t.Errorf("funds %+v, want a balance of 700 and 250 spent in the two workspaces", f)
	}
	if f := read(a.Scope, a.Account); f != (Funds{Balance: 700}) {
		t.Errorf("no workspaces: %+v", f)
	}
	other := uuid.NewV7()
	if f := read(pg.NewScope(false, nil, []uuid.UUID{other}), other, a.Workspace); f != (Funds{}) {
		t.Errorf("an account with no row: %+v", f)
	}
	if f := read(pg.NewScope(false, nil, []uuid.UUID{other}), a.Account, a.Workspace); f != (Funds{}) {
		t.Errorf("another account's row is read: %+v", f)
	}
}
