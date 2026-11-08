package operations_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// spend gives site a policy and its top-ups today a reservation of 5 and a commitment of 3 on supplier s1, and a released proposal of
// 12, so each counter holds 5+3 in three proposals.
func (db opsDB) spend(t *testing.T, site uuid.UUID) {
	t.Helper()
	r := reservations{opsDB: db, t: t, credential: uuid.NewV7()}
	p := limits(&site)
	p.DailyCount = 100 // only a pause sends a small proposal to the owner
	if _, err := db.setPolicy(t, p, 0); err != nil {
		t.Fatal(err)
	}
	direct := func(amount int64) operations.Operation {
		op, _, err := db.proposeMoney(t, topUp(r.credential, uuid.NewV7(), &site, "s1", amount))
		if err == nil {
			op, err = db.in(t, func(tx pg.Tx) (operations.Operation, error) {
				return operations.Settle(t.Context(), tx, op, map[string]any{}, "")
			})
		}
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	direct(5)
	if _, err := r.confirm(direct(3)); err != nil {
		t.Fatal(err)
	}
	refused, _, err := db.proposeMoney(t, topUp(r.credential, uuid.NewV7(), &site, "s1", 12))
	if err == nil {
		_, err = db.decide(t, refused, false)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// siteCounters is a site's counters of today and this week as reserved+committed#count, the object's days by object.
func (db opsDB) siteCounters(t *testing.T, site uuid.UUID) string {
	t.Helper()
	return db.text(t, `SELECT coalesce(string_agg(object_id || ':' || reserved || '+' || committed || '#' || count, ' '
		ORDER BY object_kind, window_kind, object_id), '') FROM operations.cap_counters WHERE site_id = '`+site.String()+`'`)
}

func (db opsDB) paused(t *testing.T, site uuid.UUID) string {
	t.Helper()
	return db.text(t, `SELECT (paused_at IS NOT NULL)::text FROM operations.cap_policies WHERE site_id = '`+site.String()+`'`)
}

func (db opsDB) reconcile(t *testing.T) int {
	t.Helper()
	n, err := operations.ReconcileCaps(t.Context(), db.DB, platformScope)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (db opsDB) repair(t *testing.T, site *uuid.UUID) (operations.CapPolicy, error) {
	t.Helper()
	var p operations.CapPolicy
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		p, err = operations.RepairCaps(t.Context(), tx, owner, operations.CategoryTopUp, site, "recount")
		return err
	})
	return p, err
}

// spent is what spend leaves in the category's day, its week and supplier s1's day.
const spent = ":5+3#3 :5+3#3 s1:5+3#3"

// drifts are the ways a site's counters may differ from its money, each a statement on the rows %s selects, and the
// counters its repair leaves.
var drifts = []struct {
	name, drift, repaired string
}{
	{"the day's reservation", `UPDATE operations.cap_counters SET reserved = reserved + 1 WHERE %s AND window_kind = 'day' AND object_kind = ''`, spent},
	{"the week's commitment", `UPDATE operations.cap_counters SET committed = 0 WHERE %s AND window_kind = 'week'`, spent},
	{"the object's count", `UPDATE operations.cap_counters SET count = 1 WHERE %s AND object_kind = 'supplier'`, spent},
	{"a counter lost", `DELETE FROM operations.cap_counters WHERE %s AND object_kind = 'supplier'`, spent},
	{"a counter with no money", `INSERT INTO operations.cap_counters (category, window_kind, window_start, object_kind, object_id, site_id, committed)
		SELECT category, 'day', window_start, 'supplier', 's9', site_id, 4 FROM operations.cap_counters WHERE %s AND window_kind = 'day' AND object_kind = ''`, spent + " s9:0+0#0"},
}

// drifted spends in a healthy site and in one site per drift, whose counters it then makes drift.
func (db opsDB) drifted(t *testing.T) (healthy uuid.UUID, sites []uuid.UUID) {
	t.Helper()
	healthy = uuid.NewV7()
	db.spend(t, healthy)
	for _, d := range drifts {
		site := uuid.NewV7()
		db.spend(t, site)
		if err := pgtest.Run(t.Context(), db.superuser, fmt.Sprintf(d.drift, "site_id = '"+site.String()+"'")); err != nil {
			t.Fatalf("%s: %v", d.name, err)
		}
		sites = append(sites, site)
	}
	return healthy, sites
}

// expect checks that query reads want.
func (db opsDB) expect(t *testing.T, what, query, want string) {
	t.Helper()
	if got := db.text(t, query); got != want {
		t.Errorf("%s: %s, want %s", what, got, want)
	}
}

// TestCapReconcile: counters that differ from what their money actions add up to, in any window and any column or by a
// row lost or a row with no money behind it, pause their scope alone, recorded and alerted once, even beside a scope
// that cannot be paused; a paused scope sends a proposal within every limit to the owner.
func TestCapReconcile(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	healthy, sites := db.drifted(t)
	orphan := uuid.NewV7() // counters with no policy, which no path makes
	if err := pgtest.Run(t.Context(), db.superuser, `INSERT INTO operations.cap_counters (category, window_kind, window_start, site_id, reserved)
		VALUES ('money.top_up', 'day', date_trunc('day', now(), 'UTC'), '`+orphan.String()+`', 1)`); err != nil {
		t.Fatal(err)
	}
	if n, err := operations.ReconcileCaps(t.Context(), db.DB, platformScope); n != len(drifts) || err == nil {
		t.Fatalf("paused %d scopes beside one without a policy (%v), want %d and its failure", n, err, len(drifts))
	}
	if err := pgtest.Run(t.Context(), db.superuser, `DELETE FROM operations.cap_counters WHERE site_id = '`+orphan.String()+`'`); err != nil {
		t.Fatal(err)
	}
	if n := db.reconcile(t); n != 0 {
		t.Errorf("a paused scope paused again: %d", n)
	}
	db.expect(t, "the scope that did not drift is paused", `SELECT (paused_at IS NOT NULL)::text FROM operations.cap_policies WHERE site_id = '`+healthy.String()+`'`, "false")
	db.expect(t, "the pauses recorded", `SELECT count(*) || ' ' || string_agg(DISTINCT actor_kind || ':' || actor_id, ',') FROM platform.audit_log WHERE action = 'cap_policy.pause'`, "5 system:operations.cap_reconcile")
	db.expect(t, "the pauses alerted", `SELECT count(*)::text FROM platform.alert_signals WHERE body->>'kind' = 'operations.cap_drift' AND body->>'severity' = 'critical'`, "5")

	waits, _, err := db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), &sites[0], "s2", 1))
	if err != nil || waits.State != operations.StatePending || !slices.Equal(waits.Approval.Reasons, []operations.Reason{operations.ReasonPaused}) {
		t.Fatalf("a proposal within every limit in a paused scope: %+v %v", waits, err)
	}
	db.expect(t, "the owner is told the scope is paused", `SELECT summary->'cap'->>'paused' FROM operations.approvals WHERE id = '`+waits.Approval.ID.String()+`'`, "true")
}

