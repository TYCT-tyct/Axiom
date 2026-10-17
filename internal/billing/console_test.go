package billing

import (
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
