package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/operations"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API's refunds (docs/domains/operations.md「转账的累计金额上限」): the agent proposes returning part of
// a paid order through its channel, and the owner always approves it. The cash the payments module quotes is reserved
// against the order site's money.refund caps; the approval freezes the credits and approves the refund as the owner's,
// leaving the operation executing; a job ends it by the refund's outcome, committing or releasing the reservation.

// refundTool is the tool whose approval hands the refund to the channel, which ends it after the approval commits.
const refundTool = "refund_request"

// RefundRequest quotes a refund of part of an order, reserves its cash against its site's caps and asks the owner to
// approve it.
func (o operationsAPI) RefundRequest(ctx context.Context, r operationsapi.RefundRequestRequestObject) (operationsapi.RefundRequestResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	order, err := payments.ParseOrderID(r.OrderId)
	if err != nil {
		return nil, payments.ErrNotFound
	}
	service, err := o.admin.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.PrepareAgentRefund(ctx, order); err != nil {
		return nil, err
	}
	c, b := callOf(ctx), r.Body
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: refundTool, Target: operations.Target{Kind: "order", ID: r.OrderId},
		Params: map[string]any{"amount": b.Amount.Amount, "currency": b.Amount.Currency}, Risk: operations.RiskRefund,
		Reason: b.Reason, Run: runOf(r.Params.XAxiomRunID),
	}
	op, replayed, err := o.runWrite(ctx, b.DryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		return o.proposeRefund(ctx, tx, service, order, p, b.Amount)
	})
	if err != nil {
		return nil, agentRefundProblem(err)
	}
	headers := operationsapi.RefundRequest200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.RefundRequest202JSONResponse{Body: operationBody(op), Headers: operationsapi.RefundRequest202ResponseHeaders(headers)}, nil
	}
	return operationsapi.RefundRequest200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// agentRefundProblem answers a refusal of the payments module as the operations contract does: a merchant that does not
// refund through its channel is a conflict of the order's state, not the credential's permission, and the payments
// module's own field checks are the request's.
func agentRefundProblem(err error) error {
	if errors.Is(err, payments.ErrRefundForbidden) {
		return &apiError{status: http.StatusConflict, code: codeConflict, message: "the order's merchant account does not refund through its channel"}
	}
	if e, ok := errors.AsType[*payments.FieldError](err); ok {
		return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: e.Reason}
	}
	return err
}

// proposeRefund quotes the refund of amount of order with the credential held, as the approval would freeze it, and
// proposes it; a refund always waits for the owner, so nothing is frozen here.
func (o operationsAPI) proposeRefund(ctx context.Context, tx pg.Tx, service *payments.Payments, order payments.OrderID, p operations.Proposal, amount operationsapi.Money) (operations.Operation, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, false, err
	}
	if op, found, err := operations.Replay(ctx, tx, p); found || err != nil {
		return op, found, err
	}
	owner, err := payments.OrderOwnerTx(ctx, tx, order)
	if err != nil {
		return operations.Operation{}, false, err
	}
	if !reaches(callOf(ctx).agent.Info(), &owner.Site) {
		return operations.Operation{}, false, payments.ErrNotFound
	}
	account, err := o.admin.accounts.Account(ctx, tx, accounts.AccountID(owner.Account))
	if err != nil {
		return operations.Operation{}, false, err
	}
	currency, err := o.admin.currencyOf(account.Site)
	if err != nil {
		return operations.Operation{}, false, err
	}
	credits, err := refundCredits(amount, currency)
	if err != nil {
		return operations.Operation{}, false, err
	}
	quote, err := service.QuoteAgentRefundTx(ctx, tx, payments.AgentRefund{Order: order, Credits: credits, Reason: p.Reason, Agent: p.Credential, Key: p.Key.String()})
	if err != nil {
		return operations.Operation{}, false, err
	}
	if quote.Credits != credits { // the batch has less left than the order's terms allow: the refund would shrink
		return operations.Operation{}, false, &apiError{
			status: http.StatusUnprocessableEntity, code: codeUnprocessable,
			message: fmt.Sprintf("only %d billionths of the order's credits are unused now", quote.Credits),
		}
	}
	m, err := refundTransfer(ctx, tx, account, currency, quote)
	if err != nil {
		return operations.Operation{}, false, err
	}
	return operations.ProposeMoney(ctx, tx, p, m)
}