// TestCapRepair: the owner's repair sets each counter to what its money adds up to, a lost row back and a row with no
// money to zero, resumes the scope and resolves its alert, recorded with the reason; a scope without a policy has
// nothing to repair, and one that did not drift is left as it is.
func TestCapRepair(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	healthy, sites := db.drifted(t)
	if n := db.reconcile(t); n != len(drifts) {
		t.Fatalf("paused %d scopes, want %d", n, len(drifts))
	}
	for i, d := range drifts {
		if p, err := db.repair(t, &sites[i]); err != nil || p.Paused != nil {
			t.Fatalf("%s: the repair: %+v %v", d.name, p, err)
		}
		if got := db.siteCounters(t, sites[i]) + " paused " + db.paused(t, sites[i]); got != d.repaired+" paused false" {
			t.Errorf("%s: repaired to %s", d.name, got)
		}
	}
	if n := db.reconcile(t); n != 0 {
		t.Errorf("paused again after the repair: %d", n)
	}
	db.expect(t, "the repairs recorded", `SELECT count(*) || ' ' || string_agg(DISTINCT actor_kind, ',') || ' ' || bool_and(reason = 'recount')
		FROM platform.audit_log WHERE action = 'cap_counters.repair'`, "5 user true")
	db.expect(t, "the repairs resolved", `SELECT count(*)::text FROM platform.alert_signals WHERE body->>'kind' = 'operations.cap_drift' AND (body->>'resolved')::bool`, "5")
	if direct, _, err := db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), &sites[1], "s2", 1)); err != nil || direct.State != operations.StateApproved {
		t.Errorf("a proposal within every limit after the repair: %+v %v", direct, err)
	}
	if _, err := db.repair(t, new(uuid.NewV7())); !errors.Is(err, operations.ErrNoCapPolicy) {
		t.Errorf("the repair of a scope without a policy: %v", err)
	}
	if p, err := db.repair(t, &healthy); err != nil || db.siteCounters(t, healthy) != spent {
		t.Errorf("the repair of a scope that did not drift: %+v %v %s", p, err, db.siteCounters(t, healthy))
	}
}

