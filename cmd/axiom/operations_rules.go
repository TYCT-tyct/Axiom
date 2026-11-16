package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
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

// PricingRulesList reads the main site's rules as the admin does, for a credential that reaches it.
func (o operationsAPI) PricingRulesList(ctx context.Context, _ operationsapi.PricingRulesListRequestObject) (operationsapi.PricingRulesListResponseObject, error) {
	if _, err := o.mainSite(ctx); err != nil {
		return nil, err
	}
	answer, err := o.admin.ListPricingRules(ctx, adminapi.ListPricingRulesRequestObject{})
	body, err := relayed[adminapi.ListPricingRules200JSONResponse](answer, err)
	return operationsapi.PricingRulesList200JSONResponse(body), err
}

// PricingRuleCreate adds a rule to the main site within its price envelope, or asks the owner to.
func (o operationsAPI) PricingRuleCreate(ctx context.Context, r operationsapi.PricingRuleCreateRequestObject) (operationsapi.PricingRuleCreateResponseObject, error) {
	site, err := o.mainSite(ctx)
	if err != nil {
		return nil, err
	}
	b := r.Body
	target := operations.Target{Kind: "site", ID: uuid.UUID(site.ID).String()}
	op, replayed, err := o.writeRule(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, "pricing_rule_create", target, b.Rule, nil, b.Reason, b.DryRun)
	if err != nil {
		return nil, err
	}
	o.admin.refreshAfterCommit(ctx, replayed || op.State != operations.StateSucceeded)
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
	o.admin.refreshAfterCommit(ctx, replayed || op.State != operations.StateSucceeded)
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

// priceView is a site's sale prices at one moment: each publication, and the audience layers routing selects a
// model's price from (billing.PublishedPrices).
type priceView struct {
	publications []billing.Publication
	sold         map[catalog.ModelID]billing.Sold
}

// refreshedPrices refreshes the site's proposals, publishing what the publisher would, and reads them: what a rule change
// is measured from and to.
func refreshedPrices(ctx context.Context, tx pg.Tx, site accounts.Site) (priceView, error) {
	if err := refreshSalePrices(ctx, tx, uuid.UUID(site.ID), site.Currency); err != nil {
		return priceView{}, err
	}
	_, publications, err := billing.PriceState(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return priceView{}, err
	}
	sold, err := billing.PublishedPrices(ctx, tx, uuid.UUID(site.ID))
	return priceView{publications, sold}, err
}

// applyRule makes c as actor, with the admin's checks, and answers the rule and every published price of site it
// moved from before, each with the model's slug.
func (a adminAPI) applyRule(ctx context.Context, tx pg.Tx, site accounts.Site, c ruleChange, actor audit.Actor, reason string, before priceView) (billing.Rule, []operations.Repricing, error) {
	rule, err := a.writeRule(ctx, tx, site, c, actor, reason)
	if err != nil {
		return rule, nil, ruleProblem(err)
	}
	after, err := refreshedPrices(ctx, tx, site)
	if err != nil {
		return rule, nil, err
	}
	buyers, err := a.buyers(ctx, tx, site, before, after)
	if err != nil {
		return rule, nil, err
	}
	moves, err := saleMoves(uuid.UUID(site.ID), buyers, before, after)
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

// buyer is one kind of buyer whose sale price a rule change can move, as billing selects it (billing.Audience), and how
// the owner is shown it.
type buyer struct {
	audience billing.Audience
	shown    billing.Selector
}

// buyers is every kind of buyer of site: each tier's accounts without a price of their own, and each account that has
// one before or after the change, in its tier. Every account is in a tier, so together they are every price an account
// pays. An account layer of an account that no longer exists has nobody to sell to.
func (a adminAPI) buyers(ctx context.Context, tx pg.Tx, site accounts.Site, prices ...priceView) ([]buyer, error) {
	tiers, err := a.accounts.Tiers(ctx, tx, site.ID)
	if err != nil {
		return nil, err
	}
	var out []buyer
	for _, t := range tiers {
		out = append(out, buyer{billing.Audience{Tier: t.ID}, billing.Selector{Kind: "tier", ID: t.ID.TypeID()}})
	}
	for _, shown := range accountLayers(prices) {
		id, err := accounts.ParseAccountID(shown.ID)
		if err != nil {
			return nil, fmt.Errorf("the audience of a price: %w", err)
		}
		detail, err := a.accounts.Account(ctx, tx, id)
		if errors.Is(err, accounts.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, buyer{billing.Audience{Account: id, Tier: detail.Tier.ID}, shown})
	}
	return out, nil
}

// accountLayers is each account audience that has a layer in any of prices, once, in the order first seen.
func accountLayers(prices []priceView) []billing.Selector {
	var out []billing.Selector
	for _, p := range prices {
		for _, pub := range p.publications {
			if pub.Audience.Kind == "account" && !slices.Contains(out, pub.Audience) {
				out = append(out, pub.Audience)
			}
		}
	}
	return out
}

// saleMoves is every change in what buyers pay for a model of site: the version billing selects for a buyer after is
// not the one it selected before, because a layer was published, given, taken away or stopped, or because another layer
// now wins (a more specific target, docs/domains/billing.md「定价」). Each distinct change of a model, from one version
// to another, is one move, however many buyers it reaches, measured from the version before as a publication is: a
// price given or taken away has no share that measures its move, and one taken away has no margin either. They are in
// the order of their models, then of buyers.
func saleMoves(site uuid.UUID, buyers []buyer, before, after priceView) ([]operations.Repricing, error) {
	was, is := byVersion(before.publications), byVersion(after.publications)
	models := slices.Collect(maps.Keys(before.sold))
	for m := range after.sold {
		if _, ok := before.sold[m]; !ok {
			models = append(models, m)
		}
	}
	slices.SortFunc(models, func(a, b catalog.ModelID) int { return bytes.Compare(a[:], b[:]) })
	type change struct {
		model     catalog.ModelID
		then, now uuid.UUID
	}
	seen := map[change]bool{}
	var moves []operations.Repricing
	for _, m := range models {
		for _, b := range buyers {
			c := change{m, before.sold[m].For(b.audience).Version, after.sold[m].For(b.audience).Version}
			if c.then == c.now || seen[c] {
				continue
			}
			seen[c] = true
			r := operations.Repricing{Site: site, Model: uuid.UUID(m), Summary: map[string]any{
				"model_id": m.String(), "audience": map[string]any{"kind": b.shown.Kind, "id": b.shown.ID},
			}}
			if err := measure(&r, was[c.then], is[c.now]); err != nil {
				return nil, fmt.Errorf("measure the move of model %s: %w", m, err)
			}
			moves = append(moves, r)
		}
	}
	moves = append(moves, unsoldMoves(site, before, after)...)
	slices.SortStableFunc(moves, func(a, b operations.Repricing) int { return bytes.Compare(a.Model[:], b.Model[:]) })
	return moves, nil
}

// unsoldMoves is each price the change gives that is not on sale yet: a layer whose proposal the change replaced and
// that sells nothing (`unpriced`: no endpoint, an endpoint switched off, or the cost unknown). The publisher publishes
// such a price by itself as soon as the model can be served, outside any envelope, so the change is weighed as giving
// it, a move no share measures, which the owner decides. A proposal that waits for a confirmation (`pending`,
// `blocked`) is weighed when it is published.
func unsoldMoves(site uuid.UUID, before, after priceView) []operations.Repricing {
	type layer struct {
		model    catalog.ModelID
		audience billing.Selector
	}
	was := make(map[layer]uuid.UUID, len(before.publications))
	for _, p := range before.publications {
		was[layer{p.Model, p.Audience}] = p.Candidate
	}
	var moves []operations.Repricing
	for _, p := range after.publications {
		if p.Status != "unpriced" || was[layer{p.Model, p.Audience}] == p.Candidate {
			continue
		}
		moves = append(moves, operations.Repricing{Site: site, Model: uuid.UUID(p.Model), Summary: map[string]any{
			"model_id": p.Model.String(), "audience": map[string]any{"kind": p.Audience.Kind, "id": p.Audience.ID},
		}})
	}
	return moves
}

// measure sets r's move and margin from then, the publication a buyer was sold before, to now, the one it is sold after;
// either is the zero Publication when the buyer was or is sold nothing, which measures nothing.
func measure(r *operations.Repricing, then, now billing.Publication) error {
	if now.Current == nil {
		return nil
	}
	moved := now
	moved.Schedule, moved.Endpoints = now.CurrentSchedule, now.CurrentEndpoints
	moved.Current, moved.CurrentSchedule, moved.CurrentEndpoints = then.Current, then.CurrentSchedule, then.CurrentEndpoints
	var err error
	r.Change, r.Margin, err = billing.PriceMove(moved)
	return err
}

// byVersion is each publication by the version it sells, which is its own: a version is published for one publication.
func byVersion(publications []billing.Publication) map[uuid.UUID]billing.Publication {
	out := make(map[uuid.UUID]billing.Publication, len(publications))
	for _, p := range publications {
		if p.Current != nil {
			out[*p.Current] = p
		}
	}
	return out
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
