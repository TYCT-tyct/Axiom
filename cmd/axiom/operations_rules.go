package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API's pricing rule writes (docs/domains/operations.md「售价的累计变动上限」): the agent adds or changes
// a rule of the main site as the admin does. The publisher publishes a rule's small moves at once, so the change is made
// in the request's transaction and the prices refreshed, and every published price that moved is measured from the
// versions before and after, never from the agent's numbers. Within the site's price envelope, all of them together,
// the change is kept and counted; otherwise it is undone to a savepoint and waits for the owner, whose approval makes
// it again and counts what it moves then.

// PricingRulesList reads the main site's rules as the admin does.
func (o operationsAPI) PricingRulesList(ctx context.Context, _ operationsapi.PricingRulesListRequestObject) (operationsapi.PricingRulesListResponseObject, error) {
	answer, err := o.admin.ListPricingRules(ctx, adminapi.ListPricingRulesRequestObject{})
	return relayed[operationsapi.PricingRulesList200JSONResponse, adminapi.ListPricingRules200JSONResponse](answer, err)
}

// PricingRuleCreate adds a rule to the main site within its price envelope, or asks the owner to.
func (o operationsAPI) PricingRuleCreate(ctx context.Context, r operationsapi.PricingRuleCreateRequestObject) (operationsapi.PricingRuleCreateResponseObject, error) {
	site, ok := o.admin.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	b := r.Body
	target := operations.Target{Kind: "site", ID: uuid.UUID(site.ID).String()}
	op, replayed, err := o.writeRule(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, "pricing_rule_create", target, b.Rule, nil, b.Reason, b.DryRun)
	if err != nil {
		return nil, err
	}
	headers := operationsapi.PricingRuleCreate200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.PricingRuleCreate202JSONResponse{Body: operationBody(op), Headers: operationsapi.PricingRuleCreate202ResponseHeaders(headers)}, nil
	}
	return operationsapi.PricingRuleCreate200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// PricingRuleUpdate changes a rule of the main site as of the revision read, within its price envelope, or asks the
// owner to.
func (o operationsAPI) PricingRuleUpdate(ctx context.Context, r operationsapi.PricingRuleUpdateRequestObject) (operationsapi.PricingRuleUpdateResponseObject, error) {
	b := r.Body
	target := operations.Target{Kind: "pricing_rule", ID: r.RuleId}
	op, replayed, err := o.writeRule(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, "pricing_rule_update", target, b.Rule, &b.Rev, b.Reason, b.DryRun)
	if err != nil {
		return nil, err
	}
	headers := operationsapi.PricingRuleUpdate200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if op.State == operations.StatePending && op.ID != (uuid.UUID{}) {
		return operationsapi.PricingRuleUpdate202JSONResponse{Body: operationBody(op), Headers: operationsapi.PricingRuleUpdate202ResponseHeaders(headers)}, nil
	}
	return operationsapi.PricingRuleUpdate200JSONResponse{Body: operationBody(op), Headers: headers}, nil
}

// writeRule proposes the rule write tool asks for on target, rev being the stored rule's revision read. A preview's
// result is the prices the change would move now.
func (o operationsAPI) writeRule(ctx context.Context, keyText string, run *string, tool string, target operations.Target, rule operationsapi.PricingRuleInput, rev *int64, reason string, dryRun *bool) (operations.Operation, bool, error) {
	key, err := keyOf(keyText)
	if err != nil {
		return operations.Operation{}, false, err
	}
	asked, err := converted[map[string]any](rule)
	if err != nil {
		return operations.Operation{}, false, err
	}
	params := map[string]any{"rule": asked}
	if rev != nil {
		params["rev"] = *rev
	}
	encoded, err := jsonx.MarshalCanonical(params)
	if err != nil {
		return operations.Operation{}, false, err
	}
	change, err := ruleChangeOf(target, encoded)
	if err != nil {
		return operations.Operation{}, false, err
	}
	p := operations.Proposal{
		Credential: uuid.UUID(callOf(ctx).agent.ID()), Key: key, Tool: tool, Target: target, Params: params,
		Risk: operations.RiskPrice, Reason: reason, Run: runOf(run),
	}
	var measured map[string]any // the prices this request moved; none when it found its key's operation
	op, replayed, err := o.runWrite(ctx, dryRun, func(tx pg.Tx) (op operations.Operation, replayed bool, err error) {
		var moves []operations.Repricing
		if op, moves, replayed, err = o.proposeRule(ctx, tx, change, p); err == nil && !replayed {
			measured = movesResult(moves)
		}
		return op, replayed, err
	})
	if err == nil && op.ID == (uuid.UUID{}) && measured != nil {
		op.Result, err = jsonx.MarshalCanonical(measured)
	}
	return op, replayed, err
}

