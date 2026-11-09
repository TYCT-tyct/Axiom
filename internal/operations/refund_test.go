package operations_test

import (
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
)

// TestRefundOutcome: an approved refund is executing with its reservation held, out of every sweep's reach, until its
// outcome: success commits it in the windows it was approved in, failure releases it and ends the operation
// target_failed, both as the system; a second conclusion changes nothing.
func TestRefundOutcome(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		succeeded bool
		want      string
	}{
		{"succeeded", true, "succeeded - committed 0+5#1 operation.execute:operations_agent,reservation.commit:system,operation.succeed:system"},
		{"failed", false, "failed target_failed released 0+0#1 operation.execute:operations_agent,reservation.release:system,operation.fail:system"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReservations(t)
			policy := r.policy
			policy.Category = operations.CategoryRefund
			if _, err := r.setPolicy(t, policy, 0); err != nil {
				t.Fatal(err)
			}
			op := r.executing()
			r.expectHeld(op)
			r.conclude(op, c.succeeded)
			r.conclude(op, c.succeeded)
			if got := r.outcome(op); got != c.want {
				t.Errorf("after the outcome: %s, want %s", got, c.want)
			}
			if listed := r.listExecuting(); len(listed) != 0 {
				t.Errorf("still executing: %+v", listed)
			}
		})
	}
}

// conclude ends op by its refund's outcome, as the outcome job does.
func (r reservations) conclude(op operations.Operation, succeeded bool) {
	r.t.Helper()
	if _, err := r.in(r.t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Conclude(r.t.Context(), tx, op.ID, succeeded, "rfd_1")
	}); err != nil {
		r.t.Fatal(err)
	}
}

// expectHeld checks the reservation of op, an executing refund, is held with no expiry, no sweep releases it, and op
// is listed as executing.
func (r reservations) expectHeld(op operations.Operation) {
	t := r.t
	t.Helper()
	if got, found, err := r.reservation(op); err != nil || !found || got.State != "reserved" || got.Expires != nil {
		t.Fatalf("while the refund is sent: %+v %v %v", got, found, err)
	}
	if n := r.expireReservations(t); n != 0 {
		t.Errorf("the sweep released %d reservations of executing refunds", n)
	}
	if listed := r.listExecuting(); len(listed) != 1 || listed[0].ID != op.ID {
		t.Errorf("the executing refunds: %+v", listed)
	}
}

// outcome is op's state, error code and reservation, the refund day's counter and the audit of the run and outcome.
func (r reservations) outcome(op operations.Operation) string {
	return r.text(r.t, `SELECT o.state || ' ' || coalesce(o.error_code, '-') || ' ' || m.state FROM operations.operations o
		JOIN operations.money_actions m ON m.operation_id = o.id WHERE o.id = '`+op.ID.String()+`'`) + " " +
		r.text(r.t, `SELECT reserved || '+' || committed || '#' || count FROM operations.cap_counters
			WHERE category = 'money.refund' AND window_kind = 'day' AND object_kind = ''`) + " " +
		r.text(r.t, `SELECT string_agg(action || ':' || actor_kind, ',' ORDER BY id) FROM platform.audit_log
			WHERE action IN ('operation.execute', 'operation.succeed', 'operation.fail') OR action LIKE 'reservation.%'`)
}

// executing proposes a refund of 5, which waits for the owner, approves it and hands it to its target as the approval
// does.
func (r reservations) executing() operations.Operation {
	r.t.Helper()
	op, _, err := r.proposeMoney(r.t, refund(topUp(r.credential, uuid.NewV7(), nil, "acct_1", 5)))
	if err != nil || op.State != operations.StatePending {
		r.t.Fatalf("a refund: %+v %v", op, err)
	}
	op, err = r.in(r.t, func(tx pg.Tx) (operations.Operation, error) {
		op, err := operations.Decide(r.t.Context(), tx, owner, op.Approval.ID, true, "ok")
		if err != nil {
			return op, err
		}
		if _, err := operations.CheckReservation(r.t.Context(), tx, op); err != nil {
			return op, err
		}
		return operations.Execute(r.t.Context(), tx, op, map[string]any{"refund_id": "rfd_1"})
	})
	if err != nil || op.State != operations.StateExecuting {
		r.t.Fatalf("the approval: %+v %v", op, err)
	}
	return op
}

func (r reservations) reservation(op operations.Operation) (got operations.Reservation, found bool, err error) {
	err = r.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		got, found, err = operations.ReservationOf(r.t.Context(), tx, op.ID)
		return err
	})
	return got, found, err
}

func (r reservations) listExecuting() []operations.Operation {
	r.t.Helper()
	var ops []operations.Operation
	if err := r.Tx(r.t.Context(), platformScope, func(tx pg.Tx) (err error) {
		ops, err = operations.Executing(r.t.Context(), tx, "refund_request", uuid.UUID{}, 10)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return ops
}
