package operations_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// TestCapLockOrder: every path takes the approval signals' lock last. An approval of a top-up waits for the counters a
// proposal holds before it takes that lock, so the proposal, which takes it after the counters, finishes first instead
// of both waiting on each other; and the sweep of reservations skips one whose operation a cancellation holds rather
// than locking the reservation the cancellation will want next. The order is made by holding a transaction open until
// the other is seen waiting, never by time.
func TestCapLockOrder(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	asked := r.pending()
	var proposal pg.Tx
	var approved error
	r.whileWaited(t, func(tx pg.Tx) error {
		proposal = tx
		_, err := tx.Exec(t.Context(), `SELECT id FROM operations.cap_counters WHERE site_id IS NULL ORDER BY object_kind, window_kind FOR UPDATE`)
		return err
	}, func() (err error) {
		approved, err = r.tryApprove(asked)
		return err
	}, func() {
		if err := alert.Persist(t.Context(), proposal, uuid.NewV7(), alert.Signal{Kind: "operations.approval_pending", Key: uuid.NewV7().String(), Severity: "warning"}); err != nil {
			t.Errorf("the proposal's signal behind the approval: %v", err)
		}
	})
	if approved != nil || r.state(asked) != "reserved" {
		t.Errorf("the approval after the proposal: %v %s", approved, r.state(asked))
	}

	due := r.direct(1)
	r.windowsEnded(t, due)
	if err := r.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM operations.operations WHERE id = $1 FOR UPDATE`, due.ID); err != nil {
			return err
		}
		if n := r.expireReservations(t); n != 0 {
			t.Errorf("the sweep took the reservation of an operation another transaction holds: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := r.expireReservations(t); n != 1 {
		t.Errorf("the sweep after the operation was let go: %d", n)
	}
}

// TestWindowEnded: a proposal checks, after the last lock it waits for, whether its UTC day is over by the database's
// clock as it is now, not as its transaction began.
func TestWindowEnded(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	for _, c := range []struct {
		day  string
		want bool
	}{{"date_trunc('day', clock_timestamp(), 'UTC')", false}, {"date_trunc('day', clock_timestamp(), 'UTC') - interval '1 day'", true}} {
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			var day time.Time
			if err := tx.QueryRow(t.Context(), "SELECT "+c.day).Scan(&day); err != nil {
				return err
			}
			ended, err := operations.New().WindowEnded(t.Context(), tx, day)
			if err == nil && ended != c.want {
				t.Errorf("the day of %s ended: %v", c.day, ended)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestExpiryJobRun: one run of the job expires the approvals past their expiry and releases the reservations past
// their windows, the waiting top-up's reservation with its approval.
func TestExpiryJobRun(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	waiting := r.pending()
	r.pastDue(t, waiting)
	unconfirmed := r.direct(1)
	r.windowsEnded(t, unconfirmed)
	if err := operations.ExpireAll(t.Context(), r.DB, platformScope); err != nil {
		t.Fatal(err)
	}
	if got := r.state(waiting) + " " + r.state(unconfirmed) + " " + string(r.operation(t, waiting).State); got != "released released expired" {
		t.Errorf("after the run: %s", got)
	}
}

// TestCapUsages: each policy is read with what its category holds in the current day and week, a waiting proposal's
// reservation included, and a policy nothing was proposed under holds nothing.
func TestCapUsages(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	site := uuid.NewV7()
	if _, err := r.setPolicy(t, limits(&site), 0); err != nil {
		t.Fatal(err)
	}
	r.direct(5)
	r.pending()
	var usage []operations.CapUsage
	if err := r.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		usage, err = operations.CapUsages(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 || usage[0].Policy.Site != nil || usage[0].Day != usage[0].Week ||
		usage[0].Day != (operations.WindowUsage{Reserved: 17, Count: 2}) || usage[1].Day != (operations.WindowUsage{}) ||
		usage[0].DayStart.Before(usage[0].WeekStart) {
		t.Errorf("the usage: %+v", usage)
	}
}

// TestCapPolicyReads: the policies are listed the platform's first, a scope's currency is its policy's, and a scope
// without one has none.
func TestCapPolicyReads(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	site := uuid.NewV7()
	cny := limits(&site)
	cny.Currency = currency("CNY")
	if _, err := r.setPolicy(t, cny, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		policies, err := operations.CapPolicies(t.Context(), tx)
		if err != nil || len(policies) != 2 || policies[0].Site != nil || *policies[1].Site != site {
			t.Errorf("the policies: %+v %v", policies, err)
		}
		for _, c := range []struct {
			site *uuid.UUID
			want string
		}{{nil, "USD"}, {&site, "CNY"}, {new(uuid.NewV7()), ""}} {
			got, err := operations.CapCurrency(t.Context(), tx, c.site, operations.CategoryTopUp)
			if got.String() != c.want || (c.want == "") != errors.Is(err, operations.ErrNoCapPolicy) {
				t.Errorf("the currency of %v: %q %v", c.site, got, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReservationOf: an operation's reservation is read back, and one that moves no money has none.
func TestReservationOf(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	direct := r.direct(2)
	retired, _, err := r.propose(t, retire(r.credential, uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		reservation, found, err := operations.ReservationOf(t.Context(), tx, direct.ID)
		if err != nil || !found || reservation.Amount != 2 || reservation.State != "reserved" || reservation.Object.ID != "s1" {
			t.Errorf("the reservation: %+v %v %v", reservation, found, err)
		}
		if _, found, err := operations.ReservationOf(t.Context(), tx, retired.ID); found || err != nil {
			t.Errorf("an operation that moves no money: %v %v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCapRefusals: a hard limit's refusal says which limit and by how much; a confirmation of another credential's
// proposal, of another object or of an operation that moves no money finds nothing, and an approval of a proposal
// whose windows ended is refused.
func TestCapRefusals(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	_, _, err := r.proposeMoney(t, topUp(r.credential, uuid.NewV7(), nil, "s1", 21))
	if err == nil || err.Error() != "operations: the hard_single cap of 20 would be exceeded: 0 used, 21 asked" {
		t.Errorf("over the hard single cap: %v", err)
	}
	direct := r.direct(1)
	retired, _, err := r.propose(t, retire(r.credential, uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		credential uuid.UUID
		proposal   operations.Operation
		object     operations.Target
	}{
		"another credential's": {uuid.NewV7(), direct, direct.Target},
		"of another object":    {r.credential, direct, operations.Target{Kind: "supplier", ID: "s2"}},
		"one moving no money":  {r.credential, retired, retired.Target},
	} {
		if _, err := r.in(t, func(tx pg.Tx) (operations.Operation, error) {
			_, err := operations.CommitReservation(t.Context(), tx, operations.Operation{ID: direct.ID, Credential: c.credential}, c.proposal.ID, c.object, "receipt")
			return operations.Operation{}, err
		}); !errors.Is(err, operations.ErrNoOperation) {
			t.Errorf("confirming %s: %v", name, err)
		}
	}
	late := r.pending()
	r.windowsEnded(t, late)
	if err := r.approve(late); !errors.Is(err, operations.ErrNotReserved) {
		t.Errorf("an approval after the windows ended: %v", err)
	}
}

func (db opsDB) operation(t *testing.T, op operations.Operation) operations.Operation {
	t.Helper()
	got, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) { return operations.Get(t.Context(), tx, op.ID) })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestFailuresReachTheCaller: a call whose database fails returns that failure, wrapped, and never an answer: each
// call of the module runs with a context already canceled.
func TestFailuresReachTheCaller(t *testing.T) {
	t.Parallel()
	r := newReservations(t)
	waiting, direct := r.pending(), r.direct(1)
	p := topUp(r.credential, uuid.NewV7(), nil, "s1", 1)
	calls := map[string]func(ctx context.Context, tx pg.Tx) error{
		"CapPolicies": func(ctx context.Context, tx pg.Tx) error { _, err := operations.CapPolicies(ctx, tx); return err },
		"CapUsages":   func(ctx context.Context, tx pg.Tx) error { _, err := operations.CapUsages(ctx, tx); return err },
		"RepairCaps": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.RepairCaps(ctx, tx, owner, operations.CategoryTopUp, nil, "r")
			return err
		},
		"SetCapPolicy": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.SetCapPolicy(ctx, tx, owner, r.policy, 1, "r")
			return err
		},
		"CapCurrency": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.CapCurrency(ctx, tx, nil, operations.CategoryTopUp)
			return err
		},
		"ProposeMoney": func(ctx context.Context, tx pg.Tx) error {
			_, _, err := operations.ProposeMoney(ctx, tx, p.p, p.m)
			return err
		},
		"Replay": func(ctx context.Context, tx pg.Tx) error { _, _, err := operations.Replay(ctx, tx, p.p); return err },
		"CheckReservation": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.CheckReservation(ctx, tx, direct)
			return err
		},
		"CommitReservation": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.CommitReservation(ctx, tx, direct, direct.ID, direct.Target, "receipt")
			return err
		},
		"ReservationOf": func(ctx context.Context, tx pg.Tx) error {
			_, _, err := operations.ReservationOf(ctx, tx, direct.ID)
			return err
		},
		"ExpireReservations": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.ExpireReservations(ctx, tx, 1)
			return err
		},
		"ExpireDue": func(ctx context.Context, tx pg.Tx) error { _, err := operations.ExpireDue(ctx, tx, 1); return err },
		"CancelLive": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.CancelLive(ctx, tx, owner, nil, "r")
			return err
		},
		"Decide": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.Decide(ctx, tx, owner, waiting.Approval.ID, true, "r")
			return err
		},
		"Withdraw": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.Withdraw(ctx, tx, r.credential, waiting.Approval.ID, "r")
			return err
		},
		"Get": func(ctx context.Context, tx pg.Tx) error { _, err := operations.Get(ctx, tx, direct.ID); return err },
		"Operations": func(ctx context.Context, tx pg.Tx) error {
			_, _, err := operations.Operations(ctx, tx, r.credential, "", uuid.UUID{}, 10)
			return err
		},
		"Approvals": func(ctx context.Context, tx pg.Tx) error {
			_, _, err := operations.Approvals(ctx, tx, "", uuid.UUID{}, 10)
			return err
		},
		"ApprovalOperation": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.ApprovalOperation(ctx, tx, waiting.Approval.ID)
			return err
		},
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range calls {
		got := r.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return call(canceled, tx) })
		if !errors.Is(got, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, got)
		}
	}
	if _, err := operations.ReconcileCaps(canceled, r.DB, platformScope); !errors.Is(err, context.Canceled) {
		t.Errorf("ReconcileCaps with a failing database: %v", err)
	}
}
