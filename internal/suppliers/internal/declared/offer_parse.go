package declared

import (
	"fmt"
	"math/big"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

func parseGroups(p offerPlan, body []byte) ([]offerGroup, error) {
	raw, ok := p.groups.items.lookup(body)
	if !ok {
		return nil, missing("groups")
	}
	items, err := readArray(raw, "groups")
	if err != nil {
		return nil, err
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
	items, err := readArray(raw, "prices")
	if err != nil {
		return nil, err
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
	items, err := readArray(raw, "models")
	if err != nil {
		return nil, err
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
