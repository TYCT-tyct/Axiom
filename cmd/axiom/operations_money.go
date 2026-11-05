package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// The operations API's money writes (docs/domains/operations.md「资金操作上限」): a top-up of a supplier is proposed by
// the agent, paid by the owner and confirmed by the agent. The amount the caps count is the server's, from the
// supplier's unit value and the exchange rate, never the agent's; the agent's numbers are only the native amount it
// asks for and the proof it confirms with.

// nano is the money unit's scale: an amount is in billionths of one unit of its currency.
var nano = big.NewInt(1_000_000_000)

// SupplierTopUpPropose reserves a top-up of a supplier against the caps of its owner, at once or for the owner to approve.
func (o operationsAPI) SupplierTopUpPropose(ctx context.Context, r operationsapi.SupplierTopUpProposeRequestObject) (operationsapi.SupplierTopUpProposeResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	id, err := supplierIDOf(r.SupplierId)
	if err != nil {
		return nil, err
	}
	c, b := callOf(ctx), r.Body
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: "supplier_top_up_propose", Target: operations.Target{Kind: "supplier", ID: r.SupplierId},
		Params: map[string]any{"amount_native": b.AmountNative}, Risk: operations.RiskTopUp, Reason: b.Reason, Run: runOf(r.Params.XAxiomRunID),
	}
	op, replayed, err := o.money(ctx, b.DryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		return o.proposeTopUp(ctx, tx, id, p, b.AmountNative)
	})
	if err != nil {
		return nil, err
	}
	headers := operationsapi.SupplierTopUpPropose200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.SupplierTopUpPropose202JSONResponse{Body: operationBody(op), Headers: operationsapi.SupplierTopUpPropose202ResponseHeaders(headers)}, nil
	}
	return operationsapi.SupplierTopUpPropose200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// SupplierTopUpConfirm commits the reservation of one of the credential's top-up proposals, with the proof of payment.
func (o operationsAPI) SupplierTopUpConfirm(ctx context.Context, r operationsapi.SupplierTopUpConfirmRequestObject) (operationsapi.SupplierTopUpConfirmResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if _, err := supplierIDOf(r.SupplierId); err != nil {
		return nil, err
	}
	proposal, err := uuid.Parse(r.OperationId)
	if err != nil {
		return nil, operations.ErrNoOperation
	}
	c, b := callOf(ctx), r.Body
	target := operations.Target{Kind: "supplier", ID: r.SupplierId}
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: "supplier_top_up_confirm", Target: target,
		Params: map[string]any{"proposal": r.OperationId, "proof": b.Proof}, Risk: operations.RiskTopUp, Reason: b.Reason,
		Run: runOf(r.Params.XAxiomRunID),
	}
	op, replayed, err := o.money(ctx, b.DryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		op, replayed, err := o.propose(ctx, tx, p)
		if err != nil || replayed {
			return op, replayed, err
		}
		reservation, err := operations.CommitReservation(ctx, tx, op, proposal, target, b.Proof)
		if err != nil {
			return operations.Operation{}, false, err
		}
		return settleTopUp(ctx, tx, op, reservation)
	})
	if err != nil {
		return nil, err
	}
	return operationsapi.SupplierTopUpConfirm200JSONResponse{Body: operationBody(op), Headers: operationsapi.SupplierTopUpConfirm200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// money runs a money write in one transaction; a preview runs it to its end and rolls it back, so every check ran and
// nothing it wrote stays, the reservation included. A hard cap's refusal says which cap and by how much.
func (o operationsAPI) money(ctx context.Context, dryRun *bool, do func(tx pg.Tx) (operations.Operation, bool, error)) (operations.Operation, bool, error) {
	preview := dryRun != nil && *dryRun
	var op operations.Operation
	var replayed bool
	err := o.db.Tx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if op, replayed, err = do(tx); err == nil && preview {
			return errPreviewed
		}
		return err
	})
	if exceeded, ok := errors.AsType[*operations.CapExceeded](err); ok {
		return op, false, &apiError{status: http.StatusUnprocessableEntity, code: codeCapExceeded, message: strings.TrimPrefix(exceeded.Error(), "operations: ")}
	}
	if preview && errors.Is(err, errPreviewed) {
		if op.State == operations.StateSucceeded { // a preview says where the operation would start
			op.State = operations.StateApproved
		}
		return operations.Operation{Tool: op.Tool, Target: op.Target, Risk: op.Risk, State: op.State}, false, nil
	}
	return op, replayed, err
}

// proposeTopUp values a top-up of amountNative of supplier id and proposes it with the credential held; one within
// every autonomous cap succeeds in tx at once.
func (o operationsAPI) proposeTopUp(ctx context.Context, tx pg.Tx, id uuid.UUID, p operations.Proposal, amountNative string) (operations.Operation, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, false, err
	}
	// A retry answers with its operation whatever happened to the supplier since; only a first request is valued.
	if op, found, err := operations.Replay(ctx, tx, p); found || err != nil {
		return op, found, err
	}
	basis, err := o.admin.suppliers.TopUpBasis(ctx, tx, id)
	if err != nil {
		return operations.Operation{}, false, err
	}
	m, err := topUpTransfer(ctx, tx, basis, amountNative)
	if err != nil {
		return operations.Operation{}, false, err
	}
	op, replayed, err := operations.ProposeMoney(ctx, tx, p, m)
	if err != nil || replayed || op.State != operations.StateApproved {
		return op, replayed, err
	}
	reservation, _, err := operations.ReservationOf(ctx, tx, op.ID)
	if err != nil {
		return operations.Operation{}, false, err
	}
	return settleTopUp(ctx, tx, op, reservation)
}

