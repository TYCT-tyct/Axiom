package declared

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// offerState is a group-and-model pass as far as it got. It is stored in the supplier's sync state, so a pass the
// supplier's limits stopped goes on at the group it stopped at instead of reading every group again.
type offerState struct {
	V          int                          `json:"v"`
	GroupsRead bool                         `json:"groups_read,omitempty"`
	Groups     []offerGroup                 `json:"groups,omitempty"`
	Models     map[string][]offerModel      `json:"models,omitempty"`
	Prices     map[string]map[string]string `json:"prices,omitempty"`
	PricesRead bool                         `json:"prices_read,omitempty"`
	Next       int                          `json:"next,omitempty"`
}

type offerGroup struct {
	Ref        string   `json:"ref"`
	Name       string   `json:"name,omitempty"`
	Ratio      string   `json:"ratio,omitempty"`
	GroupValue string   `json:"group_value,omitempty"`
	Formats    []string `json:"formats,omitempty"`
}

type offerModel struct {
	Name    string   `json:"name"`
	Formats []string `json:"formats,omitempty"`
}

const offerVersion = 1

// Offer implements adapter.Manager. A declaration reads groups once, the price list once, and each group's models in
// turn; each read is one request against the declared bucket, so a rate limit stops the pass with its position stored.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	st := decodeOffer(from)
	page, err := m.offerHead(ctx, st)
	if err != nil || page != nil {
		if err != nil {
			return adapter.Page[adapter.Group]{}, err
		}
		return *page, nil
	}
	return m.offerModels(ctx, st)
}

// offerHead reads the groups and the price list once each. It returns a page when a rate limit stopped the pass.
func (m *manager) offerHead(ctx context.Context, st *offerState) (*adapter.Page[adapter.Group], error) {
	if !st.GroupsRead {
		if page, err := m.readGroups(ctx, st); err != nil || page != nil {
			return page, err
		}
	}
	if !st.PricesRead {
		if page, err := m.readPrices(ctx, st); err != nil || page != nil {
			return page, err
		}
	}
	return nil, nil //nolint:nilnil // no page means the pass goes on to the models
}

// offerModels reads each group's models in turn, keeping the position when a rate limit stops it.
func (m *manager) offerModels(ctx context.Context, st *offerState) (adapter.Page[adapter.Group], error) {
	for st.Next < len(st.Groups) {
		err := m.readModels(ctx, st, st.Next)
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return adapter.Page[adapter.Group]{Next: encodeOffer(st), Until: blocked.Until}, nil
		}
		if err != nil {
			return adapter.Page[adapter.Group]{}, err
		}
		st.Next++
	}
	groups, err := assembleOffer(m.a.offer, *st)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	return adapter.Page[adapter.Group]{Items: groups, Done: true}, nil
}

// readGroups reads the groups once. It returns a page when the read must stop and nil to go on.
func (m *manager) readGroups(ctx context.Context, st *offerState) (*adapter.Page[adapter.Group], error) {
	resp, err := m.do(ctx, "groups", http.MethodGet, m.a.offer.groups.path, nil, m.a.offer.groups.bucket, nil)
	if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
		return &adapter.Page[adapter.Group]{Next: encodeOffer(st), Until: blocked.Until}, nil
	}
	if err != nil {
		return nil, err
	}
	groups, err := parseGroups(m.a.offer, resp.Body)
	if err != nil {
		return nil, err
	}
	st.Groups, st.GroupsRead = groups, true
	if len(groups) == 0 {
		st.PricesRead = true
	}
	return nil, nil //nolint:nilnil // the state is complete; the caller goes on to the price list
}