// refundCredits reads the credits to refund: a positive amount in the currency of the order's site.
func refundCredits(m operationsapi.Money, site platform.Currency) (int64, error) {
	if m.Currency != site.String() {
		return 0, invalidField("/amount/currency", "is not the currency of the order's site")
	}
	n, err := strconv.ParseInt(m.Amount, 10, 64)
	if err != nil || n <= 0 {
		return 0, &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the amount is a positive number of billionths that fits 64 bits"}
	}
	return n, nil
}

// refundTransfer is what refund q moves in the currency of its site's refund caps: the cash it returns, converted at the
// current reference rate when it is paid in another currency, rounded up to 10⁻⁹, so the caps never count less than
// the money. The credits, the cash and the rate are kept in the basis; the approval checks it would freeze the same.
func refundTransfer(ctx context.Context, tx pg.Tx, account accounts.AccountDetail, site platform.Currency, q payments.Refund) (operations.Transfer, error) {
	scope := uuid.UUID(account.Site)
	currency, err := operations.CapCurrency(ctx, tx, &scope, operations.CategoryRefund)
	if err != nil {
		return operations.Transfer{}, err
	}
	cash, err := billing.FromMinor(q.Amount.Currency, q.Amount.Minor)
	if err != nil {
		return operations.Transfer{}, err
	}
	basis := map[string]any{
		"credits": strconv.FormatInt(q.Credits, 10), "site_currency": site.String(),
		"cash": strconv.FormatInt(q.Amount.Minor, 10), "pay_currency": q.Amount.Currency.String(),
	}
	value := big.NewRat(cash, 1_000_000_000)
	if q.Amount.Currency != currency {
		if err := convert(ctx, tx, value, q.Amount.Currency.String(), currency, basis); err != nil {
			return operations.Transfer{}, err
		}
	}
	amount := nanosUp(value)
	if !amount.IsInt64() {
		return operations.Transfer{}, invalidField("/amount/amount", "is more than a cap can count")
	}
	basis["amount"], basis["currency"] = amount.String(), currency.String()
	return operations.Transfer{
		Site: &scope, Category: operations.CategoryRefund, Object: operations.Target{Kind: "account", ID: account.ID.TypeID()},
		Amount: amount.Int64(), Currency: currency, Basis: basis,
		Summary: map[string]any{
			"order_id": q.Order.TypeID(), "account_id": account.ID.TypeID(), "account": account.Name, "email": account.Owner.Email,
			"site_id": account.Site.TypeID(), "credits": basis["credits"], "site_currency": site.String(),
			"cash": basis["cash"], "pay_currency": basis["pay_currency"], "amount": amount.String(), "currency": currency.String(),
		},
	}, nil
}

// runRefundRequest freezes and approves the refund op asks for, as the agent's request and the owner's approval, once
// the owner approved the operation; the channel sends it after the approval commits. The reservation is rechecked
// first. An order changed since the quote, so that the refund would freeze or return another amount, is a stale
// revision, and a refusal of the payments module is the request's: either ends op rejected and releases it, with
// nothing frozen.
func (a adminAPI) runRefundRequest(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	reservation, err := operations.CheckReservation(ctx, tx, op)
	if err != nil {
		return nil, err
	}
	var params struct {
		Amount string `json:"amount"`
	}
	var quoted struct {
		Credits     string `json:"credits"`
		Cash        string `json:"cash"`
		PayCurrency string `json:"pay_currency"`
	}
	if err := errors.Join(jsonx.Unmarshal(op.Params, &params), jsonx.Unmarshal(reservation.Basis, &quoted)); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	order, err := payments.ParseOrderID(op.Target.ID)
	if err != nil {
		return nil, err
	}
	credits, err := strconv.ParseInt(params.Amount, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("the amount of operation %s: %w", op.ID, err)
	}
	service, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	refund, err := service.RequestAgentRefundTx(ctx, tx, payments.AgentRefund{
		Order: order, Credits: credits, Reason: op.Reason, Agent: op.Credential, Key: op.ID.String(),
		Approver: uuid.UUID(callOf(ctx).principal.UserID()), ApprovalReason: op.Approval.DecisionReason,
	})
	if err != nil {
		return nil, agentRefundProblem(err)
	}
	if strconv.FormatInt(refund.Credits, 10) != quoted.Credits || strconv.FormatInt(refund.Amount.Minor, 10) != quoted.Cash || refund.Amount.Currency.String() != quoted.PayCurrency {
		return nil, &apiError{status: http.StatusConflict, code: codeStaleRevision, message: "the order changed since the refund was quoted"}
	}
	result, err := moneyResult(reservation)
	if err != nil {
		return nil, err
	}
	result["refund_id"], result["refund_approved"] = refund.ID.TypeID(), refund.Approved
	return result, nil
}

