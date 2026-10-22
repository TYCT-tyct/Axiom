package billing

import (
	"math"
	"strconv"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

// readBalance reads the account's money as the console does, in the account's own scope.
func (b *bank) readBalance(workspaces ...uuid.UUID) BalanceView {
	b.t.Helper()
	var view BalanceView
	if err := b.app.ReadTx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		view, err = ReadBalance(b.t.Context(), tx, b.admitted.Account, workspaces)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return view
}

// TestReadBalance: the balance, what reservations hold and the floor are the balance row's; the month's spending is what
// the given workspaces were charged since the first of the month, once, and an account that never opened a balance has
// nothing.
func TestReadBalance(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, -50, Config{})
	if _, err := b.reserve(b.admitted, 300); err != nil {
		t.Fatal(err)
	}
	a := b.admitted
	spend := func(subject uuid.UUID, offset string, spent int) string {
		return "INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent) VALUES ('" + subject.String() +
			"', date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '" + offset + "', '" + a.Site.String() + "', '" + a.Account.String() + "', " + strconv.Itoa(spent) + ")"
	}
	other := uuid.NewV7()
	b.sql(
		spend(a.Workspace, "2 hours", 700), spend(a.Workspace, "-2 hours", 50), spend(other, "2 hours", 9), spend(a.Key, "2 hours", 700),
	)
	got := b.readBalance(a.Workspace)
	if got != (BalanceView{Balance: 1000, Reserved: 300, Floor: -50, SpentThisMonth: 700}) {
		t.Errorf("the account's money: %+v", got)
	}
	if got := b.readBalance(a.Workspace, other); got.SpentThisMonth != 709 {
		t.Errorf("with a second workspace: %d, want 709", got.SpentThisMonth)
	}
	if got := b.readBalance(); got.SpentThisMonth != 0 || got.Balance != 1000 {
		t.Errorf("with no workspaces: %+v", got)
	}
	stranger := &bank{t: t, app: b.app}
	stranger.admitted = Admitted{Scope: pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()}), Account: uuid.NewV7()}
	if got := stranger.readBalance(); got != (BalanceView{}) {
		t.Errorf("an account without a balance row: %+v", got)
	}
}

// TestKeysUsed: what a key has used is what its limit is checked against: spent and reserved, in the window of its own
// reset, so one key can be asked in several windows at once and a key nobody charged has used nothing.
func TestKeysUsed(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	a := b.admitted
	if _, err := b.reserve(a, 300); err != nil { // today, on the workspace and the key
		t.Fatal(err)
	}
	b.sql("INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent) VALUES ('" + a.Key.String() +
		"', date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 day', '" + a.Site.String() + "', '" + a.Account.String() + "', 200)")
	idle := uuid.NewV7()
	var used map[uuid.UUID]int64
	if err := b.app.ReadTx(t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		used, err = KeysUsed(t.Context(), tx, []KeyWindow{{a.Key, ResetDaily}, {idle, ResetDaily}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if used[a.Key] != 300 || used[idle] != 0 || len(used) != 2 {
		t.Errorf("daily windows: %v, want 300 for the charged key and 0 for the idle one", used)
	}
	if err := b.app.ReadTx(t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		used, err = KeysUsed(t.Context(), tx, []KeyWindow{{a.Key, ResetNever}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if used[a.Key] != 500 {
		t.Errorf("a limit that never resets counts every day: %v, want 500", used)
	}
}

func TestReadFrozenBalance(t *testing.T) {
	b := newBank(t, 0, 0, Config{})
	f := b.refundable(1000)
	if _, err := b.reserve(b.admitted, 300); err != nil {
		t.Fatal(err)
	}
	f.Amount = 700
	b.freeze(f)
	got := b.readBalance()
	if got.Balance != 1000 || got.Reserved != 300 || got.Frozen != 700 || got.Available().Sign() != 0 {
		t.Fatalf("frozen balance: %+v, available %s", got, got.Available())
	}
}

func TestAvailableBalanceExtremes(t *testing.T) {
	for _, tc := range []struct {
		view BalanceView
		want string
	}{
		{BalanceView{Balance: 100, Reserved: 20, Frozen: 30}, "50"},
		{BalanceView{Balance: math.MinInt64, Reserved: math.MaxInt64, Frozen: math.MaxInt64}, "-27670116110564327422"},
		{BalanceView{Balance: math.MaxInt64, Frozen: math.MaxInt64}, "0"},
	} {
		if got := tc.view.Available().String(); got != tc.want {
			t.Errorf("available %+v = %s, want %s", tc.view, got, tc.want)
		}
	}
}
