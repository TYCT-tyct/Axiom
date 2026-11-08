package operations

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// The reads of operations and approvals: the agent's own, through the operations API, and the owner's queue.

// Get reads operation id with its approval, in tx.
func Get(ctx context.Context, tx pg.Tx, id uuid.UUID) (Operation, error) {
	rows, err := New().OperationByID(ctx, tx, id)
	if err != nil {
		return Operation{}, fmt.Errorf("read operation %s: %w", id, err)
	}
	if len(rows) == 0 {
		return Operation{}, ErrNoOperation
	}
	return withApproval(ctx, tx, operationOf(rows[0]))
}

// Operations is a page of credential's operations newest first, in state or all of them ("" ), after the operation
// after (the zero ID: from the start), and whether more follow.
func Operations(ctx context.Context, tx pg.Tx, credential uuid.UUID, state State, after uuid.UUID, pageSize int) ([]Operation, bool, error) {
	rows, err := New().OperationsOf(ctx, tx, OperationsOfParams{
		CredentialID: credential, State: optionalText(string(state)), After: after, PageSize: int32(pageSize + 1), // #nosec G115 -- the API bounds the page to 200
	})
	if err != nil {
		return nil, false, fmt.Errorf("read the operations: %w", err)
	}
	more := len(rows) > pageSize
	ops := make([]Operation, min(len(rows), pageSize))
	ids := make([]uuid.UUID, len(ops))
	for i := range ops {
		ops[i], ids[i] = operationOf(rows[i]), rows[i].ID
	}
	approvals, err := New().ApprovalsOf(ctx, tx, ids)
	if err != nil {
		return nil, false, fmt.Errorf("read the operations' approvals: %w", err)
	}
	byOperation := make(map[uuid.UUID]*Approval, len(approvals))
	for _, a := range approvals {
		byOperation[a.OperationsApproval.OperationID] = approvalOf(a.OperationsApproval, a.Decidable)
	}
	for i := range ops {
		ops[i].Approval = byOperation[ops[i].ID]
	}
	return ops, more, nil
}

// Approvals is a page of every approval newest first, in state or all of them (""), after the approval after, each
// with its operation, and whether more follow: the owner's queue.
func Approvals(ctx context.Context, tx pg.Tx, state ApprovalState, after uuid.UUID, pageSize int) ([]Operation, bool, error) {
	q := New()
	rows, err := q.Approvals(ctx, tx, ApprovalsParams{State: optionalText(string(state)), After: after, PageSize: int32(pageSize + 1)}) // #nosec G115 -- the API bounds the page to 200
	if err != nil {
		return nil, false, fmt.Errorf("read the approvals: %w", err)
	}
	more := len(rows) > pageSize
	rows = rows[:min(len(rows), pageSize)]
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.OperationsApproval.OperationID
	}
	found, err := q.OperationsByID(ctx, tx, ids)
	if err != nil {
		return nil, false, fmt.Errorf("read the approvals' operations: %w", err)
	}
	byID := make(map[uuid.UUID]OperationsOperation, len(found))
	for _, o := range found {
		byID[o.ID] = o
	}
	ops := make([]Operation, len(rows))
	for i, r := range rows {
		ops[i] = operationOf(byID[r.OperationsApproval.OperationID])
		ops[i].Approval = approvalOf(r.OperationsApproval, r.Decidable)
	}
	return ops, more, nil
}

// ApprovalOperation reads approval id's operation with it.
func ApprovalOperation(ctx context.Context, tx pg.Tx, id uuid.UUID) (Operation, error) {
	found, err := New().ApprovalByID(ctx, tx, id)
	if err != nil {
		return Operation{}, fmt.Errorf("read approval %s: %w", id, err)
	}
	if len(found) == 0 {
		return Operation{}, ErrNoApproval
	}
	return Get(ctx, tx, found[0].OperationID)
}

func withApproval(ctx context.Context, tx pg.Tx, op Operation) (Operation, error) {
	rows, err := New().ApprovalsOf(ctx, tx, []uuid.UUID{op.ID})
	if err != nil {
		return Operation{}, fmt.Errorf("read the approval of operation %s: %w", op.ID, err)
	}
	if len(rows) > 0 {
		op.Approval = approvalOf(rows[0].OperationsApproval, rows[0].Decidable)
	}
	return op, nil
}

func operationOf(r OperationsOperation) Operation {
	return Operation{
		ID: r.ID, Credential: r.CredentialID, Tool: r.Tool, Target: Target{Kind: r.TargetKind, ID: r.TargetID},
		Params: r.Params, Risk: Risk(r.Risk), State: State(r.State), Reason: r.Reason, Run: deref(r.RunID),
		Result: r.Result, Error: deref(r.ErrorCode), Created: r.CreatedAt, Updated: r.UpdatedAt,
	}
}

func approvalOf(r OperationsApproval, decidable bool) *Approval {
	var reasons []Reason
	for _, text := range r.Reasons {
		reasons = append(reasons, Reason(text))
	}
	return &Approval{
		ID: r.ID, Operation: r.OperationID, State: ApprovalState(r.State), Summary: r.Summary, Reasons: reasons, Decidable: decidable,
		Requested: r.RequestedAt, Expires: r.ExpiresAt, Decided: r.DecidedAt, DecidedBy: deref(r.DecidedBy),
		DecisionReason: deref(r.DecisionReason),
	}
}

// reasonTexts is reasons as the column stores them: none is NULL, which the column's CHECK keeps for an older node.
func reasonTexts(reasons []Reason) []string {
	var texts []string
	for _, r := range reasons {
		texts = append(texts, string(r))
	}
	return texts
}
