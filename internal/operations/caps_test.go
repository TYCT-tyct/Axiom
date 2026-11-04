package operations_test

import (
	"errors"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var usd, _ = platform.ParseCurrency("USD")

// limits is the policy every test sets unless it says otherwise: autonomous single 10, day 25, week 40, object day 15,
// 3 a day; hard single 20, day 50, week 60.
func limits(site *uuid.UUID) operations.CapPolicy {
	return operations.CapPolicy{
		Site: site, Category: operations.CategoryTopUp, Currency: usd, Single: 10, Daily: 25, Weekly: 40, ObjectDaily: 15,
		DailyCount: 3, HardSingle: 20, HardDaily: 50, HardWeekly: 60,
	}
}

func (db opsDB) setPolicy(t *testing.T, p operations.CapPolicy, rev int64) (operations.CapPolicy, error) {
	t.Helper()
	var set operations.CapPolicy
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		set, err = operations.SetCapPolicy(t.Context(), tx, owner, p, rev, "caps")
		return err
	})
	return set, err
}

// proposal is a money proposal and what it moves.
type proposal struct {
	p operations.Proposal
	m operations.Transfer
}

func topUp(credential, key uuid.UUID, site *uuid.UUID, supplier string, amount int64) proposal {
	p := operations.Proposal{
		Credential: credential, Key: key, Tool: "supplier_top_up_propose", Target: operations.Target{Kind: "supplier", ID: supplier},
		Params: map[string]any{"amount_native": fmt.Sprint(amount)}, Risk: operations.RiskTopUp, Reason: "low balance",
	}
	return proposal{p, operations.Transfer{
		Site: site, Category: operations.CategoryTopUp, Object: p.Target, Amount: amount, Currency: usd,
		Basis: map[string]any{"amount_native": amount}, Summary: map[string]any{"supplier": supplier},
	}}
}

func (db opsDB) proposeMoney(t *testing.T, pm proposal) (operations.Operation, bool, error) {
	t.Helper()
	var op operations.Operation
	var replayed bool
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		op, replayed, err = operations.ProposeMoney(t.Context(), tx, pm.p, pm.m)
		return err
	})
	return op, replayed, err
}

// seed puts used amounts in a site's counters for today: the category's day, its week, and supplier s1's day, with
// the day's count of proposals.
func (db opsDB) seed(t *testing.T, site uuid.UUID, day, week, object int64, count int) {
	t.Helper()
	if err := pgtest.Run(t.Context(), db.superuser, fmt.Sprintf(`INSERT INTO operations.cap_counters
		(category, window_kind, window_start, object_kind, object_id, site_id, committed, count) VALUES
		('money.top_up', 'day', date_trunc('day', now(), 'UTC'), '', '', '%[1]s', %[2]d, %[5]d),
		('money.top_up', 'week', date_trunc('week', now(), 'UTC'), '', '', '%[1]s', %[3]d, 0),
		('money.top_up', 'day', date_trunc('day', now(), 'UTC'), 'supplier', 's1', '%[1]s', %[4]d, 0)`,
		site, day, week, object, count)); err != nil {
		t.Fatal(err)
	}
}

// counters is a site's counters for today as day/week/object reserved+committed and the day's count.
func (db opsDB) counters(t *testing.T, site *uuid.UUID) string {
	t.Helper()
	where := "site_id IS NULL"
	if site != nil {
		where = "site_id = '" + site.String() + "'"
	}
	return db.text(t, `SELECT coalesce(string_agg(reserved || '+' || committed || '#' || count, ' ' ORDER BY object_kind, window_kind), '')
		FROM operations.cap_counters WHERE `+where)
}