// prepareRefundRun advances the ledger of the order a refund operation, approval id's, would refund, before the
// approval's transaction holds the order and the account's balance (payments.PrepareAgentRefund). Any other operation
// needs nothing.
func (a adminAPI) prepareRefundRun(ctx context.Context, id uuid.UUID) error {
	var op operations.Operation
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		op, err = operations.ApprovalOperation(ctx, tx, id)
		return err
	})
	if err != nil || op.Tool != refundTool {
		return err
	}
	order, err := payments.ParseOrderID(op.Target.ID)
	if err != nil {
		return fmt.Errorf("the order of operation %s: %w", op.ID, err)
	}
	service, err := a.paymentsService(ctx)
	if err != nil {
		return err
	}
	return service.PrepareAgentRefund(ctx, order)
}

// refundOutcomeBatch bounds one read of the executing refunds.
const refundOutcomeBatch = 100

// refundOutcomeArgs is one run of the job that ends the agent's executing refunds by their outcome.
type refundOutcomeArgs struct{}

// Kind implements river.JobArgs.
func (refundOutcomeArgs) Kind() string { return "operations.refund_outcome" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time, on the money queue with the caps' other jobs.
func (refundOutcomeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// registerRefundOutcomes adds the job to queue: every minute, each refund the agent's operations handed to a channel
// that has ended, succeeded or failed, ends its operation the same way. A refund still in flight, or whose outcome is
// unknown, keeps its operation executing and its reservation held. A failed run is logged and not retried: the next one
// comes within the minute.
func registerRefundOutcomes(db *pg.DB, queue *pg.Queue, service *payments.Payments, logger *slog.Logger) error {
	work := func(ctx context.Context, _ *river.Job[refundOutcomeArgs]) error {
		if err := concludeRefunds(ctx, db, service); err != nil {
			logger.ErrorContext(ctx, "operations: the outcome of the agent's refunds was not read", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return refundOutcomeArgs{} })
	return nil
}

// concludeRefunds ends every executing refund operation whose refund has ended, one transaction each. The refund's
// state is final once it is succeeded or failed, so a conclusion another node made first changes nothing.
func concludeRefunds(ctx context.Context, db *pg.DB, service *payments.Payments) error {
	var errs []error
	for after := (uuid.UUID{}); ; {
		var ops []operations.Operation
		if err := db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) (err error) {
			ops, err = operations.Executing(ctx, tx, refundTool, after, refundOutcomeBatch)
			return err
		}); err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, op := range ops {
			errs = append(errs, concludeRefund(ctx, db, service, op))
		}
		if len(ops) < refundOutcomeBatch {
			return errors.Join(errs...)
		}
		after = ops[len(ops)-1].ID
	}
}

// concludeRefund ends op by its refund's outcome, if the refund has ended.
func concludeRefund(ctx context.Context, db *pg.DB, service *payments.Payments, op operations.Operation) error {
	order, err := payments.ParseOrderID(op.Target.ID)
	if err != nil {
		return fmt.Errorf("operation %s: %w", op.ID, err)
	}
	refund, found, err := service.AgentRefundOf(ctx, order, op.ID.String())
	switch {
	case err != nil:
		return fmt.Errorf("the refund of operation %s: %w", op.ID, err)
	case !found: // made in the approval's transaction with the operation's start, so never absent
		return fmt.Errorf("operation %s is executing without its refund", op.ID)
	case refund.State != "succeeded" && refund.State != "failed":
		return nil
	}
	return db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := operations.Conclude(ctx, tx, op.ID, refund.State == "succeeded", refund.ID.TypeID())
		return err
	})
}
