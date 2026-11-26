package persist

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"axiom/internal/suppliers/internal/store"
)

// intakeRule is which of a supplier's upstream names the projection makes endpoints of (docs/domains/suppliers.md
// 「接入规则」). The zero rule is `listed`, the choice every supplier had before rules were stored: a name of a model on
// sale.
type intakeRule struct {
	// drafts is `known`: a name of a draft model is mapped and taken too; its endpoint waits until the model is listed,
	// because routing serves only listed models.
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
