package declared

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// offerState is a group-and-model pass as far as it got. It is stored in the supplier's sync state, so a pass the
// supplier's limits stopped goes on at the group it stopped at instead of reading every group again.
type offerState struct {
	V           int                          `json:"v"`
	Fingerprint string                       `json:"fingerprint,omitempty"`
	GroupsRead  bool                         `json:"groups_read,omitempty"`
	Groups      []offerGroup                 `json:"groups,omitempty"`
	Models      map[string][]offerModel      `json:"models,omitempty"`
	Prices      map[string]map[string]string `json:"prices,omitempty"`
	PricesRead  bool                         `json:"prices_read,omitempty"`
	Next        int                          `json:"next,omitempty"`
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

const offerVersion = 2

// Offer implements adapter.Manager. A declaration reads groups once, the price list once, and each group's models in
// turn; each read is one request against the declared bucket, so a rate limit stops the pass with its position stored.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	st := decodeOffer(from, m.a.offer.fingerprint)
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
	if len(fmts) == 0 {
		fmts = p.fallback
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
	multiplier, ok := new(big.Rat).SetString(ratio)
	if !ok {
		return nil, fmt.Errorf("%w: the ratio of a group is not a decimal", adapter.ErrUnrecognised)
	}
	scale := new(big.Rat).Mul(p.prices.scale, multiplier)
	rates := prices[model]
	if scale.Cmp(big.NewRat(1, 1)) != 0 {
		rates = make(map[string]string, len(prices[model]))
		for item, text := range prices[model] {
			scaled, err := scaledRate(text, scale)
			if err != nil {
				return nil, fmt.Errorf("%w: the cost of %s: %w", adapter.ErrUnrecognised, model, err)
			}
			rates[item] = scaled
		}
	}
	raw, err := jsonx.MarshalCanonical(struct {
		V        int               `json:"v"`
		Currency string            `json:"currency"`
		Rates    map[string]string `json:"rates"`
	}{1, platform.CurrencyNone.String(), rates})
	if err != nil {
		return nil, fmt.Errorf("declared: the cost of %s: %w", model, err)
	}
	schedule, err := catalog.ParseSchedule(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: the cost of %s: %w", adapter.ErrUnrecognised, model, err)
	}
	return &schedule, nil
}

// scaledRate applies the declaration's native-unit scale and the group's ratio before the parsed schedule sees a
// price. The raw number can exceed platform.UnitPrice's per-token limit and still become a valid final price after
// scaling; parsing first would reject it. The final price is rounded up once, at 10⁻¹⁵.
func scaledRate(text string, factor *big.Rat) (string, error) {
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Sign() < 0 {
		return "", fmt.Errorf("price %q is not a decimal", text)
	}
	scaled := new(big.Rat).Mul(value, factor)
	units, rem := new(big.Int).QuoRem(
		new(big.Int).Mul(scaled.Num(), big.NewInt(unitPriceScale)),
		scaled.Denom(), new(big.Int),
	)
	if rem.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	if !units.IsInt64() || units.Int64() > maxUnitPrice {
		return "", fmt.Errorf("price %q is over the unit-price limit after scaling", text)
	}
	return formatUnitPrice(units), nil
}

const (
	unitPriceScale = 1_000_000_000_000_000
	maxUnitPrice   = 1000 * unitPriceScale
)

// formatUnitPrice writes units of 10⁻¹⁵ in UnitPrice's canonical spelling: no leading zeros, no trailing zeros.
func formatUnitPrice(units *big.Int) string {
	if units.Sign() == 0 {
		return "0"
	}
	digits := units.String()
	if len(digits) <= 15 {
		digits = strings.Repeat("0", 16-len(digits)) + digits
	}
	whole, fraction := digits[:len(digits)-15], strings.TrimRight(digits[len(digits)-15:], "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

func encodeOffer(st *offerState) adapter.Listing {
	st.V = offerVersion
	return adapter.ListingOf(st)
}

func decodeOffer(from adapter.Listing, fingerprint string) *offerState {
	fresh := &offerState{V: offerVersion, Fingerprint: fingerprint}
	var st offerState
	if len(from) == 0 || jsonx.UnmarshalKnown(from, &st) != nil || st.V != offerVersion ||
		st.Fingerprint != fingerprint || st.Next < 0 || st.Next > len(st.Groups) {
		return fresh
	}
	return &st
}
