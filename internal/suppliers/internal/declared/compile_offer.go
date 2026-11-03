package declared

import (
	"errors"
	"fmt"
	"math/big"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

func (c *compiler) offer(spec Offer) (offerPlan, error) {
	var o offerPlan
	var err error
	if o.groups, err = c.groups(spec.Groups); err != nil {
		return o, err
	}
	if o.models, err = c.models(spec.Models); err != nil {
		return o, err
	}
	if spec.Prices != nil {
		if o.prices, err = c.prices(*spec.Prices); err != nil {
			return o, err
		}
	}
	o.formats = make(map[string]protocol.Format, len(spec.Formats))
	for from, to := range spec.Formats {
		if from == "" {
			return o, errors.New("offer.formats: an empty value")
		}
		f, ok := formatNames[to]
		if !ok {
			return o, fmt.Errorf("offer.formats: %q is not a protocol format", to)
		}
		o.formats[from] = f
	}
	return o, nil
}

func (c *compiler) groups(spec Groups) (groupPlan, error) {
	var g groupPlan
	var err error
	if g.path, err = c.pathTemplate("offer.groups.path", spec.Path); err != nil {
		return g, err
	}
	if g.items, err = c.path("offer.groups.items", spec.Items); err != nil {
		return g, err
	}
	if g.ref, err = c.path("offer.groups.ref", spec.Ref); err != nil {
		return g, err
	}
	if g.name, err = c.optionalPath("offer.groups.name", spec.Name); err != nil {
		return g, err
	}
	if g.ratio, err = c.optionalPath("offer.groups.ratio", spec.Ratio); err != nil {
		return g, err
	}
	if g.formats, err = c.optionalPath("offer.groups.formats", spec.Formats); err != nil {
		return g, err
	}
	if g.skipRatio, err = skipRatios(spec.SkipRatio, g.ratio); err != nil {
		return g, err
	}
	if g.bucket, err = c.bucket("offer.groups", spec.Bucket); err != nil {
		return g, err
	}
	return g, nil
}

func skipRatios(names []string, ratio *path) (map[string]bool, error) {
	if len(names) > 0 && ratio == nil {
		return nil, errors.New("offer.groups.skip_ratio: a ratio is not read")
	}
	out := make(map[string]bool, len(names))
	for _, s := range names {
		if s == "" || out[s] {
			return nil, fmt.Errorf("offer.groups.skip_ratio: %q is empty or listed twice", s)
		}
		out[s] = true
	}
	return out, nil
}

func (c *compiler) models(spec Models) (modelPlan, error) {
	var m modelPlan
	var err error
	if m.path, err = c.pathTemplate("offer.models.path", spec.Path, "group"); err != nil {
		return m, err
	}
	if !m.path.has("group") {
		return m, errors.New("offer.models.path: must name {group}, the group whose models are read")
	}
	if m.items, err = c.path("offer.models.items", spec.Items); err != nil {
		return m, err
	}
	if m.name, err = c.optionalPath("offer.models.name", spec.Name); err != nil {
		return m, err
	}
	if m.formats, err = c.optionalPath("offer.models.formats", spec.Formats); err != nil {
		return m, err
	}
	if m.group, err = c.optionalPath("offer.models.group", spec.Group); err != nil {
		return m, err
	}
	if m.bucket, err = c.bucket("offer.models", spec.Bucket); err != nil {
		return m, err
	}
	return m, nil
}

func (c *compiler) prices(spec Prices) (*pricePlan, error) {
	p := &pricePlan{scale: big.NewRat(1, 1)}
	var err error
	if p.path, err = c.pathTemplate("offer.prices.path", spec.Path); err != nil {
		return nil, err
	}
	if p.items, err = c.path("offer.prices.items", spec.Items); err != nil {
		return nil, err
	}
	if p.model, err = c.path("offer.prices.model", spec.Model); err != nil {
		return nil, err
	}
	if len(spec.Rates) == 0 {
		return nil, errors.New("offer.prices.rates: declare at least one item")
	}
	p.rates = make(map[catalog.Item]path, len(spec.Rates))
	for name, field := range spec.Rates {
		item, err := catalog.ParseItem(name)
		if err != nil {
			return nil, fmt.Errorf("offer.prices.rates: %w", err)
		}
		if p.rates[item], err = c.path("offer.prices.rates."+name, field); err != nil {
			return nil, err
		}
	}
	if spec.Scale != "" {
		scale, ok := new(big.Rat).SetString(spec.Scale)
		if !ok || scale.Sign() <= 0 {
			return nil, fmt.Errorf("offer.prices.scale: %q is not a positive multiplier", spec.Scale)
		}
		p.scale = scale
	}
	if p.bucket, err = c.bucket("offer.prices", spec.Bucket); err != nil {
		return nil, err
	}
	return p, nil
}