// ruleChange is a pricing rule write as its operation keeps it: the rule asked for and, to change a stored rule, its
// ID and the revision it was read at.
type ruleChange struct {
	id  *uuid.UUID
	rev int64
	in  billing.RuleInput
}

// ruleChangeOf reads a rule write from its operation's target and parameters, which are also what is approved.
func ruleChangeOf(t operations.Target, params []byte) (ruleChange, error) {
	var p struct {
		Rule adminapi.PricingRuleInput `json:"rule"`
		Rev  int64                     `json:"rev"`
	}
	if err := jsonx.Unmarshal(params, &p); err != nil {
		return ruleChange{}, fmt.Errorf("the rule write's parameters: %w", err)
	}
	in, err := ruleInputOf(p.Rule)
	if err != nil {
		return ruleChange{}, err
	}
	c := ruleChange{in: in, rev: p.Rev}
	if t.Kind == "pricing_rule" {
		id, err := uuid.Parse(t.ID)
		if err != nil || id.String() != t.ID {
			return ruleChange{}, billing.ErrRuleNotFound
		}
		c.id = &id
	}
	return c, nil
}

// proposeRule makes c in tx with the credential held, after a savepoint, and proposes p with every price it moved:
// within the envelope it is kept, counted and settled; outside it, it is undone to the savepoint and waits. A retry of
// p's key, alongside the first or after it, waits for it on the site's price policy and finds its operation before
// anything is changed.
func (o operationsAPI) proposeRule(ctx context.Context, tx pg.Tx, c ruleChange, p operations.Proposal) (operations.Operation, []operations.Repricing, bool, error) {
	if err := o.admin.accounts.HoldAgent(ctx, tx, accounts.AgentID(p.Credential)); err != nil {
		return operations.Operation{}, nil, false, err
	}
	if err := o.holdPrices(ctx, tx); err != nil {
		return operations.Operation{}, nil, false, err
	}
	if op, found, err := operations.Replay(ctx, tx, p); found || err != nil {
		return op, nil, found, err
	}
	site, _ := o.admin.accounts.MainSite() // holdPrices found it
	before, err := refreshedPrices(ctx, tx, site)
	if err != nil {
		return operations.Operation{}, nil, false, err
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT pricing_rule"); err != nil {
		return operations.Operation{}, nil, false, fmt.Errorf("propose %s: %w", p.Tool, err)
	}
	rule, moves, err := o.admin.applyRule(ctx, tx, site, c, audit.OperationsAgent(p.Credential), p.Reason, before)
	if err != nil {
		return operations.Operation{}, nil, false, err
	}
	p.Summary = map[string]any{"currency": site.Currency.String()}
	if c.id != nil {
		p.Summary["rule_id"], p.Summary["rule_rev"] = c.id.String(), c.rev
	}
	op, replayed, err := operations.ProposePrices(ctx, tx, p, uuid.UUID(site.ID), moves, func() error {
		_, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT pricing_rule")
		return err
	})
	if err != nil || replayed || op.State != operations.StateApproved {
		return op, moves, replayed, err
	}
	op, err = operations.Settle(ctx, tx, op, ruleResult(rule, moves), "")
	return op, moves, false, err
}