// TestCapReconcileRaces: a proposal still in its transaction is no drift, as the reconciliation reads the counters and
// the money in one snapshot; and a repair waits for a proposal that holds the scope, then counts its money, on an
// object that had no counter when the repair began, instead of writing over it; and a reconciliation that found drift
// the owner then repaired checks again under the policy's lock and leaves the scope alone.
func TestCapReconcileRaces(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site := uuid.NewV7()
	db.spend(t, site)
	inFlight := topUp(uuid.NewV7(), uuid.NewV7(), &site, "s1", 1)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, _, err := operations.ProposeMoney(t.Context(), tx, inFlight.p, inFlight.m)
		if n := db.reconcile(t); n != 0 {
			t.Errorf("a proposal in its transaction was taken for drift: %d", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE operations.cap_counters SET reserved = 0 WHERE site_id = '`+site.String()+`'`); err != nil {
		t.Fatal(err)
	}
	var repaired error
	held := topUp(uuid.NewV7(), uuid.NewV7(), &site, "s2", 2)
	db.whileWaited(t, func(tx pg.Tx) error {
		_, _, err := operations.ProposeMoney(t.Context(), tx, held.p, held.m)
		return err
	}, func() (err error) {
		_, repaired = db.repair(t, &site)
		return nil
	}, nil)
	if repaired != nil {
		t.Fatal(repaired)
	}
	if got := db.siteCounters(t, site); got != ":8+3#5 :8+3#5 s1:6+3#4 s2:2+0#1" {
		t.Errorf("the counters after a repair that waited for a proposal: %s", got)
	}
	if n := db.reconcile(t); n != 0 {
		t.Errorf("the repair left drift: %d", n)
	}

	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE operations.cap_counters SET count = 0 WHERE site_id = '`+site.String()+`'`); err != nil {
		t.Fatal(err)
	}
	var paused int
	db.whileWaited(t, func(tx pg.Tx) error {
		_, err := operations.RepairCaps(t.Context(), tx, owner, operations.CategoryTopUp, &site, "recount")
		return err
	}, func() (err error) {
		paused, err = operations.ReconcileCaps(t.Context(), db.DB, platformScope)
		return err
	}, nil)
	if paused != 0 || db.paused(t, site) != "false" {
		t.Errorf("the reconciliation paused a scope the owner repaired while it ran: %d", paused)
	}
}

// TestCapDriftAuditParts: drift in more counters than one audit entry's 8 KiB detail holds is recorded in as many
// numbered entries as it needs, every counter in one of them, so the pause, its alert and the repair still commit.
func TestCapDriftAuditParts(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site := uuid.NewV7()
	db.spend(t, site)
	if err := pgtest.Run(t.Context(), db.superuser, `INSERT INTO operations.cap_counters (category, window_kind, window_start, object_kind, object_id, site_id, committed)
		SELECT 'money.top_up', 'day', date_trunc('day', now(), 'UTC'), 'supplier', n || repeat('x', 200), '`+site.String()+`', 1
		FROM generate_series(1, 40) n`); err != nil {
		t.Fatal(err)
	}
	if n := db.reconcile(t); n != 1 {
		t.Fatalf("paused %d scopes", n)
	}
	if _, err := db.repair(t, &site); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"cap_policy.pause", "cap_counters.repair"} {
		db.expect(t, action, `SELECT (count(*) > 1) || ' ' || sum(jsonb_array_length(detail->'counters')) || ' ' || bool_and((detail->>'parts')::int = count)
			|| ' ' || max(char_length(detail->'counters'->0->>'object_id'))
			FROM (SELECT *, count(*) OVER () AS count FROM platform.audit_log WHERE action = '`+action+`') a`, "true 40 true 128")
	}
	db.expect(t, "the alert", `SELECT count(*)::text FROM platform.alert_signals WHERE body->>'kind' = 'operations.cap_drift'`, "2")
}

