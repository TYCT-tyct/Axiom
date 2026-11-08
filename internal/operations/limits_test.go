package operations_test

import (
	"errors"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
)

func (db opsDB) setLimits(t *testing.T, l operations.AgentLimits, rev int64) (operations.AgentLimits, error) {
	t.Helper()
	var set operations.AgentLimits
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		set, err = operations.SetAgentLimits(t.Context(), tx, owner, l, rev, "fewer pages")
		return err
	})
	return set, err
}

// ceilings are the limits before the owner sets any, which the code allows at most.
var ceilings = operations.AgentLimits{
	RequestsPerMinute: operations.MaxRequestsPerMinute, MoneyWritesPerMinute: operations.MaxMoneyWritesPerMinute,
	MoneyWritesPerHour: operations.MaxMoneyWritesPerHour, AlertsPerHour: operations.MaxAlertsPerHour,
}

// with is the ceilings with change applied.
func with(change func(*operations.AgentLimits)) operations.AgentLimits {
	l := ceilings
	change(&l)
	return l
}

func (db opsDB) limits(t *testing.T) operations.AgentLimits {
	t.Helper()
	var l operations.AgentLimits
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		l, err = operations.ReadAgentLimits(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return l
}

// TestAgentLimits: until the owner sets them the ceilings apply; the owner lowers them from the revision read, each
// within 1 and its ceiling, and every change is audited.
func TestAgentLimits(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if l := db.limits(t); l != ceilings {
		t.Fatalf("before any set: %+v", l)
	}
	for _, step := range []struct {
		name      string
		limits    operations.AgentLimits
		rev, made int64 // made is the revision a step that succeeds leaves
		err       error
	}{
		{"no requests", with(func(l *operations.AgentLimits) { l.RequestsPerMinute = 0 }), 0, 0, operations.ErrBadLimit},
		{"requests past the ceiling", with(func(l *operations.AgentLimits) { l.RequestsPerMinute++ }), 0, 0, operations.ErrBadLimit},
		{"no money writes a minute", with(func(l *operations.AgentLimits) { l.MoneyWritesPerMinute = 0 }), 0, 0, operations.ErrBadLimit},
		{"money writes a minute past the ceiling", with(func(l *operations.AgentLimits) { l.MoneyWritesPerMinute++ }), 0, 0, operations.ErrBadLimit},
		{"no money writes an hour", with(func(l *operations.AgentLimits) { l.MoneyWritesPerHour = 0 }), 0, 0, operations.ErrBadLimit},
		{"money writes an hour past the ceiling", with(func(l *operations.AgentLimits) { l.MoneyWritesPerHour++ }), 0, 0, operations.ErrBadLimit},
		{"no alerts", with(func(l *operations.AgentLimits) { l.AlertsPerHour = 0 }), 0, 0, operations.ErrBadLimit},
		{"alerts past the ceiling", with(func(l *operations.AgentLimits) { l.AlertsPerHour++ }), 0, 0, operations.ErrBadLimit},
		{"the first set", operations.AgentLimits{RequestsPerMinute: 100, MoneyWritesPerMinute: 5, MoneyWritesPerHour: 50, AlertsPerHour: 10}, 0, 1, nil},
		{"from a stale revision", with(func(l *operations.AgentLimits) { l.AlertsPerHour = 20 }), 0, 0, operations.ErrLimitsChanged},
		{"the second set", operations.AgentLimits{RequestsPerMinute: 1, MoneyWritesPerMinute: 1, MoneyWritesPerHour: 1, AlertsPerHour: 1}, 1, 2, nil},
	} {
		if set, err := db.setLimits(t, step.limits, step.rev); !errors.Is(err, step.err) || set.Rev != step.made {
			t.Errorf("%s: %+v %v, want revision %d and %v", step.name, set, err, step.made, step.err)
		}
	}
	if l := db.limits(t); l.RequestsPerMinute != 1 || l.MoneyWritesPerMinute != 1 || l.MoneyWritesPerHour != 1 || l.AlertsPerHour != 1 || l.Rev != 2 || l.Updated == nil {
		t.Errorf("read back: %+v", l)
	}
	audits := `SELECT string_agg(concat_ws('>', detail->'before', detail->'after'), ' ' ORDER BY id)
		FROM platform.audit_log WHERE action = 'agent_limits.set'`
	want := `{"rev": 0, "alerts_per_hour": 60, "requests_per_minute": 600, "money_writes_per_hour": 300, "money_writes_per_minute": 30}>` +
		`{"rev": 1, "alerts_per_hour": 10, "requests_per_minute": 100, "money_writes_per_hour": 50, "money_writes_per_minute": 5} ` +
		`{"rev": 1, "alerts_per_hour": 10, "requests_per_minute": 100, "money_writes_per_hour": 50, "money_writes_per_minute": 5}>` +
		`{"rev": 2, "alerts_per_hour": 1, "requests_per_minute": 1, "money_writes_per_hour": 1, "money_writes_per_minute": 1}`
	if got := db.text(t, audits); got != want {
		t.Errorf("the audit:\n%s\nwant\n%s", got, want)
	}
}

// TestAgentLimitsOfAnOldNode: a row an old node made holds only the alerts' limit, and the others read as their
// ceilings; the old node's change of it leaves the owner's other limits as they are.
func TestAgentLimitsOfAnOldNode(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	oldNode := func(statement string) { // what the old node's queries write, which knows only the alerts' limit
		t.Helper()
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { _, err := tx.Exec(t.Context(), statement); return err }); err != nil {
			t.Fatal(err)
		}
	}
	oldNode(`INSERT INTO operations.agent_limits (alerts_per_hour, rev) VALUES (10, 1)`)
	got := db.limits(t)
	if got.Rev, got.Updated = 0, nil; got != with(func(l *operations.AgentLimits) { l.AlertsPerHour = 10 }) {
		t.Errorf("the old node's row: %+v", got)
	}
	if _, err := db.setLimits(t, operations.AgentLimits{RequestsPerMinute: 100, MoneyWritesPerMinute: 5, MoneyWritesPerHour: 50, AlertsPerHour: 10}, 1); err != nil {
		t.Fatal(err)
	}
	oldNode(`UPDATE operations.agent_limits SET alerts_per_hour = 20, rev = rev + 1, updated_at = now()`)
	if l := db.limits(t); l.RequestsPerMinute != 100 || l.MoneyWritesPerMinute != 5 || l.MoneyWritesPerHour != 50 || l.AlertsPerHour != 20 || l.Rev != 3 {
		t.Errorf("after the old node's change: %+v", l)
	}
}