// readPrices reads the price list once. A declaration without one marks the read done.
func (m *manager) readPrices(ctx context.Context, st *offerState) (*adapter.Page[adapter.Group], error) {
	if m.a.offer.prices == nil {
		st.PricesRead = true
		return nil, nil //nolint:nilnil // no price list means nothing to read, not a failure
	}
	resp, err := m.do(ctx, "prices", http.MethodGet, m.a.offer.prices.path, nil, m.a.offer.prices.bucket, nil)
	if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
		return &adapter.Page[adapter.Group]{Next: encodeOffer(st), Until: blocked.Until}, nil
	}
	if err != nil {
		return nil, err
	}
	prices, err := parsePrices(m.a.offer.prices, resp.Body)
	if err != nil {
		return nil, err
	}
	st.Prices, st.PricesRead = prices, true
	return nil, nil //nolint:nilnil // the state is complete; the caller goes on to the models
}

// readModels reads one group's models into the state.
func (m *manager) readModels(ctx context.Context, st *offerState, i int) error {
	g := st.Groups[i]
	resp, err := m.do(ctx, "models", http.MethodGet, m.a.offer.models.path, map[string]string{"group": g.GroupValue}, m.a.offer.models.bucket, nil)
	if err != nil {
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return blocked
		}
		return err
	}
	models, err := parseModels(m.a.offer.models, resp.Body)
	if err != nil {
		return err
	}
	if st.Models == nil {
		st.Models = make(map[string][]offerModel, len(st.Groups))
	}
	st.Models[g.Ref] = models
	return nil
}

func parseGroups(p offerPlan, body []byte) ([]offerGroup, error) {
	raw, ok := p.groups.items.lookup(body)
	if !ok {
		return nil, missing("groups")
	}
	var items []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: the groups are not an array", adapter.ErrUnrecognised)
	}
	out := make([]offerGroup, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		g, skip, err := parseGroup(p, item)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		if g.Ref == "" || seen[g.Ref] {
			return nil, fmt.Errorf("%w: a group ref is empty or repeated", adapter.ErrUnrecognised)
		}
		seen[g.Ref] = true
		out = append(out, g)
	}
	return out, nil
}

func parseGroup(p offerPlan, item jsonx.Raw) (offerGroup, bool, error) {
	ref, err := readTextAt(p.groups.ref, item, "group ref")
	if err != nil {
		return offerGroup{}, false, err
	}
	g := offerGroup{Ref: ref, Name: ref, GroupValue: ref}
	if p.groups.name != nil {
		if g.Name, err = readTextAt(*p.groups.name, item, "group name"); err != nil {
			return offerGroup{}, false, err
		}
	}
	skip, err := groupRatio(p, &g, item)
	if err != nil || skip {
		return g, skip, err
	}
	if p.groups.formats != nil {
		if g.Formats, err = readTextsAt(*p.groups.formats, item, "group formats"); err != nil {
			return offerGroup{}, false, err
		}
	}
	if p.models.group != nil {
		if g.GroupValue, err = readTextAt(*p.models.group, item, "group model key"); err != nil {
			return offerGroup{}, false, err
		}
	}
	return g, false, nil
}

// groupRatio reads the group's multiplier. A ratio in skip_ratio is a group the declaration does not sell (new-api's
// auto), so it is skipped before the value is validated as a decimal.
func groupRatio(p offerPlan, g *offerGroup, item jsonx.Raw) (bool, error) {
	if p.groups.ratio == nil {
		return false, nil
	}
	ratio, err := readTextAt(*p.groups.ratio, item, "group ratio")
	if err != nil {
		return false, err
	}
	if p.groups.skipRatio[ratio] {
		return true, nil
	}
	value, ok := new(big.Rat).SetString(ratio)
	if !ok || value.Sign() < 0 {
		return false, fmt.Errorf("%w: a group ratio is not a non-negative number", adapter.ErrUnrecognised)
	}
	g.Ratio = ratio
	return false, nil
}