// settleTopUp ends op succeeded with what its reservation is now.
func settleTopUp(ctx context.Context, tx pg.Tx, op operations.Operation, r operations.Reservation) (operations.Operation, bool, error) {
	result, err := topUpResult(r)
	if err != nil {
		return operations.Operation{}, false, err
	}
	op, err = operations.Settle(ctx, tx, op, result, "")
	return op, false, err
}

// topUpTransfer is what a top-up of amountNative of the supplier moves in its owner's cap currency: amountNative times
// the unit value, times the exchange rate when the supplier settles in another currency, rounded up to 10⁻⁹, so the
// caps never count less than the money. Every input is kept in the basis so the audit can recompute it.
func topUpTransfer(ctx context.Context, tx pg.Tx, s suppliers.TopUpBasis, amountNative string) (operations.Transfer, error) {
	switch {
	case s.Account || s.Status == suppliers.Removed || s.Status == suppliers.Removing:
		return operations.Transfer{}, &apiError{status: http.StatusConflict, code: codeConflict, message: "only a supplier of the platform or a site that is not removed is topped up"}
	case s.UnitValue == "":
		return operations.Transfer{}, &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the supplier has no confirmed unit value"}
	}
	native, ok1 := new(big.Rat).SetString(amountNative)
	unit, ok2 := new(big.Rat).SetString(s.UnitValue)
	if !ok1 || !ok2 || native.Sign() <= 0 {
		return operations.Transfer{}, invalidField("/amount_native", "must be a decimal above zero")
	}
	currency, err := operations.CapCurrency(ctx, tx, s.Site, operations.CategoryTopUp)
	if err != nil {
		return operations.Transfer{}, err
	}
	value := native.Mul(native, unit)
	basis := map[string]any{
		"amount_native": amountNative, "native_unit": s.NativeUnit, "unit_value_id": s.UnitValueID, "unit_value": s.UnitValue,
		"settlement_currency": s.SettlementCurrency,
	}
	if s.SettlementCurrency != currency.String() {
		if err := convert(ctx, tx, value, s.SettlementCurrency, currency, basis); err != nil {
			return operations.Transfer{}, err
		}
	}
	amount := nanosUp(value)
	if !amount.IsInt64() {
		return operations.Transfer{}, invalidField("/amount_native", "is more than a cap can count")
	}
	basis["amount"], basis["currency"] = amount.String(), currency.String()
	target := operations.Target{Kind: "supplier", ID: s.ID.String()}
	return operations.Transfer{
		Site: s.Site, Category: operations.CategoryTopUp, Object: target, Amount: amount.Int64(), Currency: currency, Basis: basis,
		Summary: map[string]any{
			"supplier": s.Name, "supplier_id": s.ID.String(), "amount_native": amountNative, "native_unit": s.NativeUnit,
			"unit_value": s.UnitValue, "settlement_currency": s.SettlementCurrency, "amount": amount.String(), "currency": currency.String(),
		},
	}, nil
}

// convert turns value from one currency into to at the current reference rate, and records the rate in basis.
func convert(ctx context.Context, tx pg.Tx, value *big.Rat, from string, to platform.Currency, basis map[string]any) error {
	rate, factor, err := currentRate(ctx, tx, from, to)
	if err != nil {
		return err
	}
	value.Mul(value, factor)
	basis["rate"] = map[string]any{
		"id": rate.ID.String(), "version": rate.Version, "base": rate.Base, "quote": rate.Quote,
		"numerator": rate.Numerator, "denominator": rate.Denominator,
	}
	return nil
}

// nanosUp is value in billionths, rounded up.
func nanosUp(value *big.Rat) *big.Int {
	amount, rest := new(big.Int).QuoRem(new(big.Int).Mul(value.Num(), nano), value.Denom(), new(big.Int))
	if rest.Sign() > 0 {
		amount.Add(amount, big.NewInt(1))
	}
	return amount
}

// currentRate is the current reference rate of the pair and how many units of to one unit of from is worth by it; a
// pair without one is refused, as nothing can be valued in a currency there is no rate to.
func currentRate(ctx context.Context, tx pg.Tx, from string, to platform.Currency) (platform.ExchangeRate, *big.Rat, error) {
	source, err := platform.ParseCurrency(from)
	if err != nil {
		return platform.ExchangeRate{}, nil, err
	}
	rates, err := billing.ExchangeRates(ctx, tx)
	if err != nil {
		return platform.ExchangeRate{}, nil, err
	}
	for _, r := range rates {
		if factor, ok := r.Basis().Between(source, to); ok {
			return r.Basis(), factor, nil
		}
	}
	return platform.ExchangeRate{}, nil, &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "no exchange rate from the supplier's currency to the cap's"}
}

// topUpResult is what a top-up operation answers: the amount its caps count, in which currency, what it was computed
// from, and where its reservation is.
func topUpResult(r operations.Reservation) (map[string]any, error) {
	var basis map[string]any
	if err := jsonx.Unmarshal(r.Basis, &basis); err != nil {
		return nil, fmt.Errorf("the basis of operation %s: %w", r.Operation, err)
	}
	return map[string]any{
		"amount": strconv.FormatInt(r.Amount, 10), "currency": r.Currency, "basis": basis, "reservation": r.State,
		"reserved_until": r.Expires.UTC().Format(time.RFC3339),
	}, nil
}

// runOf is the request's X-Axiom-Run-ID, or "" without one.
func runOf(run *string) string {
	if run == nil {
		return ""
	}
	return *run
}