// runPricingRule makes the rule change of op, which the owner approved, and counts every price it moves now in the
// day's envelope.
func (a adminAPI) runPricingRule(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	c, err := ruleChangeOf(op.Target, op.Params)
	if err != nil {
		return nil, err
	}
	site, ok := a.accounts.MainSite()
	if !ok {
		return nil, accounts.ErrNotFound
	}
	before, err := refreshedPrices(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rule, moves, err := a.applyRule(ctx, tx, site, c, audit.OperationsAgent(op.Credential), op.Reason, before)
	if err != nil {
		return nil, err
	}
	if err := operations.CountPrices(ctx, tx, uuid.UUID(site.ID), moves); err != nil {
		return nil, err
	}
	return ruleResult(rule, moves), nil
}

// refreshedPrices refreshes the site's proposals, publishing what the publisher would, and reads them: what a rule change
// is measured from and to.
func refreshedPrices(ctx context.Context, tx pg.Tx, site accounts.Site) ([]billing.Publication, error) {
	if err := refreshSalePrices(ctx, tx, uuid.UUID(site.ID), site.Currency); err != nil {
		return nil, err
	}
	_, prices, err := billing.PriceState(ctx, tx, uuid.UUID(site.ID))
	return prices, err
}

// applyRule makes c as actor, with the admin's checks, and answers the rule and every published price of site it
// moved from before, each with the model's slug.
func (a adminAPI) applyRule(ctx context.Context, tx pg.Tx, site accounts.Site, c ruleChange, actor audit.Actor, reason string, before []billing.Publication) (billing.Rule, []operations.Repricing, error) {
	rule, err := a.writeRule(ctx, tx, site, c, actor, reason)
	if err != nil {
		return rule, nil, ruleProblem(err)
	}
	after, err := refreshedPrices(ctx, tx, site)
	if err != nil {
		return rule, nil, err
	}
	moves, err := saleMoves(uuid.UUID(site.ID), before, after)
	if err != nil {
		return rule, nil, err
	}
	snap, err := catalog.ReadSnapshot(ctx, tx)
	if err != nil {
		return rule, nil, err
	}
	for _, r := range moves {
		if model, ok := snap.Model(catalog.ModelID(r.Model)); ok {
			r.Summary["slug"] = model.Slug
		}
	}
	return rule, moves, nil
}

// writeRule adds or changes the rule c asks for as the admin's handlers do, with the same checks.
func (a adminAPI) writeRule(ctx context.Context, tx pg.Tx, site accounts.Site, c ruleChange, actor audit.Actor, reason string) (billing.Rule, error) {
	check := func() error {
		if err := a.checkSubjects(ctx, c.in); err != nil {
			return err
		}
		return a.checkRuleAudience(ctx, tx, site, c.in)
	}
	if c.id != nil {
		return billing.UpdateRule(ctx, tx, actor, uuid.UUID(site.ID), site.Currency, *c.id, c.rev, c.in, a.catalog.AllowSchedule, reason, check)
	}
	if err := c.in.Check(site.Currency); err != nil {
		return billing.Rule{}, err
	}
	if err := check(); err != nil {
		return billing.Rule{}, err
	}
	return billing.CreateRule(ctx, tx, actor, uuid.UUID(site.ID), site.Currency, c.in, a.catalog.AllowSchedule, reason)
}

// saleMoves is every published sale price of site that after sells at another version than before: one sold at a new
// version, given where none was sold, or taken away. Each is measured from the version before, as a publication is,
// so a price given or taken away has no share that measures its move, and one taken away has no margin either. They
// are in the order of their models and audiences.
func saleMoves(site uuid.UUID, before, after []billing.Publication) ([]operations.Repricing, error) {
	type sold struct {
		model    uuid.UUID
		audience billing.Selector
	}
	was, is := map[sold]billing.Publication{}, map[sold]billing.Publication{}
	var keys []sold
	for _, p := range before {
		k := sold{uuid.UUID(p.Model), p.Audience}
		was[k], keys = p, append(keys, k)
	}
	for _, p := range after {
		k := sold{uuid.UUID(p.Model), p.Audience}
		if _, ok := was[k]; !ok {
			keys = append(keys, k)
		}
		is[k] = p
	}
	slices.SortFunc(keys, func(a, b sold) int {
		return cmp.Or(bytes.Compare(a.model[:], b.model[:]), cmp.Compare(a.audience.Kind, b.audience.Kind), cmp.Compare(a.audience.ID, b.audience.ID))
	})
	var moves []operations.Repricing
	for _, k := range keys {
		then, now := soldVersion(was[k]), soldVersion(is[k])
		if then == now {
			continue
		}
		r := operations.Repricing{Site: site, Model: k.model, Summary: map[string]any{
			"model_id": k.model.String(), "audience": map[string]any{"kind": k.audience.Kind, "id": k.audience.ID},
		}}
		if now != (uuid.UUID{}) {
			moved := is[k]
			moved.Schedule, moved.Endpoints = moved.CurrentSchedule, moved.CurrentEndpoints
			moved.Current, moved.CurrentSchedule, moved.CurrentEndpoints = nil, nil, nil
			if then != (uuid.UUID{}) {
				old := was[k]
				moved.Current, moved.CurrentSchedule, moved.CurrentEndpoints = old.Current, old.CurrentSchedule, old.CurrentEndpoints
			}
			var err error
			if r.Change, r.Margin, err = billing.PriceMove(moved); err != nil {
				return nil, fmt.Errorf("measure the move of model %s: %w", k.model, err)
			}
		}
		moves = append(moves, r)
	}
	return moves, nil
}

// soldVersion is the version p sells at, which routing serves (billing's publishedLayer), or the zero ID when it sells
// none.
func soldVersion(p billing.Publication) uuid.UUID {
	if p.Current == nil || p.Status != "published" && p.Status != "pending" {
		return uuid.UUID{}
	}
	return *p.Current
}

// movesResult is what a rule write answers of the prices it moves: how many, and the first of them.
func movesResult(moves []operations.Repricing) map[string]any {
	return map[string]any{"moves_total": len(moves), "moves": operations.MovesDetail(moves)}
}

// ruleResult is what a rule write that ran answers: the rule as it is now, and the prices it moved.
func ruleResult(rule billing.Rule, moves []operations.Repricing) map[string]any {
	result := movesResult(moves)
	result["rule_id"], result["rev"] = rule.ID.String(), rule.Rev
	return result
}
