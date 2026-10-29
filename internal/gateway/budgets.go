package gateway

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

type budgetView struct {
	GuardrailID *string    `json:"guardrail_id"`
	BudgetID    *string    `json:"budget_id"`
	IncludeBYOK bool       `json:"include_byok"`
	Scope       string     `json:"scope"`
	Limit       float64    `json:"limit_usd"`
	Spend       float64    `json:"spend_usd"`
	Reserved    float64    `json:"reserved_usd"`
	Remaining   float64    `json:"remaining_usd"`
	Reset       *string    `json:"reset_interval"`
	ResetsAt    *time.Time `json:"resets_at"`
}

func (h *Handler) keyRateLimit(a accounts.Admission) map[string]any {
	account, key := a.RateLimits()
	effective := h.Limits.cfg.KeyRate
	for _, n := range [...]int{account, key} {
		if n > 0 {
			effective = min(effective, n)
		}
	}
	return map[string]any{"requests": effective, "interval": h.Limits.cfg.RateWindow.String(), "account_requests": account, "key_requests": key}
}

func keyBudgets(ctx context.Context, tx pg.Tx, a accounts.Admission, keyUsage billing.KeyUsage) ([]budgetView, *budgetView, error) {
	budgets := make([]budgetView, 0, 2)
	caller := a.Caller()
	keyAmount, keyReset, keyLimited := a.SpendLimit()
	workspaceAmount, workspaceReset, workspaceLimited := a.Budget()
	var effective *budgetView
	var least int64
	// Workspace precedes key, matching billing's limit checks when both are exhausted.
	for _, limit := range [...]struct {
		subject uuid.UUID
		scope   string
		amount  int64
		reset   string
		limited bool
	}{
		{uuid.UUID(caller.WorkspaceID()), "workspace", workspaceAmount, workspaceReset, workspaceLimited},
		{uuid.UUID(caller.KeyID()), "key_limit", keyAmount, keyReset, keyLimited},
	} {
		if !limit.limited {
			continue
		}
		use := keyUsage.BudgetUsage
		if limit.scope == "workspace" {
			var err error
			use, err = billing.ReadBudgetUsage(ctx, tx, limit.subject, billing.Reset(limit.reset))
			if err != nil {
				return nil, nil, err
			}
		}
		var reset *string
		if limit.reset != "" {
			reset = new(limit.reset)
		}
		remaining := max(0, limit.amount-use.Spent)
		remaining = max(0, remaining-use.Reserved)
		var resetsAt *time.Time
		if !use.ResetsAt.IsZero() {
			resetsAt = &use.ResetsAt
		}
		budgets = append(budgets, budgetView{Scope: limit.scope, Limit: credits(limit.amount), Spend: credits(use.Spent), Reserved: credits(use.Reserved), Remaining: credits(remaining), Reset: reset, ResetsAt: resetsAt})
		b := &budgets[len(budgets)-1]
		if effective == nil || remaining < least {
			effective, least = b, remaining
		}
	}
	return budgets, effective, nil
}
