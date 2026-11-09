package operations

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// An operation whose target finishes it after the approval's transaction, as a refund the payment channel sends, is
// executing until the outcome is known, and its reservation is held until then (docs/domains/operations.md「失败、重试与回滚」).

// untilOutcome is the expiry of a reservation that ends with its money's outcome rather than with its day: a refund
// is sent after the approval's transaction and may end on a later day, and the money that left must stay counted in the
// windows it was approved in until the channel says (「失败、重试与回滚」). No sweep reaches it, an older node's
// included, which knows nothing of the rule; while the proposal waits, the approval's own expiry, never past the end of
// its day, releases it.
var untilOutcome = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// expiry is when a reservation of c made in windows w ends unless it is settled first.
func (c Category) expiry(w CapWindowsRow) time.Time {
	if c == CategoryRefund {
		return untilOutcome
	}
	return w.DayEnd
}

// Execute moves op, which its caller locked as approved and handed in tx to a target that finishes it after tx
// commits, to executing with what the target answered. Its reservation stays held, in the windows it was approved in,
// until Conclude learns the outcome.
func Execute(ctx context.Context, tx pg.Tx, op Operation, result map[string]any) (Operation, error) {
	if op.State != StateApproved {
		return Operation{}, fmt.Errorf("execute operation %s: it is %s, not approved", op.ID, op.State)
	}
	body, err := jsonx.MarshalCanonical(result)
	if err != nil {
		return Operation{}, fmt.Errorf("execute operation %s: encode the result: %w", op.ID, err)
	}
	if err := setState(ctx, tx, &op, StateExecuting, body, ""); err != nil {
		return Operation{}, err
	}
	if err := record(ctx, tx, audit.OperationsAgent(op.Credential), "operation.execute", op, op.Reason, nil); err != nil {
		return Operation{}, err
	}
	if op.Approval != nil {
		return op, resolve(ctx, tx, op.Approval.ID)
	}
	return op, nil
}

// outcomeActor is who ends an executing operation by the outcome its target reported.
var outcomeActor = audit.System("operations.outcome")

// codeTargetFailed is the error code of an operation whose target reported its change failed.
const codeTargetFailed = "target_failed"

// Executing lists up to batch operations of tool that are executing, after the ID after, in ID order: what waits for
// its target's outcome.
func Executing(ctx context.Context, tx pg.Tx, tool string, after uuid.UUID, batch int) ([]Operation, error) {
	rows, err := New().ExecutingOperations(ctx, tx, ExecutingOperationsParams{Tool: tool, After: after, Batch: int32(batch)}) // #nosec G115 -- a small constant
	if err != nil {
		return nil, fmt.Errorf("list the executing %s operations: %w", tool, err)
	}
	ops := make([]Operation, len(rows))
	for i, r := range rows {
		ops[i] = operationOf(r)
	}
	return ops, nil
}

// Conclude ends executing operation id by its target's outcome, as the system's: succeeded commits its reservation in
// the windows it was approved in, with proof of where the money went; failed releases it and ends it with the code
// target_failed. An operation no longer executing was concluded already and is returned as it is, so a second
// conclusion, another node's included, changes nothing.
func Conclude(ctx context.Context, tx pg.Tx, id uuid.UUID, succeeded bool, proof string) (Operation, error) {
	q := New()
	locked, err := q.LockOperation(ctx, tx, id)
	switch {
	case err != nil:
		return Operation{}, fmt.Errorf("lock operation %s: %w", id, err)
	case len(locked) == 0:
		return Operation{}, ErrNoOperation
	}
	op := operationOf(locked[0])
	if op.State != StateExecuting {
		return op, nil
	}
	action, state, code := "operation.succeed", StateSucceeded, ""
	if succeeded {
		rows, err := q.MoneyActionOf(ctx, tx, id)
		if err != nil {
			return Operation{}, fmt.Errorf("read the reservation of operation %s: %w", id, err)
		}
		if len(rows) > 0 {
			if _, err := commit(ctx, tx, rows[0], op, proof, outcomeActor); err != nil {
				return Operation{}, err
			}
		}
	} else {
		action, state, code = "operation.fail", StateFailed, codeTargetFailed
		if err := release(ctx, tx, outcomeActor, op); err != nil {
			return Operation{}, err
		}
	}
	if err := setState(ctx, tx, &op, state, op.Result, code); err != nil {
		return Operation{}, err
	}
	return op, record(ctx, tx, outcomeActor, action, op, "", map[string]any{"proof": proof})
}
