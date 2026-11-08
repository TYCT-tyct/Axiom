package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// State is where an operation is in its life (docs/domains/operations.md「运营接口」): pending_approval and approved have
// not started; executing has; the others are where it ended, and a finished operation is never reused.
type State string

// The states an operation is in today; executing, failed, unknown and reverted come with the external calls.
const (
	StatePending   State = "pending_approval"
	StateApproved  State = "approved"
	StateSucceeded State = "succeeded"
	StateCanceled  State = "canceled"
	StateExpired   State = "expired"
	StateRejected  State = "rejected"
)

// Risk is an operation's class, which decides whether it waits for a person (「风险分级与审批」).
type Risk string

// The classes of the writes there are today.
const (
	RiskRoutine   Risk = "routine"
	RiskDelisting Risk = "delisting"
)

// ApprovalState is where an approval is: pending until a person decides, it expires, or the agent withdraws it.
type ApprovalState string

// The states of an approval.
const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalRejected ApprovalState = "rejected"
	ApprovalExpired  ApprovalState = "expired"
	ApprovalCanceled ApprovalState = "canceled"
)

// approvalTTL is the longest a pending approval waits for a person; it never outlives the UTC day it was asked on, so
// a decision is not made on a day other than the proposal's.
const approvalTTL = 30 * time.Minute

// approvalSignal is the alert signal of a pending approval, keyed by its ID: raised when it is asked for and resolved
// when it is decided, expires or is withdrawn, so the owner is told through the notification channels a rule for this
// kind names (docs/domains/observability.md「通知渠道」).
const approvalSignal = "operations.approval_pending"

var (
	// ErrKeyReused is an Idempotency-Key the credential used before for another request.
	ErrKeyReused = errors.New("operations: the idempotency key was used for another request")
	// ErrNoOperation is an operation that does not exist or is another credential's.
	ErrNoOperation = errors.New("operations: no such operation")
	// ErrNoApproval is an approval that does not exist or is of another credential's operation.
	ErrNoApproval = errors.New("operations: no such approval")
	// ErrNotPending is an approval already decided, expired or withdrawn.
	ErrNotPending = errors.New("operations: the approval is not pending")
	// ErrParamsTooLarge is a write whose parameters are more than an operation keeps (maxParams).
	ErrParamsTooLarge = errors.New("operations: the parameters are larger than an operation keeps (8 KiB)")
)

// maxParams is the most an operation's canonical parameters take, as the table's CHECK holds them: what is approved is
// what runs, so they are kept whole or refused.
const maxParams = 8192

// Reason is why a proposal waits for a person, as the server decided it when the agent asked: the decision's own
// output, so a page never derives it again from the summary (OperationsApprovalReason in api/admin.yaml).
type Reason string

// ReasonAlways is a class of operation that waits for a person whatever it moves: a credit grant, retiring a model.
// The cap's and the envelope's reasons are in caps.go and prices.go.
const ReasonAlways Reason = "always"

// Target is the one object an operation acts on: its kind, such as model, and its ID.
type Target struct{ Kind, ID string }

// Proposal is a write the agent asks for: which credential with which Idempotency-Key, the tool, its target and
// parameters, the risk and the agent's reason. Summary, when not nil, makes the operation wait for a person, who is
// shown it: what the server read of the target, never the agent's words; Reasons, at least one then, say why it waits.
type Proposal struct {
	Credential uuid.UUID
	Key        uuid.UUID
	Tool       string
	Target     Target
	Params     map[string]any
	Risk       Risk
	Reason     string
	Run        string
	Summary    map[string]any
	Reasons    []Reason
}

// Operation is one write of the operations API. Params and Result are canonical JSON; Error is the code it ended with;
// Approval is set when it waits or waited for a person.
type Operation struct {
	ID         uuid.UUID
	Credential uuid.UUID
	Tool       string
	Target     Target
	Params     []byte
	Risk       Risk
	State      State
	Reason     string
	Run        string
	Result     []byte
	Error      string
	Created    time.Time
	Updated    time.Time
	Approval   *Approval
}

// Approval is a person's decision an operation waits or waited on. Summary is canonical JSON; Reasons are why it waits,
// nil when an older node asked for it; Decidable is whether a person can still decide it, by the database's clock when
// it was read, as Decide checks it; DecidedBy is the actor who decided, expired or withdrew it.
type Approval struct {
	ID             uuid.UUID
	Operation      uuid.UUID
	State          ApprovalState
	Summary        []byte
	Reasons        []Reason
	Decidable      bool
	Requested      time.Time
	Expires        time.Time
	Decided        *time.Time
	DecidedBy      string
	DecisionReason string
}