// TestCapDimensions: each autonomous limit sends a proposal over it to the owner, each hard limit refuses one over it
// with nothing written, and a scope without a policy moves no money. Every case is its own site.
func TestCapDimensions(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	for _, c := range []struct {
		name                     string
		day, week, object, count int64
		amount                   int64
		want                     string
	}{
		{"within every limit", 0, 0, 0, 0, 5, "approved"},
		{"at every limit", 15, 30, 5, 2, 10, "approved"},
		{"over the single limit", 0, 0, 0, 0, 11, "pending_approval"},
		{"over the daily limit", 20, 20, 0, 0, 6, "pending_approval"},
		{"over the weekly limit", 0, 36, 0, 0, 5, "pending_approval"},
		{"over the object's daily limit", 12, 12, 12, 0, 4, "pending_approval"},
		{"over the daily count", 0, 0, 0, 3, 1, "pending_approval"},
		{"over the hard single limit", 0, 0, 0, 0, 21, "hard_single"},
		{"over the hard daily limit", 46, 46, 0, 0, 5, "hard_daily"},
		{"over the hard weekly limit", 0, 56, 0, 0, 5, "hard_weekly"},
	} {
		site := uuid.NewV7()
		if _, err := db.setPolicy(t, limits(&site), 0); err != nil {
			t.Fatal(err)
		}
		db.seed(t, site, c.day, c.week, c.object, int(c.count))
		op, _, err := db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), &site, "s1", c.amount))
		got := string(op.State)
		if exceeded := (*operations.CapExceeded)(nil); errors.As(err, &exceeded) {
			got = exceeded.Limit
		} else if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		want := fmt.Sprintf("%d+%d#%d %d+%d#1 %d+%d#1", c.amount, c.day, c.count+1, c.amount, c.week, c.amount, c.object)
		if op.ID == (uuid.UUID{}) {
			want = fmt.Sprintf("0+%d#%d 0+%d#0 0+%d#0", c.day, c.count, c.week, c.object)
		}
		if got := db.counters(t, &site); got != want {
			t.Errorf("%s: the counters are %s, want %s", c.name, got, want)
		}
	}
	other := uuid.NewV7()
	if _, _, err := db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), &other, "s1", 1)); !errors.Is(err, operations.ErrNoCapPolicy) {
		t.Errorf("a site without a policy: %v", err)
	}
	if _, _, err := db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), nil, "s1", 1)); !errors.Is(err, operations.ErrNoCapPolicy) {
		t.Errorf("the platform without a policy: %v", err)
	}
	if got := db.text(t, `SELECT count(*)::text FROM operations.operations WHERE state NOT IN ('approved', 'pending_approval')`); got != "0" {
		t.Errorf("%s operations were left by refusals", got)
	}
}

// TestSetCapPolicy: a policy is made at revision 0 and changed from the revision read, keeps its currency, holds each
// hard limit at least its autonomous one, and every change is in the audit log.
func TestSetCapPolicy(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	made, err := db.setPolicy(t, limits(nil), 0)
	if err != nil || made.Rev != 1 {
		t.Fatalf("making: %+v %v", made, err)
	}
	if _, err := db.setPolicy(t, limits(nil), 0); !errors.Is(err, operations.ErrPolicyChanged) {
		t.Errorf("making it again: %v", err)
	}
	cny, _ := platform.ParseCurrency("CNY")
	other := limits(nil)
	other.Currency = cny
	if _, err := db.setPolicy(t, other, 1); !errors.Is(err, operations.ErrCurrencyFixed) {
		t.Errorf("another currency: %v", err)
	}
	bad := limits(nil)
	bad.HardDaily = bad.Daily - 1
	if _, err := db.setPolicy(t, bad, 1); !errors.Is(err, operations.ErrBadPolicy) {
		t.Errorf("a hard limit under its autonomous one: %v", err)
	}
	raised := limits(nil)
	raised.HardDaily = 70
	changed, err := db.setPolicy(t, raised, 1)
	if err != nil || changed.Rev != 2 || changed.HardDaily != 70 {
		t.Errorf("changing: %+v %v", changed, err)
	}
	if got := db.text(t, `SELECT string_agg(action || ':' || coalesce(detail->'before'->>'rev', '-') || '>' || (detail->'after'->>'rev'), ',' ORDER BY id) FROM platform.audit_log`); got != "cap_policy.set:->1,cap_policy.set:1>2" {
		t.Errorf("the audit is %s", got)
	}
}