// TestAgentLimitsFirstSetRace: two first sets from revision 0, the second waiting on the first's row, leave the first
// one's limit, and the second is told the limits changed rather than failing.
func TestAgentLimitsFirstSetRace(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.whileWaited(t, func(tx pg.Tx) error {
		_, err := operations.SetAgentLimits(t.Context(), tx, owner, with(func(l *operations.AgentLimits) { l.AlertsPerHour = 10 }), 0, "first")
		return err
	}, func() error {
		if _, err := db.setLimits(t, with(func(l *operations.AgentLimits) { l.AlertsPerHour = 20 }), 0); !errors.Is(err, operations.ErrLimitsChanged) {
			return fmt.Errorf("the second first set: %w", err)
		}
		return nil
	}, nil)
	if l := db.limits(t); l.AlertsPerHour != 10 || l.Rev != 1 {
		t.Errorf("after the race: %+v", l)
	}
}

// TestAgentAlertSignals: a raise and a resolution are signals under the operation's ID, keyed by the credential and the
// agent's key, so another credential's alert under the same words is another incident.
func TestAgentAlertSignals(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	first, other := uuid.NewV7(), uuid.NewV7()
	op := func(credential uuid.UUID) operations.Operation {
		return operations.Operation{ID: uuid.NewV7(), Credential: credential, Target: operations.Target{Kind: "alert", ID: "supplier/acme/balance"}}
	}
	raised, resolved := op(first), op(other)
	if got, want := operations.AlertKey(raised), first.String()+"/supplier/acme/balance"; got != want {
		t.Errorf("the key: %s, want %s", got, want)
	}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := operations.RaiseAlert(t.Context(), tx, raised, "warning", "low"); err != nil {
			return err
		}
		return operations.ResolveAlert(t.Context(), tx, resolved)
	}); err != nil {
		t.Fatal(err)
	}
	signals := `SELECT string_agg(id::text || '|' || (body->>'key') || '|' || (body->>'severity') || '|' ||
		coalesce(body->'fields'->>'operation', '-') || '|' || coalesce(body->>'resolved', 'false'), ',' ORDER BY seq)
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.agent_alert'`
	want := raised.ID.String() + "|" + operations.AlertKey(raised) + "|warning|" + raised.ID.String() + "|false," +
		resolved.ID.String() + "|" + operations.AlertKey(resolved) + "|info|-|true"
	if got := db.text(t, signals); got != want {
		t.Errorf("the signals:\n%s\nwant\n%s", got, want)
	}
}