func parsePrices(p *pricePlan, body []byte) (map[string]map[string]string, error) {
	raw, ok := p.items.lookup(body)
	if !ok {
		return nil, missing("prices")
	}
	var items []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: the prices are not an array", adapter.ErrUnrecognised)
	}
	out := make(map[string]map[string]string, len(items))
	for _, item := range items {
		model, err := priceModel(p, item)
		if err != nil {
			return nil, err
		}
		rates, err := priceRates(p, item, model)
		if err != nil {
			return nil, err
		}
		if len(rates) > 0 {
			out[model] = rates
		}
	}
	return out, nil
}

func priceModel(p *pricePlan, item jsonx.Raw) (string, error) {
	model, err := readTextAt(p.model, item, "price model")
	if err != nil {
		return "", err
	}
	if model == "" {
		return "", fmt.Errorf("%w: a price has no model", adapter.ErrUnrecognised)
	}
	return model, nil
}

func priceRates(p *pricePlan, item jsonx.Raw, model string) (map[string]string, error) {
	rates := make(map[string]string, len(p.rates))
	for it, field := range p.rates {
		raw, ok := field.lookup(item)
		if !ok {
			continue
		}
		text, ok := readText(raw)
		if !ok || !decimal(text) {
			return nil, fmt.Errorf("%w: the price of %s for %s is not a decimal", adapter.ErrUnrecognised, it, model)
		}
		rates[it.String()] = text
	}
	return rates, nil
}