// TestReservations: a reservation is released when its proposal is rejected, withdrawn, canceled, refused because the
// policy was lowered under it (one loosened leaves it alone), or its windows end unconfirmed; it is committed once when the agent confirms the money; a retry
// reserves nothing twice.
func TestReservations(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	policy := limits(nil)
	policy.DailyCount, policy.ObjectDaily = 100, 100
	if _, err := db.setPolicy(t, policy, 0); err != nil {
		t.Fatal(err)
	}
	credential := uuid.NewV7()
	pending := func() operations.Operation {
		t.Helper()
		op, _, err := db.proposeMoney(t, topUp(credential, uuid.NewV7(), nil, "s1", 12))
		if err != nil || op.State != operations.StatePending {
			t.Fatalf("a pending top-up: %+v %v", op, err)
		}
		return op
	}
	reservation := func(op operations.Operation) string {
		return db.text(t, `SELECT state FROM operations.money_actions WHERE operation_id = '`+op.ID.String()+`'`)
	}

	rejected := pending()
	if _, err := db.decide(t, rejected, false); err != nil {
		t.Fatal(err)
	}
	withdrawn := pending()
	if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Withdraw(t.Context(), tx, credential, withdrawn.Approval.ID, "no")
	}); err != nil {
		t.Fatal(err)
	}
	canceled := pending()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := operations.CancelLive(t.Context(), tx, owner, nil, "stop")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []operations.Operation{rejected, withdrawn, canceled} {
		if got := reservation(op); got != "released" {
			t.Errorf("the reservation of a %s proposal is %s", op.State, got)
		}
	}
	if got := db.counters(t, nil); got != "0+0#3 0+0#3 0+0#3" {
		t.Errorf("after the releases the counters are %s", got)
	}

	// approve approves op and ends it as the admin does: rejected, and committed so, when the recheck refuses it.
	approve := func(op operations.Operation) (checked error) {
		if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
			op, err := operations.Decide(t.Context(), tx, owner, op.Approval.ID, true, "ok")
			if err != nil {
				return op, err
			}
			refused := ""
			if _, checked = operations.CheckReservation(t.Context(), tx, op); checked != nil {
				refused = "stale_revision"
			}
			return operations.Settle(t.Context(), tx, op, map[string]any{}, refused)
		}); err != nil {
			t.Fatal(err)
		}
		return checked
	}
	loosened := pending()
	raised := policy
	raised.HardWeekly = 100
	if _, err := db.setPolicy(t, raised, 1); err != nil {
		t.Fatal(err)
	}
	if err := approve(loosened); err != nil {
		t.Errorf("an approval after the policy was loosened: %v", err)
	}
	tightened := pending()
	lowered := policy
	lowered.Daily, lowered.HardDaily = 5, 11
	if _, err := db.setPolicy(t, lowered, 2); err != nil {
		t.Fatal(err)
	}
	if err := approve(tightened); !errors.Is(err, operations.ErrPolicyChanged) {
		t.Errorf("an approval after the policy was lowered under it: %v", err)
	}
	if got := reservation(loosened) + " " + reservation(tightened); got != "reserved released" {
		t.Errorf("the reservations of the loosened and the tightened: %s", got)
	}
	if _, err := db.setPolicy(t, policy, 3); err != nil {
		t.Fatal(err)
	}

	five := topUp(credential, uuid.NewV7(), nil, "s1", 5)
	direct, _, err := db.proposeMoney(t, five)
	if err != nil {
		t.Fatal(err)
	}
	if again, replayed, err := db.proposeMoney(t, five); err != nil || !replayed || again.ID != direct.ID {
		t.Errorf("the retry: %+v %v %v", again, replayed, err)
	}
	if direct, err = db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, direct, map[string]any{}, "")
	}); err != nil {
		t.Fatal(err)
	}
	confirm := func(proposal operations.Operation) (operations.Reservation, error) {
		var r operations.Reservation
		_, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
			c, _, err := operations.Propose(t.Context(), tx, operations.Proposal{
				Credential: credential, Key: uuid.NewV7(), Tool: "supplier_top_up_confirm", Target: proposal.Target,
				Params: map[string]any{"proposal": proposal.ID.String()}, Risk: operations.RiskTopUp, Reason: "paid",
			})
			if err != nil {
				return c, err
			}
			r, err = operations.CommitReservation(t.Context(), tx, c, proposal.ID, proposal.Target, "receipt 42")
			return c, err
		})
		return r, err
	}
	if r, err := confirm(direct); err != nil || r.State != "committed" || r.Proof != "receipt 42" {
		t.Errorf("the confirmation: %+v %v", r, err)
	}
	if _, err := confirm(direct); !errors.Is(err, operations.ErrNotReserved) {
		t.Errorf("a second confirmation: %v", err)
	}
	if got := db.counters(t, nil); got != "12+5#6 12+5#6 12+5#6" {
		t.Errorf("after the confirmation the counters are %s", got)
	}

	unconfirmed, _, err := db.proposeMoney(t, topUp(credential, uuid.NewV7(), nil, "s1", 3))
	if err != nil {
		t.Fatal(err)
	}
	if unconfirmed, err = db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, unconfirmed, map[string]any{}, "")
	}); err != nil {
		t.Fatal(err)
	}
	if n := db.expireReservations(t); n != 0 {
		t.Errorf("the sweep released %d reservations still in their windows", n)
	}
	db.windowsEnded(t, unconfirmed)
	if _, err := confirm(unconfirmed); !errors.Is(err, operations.ErrNotReserved) {
		t.Errorf("a confirmation after the windows ended: %v", err)
	}
	if n := db.expireReservations(t); n != 1 {
		t.Errorf("the sweep released %d", n)
	}
	if got := reservation(unconfirmed) + " " + db.counters(t, nil); got != "released 12+5#7 12+5#7 12+5#7" {
		t.Errorf("after the sweep: %s", got)
	}
	if got := db.text(t, `SELECT string_agg(action || ':' || actor_kind, ',' ORDER BY id) FROM platform.audit_log WHERE action LIKE 'reservation.%'`); got != "reservation.release:user,reservation.release:operations_agent,reservation.release:user,reservation.release:operations_agent,reservation.commit:operations_agent,reservation.release:system" {
		t.Errorf("the reservations' audit is %s", got)
	}
}

