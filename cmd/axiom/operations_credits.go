package main

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API's credit grants (docs/domains/operations.md「转账的累计金额上限」): the agent proposes a gift to an
// account, which the admin's own rules for a grant bound, and the owner always approves it. Its face value is reserved
// against the site's money.credit caps while it waits; the approval gives the credit as the agent's and commits it.

// CreditGrant reserves a gift of credit to an account against its site's caps and asks the owner to approve it.
func (o operationsAPI) CreditGrant(ctx context.Context, r operationsapi.CreditGrantRequestObject) (operationsapi.CreditGrantResponseObject, error) {
	key, err := keyOf(r.Params.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	id, err := accountID(r.AccountId)
	if err != nil {
		return nil, err
	}
	c, b := callOf(ctx), r.Body
	params := map[string]any{"amount": b.Amount.Amount, "currency": b.Amount.Currency}
	if b.ExpiresAt != nil {
		params["expires_at"] = b.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	p := operations.Proposal{
		Credential: uuid.UUID(c.agent.ID()), Key: key, Tool: "credit_grant", Target: operations.Target{Kind: "account", ID: r.AccountId},
		Params: params, Risk: operations.RiskGift, Reason: b.Reason, Run: runOf(r.Params.XAxiomRunID),
	}
	op, replayed, err := o.money(ctx, b.DryRun, func(tx pg.Tx) (operations.Operation, bool, error) {
		return o.proposeCredit(ctx, tx, id, p, b)
	})
	if err != nil {
		return nil, err
	}
	headers := operationsapi.CreditGrant200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.CreditGrant202JSONResponse{Body: operationBody(op), Headers: operationsapi.CreditGrant202ResponseHeaders(headers)}, nil
	}
	return operationsapi.CreditGrant200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// proposeCredit reads the grant of b to account id as the admin's grant does and proposes it with the credential held.
// A credit grant always waits for the owner, so nothing is given here.
func (o operationsAPI) proposeCredit(ctx context.Context, tx pg.Tx, id accounts.AccountID, p operations.Proposal, b *operationsapi.CreditGrantProposal) (operations.Operation, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, false, err
	}
	if op, found, err := operations.Replay(ctx, tx, p); found || err != nil {
		return op, found, err
	}
	account, err := o.admin.accounts.Account(ctx, tx, id)
	if err != nil {
		return operations.Operation{}, false, err
	}
	site := uuid.UUID(account.Site)
	if !reaches(callOf(ctx).agent.Info(), &site) {
		return operations.Operation{}, false, accounts.ErrNotFound
	}
	currency, err := o.admin.currencyOf(account.Site)
	if err != nil {
		return operations.Operation{}, false, err
	}
	amount := adminapi.Money(b.Amount)
	g, err := grantOf(&adminapi.CreditGrantRequest{Amount: &amount, ExpiresAt: b.ExpiresAt, Reason: b.Reason}, currency, time.Now())
	if err != nil {
		return operations.Operation{}, false, err
	}
	m, err := creditTransfer(ctx, tx, account, currency, g)
	if err != nil {
		return operations.Operation{}, false, err
	}
	return operations.ProposeMoney(ctx, tx, p, m)
}

// reaches is whether a credential acts in site, nil for the platform: the platform with its platform grant, a site
// when it is one of its sites or it is kept to none.
func reaches(self accounts.AgentInfo, site *uuid.UUID) bool {
	if site == nil {
		return self.Platform
	}
	return self.Sites == nil || slices.Contains(self.Sites, accounts.SiteID(*site))
}

// creditTransfer is what a gift of g to account moves in the currency of its site's credit caps: the face value, at
// the current reference rate when the caps count in another currency than the site's, rounded up to 10⁻⁹.
func creditTransfer(ctx context.Context, tx pg.Tx, account accounts.AccountDetail, currency platform.Currency, g creditInput) (operations.Transfer, error) {
	site := uuid.UUID(account.Site)
	capCurrency, err := operations.CapCurrency(ctx, tx, &site, operations.CategoryCredit)
	if err != nil {
		return operations.Transfer{}, err
	}
	face := strconv.FormatInt(g.amount, 10)
	basis := map[string]any{"amount_site": face, "site_currency": currency.String()}
	amount := g.amount
	if capCurrency != currency {
		value := big.NewRat(g.amount, 1_000_000_000)
		if err := convert(ctx, tx, value, currency.String(), capCurrency, basis); err != nil {
			return operations.Transfer{}, err
		}
		n := nanosUp(value)
		if !n.IsInt64() {
			return operations.Transfer{}, invalidField("/amount/amount", "is more than a cap can count")
		}
		amount = n.Int64()
	}
	basis["amount"], basis["currency"] = strconv.FormatInt(amount, 10), capCurrency.String()
	summary := map[string]any{
		"account_id": account.ID.TypeID(), "account": account.Name, "site_id": account.Site.TypeID(), "amount": face,
		"currency": currency.String(),
	}
	if g.expires != nil {
		summary["expires_at"] = g.expires.Format(time.RFC3339Nano)
	}
	return operations.Transfer{
		Site: &site, Category: operations.CategoryCredit, Object: operations.Target{Kind: "account", ID: account.ID.TypeID()},
		Amount: amount, Currency: capCurrency, Basis: basis, Summary: summary,
	}, nil
}

// runCreditGrant gives the credit op asks for, as the agent's, once the owner approved it: the batch takes the
// operation's ID, so it is given once, and the reservation, rechecked first, is committed with it. An account gone,
// moved to another currency, or an expiry passed since the proposal is refused, which ends op rejected and releases it.
func (a adminAPI) runCreditGrant(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	if _, err := operations.CheckReservation(ctx, tx, op); err != nil {
		return nil, err
	}
	var params struct {
		Amount    string     `json:"amount"`
		Currency  string     `json:"currency"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	id, err := accountID(op.Target.ID)
	if err != nil {
		return nil, err
	}
	account, err := a.accounts.Account(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	currency, err := a.currencyOf(account.Site)
	if err != nil {
		return nil, err
	}
	g, err := grantOf(&adminapi.CreditGrantRequest{
		Amount: &adminapi.Money{Amount: params.Amount, Currency: params.Currency}, ExpiresAt: params.ExpiresAt, Reason: op.Reason,
	}, currency, time.Now())
	if err != nil {
		return nil, err
	}
	if err := billing.AddCredit(ctx, tx, billing.Credit{
		ID: op.ID, Site: uuid.UUID(account.Site), Account: uuid.UUID(id), Amount: g.amount, ExpiresAt: g.expires,
		Actor: audit.OperationsAgent(op.Credential), Reason: op.Reason,
	}); err != nil {
		return nil, err
	}
	r, err := operations.CommitApproved(ctx, tx, op, "credit_batch:"+op.ID.String())
	if err != nil {
		return nil, err
	}
	result, err := moneyResult(r)
	if err != nil {
		return nil, err
	}
	result["credit_batch_id"] = op.ID.String()
	return result, nil
}
