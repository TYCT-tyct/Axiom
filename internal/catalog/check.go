package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"axiom/internal/platform/pg"
)

// ModelCheck is the catalog's view of one model of a vendor, for the first-version acceptance check
// (docs/domains/catalog.md「参考价怎么进入目录」). Problems is empty when the model is listed, has a public name and a
// price in each currency it has, and the prices cover everything its spec says the upstream may report.
type ModelCheck struct {
	Slug        string      `json:"slug"`
	Status      Status      `json:"status"`
	PublicNames []string    `json:"public_names"`
	Source      PriceSource `json:"price_source"`
	Currencies  []string    `json:"currencies"`
	Problems    []string    `json:"problems,omitempty"`
}

// Check reads every draft or listed model of the vendors, read-only. It does not know which models a vendor sells now:
// that is on the vendor's page, so a model the catalog lacks is found by comparing with a dataset (Unrecognized).
func (c *Catalog) Check(ctx context.Context, authors []string) ([]ModelCheck, error) {
	var checks []ModelCheck
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) error {
		rows, err := New().ListModelsOfAuthors(ctx, tx, authors)
		if err != nil {
			return err
		}
		for _, row := range rows {
			m, err := followerOf(ctx, tx, row)
			if err != nil {
				return err
			}
			check, err := checkModel(m)
			if err != nil {
				return err
			}
			checks = append(checks, check)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catalog check: %w", err)
	}
	return checks, nil
}

func checkModel(m syncModel) (ModelCheck, error) {
	check := ModelCheck{Slug: m.row.Slug, Status: Status(m.row.Status), Source: PriceSource(m.row.PriceSource), Currencies: m.heads.priced()}
	for _, n := range m.names {
		if Visibility(n.Visibility) == VisibilityPublic {
			check.PublicNames = append(check.PublicNames, n.Name)
		}
	}
	slices.Sort(check.PublicNames)
	problem := func(format string, args ...any) {
		check.Problems = append(check.Problems, fmt.Sprintf(format, args...))
	}
	if check.Status != StatusListed {
		problem("it is %s, not listed", check.Status)
	}
	if len(check.PublicNames) == 0 {
		problem("it has no public name")
	}
	spec, err := parseSpec(m.row.Spec)
	if err != nil {
		return ModelCheck{}, err
	}
	if !spec.declared {
		problem("its spec does not declare the items and service tiers the upstream may report")
	}
	if len(check.Currencies) == 0 {
		problem("it has no current price")
	}
	for _, currency := range check.Currencies {
		if err := covers(currency, *m.heads[currency].schedule, spec.reportable); err != nil {
			problem("%v", err)
		}
	}
	return check, nil
}

// Unrecognized lists the text models of the vendors in an OpenRouter list that no name of the catalog stands for, listed
// or retired: the models the vendors sell that the catalog lacks, as far as OpenRouter lists them. It uses the
// snapshot, so the catalog must have been refreshed.
func (c *Catalog) Unrecognized(entries []Entry, authors []string) ([]string, error) {
	snap, err := c.Current()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, e := range entries {
		author, _, _ := strings.Cut(e.Key, "/")
		if !e.Text || !slices.Contains(authors, author) {
			continue
		}
		if !slices.ContainsFunc(e.Names, func(name string) bool { r, ok := snap.Recognize(name); return ok && r.Exact }) {
			missing = append(missing, e.Key)
		}
	}
	slices.Sort(missing)
	return missing, nil
}