func (db opsDB) expireReservations(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		n, err = operations.ExpireReservations(t.Context(), tx, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// windowsEnded moves a reservation past the end of its windows, as the clock would.
func (db opsDB) windowsEnded(t *testing.T, op operations.Operation) {
	t.Helper()
	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE operations.money_actions SET expires_at = clock_timestamp()
		WHERE operation_id = '`+op.ID.String()+`'`); err != nil {
		t.Fatal(err)
	}
}

// TestCapRaces: two proposals that each fit the hard daily limit but not together are serialized by the counters, so
// the second waits for the first and is refused; a retry of a key waits for the first request and reserves nothing; a
// sweep skips a reservation another transaction holds. The order is made by holding a transaction open
// until the other is seen waiting, never by time.
func TestCapRaces(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site := uuid.NewV7()
	if _, err := db.setPolicy(t, limits(&site), 0); err != nil {
		t.Fatal(err)
	}
	db.seed(t, site, 35, 35, 0, 0)
	var second error
	db.whileWaited(t, func(tx pg.Tx) error {
		ten := topUp(uuid.NewV7(), uuid.NewV7(), &site, "s1", 10)
		_, _, err := operations.ProposeMoney(t.Context(), tx, ten.p, ten.m)
		return err
	}, func() error {
		_, _, second = db.proposeMoney(t, topUp(uuid.NewV7(), uuid.NewV7(), &site, "s2", 10))
		return nil
	}, nil)
	if exceeded := (*operations.CapExceeded)(nil); !errors.As(second, &exceeded) || exceeded.Limit != "hard_daily" || exceeded.Used != 45 {
		t.Errorf("the second proposal: %v", second)
	}

	credential := uuid.NewV7()
	one := topUp(credential, uuid.NewV7(), &site, "s1", 1)
	var first, retry operations.Operation
	var replayed bool
	db.whileWaited(t, func(tx pg.Tx) (err error) {
		first, _, err = operations.ProposeMoney(t.Context(), tx, one.p, one.m)
		return err
	}, func() (err error) {
		retry, replayed, err = db.proposeMoney(t, one)
		return err
	}, nil)
	if !replayed || retry.ID != first.ID {
		t.Errorf("the retry: %+v %v", retry, replayed)
	}
	if got := db.counters(t, &site); got != "11+35#2 11+35#2 11+0#2" {
		t.Errorf("the counters are %s", got)
	}

	db.windowsEnded(t, first)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM operations.money_actions WHERE operation_id = $1 FOR UPDATE`, first.ID); err != nil {
			return err
		}
		if n := db.expireReservations(t); n != 0 {
			t.Errorf("the sweep took a held reservation: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
