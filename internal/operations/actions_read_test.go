package operations_test

import (
	"errors"
	"log/slog"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
)

func (db opsDB) operationsPage(t *testing.T, credential uuid.UUID, state operations.State, after uuid.UUID, pageSize int) ([]operations.Operation, bool, error) {
	t.Helper()
	var ops []operations.Operation
	var more bool
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		ops, more, err = operations.Operations(t.Context(), tx, credential, state, after, pageSize)
		return err
	})
	return ops, more, err
}

func (db opsDB) approvalsPage(t *testing.T, state operations.ApprovalState, after uuid.UUID, pageSize int) ([]operations.Operation, bool, error) {
	t.Helper()
	var ops []operations.Operation
	var more bool
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		ops, more, err = operations.Approvals(t.Context(), tx, state, after, pageSize)
		return err
	})
	return ops, more, err
}

// TestOperationsPage: a credential reads only its operations, newest first, paging by ID with each waiting
// operation's approval attached.
func TestOperationsPage(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential := uuid.NewV7()
	first, _, err := db.propose(t, retire(credential, uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := db.propose(t, retire(credential, uuid.NewV7(), 2, true))
	if err != nil {
		t.Fatal(err)
	}

	page, more, err := db.operationsPage(t, credential, "", uuid.UUID{}, 1)
	if err != nil || len(page) != 1 || !more || page[0].ID != second.ID || page[0].Approval == nil {
		t.Fatalf("the first page: %+v more=%t err=%v", page, more, err)
	}
	assertNextOperationsPage(t, db, credential, page, first)
}

func assertNextOperationsPage(t *testing.T, db opsDB, credential uuid.UUID, page []operations.Operation, first operations.Operation) {
	t.Helper()
	next, more, err := db.operationsPage(t, credential, "", page[0].ID, 1)
	if err != nil || len(next) != 1 || more || next[0].ID != first.ID || next[0].Approval != nil {
		t.Fatalf("the next page: %+v more=%t err=%v", next, more, err)
	}
}

// TestOperationsPageFilters: the state filter and the credential filter select independently of the page cursor.
func TestOperationsPageFilters(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	credential, other := uuid.NewV7(), uuid.NewV7()
	first, _, err := db.propose(t, retire(credential, uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.propose(t, retire(other, uuid.NewV7(), 1, false)); err != nil {
		t.Fatal(err)
	}

	approved, more, err := db.operationsPage(t, credential, operations.StateApproved, uuid.UUID{}, 10)
	if err != nil || len(approved) != 1 || more || approved[0].ID != first.ID {
		t.Fatalf("the approved page: %+v more=%t err=%v", approved, more, err)
	}
	otherPage, more, err := db.operationsPage(t, other, "", uuid.UUID{}, 10)
	if err != nil || len(otherPage) != 1 || more || otherPage[0].Credential != other {
		t.Fatalf("another credential's page: %+v more=%t err=%v", otherPage, more, err)
	}
}

// TestApprovalsQueue: the owner's approval queue is newest first and pages by approval ID.
func TestApprovalsQueue(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	first, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 2, true))
	if err != nil {
		t.Fatal(err)
	}

	page, more, err := db.approvalsPage(t, operations.ApprovalPending, uuid.UUID{}, 1)
	if err != nil || len(page) != 1 || !more || page[0].ID != second.ID {
		t.Fatalf("the first approval page: %+v more=%t err=%v", page, more, err)
	}
	next, more, err := db.approvalsPage(t, operations.ApprovalPending, page[0].Approval.ID, 1)
	if err != nil || len(next) != 1 || more || next[0].ID != first.ID {
		t.Fatalf("the next approval page: %+v more=%t err=%v", next, more, err)
	}
}

// TestApprovalsQueueEmpty: the state filter returns no approvals when none are in that state.
func TestApprovalsQueueEmpty(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true)); err != nil {
		t.Fatal(err)
	}
	empty, more, err := db.approvalsPage(t, operations.ApprovalExpired, uuid.UUID{}, 10)
	if err != nil || len(empty) != 0 || more {
		t.Fatalf("the expired approvals: %+v more=%t err=%v", empty, more, err)
	}
}