// Propose records p as an operation in tx, which must be in the platform scope: approved when it needs no person, so
// the caller runs it in tx and settles it, otherwise pending_approval with its approval, recorded in the audit log and
// signalled to the owner. A retry with the same key and request finds the first operation (replayed) as it is now; the
// same key with another request is ErrKeyReused. A second request with the key waits for the first's transaction.
func Propose(ctx context.Context, tx pg.Tx, p Proposal) (op Operation, replayed bool, err error) {
	params, err := jsonx.MarshalCanonical(p.Params)
	if err != nil {
		return Operation{}, false, fmt.Errorf("propose %s: encode the parameters: %w", p.Tool, err)
	}
	if len(params) > maxParams {
		return Operation{}, false, ErrParamsTooLarge
	}
	hash := requestHash(p.Tool, p.Target, params)
	state := StateApproved
	if p.Summary != nil {
		state = StatePending
	}
	q := New()
	id := uuid.NewV7()
	inserted, err := q.InsertOperation(ctx, tx, InsertOperationParams{
		ID: id, CredentialID: p.Credential, IdempotencyKey: p.Key, RequestHash: hash[:], Tool: p.Tool,
		TargetKind: p.Target.Kind, TargetID: p.Target.ID, Params: params, Risk: string(p.Risk), State: string(state),
		Reason: p.Reason, RunID: optionalText(p.Run),
	})
	if err != nil {
		return Operation{}, false, fmt.Errorf("propose %s: %w", p.Tool, err)
	}
	if len(inserted) == 0 {
		row, err := q.OperationByKey(ctx, tx, OperationByKeyParams{CredentialID: p.Credential, IdempotencyKey: p.Key})
		if err != nil {
			return Operation{}, false, fmt.Errorf("propose %s: the key's operation: %w", p.Tool, err)
		}
		if !bytes.Equal(row.RequestHash, hash[:]) {
			return Operation{}, false, ErrKeyReused
		}
		op, err := withApproval(ctx, tx, operationOf(row))
		return op, true, err
	}
	op, err = Get(ctx, tx, id)
	if err != nil || p.Summary == nil {
		return op, false, err
	}
	return op, false, requestApproval(ctx, tx, &op, p.Summary, p.Reasons)
}

// requestHash tells requests under one key apart: the same tool, target and parameters are the same request.
func requestHash(tool string, t Target, params []byte) [32]byte {
	return sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%s", tool, t.Kind, t.ID, params))
}

// requestApproval asks a person to decide op for reasons, which waits until approvalTTL has passed at most.
func requestApproval(ctx context.Context, tx pg.Tx, op *Operation, summary map[string]any, reasons []Reason) error {
	body, err := jsonx.MarshalCanonical(summary)
	if err != nil {
		return fmt.Errorf("request an approval: encode the summary: %w", err)
	}
	row, err := New().InsertApproval(ctx, tx, InsertApprovalParams{
		ID: uuid.NewV7(), OperationID: op.ID, Summary: body, Reasons: reasonTexts(reasons), TtlSeconds: approvalTTL.Seconds(),
	})
	if err != nil {
		return fmt.Errorf("request an approval: %w", err)
	}
	// It expires after the clock it was asked by (InsertApproval), so it is decidable as it is made.
	op.Approval = approvalOf(row, true)
	if err := record(ctx, tx, audit.OperationsAgent(op.Credential), "approval.request", *op, op.Reason, nil); err != nil {
		return err
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: approvalSignal, Key: row.ID.String(), Severity: "warning",
		Fields: map[string]string{"tool": op.Tool, "target": op.Target.Kind + ":" + op.Target.ID, "expires_at": row.ExpiresAt.UTC().Format(time.RFC3339)},
	})
}

// Settle ends op, which its caller locked as approved and then ran in the same transaction: succeeded with what the
// change answered, or rejected with the error code of the target's refusal when the target changed after approval.
// It is recorded in the audit log as the agent's.
func Settle(ctx context.Context, tx pg.Tx, op Operation, result map[string]any, refused string) (Operation, error) {
	if op.State != StateApproved {
		return Operation{}, fmt.Errorf("settle operation %s: it is %s, not approved", op.ID, op.State)
	}
	action, state := "operation.succeed", StateSucceeded
	var body []byte
	if refused != "" {
		action, state = "operation.reject", StateRejected
	} else {
		var err error
		if body, err = jsonx.MarshalCanonical(result); err != nil {
			return Operation{}, fmt.Errorf("settle operation %s: encode the result: %w", op.ID, err)
		}
	}
	if err := setState(ctx, tx, &op, state, body, refused); err != nil {
		return Operation{}, err
	}
	if err := record(ctx, tx, audit.OperationsAgent(op.Credential), action, op, op.Reason, nil); err != nil {
		return Operation{}, err
	}
	if refused != "" {
		if err := release(ctx, tx, audit.OperationsAgent(op.Credential), op); err != nil {
			return Operation{}, err
		}
	}
	if op.Approval != nil { // an approved one: its signal is resolved last, after every row lock its run took
		return op, resolve(ctx, tx, op.Approval.ID)
	}
	return op, nil
}