func parseModels(p modelPlan, body []byte) ([]offerModel, error) {
	raw, ok := p.items.lookup(body)
	if !ok {
		return nil, missing("models")
	}
	var items []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: the models are not an array", adapter.ErrUnrecognised)
	}
	out := make([]offerModel, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		name, err := modelName(p, item)
		if err != nil {
			return nil, err
		}
		if name == "" || seen[name] {
			return nil, fmt.Errorf("%w: a model name is empty or repeated", adapter.ErrUnrecognised)
		}
		seen[name] = true
		m := offerModel{Name: name}
		if p.formats != nil {
			if m.Formats, err = readTextsAt(*p.formats, item, "model formats"); err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func modelName(p modelPlan, item jsonx.Raw) (string, error) {
	if p.name == nil {
		name, ok := readText(item)
		if !ok {
			return "", fmt.Errorf("%w: a model is not a name and no name field is declared", adapter.ErrUnrecognised)
		}
		return name, nil
	}
	return readTextAt(*p.name, item, "model name")
}

func readTextAt(p path, raw jsonx.Raw, what string) (string, error) {
	raw, ok := p.lookup(raw)
	if !ok {
		return "", missing(what)
	}
	text, ok := readText(raw)
	if !ok {
		return "", fmt.Errorf("%w: the %s is not text", adapter.ErrUnrecognised, what)
	}
	return text, nil
}

func readTextsAt(p path, raw jsonx.Raw, what string) ([]string, error) {
	raw, ok := p.lookup(raw)
	if !ok {
		return nil, nil // a group or model that states no formats inherits the ones above it
	}
	var values []jsonx.Raw
	if err := jsonx.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%w: the %s are not an array", adapter.ErrUnrecognised, what)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		text, ok := readText(v)
		if !ok {
			return nil, fmt.Errorf("%w: a value in the %s is not text", adapter.ErrUnrecognised, what)
		}
		out = append(out, text)
	}
	return out, nil
}

// assembleOffer turns the state into the groups the module stores. The cost of a model is the price list times the
// declaration's scale and the group's ratio; a model the price list omitted has no cost, which keeps it out of
// routing rather than pricing it at zero.
func assembleOffer(p offerPlan, st offerState) ([]adapter.Group, error) {
	out := make([]adapter.Group, 0, len(st.Groups))
	for _, g := range st.Groups {
		group, err := assembleGroup(p, st, g)
		if err != nil {
			return nil, err
		}
		out = append(out, group)
	}
	return out, nil
}

func assembleGroup(p offerPlan, st offerState, g offerGroup) (adapter.Group, error) {
	ratio := g.Ratio
	if ratio == "" {
		ratio = "1"
	}
	fmts, err := p.formatsOf(g.Formats)
	if err != nil {
		return adapter.Group{}, err
	}
	group := adapter.Group{Ref: g.Ref, Name: g.Name, Ratio: adapter.Amount(ratio), Facts: factsOf(fmts)}
	for _, m := range st.Models[g.Ref] {
		model, err := assembleModel(p, m, ratio, fmts, st.Prices)
		if err != nil {
			return adapter.Group{}, err
		}
		group.Models = append(group.Models, model)
	}
	return group, nil
}

func assembleModel(p offerPlan, m offerModel, ratio string, groupFormats []protocol.Format, prices map[string]map[string]string) (adapter.Model, error) {
	model := adapter.Model{UpstreamName: m.Name}
	var err error
	if model.Formats, err = p.formatsOf(m.Formats); err != nil {
		return adapter.Model{}, err
	}
	if len(model.Formats) == 0 {
		model.Formats = groupFormats
	}
	if model.Cost, err = p.costOf(m.Name, ratio, prices); err != nil {
		return adapter.Model{}, err
	}
	return model, nil
}

func (p offerPlan) formatsOf(values []string) ([]protocol.Format, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]protocol.Format, 0, len(values))
	seen := map[protocol.Format]bool{}
	for _, v := range values {
		f, ok := p.formats[v]
		if !ok {
			f, ok = formatNames[v]
		}
		if !ok {
			return nil, fmt.Errorf("%w: the format %q is not mapped", adapter.ErrUnrecognised, v)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, nil
}

func factsOf(fmts []protocol.Format) adapter.GroupFacts {
	if len(fmts) == 0 {
		return nil
	}
	names := make([]string, 0, len(fmts))
	for _, f := range fmts {
		for name, known := range formatNames {
			if known == f {
				names = append(names, name)
				break
			}
		}
	}
	raw, err := jsonx.MarshalCanonical(struct {
		Formats []string `json:"formats"`
	}{names})
	if err != nil {
		return nil
	}
	return raw
}

func (p offerPlan) costOf(model, ratio string, prices map[string]map[string]string) (*catalog.Schedule, error) {
	if p.prices == nil || len(prices[model]) == 0 {
		return nil, nil //nolint:nilnil // the model has no declared price, so it has no cost
	}
	raw, err := jsonx.MarshalCanonical(struct {
		V        int               `json:"v"`
		Currency string            `json:"currency"`
		Rates    map[string]string `json:"rates"`
	}{1, platform.CurrencyNone.String(), prices[model]})
	if err != nil {
		return nil, fmt.Errorf("declared: the cost of %s: %w", model, err)
	}
	schedule, err := catalog.ParseSchedule(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: the cost of %s: %w", adapter.ErrUnrecognised, model, err)
	}
	multiplier, ok := new(big.Rat).SetString(ratio)
	if !ok {
		return nil, fmt.Errorf("%w: the ratio of a group is not a decimal", adapter.ErrUnrecognised)
	}
	scale := new(big.Rat).Mul(p.prices.scale, multiplier)
	if scale.Cmp(big.NewRat(1, 1)) == 0 {
		return &schedule, nil
	}
	scaled, err := schedule.Scaled(scale.RatString(), platform.CurrencyNone)
	if err != nil {
		return nil, fmt.Errorf("%w: the cost of %s: %w", adapter.ErrUnrecognised, model, err)
	}
	return &scaled, nil
}

func encodeOffer(st *offerState) adapter.Listing {
	st.V = offerVersion
	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		return nil
	}
	return raw
}

func decodeOffer(from adapter.Listing) *offerState {
	var st offerState
	if len(from) == 0 || jsonx.UnmarshalKnown(from, &st) != nil || st.V != offerVersion || st.Next < 0 || st.Next > len(st.Groups) {
		return &offerState{V: offerVersion}
	}
	return &st
}