// TestApprovalOperation: an approval reads its operation, and an unknown approval is ErrNoApproval.
func TestApprovalOperation(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	first, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}

	var found operations.Operation
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		found, err = operations.ApprovalOperation(t.Context(), tx, first.Approval.ID)
		return err
	}); err != nil || found.ID != first.ID || found.Approval == nil || found.Approval.ID != first.Approval.ID {
		t.Fatalf("the approval's operation: %+v err=%v", found, err)
	}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := operations.ApprovalOperation(t.Context(), tx, uuid.NewV7())
		return err
	}); !errors.Is(err, operations.ErrNoApproval) {
		t.Fatalf("an unknown approval: %v", err)
	}
}

// TestCancel: a person cancels a pending approval and its operation in one step; it cannot be canceled twice and an
// unknown approval is ErrNoApproval.
func TestCancel(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	pending, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true))
	if err != nil {
		t.Fatal(err)
	}
	cancel := func(id uuid.UUID) (operations.Operation, error) {
		var op operations.Operation
		err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
			op, err = operations.Cancel(t.Context(), tx, owner, id, "revoked")
			return err
		})
		return op, err
	}
	canceled, err := cancel(pending.Approval.ID)
	if err != nil || canceled.State != operations.StateCanceled || canceled.Approval.State != operations.ApprovalCanceled ||
		canceled.Approval.DecidedBy != owner.String() || canceled.Approval.DecisionReason != "revoked" {
		t.Fatalf("the canceled operation: %+v err=%v", canceled, err)
	}
	if _, err := cancel(pending.Approval.ID); !errors.Is(err, operations.ErrNotPending) {
		t.Errorf("canceling twice: %v", err)
	}
	if _, err := cancel(uuid.NewV7()); !errors.Is(err, operations.ErrNoApproval) {
		t.Errorf("an unknown approval: %v", err)
	}
}

// TestRegisterExpiry: the sweep registers on the queue before it starts.
func TestRegisterExpiry(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	logger := slog.New(slog.DiscardHandler)
	queue := db.NewQueue(logger, platformScope, nil)
	if err := operations.RegisterExpiry(db.DB, platformScope, queue, logger); err != nil {
		t.Fatal(err)
	}
}

// TestSettleRefused: a target that refuses after approval ends the operation as rejected with its code, and a pending
// operation cannot be settled before a person approves it.
func TestSettleRefused(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	direct, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, false))
	if err != nil {
		t.Fatal(err)
	}
	refused, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, direct, nil, "version_conflict")
	})
	if err != nil || refused.State != operations.StateRejected || refused.Error != "version_conflict" || refused.Result != nil {
		t.Fatalf("the refused operation: %+v err=%v", refused, err)
	}
	pending, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 2, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Settle(t.Context(), tx, pending, nil, "")
	}); err == nil {
		t.Error("a pending operation was settled")
	}
}

// TestProposeRejectsAnUnencodableSummary: a summary that is not JSON rolls the operation back.
func TestProposeRejectsAnUnencodableSummary(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	p := retire(uuid.NewV7(), uuid.NewV7(), 1, true)
	p.Summary = map[string]any{"bad": make(chan int)}
	if _, _, err := db.propose(t, p); err == nil {
		t.Error("an unencodable summary was accepted")
	}
}

// TestGetUnknown: an unknown operation is ErrNoOperation.
func TestGetUnknown(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if _, err := db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		return operations.Get(t.Context(), tx, uuid.NewV7())
	}); !errors.Is(err, operations.ErrNoOperation) {
		t.Errorf("Get(unknown) = %v", err)
	}
}