// Decide is a person's decision on approval id: approve leaves its operation approved for the caller to run in tx and
// Settle, reject ends it. An approval that is no longer pending, or is past its expiry though the sweep has not reached
// it, is ErrNotPending, and nothing is written.
func Decide(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, approve bool, reason string) (Operation, error) {
	op, err := lockApproval(ctx, tx, id, uuid.UUID{})
	if err != nil {
		return Operation{}, err
	}
	if approve { // Settle resolves the signal once the caller has run it
		return op, finish(ctx, tx, actor, "approval.approve", &op, StateApproved, ApprovalApproved, reason)
	}
	if err := finish(ctx, tx, actor, "approval.reject", &op, StateRejected, ApprovalRejected, reason); err != nil {
		return Operation{}, err
	}
	return op, resolve(ctx, tx, op.Approval.ID)
}

// Withdraw is the agent taking back its own pending proposal; another credential's approval is ErrNoApproval.
func Withdraw(ctx context.Context, tx pg.Tx, credential, id uuid.UUID, reason string) (Operation, error) {
	op, err := lockApproval(ctx, tx, id, credential)
	if err != nil {
		return Operation{}, err
	}
	if err := finish(ctx, tx, audit.OperationsAgent(credential), "operation.cancel", &op, StateCanceled, ApprovalCanceled, reason); err != nil {
		return Operation{}, err
	}
	return op, resolve(ctx, tx, op.Approval.ID)
}

// Cancel ends pending approval id and its operation as actor's, whoever proposed it: the proposal of a credential that
// is no longer live, which a node without CancelLive left pending when it revoked the credential.
func Cancel(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, reason string) (Operation, error) {
	op, err := lockApproval(ctx, tx, id, uuid.UUID{})
	if err != nil {
		return Operation{}, err
	}
	if err := finish(ctx, tx, actor, "operation.cancel", &op, StateCanceled, ApprovalCanceled, reason); err != nil {
		return Operation{}, err
	}
	return op, resolve(ctx, tx, op.Approval.ID)
}

// CancelLive cancels, as actor, the proposals a credential is waiting on, or every credential's when credential is nil:
// the revocation of a credential and the emergency stop, in their transaction. It returns how many it canceled. An
// approved operation runs and settles in the transaction that approved it, so there is no other kind to cancel.
func CancelLive(ctx context.Context, tx pg.Tx, actor audit.Actor, credential *uuid.UUID, reason string) (int, error) {
	params := LockLiveOperationsParams{Every: credential == nil}
	if credential != nil {
		params.CredentialID = *credential
	}
	ids, err := New().LockLiveOperations(ctx, tx, params)
	if err != nil {
		return 0, fmt.Errorf("cancel the operations not started: %w", err)
	}
	return len(ids), endLocked(ctx, tx, ids, actor, StateCanceled, ApprovalCanceled, "operation.cancel", reason)
}

// expiryActor is who expires an approval nobody decided in time.
var expiryActor = audit.System("operations.approval_expiry")

// ExpireDue expires up to batch pending approvals past their expiry, their operations with them, and returns how many.
// Rows another sweep holds are skipped; a decision that took the lock first wins, and the sweep finds it decided.
func ExpireDue(ctx context.Context, tx pg.Tx, batch int) (int, error) {
	ids, err := New().LockDueOperations(ctx, tx, int32(batch)) // #nosec G115 -- a small constant
	if err != nil {
		return 0, fmt.Errorf("expire the approvals past their time: %w", err)
	}
	return len(ids), endLocked(ctx, tx, ids, expiryActor, StateExpired, ApprovalExpired, "approval.expire", "")
}

// endLocked ends the pending operations ids, which tx has locked, with their approvals, and resolves their signals only
// after all of them: a batch that took the signals' lock after the first would hold it while it waits for the next
// one's counters, which a proposal holding them waits on the signals' lock to release.
func endLocked(ctx context.Context, tx pg.Tx, ids []uuid.UUID, actor audit.Actor, state State, decided ApprovalState, action, reason string) error {
	approvals := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		op, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := cancelLocked(ctx, tx, actor, &op, state, decided, action, reason); err != nil {
			return err
		}
		approvals[i] = op.Approval.ID
	}
	return resolve(ctx, tx, approvals...)
}

