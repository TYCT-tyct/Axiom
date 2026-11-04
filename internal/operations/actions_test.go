package operations_test

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var owner = audit.User(uuid.NewV7())

// opsDB is a database with the module's tables, and its superuser's URL to move an approval past its expiry.
type opsDB struct {
	*pg.DB
	superuser string
}

func openOps(t *testing.T) opsDB {
	t.Helper()
	database := pgtest.New(t, operations.Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return opsDB{db, database.Superuser}
}

func retire(credential, key uuid.UUID, rev int, needsApproval bool) operations.Proposal {
	p := operations.Proposal{
		Credential: credential, Key: key, Tool: "model_set_status", Target: operations.Target{Kind: "model", ID: "m1"},
		Params: map[string]any{"status": "retired", "rev": rev}, Risk: operations.RiskDelisting, Reason: "unused",
	}
	if needsApproval {
		p.Summary = map[string]any{"slug": "acme/m1"}
	}
	return p
}

func (db opsDB) propose(t *testing.T, p operations.Proposal) (operations.Operation, bool, error) {
	t.Helper()
	var op operations.Operation
	var replayed bool
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		op, replayed, err = operations.Propose(t.Context(), tx, p)
		return err
	})
	return op, replayed, err
}

func (db opsDB) in(t *testing.T, do func(tx pg.Tx) (operations.Operation, error)) (operations.Operation, error) {
	t.Helper()
	var op operations.Operation
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		op, err = do(tx)
		return err
	})
	return op, err
}

func (db opsDB) text(t *testing.T, query string) string {
	t.Helper()
	var v string
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error { return tx.QueryRow(t.Context(), query).Scan(&v) }); err != nil {
		t.Fatal(err)
	}
	return v
}