// TestCapRepairLockOrder: a confirmation or a release whose object's counter is missing waits for a repair that holds
// the scope at the policy, before it makes the missing row, instead of making it and then waiting for the repair's
// counters while the repair waits to write that row back. The order is made by holding the repair's locks until the
// other is seen waiting, never by time.
func TestCapRepairLockOrder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// reserve makes a reservation on supplier s1 and returns what ends it.
		reserve func(r reservations) func() error
	}{
		{"a confirmation", func(r reservations) func() error {
			op := r.direct(5)
			return func() error { _, err := r.confirm(op); return err }
		}},
		{"a withdrawal", func(r reservations) func() error {
			op := r.pending()
			return func() error {
				_, err := r.in(r.t, func(tx pg.Tx) (operations.Operation, error) {
					return operations.Withdraw(r.t.Context(), tx, r.credential, op.Approval.ID, "no")
				})
				return err
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReservations(t)
			end := c.reserve(r)
			if err := pgtest.Run(t.Context(), r.superuser, `DELETE FROM operations.cap_counters WHERE object_kind = 'supplier'`); err != nil {
				t.Fatal(err)
			}
			var repair pg.Tx
			r.whileWaited(t, func(tx pg.Tx) error {
				repair = tx // the repair's first locks: the policy, then the counters that exist
				if _, err := tx.Exec(t.Context(), `SELECT 1 FROM operations.cap_policies WHERE site_id IS NULL FOR UPDATE`); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `SELECT id FROM operations.cap_counters WHERE site_id IS NULL ORDER BY object_kind, window_kind FOR UPDATE`)
				return err
			}, end, func() {
				if _, err := operations.RepairCaps(t.Context(), repair, owner, operations.CategoryTopUp, nil, "recount"); err != nil {
					t.Errorf("the repair beside %s: %v", c.name, err)
				}
			})
			if n := r.reconcile(t); n != 0 {
				t.Errorf("the counters drift after the repair and %s: %d", c.name, n)
			}
		})
	}
}

// reserveAsOldNode proposes pm and reserves it as ProposeMoney did before the pause: within the caps at once.
func (db opsDB) reserveAsOldNode(t *testing.T, pm proposal) error {
	t.Helper()
	_, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		op, _, err := operations.Propose(t.Context(), tx, pm.p)
		if err != nil {
			return op, err
		}
		w, err := operations.New().CapWindows(t.Context(), tx)
		if err != nil {
			return op, err
		}
		return op, operations.New().InsertMoneyAction(t.Context(), tx, operations.InsertMoneyActionParams{
			OperationID: op.ID, Category: string(pm.m.Category), SiteID: pm.m.Site, ObjectKind: pm.m.Object.Kind,
			ObjectID: pm.m.Object.ID, Amount: pm.m.Amount, Currency: pm.m.Currency.String(), Basis: []byte(`{}`), PolicyRev: 1,
			DayStart: w.DayStart, WeekStart: w.WeekStart, ExpiresAt: w.DayEnd,
		})
	})
	return err
}

// TestCapPauseHoldsOldNodes: a node that predates the pause, or this one rolled back to, reserves a proposal within
// the caps without asking the owner; the database refuses that reservation in a paused scope, and keeps reserving one
// that waits for the owner there, and any in a scope that is not paused.
func TestCapPauseHoldsOldNodes(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	paused, running := uuid.NewV7(), uuid.NewV7()
	for _, site := range []uuid.UUID{paused, running} {
		db.spend(t, site)
	}
	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE operations.cap_policies SET paused_at = now() WHERE site_id = '`+paused.String()+`'`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		site  uuid.UUID
		owner bool // the proposal waits for the owner
		fails bool
	}{
		{"an old node's proposal in a paused scope", paused, false, true},
		{"a proposal for the owner in a paused scope", paused, true, false},
		{"an old node's proposal in a scope not paused", running, false, false},
	} {
		pm := topUp(uuid.NewV7(), uuid.NewV7(), &c.site, "s1", 1)
		if c.owner {
			pm.p.Summary = pm.m.Summary
		}
		err := db.reserveAsOldNode(t, pm)
		if failed := err != nil && strings.Contains(err.Error(), "paused"); failed != c.fails || (err != nil && !failed) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