// cancelLocked ends op, which tx has locked while it is pending_approval, and its approval, which moves with it.
func cancelLocked(ctx context.Context, tx pg.Tx, actor audit.Actor, op *Operation, state State, decided ApprovalState, action, reason string) error {
	rows, err := New().LockApprovalOf(ctx, tx, op.ID)
	if err != nil || len(rows) == 0 {
		return fmt.Errorf("lock the approval of operation %s: %w", op.ID, err)
	}
	op.Approval = approvalOf(rows[0].OperationsApproval, false)
	return finish(ctx, tx, actor, action, op, state, decided, reason)
}

// lockApproval locks approval id's operation, then the approval, in the order every path takes, and returns the
// operation if the approval is still pending and in time. A credential other than the zero ID must own it.
func lockApproval(ctx context.Context, tx pg.Tx, id, credential uuid.UUID) (Operation, error) {
	q := New()
	found, err := q.ApprovalByID(ctx, tx, id)
	if err != nil {
		return Operation{}, fmt.Errorf("read approval %s: %w", id, err)
	}
	if len(found) == 0 {
		return Operation{}, ErrNoApproval
	}
	locked, err := q.LockOperation(ctx, tx, found[0].OperationID)
	if err != nil || len(locked) == 0 {
		return Operation{}, fmt.Errorf("lock the operation of approval %s: %w", id, err)
	}
	op := operationOf(locked[0])
	if credential != (uuid.UUID{}) && op.Credential != credential {
		return Operation{}, ErrNoApproval
	}
	rows, err := q.LockApprovalOf(ctx, tx, op.ID)
	if err != nil || len(rows) == 0 {
		return Operation{}, fmt.Errorf("lock approval %s: %w", id, err)
	}
	op.Approval = approvalOf(rows[0].OperationsApproval, rows[0].OperationsApproval.State == string(ApprovalPending) && !rows[0].Due)
	if !op.Approval.Decidable {
		return Operation{}, ErrNotPending
	}
	return op, nil
}

// finish ends op's pending approval as decided and moves op to state and records it. One that ends without its change
// releases its reservation; one approved holds its reservation's rows for the caller's run, in the order every path
// takes them. The approval's signal is left to the caller to resolve last.
func finish(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, op *Operation, state State, decided ApprovalState, reason string) error {
	at, err := New().DecideApproval(ctx, tx, DecideApprovalParams{
		ID: op.Approval.ID, State: string(decided), DecidedBy: new(actor.String()), DecisionReason: optionalText(reason),
	})
	if err != nil {
		return fmt.Errorf("decide approval %s: %w", op.Approval.ID, err)
	}
	op.Approval.State, op.Approval.Decidable, op.Approval.Decided = decided, false, at
	op.Approval.DecidedBy, op.Approval.DecisionReason = actor.String(), reason
	if err := setState(ctx, tx, op, state, nil, ""); err != nil {
		return err
	}
	if err := record(ctx, tx, actor, action, *op, reason, map[string]any{"approval_id": op.Approval.ID.String()}); err != nil {
		return err
	}
	if state == StateApproved {
		return holdReservation(ctx, tx, op.ID)
	}
	return release(ctx, tx, actor, *op)
}

// resolve resolves the signals of approvals. alert.Persist holds one lock every producer shares until tx ends, so it
// is the last lock any path of this module takes: a transaction holding it never waits for a row another one holds.
func resolve(ctx context.Context, tx pg.Tx, approvals ...uuid.UUID) error {
	for _, id := range approvals {
		if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: approvalSignal, Key: id.String(), Severity: "info", Resolved: true}); err != nil {
			return err
		}
	}
	return nil
}

func setState(ctx context.Context, tx pg.Tx, op *Operation, state State, result []byte, code string) error {
	updated, err := New().SetOperationState(ctx, tx, SetOperationStateParams{
		ID: op.ID, State: string(state), Result: result, ErrorCode: optionalText(code),
	})
	if err != nil {
		return fmt.Errorf("move operation %s to %s: %w", op.ID, state, err)
	}
	op.State, op.Result, op.Error, op.Updated = state, result, code, updated
	return nil
}

// record writes the audit entry of action on op. The detail names the operation, its tool and target and where it is
// now; the reason is the actor's own words, untrusted when the actor is the agent.
func record(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, op Operation, reason string, extra map[string]any) error {
	detail := map[string]any{
		"operation_id": op.ID.String(), "tool": op.Tool, "target_kind": op.Target.Kind, "target_id": op.Target.ID,
		"state": string(op.State),
	}
	if op.Error != "" {
		detail["error_code"] = op.Error
	}
	maps.Copy(detail, extra)
	kind, id := "operation", op.ID.String()
	if op.Approval != nil {
		kind, id = "approval", op.Approval.ID.String()
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason, Detail: detail,
		Target: audit.Target{Module: "operations", Kind: kind, ID: id},
	})
}