// pastDue moves an operation's approval past its expiry, as the clock would.
func (db opsDB) pastDue(t *testing.T, op operations.Operation) {
	t.Helper()
	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE operations.approvals SET requested_at = now() - interval '2 hours',
		expires_at = now() - interval '1 hour' WHERE id = '`+op.Approval.ID.String()+`'`); err != nil {
		t.Fatal(err)
	}
}

// TestProposeOnceAKey: an operation that needs no person is approved for its caller to run and settle; the same key
// with the same request finds it as it is now, with another request is refused, and another credential's key is its
// own.
func TestProposeOnceAKey(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential, key := uuid.NewV7(), uuid.NewV7()
	op, replayed, err := db.propose(t, retire(credential, key, 3, false))
	if got := fmt.Sprintf("%v %v %v %v", err, replayed, op.State, op.Approval); got != "<nil> false approved <nil>" {
		t.Fatalf("a direct operation: %s", got)
	}
	if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, op, map[string]any{"status": "retired"}, "")
	}); err != nil {
		t.Fatal(err)
	}
	again, replayed, err := db.propose(t, retire(credential, key, 3, false))
	if got := fmt.Sprintf("%v %v %v %v %s", err, replayed, again.ID == op.ID, again.State, again.Result); got != `<nil> true true succeeded {"status":"retired"}` {
		t.Errorf("the retry: %s", got)
	}
	if _, _, err := db.propose(t, retire(credential, key, 4, false)); !errors.Is(err, operations.ErrKeyReused) {
		t.Errorf("the key with another request: %v", err)
	}
	if _, replayed, err := db.propose(t, retire(uuid.NewV7(), key, 3, false)); err != nil || replayed {
		t.Errorf("another credential's key is its own: %v %v", replayed, err)
	}
	if got := db.text(t, `SELECT string_agg(action || ':' || actor_kind, ',') FROM platform.audit_log`); got != "operation.succeed:operations_agent" {
		t.Errorf("the audit is %s", got)
	}
}

// TestProposeWaitsForTheOwner: an operation that needs a person waits on an approval that expires within 30 minutes
// and its UTC day, recorded as the agent's and signalled to the owner.
func TestProposeWaitsForTheOwner(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	pending, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 3, true))
	if err != nil || pending.State != operations.StatePending || pending.Approval == nil || pending.Approval.State != operations.ApprovalPending {
		t.Fatalf("an operation that needs a person: %+v %v", pending, err)
	}
	a := pending.Approval
	endOfDay := a.Requested.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	if wait := a.Expires.Sub(a.Requested); wait <= 0 || wait > 30*time.Minute || a.Expires.After(endOfDay) {
		t.Errorf("the approval expires %v after it was asked, at %v", wait, a.Expires)
	}
	if got := db.text(t, `SELECT string_agg(action || ':' || actor_kind, ',') FROM platform.audit_log`); got != "approval.request:operations_agent" {
		t.Errorf("the audit is %s", got)
	}
	if got := db.text(t, `SELECT count(*)::text FROM platform.alert_signals WHERE body->>'kind' = 'operations.approval_pending' AND body->>'key' = '`+a.ID.String()+`'`); got != "1" {
		t.Errorf("%s signals of the approval", got)
	}
}

// TestDecide: a person approves an approval once, and its operation is left approved to run; a rejected one ends.
func TestDecide(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential := uuid.NewV7()
	propose := func() operations.Operation {
		op, _, err := db.propose(t, retire(credential, uuid.NewV7(), 1, true))
		if err != nil {
			t.Fatal(err)
		}
		return op
	}

	approved, err := db.decide(t, propose(), true)
	if err != nil || approved.State != operations.StateApproved || approved.Approval.State != operations.ApprovalApproved || approved.Approval.DecidedBy != owner.String() {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := db.decide(t, approved, false); !errors.Is(err, operations.ErrNotPending) {
		t.Errorf("deciding twice: %v", err)
	}
	rejected, err := db.decide(t, propose(), false)
	if err != nil || rejected.State != operations.StateRejected || rejected.Approval.State != operations.ApprovalRejected {
		t.Errorf("reject: %+v %v", rejected, err)
	}
}

func (db opsDB) decide(t *testing.T, op operations.Operation, approve bool) (operations.Operation, error) {
	t.Helper()
	return db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Decide(t.Context(), tx, owner, op.Approval.ID, approve, "looked at it")
	})
}

// TestApprovalExpiry: an approval past its expiry is refused a decision even before the sweep, which expires it as the
// system once.
func TestApprovalExpiry(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	late, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}
	db.pastDue(t, late)
	if _, err := db.decide(t, late, true); !errors.Is(err, operations.ErrNotPending) {
		t.Errorf("approving after the expiry: %v", err)
	}
	if n := db.expire(t); n != 1 {
		t.Errorf("the sweep expired %d", n)
	}
	if got := db.state(t, late.ID); got != "expired:expired:system:operations.approval_expiry" {
		t.Errorf("the late one is %s", got)
	}
	if n := db.expire(t); n != 0 {
		t.Errorf("the second sweep expired %d", n)
	}
}

// TestWithdraw: the agent withdraws its own pending proposal, never another credential's; every decided approval's
// signal is resolved.
func TestWithdraw(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential := uuid.NewV7()
	mine, _, err := db.propose(t, retire(credential, uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}
	withdraw := func(by uuid.UUID) error {
		_, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
			return operations.Withdraw(t.Context(), tx, by, mine.Approval.ID, "not needed")
		})
		return err
	}
	if err := withdraw(uuid.NewV7()); !errors.Is(err, operations.ErrNoApproval) {
		t.Errorf("another credential withdrew it: %v", err)
	}
	if err := withdraw(credential); err != nil {
		t.Fatal(err)
	}
	if got := db.state(t, mine.ID); got != "canceled:canceled:operations_agent:"+credential.String() {
		t.Errorf("the withdrawn one is %s", got)
	}
	if got := db.text(t, `SELECT count(*)::text FROM platform.alert_signals WHERE (body->>'resolved')::bool AND body->>'key' = '`+mine.Approval.ID.String()+`'`); got != "1" {
		t.Errorf("%s resolutions of the withdrawn approval's signal", got)
	}
}

func (db opsDB) expire(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		n, err = operations.ExpireDue(t.Context(), tx, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// state is an operation's state, its approval's and who decided it, as operation:approval:decider.
func (db opsDB) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	return db.text(t, `SELECT o.state || ':' || a.state || ':' || coalesce(a.decided_by, '') FROM operations.operations o
		JOIN operations.approvals a ON a.operation_id = o.id WHERE o.id = '`+id.String()+`'`)
}

// TestCancelLive: a revocation cancels the credential's pending proposals and no other's; the stop cancels the rest.
func TestCancelLive(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	one, two := uuid.NewV7(), uuid.NewV7()
	for _, c := range []uuid.UUID{one, one, two} {
		if _, _, err := db.propose(t, retire(c, uuid.NewV7(), 1, true)); err != nil {
			t.Fatal(err)
		}
	}
	direct, _, err := db.propose(t, retire(two, uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, direct, nil, "")
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		credential *uuid.UUID
		want       int
	}{{&one, 2}, {&one, 0}, {nil, 1}, {nil, 0}} {
		var n int
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
			n, err = operations.CancelLive(t.Context(), tx, owner, c.credential, "stop")
			return err
		}); err != nil || n != c.want {
			t.Errorf("CancelLive(%v) canceled %d, want %d: %v", c.credential, n, c.want, err)
		}
	}
	if got := db.text(t, `SELECT string_agg(state, ',' ORDER BY state) FROM operations.operations`); got != "canceled,canceled,canceled,succeeded" {
		t.Errorf("the operations are %s", got)
	}
}

// TestRaces: a second request with a key waits for the first's transaction and finds its operation; a withdrawal
// that waits for an approval finds it decided; a sweep skips the operation a decision holds and expires it after. The
// order is made by holding a transaction open until the other is seen waiting, never by time.
func TestRaces(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential, key := uuid.NewV7(), uuid.NewV7()

	var first operations.Operation
	var second operations.Operation
	var replayed bool
	db.whileWaited(t, func(tx pg.Tx) (err error) {
		first, _, err = operations.Propose(t.Context(), tx, retire(credential, key, 1, true))
		return err
	}, func() error {
		var err error
		second, replayed, err = db.propose(t, retire(credential, key, 1, true))
		return err
	})
	if !replayed || second.ID != first.ID || db.text(t, `SELECT count(*)::text FROM operations.operations`) != "1" {
		t.Errorf("the key's second request: %+v %v", second, replayed)
	}

	var withdrawn error
	db.whileWaited(t, func(tx pg.Tx) error {
		_, err := operations.Decide(t.Context(), tx, owner, first.Approval.ID, true, "ok")
		return err
	}, func() error {
		_, withdrawn = db.in(t, func(tx pg.Tx) (operations.Operation, error) {
			return operations.Withdraw(t.Context(), tx, credential, first.Approval.ID, "late")
		})
		return nil
	})
	if !errors.Is(withdrawn, operations.ErrNotPending) {
		t.Errorf("the withdrawal after the approval: %v", withdrawn)
	}

	late, _, err := db.propose(t, retire(credential, uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}
	db.pastDue(t, late)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := operations.Decide(t.Context(), tx, owner, late.Approval.ID, true, "too late"); !errors.Is(err, operations.ErrNotPending) {
			t.Errorf("the late decision: %v", err)
		}
		if n := db.expire(t); n != 0 {
			t.Errorf("the sweep took a held operation: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := db.expire(t); n != 1 {
		t.Errorf("the sweep after the decision let go: %d", n)
	}
}

// whileWaited runs hold in a transaction it keeps open until wait, started alongside, is seen waiting on a lock, then
// commits it and returns once wait has.
func (db opsDB) whileWaited(t *testing.T, hold func(tx pg.Tx) error, wait func() error) {
	t.Helper()
	done := make(chan error, 1)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := hold(tx); err != nil {
			return err
		}
		go func() { done <- wait() }()
		for t.Context().Err() == nil {
			n, err := strconv.Atoi(db.text(t, `SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`))
			if err != nil || n > 0 {
				return err
			}
			runtime.Gosched()
		}
		return t.Context().Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
