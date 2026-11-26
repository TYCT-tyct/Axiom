package persist

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/store"
)

// intakeRule is which of a supplier's upstream names the projection makes endpoints of (docs/domains/suppliers.md
// 「接入规则」). The zero rule is `listed`, the choice every supplier had before rules were stored: a name of a model on
// sale.
type intakeRule struct {
	// drafts is `known`: a name of a draft model is mapped and taken too (withDrafts); its endpoint waits until the
	// model is listed, because routing builds endpoints of listed models only (gateway's listedModelOf).
	drafts bool
	// include and exclude are `names`: a name of a model on sale is taken when it matches one of include and none of
	// exclude. include is empty in every other mode.
	include, exclude []*regexp.Regexp
}

// readIntakeRule reads the supplier's stored rule; a supplier with none has the zero rule.
func (p *projector) readIntakeRule(ctx context.Context) (intakeRule, error) {
	rows, err := p.q.GetIntakeRule(ctx, p.tx, p.id)
	if err != nil {
		return intakeRule{}, fmt.Errorf("read the intake rule: %w", err)
	}
	if len(rows) == 0 {
		return intakeRule{}, nil
	}
	return intakeRuleOf(rows[0])
}

func intakeRuleOf(r store.GetIntakeRuleRow) (intakeRule, error) {
	switch r.Mode {
	case "listed":
		return intakeRule{}, nil
	case "known":
		return intakeRule{drafts: true}, nil
	case "names":
		rule := intakeRule{include: make([]*regexp.Regexp, len(r.Include)), exclude: make([]*regexp.Regexp, len(r.Exclude))}
		for i, p := range r.Include {
			rule.include[i] = namePattern(p)
		}
		for i, p := range r.Exclude {
			rule.exclude[i] = namePattern(p)
		}
		return rule, nil
	}
	return intakeRule{}, fmt.Errorf("the intake rule has the mode %q, which this program does not know", r.Mode)
}

// withDrafts is the catalog the projection maps by under the rule `known`: the snapshot, which holds no draft, and for
// what it does not hold the catalog's own recognition in the projection's transaction, drafts included
// (catalog.RecognizeIn, catalog.ModelsIn). Only a draft is taken from there, so a name of a listed or retired model maps
// by the snapshot alone, exactly as under `listed`. It asks the tables once for every name of the listing and every
// alias's model that the snapshot does not know.
func (p *projector) withDrafts(ctx context.Context) (draftCatalog, error) {
	names, ids := p.notInSnapshot()
	known, err := catalog.RecognizeIn(ctx, p.tx, names)
	if err != nil {
		return draftCatalog{}, err
	}
	models, err := catalog.ModelsIn(ctx, p.tx, ids)
	if err != nil {
		return draftCatalog{}, err
	}
	c := draftCatalog{snap: p.snap, names: map[string]catalog.Known{}, models: map[catalog.ModelID]catalog.Known{}}
	for name, k := range known {
		if k.Status == catalog.StatusDraft {
			c.names[name], c.models[k.Model] = k, k
		}
	}
	for id, k := range models {
		if k.Status == catalog.StatusDraft {
			c.models[id] = k
		}
	}
	return c, nil
}

// notInSnapshot are the listing's names the snapshot does not recognise and the models of aliases it does not hold.
func (p *projector) notInSnapshot() (names []string, ids []catalog.ModelID) {
	for _, g := range p.st.Groups {
		for _, m := range g.Models {
			if _, ok := p.snap.Recognize(m.Name); !ok {
				names = append(names, m.Name)
			}
		}
	}
	for _, a := range p.aliases {
		if _, ok := p.snap.Model(a.model); !ok {
			ids = append(ids, a.model)
		}
	}
	return names, ids
}

// draftCatalog is the snapshot with the drafts withDrafts read beside it.
type draftCatalog struct {
	snap   *catalog.Snapshot
	names  map[string]catalog.Known
	models map[catalog.ModelID]catalog.Known
}

func (c draftCatalog) Recognize(name string) (catalog.Recognition, bool) {
	if r, ok := c.snap.Recognize(name); ok {
		return r, true
	}
	k, ok := c.names[name]
	return catalog.Recognition{Model: k.Model, Exact: k.Exact}, ok
}

// Model is a snapshot's model, or a draft with its status alone: mapping reads nothing else of it.
func (c draftCatalog) Model(id catalog.ModelID) (catalog.Model, bool) {
	if m, ok := c.snap.Model(id); ok {
		return m, true
	}
	k, ok := c.models[id]
	return catalog.Model{ID: k.Model, Status: k.Status}, ok
}

// namePattern is a pattern where * stands for any run of characters, matching the whole name whatever its case.
func namePattern(pattern string) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile(`(?is)\A` + strings.Join(parts, ".*") + `\z`)
}

// takes reports whether the rule makes an endpoint of a name the catalog mapped.
func (r intakeRule) takes(name string) bool {
	if len(r.include) == 0 {
		return true
	}
	return matchesAny(r.include, name) && !matchesAny(r.exclude, name)
}

func matchesAny(patterns []*regexp.Regexp, name string) bool {
	for _, p := range patterns {
		if p.MatchString(name) {
			return true
		}
	}
	return false
}
