package operations_test

import (
	"errors"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
)

func (db opsDB) setLimits(t *testing.T, alerts int, rev int64) (operations.AgentLimits, error) {
	t.Helper()
	var set operations.AgentLimits
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		set, err = operations.SetAgentLimits(t.Context(), tx, owner, operations.AgentLimits{AlertsPerHour: alerts}, rev, "fewer pages")
		return err
	})
	return set, err
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

// TestAgentLimits: until the owner sets them the ceiling applies; the owner lowers it from the revision read, within 1
// and the ceiling, and every change is audited.
func TestAgentLimits(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if l := db.limits(t); l.AlertsPerHour != operations.MaxAlertsPerHour || l.Rev != 0 || l.Updated != nil {
		t.Fatalf("before any set: %+v", l)
	}
	for _, step := range []struct {
		alerts    int
		rev, made int64 // made is the revision a step that succeeds leaves
		err       error
	}{
		{0, 0, 0, operations.ErrBadLimit},
		{operations.MaxAlertsPerHour + 1, 0, 0, operations.ErrBadLimit},
		{10, 0, 1, nil},
		{20, 0, 0, operations.ErrLimitsChanged},
		{1, 1, 2, nil},
	} {
		if set, err := db.setLimits(t, step.alerts, step.rev); !errors.Is(err, step.err) || set.Rev != step.made {
			t.Errorf("%d alerts an hour from revision %d: %+v %v, want revision %d and %v", step.alerts, step.rev, set, err, step.made, step.err)
		}
	}
	if l := db.limits(t); l.AlertsPerHour != 1 || l.Rev != 2 || l.Updated == nil {
		t.Errorf("read back: %+v", l)
	}
	audits := `SELECT string_agg((detail->'before'->>'alerts_per_hour') || '>' || (detail->'after'->>'alerts_per_hour'), ' ' ORDER BY id)
		FROM platform.audit_log WHERE action = 'agent_limits.set'`
	if got := db.text(t, audits); got != "60>10 10>1" {
		t.Errorf("the audit: %s", got)
	}
}

// TestAgentLimitsFirstSetRace: two first sets from revision 0, the second waiting on the first's row, leave the first
// one's limit, and the second is told the limits changed rather than failing.
func TestAgentLimitsFirstSetRace(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.whileWaited(t, func(tx pg.Tx) error {
		_, err := operations.SetAgentLimits(t.Context(), tx, owner, operations.AgentLimits{AlertsPerHour: 10}, 0, "first")
		return err
	}, func() error {
		if _, err := db.setLimits(t, 20, 0); !errors.Is(err, operations.ErrLimitsChanged) {
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
