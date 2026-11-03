package declared

import (
	"errors"
	"fmt"

	"axiom/internal/catalog"
)

func (c *compiler) charges(spec Charges) (chargesPlan, error) {
	var p chargesPlan
	if err := c.chargeCore(&p, spec); err != nil {
		return p, err
	}
	if err := c.chargeExtras(&p, spec); err != nil {
		return p, err
	}
	return p, nil
}

func (c *compiler) chargeCore(p *chargesPlan, spec Charges) error {
	var err error
	if p.path, err = c.pathTemplate("charges.path", spec.Path, "start", "end", "page", "size"); err != nil {
		return err
	}
	for _, v := range []string{"start", "end", "page", "size"} {
		if !p.path.has(v) {
			return fmt.Errorf("charges.path: must name {%s}", v)
		}
	}
	if spec.PageSize <= 0 || spec.PageSize > 1000 {
		return fmt.Errorf("charges.page_size: %d, want 1 to 1000", spec.PageSize)
	}
	p.pageSize = spec.PageSize
	if p.items, err = c.path("charges.items", spec.Items); err != nil {
		return err
	}
	if p.ref, err = c.path("charges.ref", spec.Ref); err != nil {
		return err
	}
	if p.key, err = c.path("charges.key", spec.Key); err != nil {
		return err
	}
	if p.occurred, err = c.path("charges.time", spec.Time); err != nil {
		return err
	}
	if p.amount, err = c.path("charges.amount", spec.Amount); err != nil {
		return err
	}
	if p.model, err = c.optionalPath("charges.model", spec.Model); err != nil {
		return err
	}
	return nil
}

func (c *compiler) chargeExtras(p *chargesPlan, spec Charges) error {
	var err error
	if p.funding, err = c.funding("charges.funding", spec.Funding); err != nil {
		return err
	}
	if p.filter, err = c.chargeFilter(spec.Filter); err != nil {
		return err
	}
	if p.usage, err = c.chargeUsage(spec.Usage); err != nil {
		return err
	}
	p.bucket, err = c.bucket("charges", spec.Bucket)
	return err
}

func (c *compiler) chargeFilter(spec *Filter) (*filterPlan, error) {
	if spec == nil {
		return nil, nil //nolint:nilnil // the declaration does not filter charges
	}
	f := &filterPlan{value: spec.Value}
	var err error
	if f.path, err = c.path("charges.filter.path", spec.Path); err != nil {
		return nil, err
	}
	if f.value == "" {
		return nil, errors.New("charges.filter.value: an empty value")
	}
	return f, nil
}

func (c *compiler) chargeUsage(spec map[string]string) (map[catalog.Item]path, error) {
	out := make(map[catalog.Item]path, len(spec))
	for name, field := range spec {
		item, err := catalog.ParseItem(name)
		if err != nil {
			return nil, fmt.Errorf("charges.usage: %w", err)
		}
		if out[item], err = c.path("charges.usage."+name, field); err != nil {
			return nil, err
		}
	}
	return out, nil
}
