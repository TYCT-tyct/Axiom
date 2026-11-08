package routing

import (
	"slices"

	"axiom/internal/platform/jsonx"
)

// What a page shows of a preset's rules and whether they keep any endpoint are worked out here, from the compiled rules
// and the snapshot Plan routes by, so every frontend shows the same and none mirrors the rules' semantics
// (docs/domains/routing.md「接口、权限与审计」).

// RulesSummary is what a list shows of a preset's rules: the default rule's sort and source classes (nil when it keeps
// every class), whether it keeps only verified endpoints, whether any rule limits a price, and how many overrides
// follow.
type RulesSummary struct {
	Sort         string
	Classes      []string
	VerifiedOnly bool
	Priced       bool
	Overrides    int
}

// Summary is what a list shows of the stored preset's rules, or false when this node cannot read them, as for rules a
// newer node wrote: a page then says it does not know rather than guess.
func (s StoredPreset) Summary() (RulesSummary, bool) {
	p := Preset{Scope: s.Scope, Currency: s.Currency}
	if err := p.ReadRules(s.Rules); err != nil {
		return RulesSummary{}, false
	}
	return p.summary(), true
}

func (p *Preset) summary() RulesSummary {
	out := RulesSummary{
		Sort: sortName(p.Strategy), VerifiedOnly: p.VerifiedOnly, Priced: anyPriced(p.Rule, p.Authors, p.Models),
		Overrides: len(p.Authors) + len(p.Models),
	}
	if !slices.Contains(p.Classes, "*") {
		out.Classes = make([]string, len(p.Classes))
		for i, c := range p.Classes {
			out.Classes[i] = string(c)
		}
	}
	return out
}

// sortName is the rules' name of a sort a compiled rule holds, which ReadRules took from sortNames.
func sortName(s Strategy) string {
	for name, v := range sortNames {
		if v == s {
			return name
		}
	}
	return ""
}

// Unmatched is a rule of a preset that keeps no endpoint. Rule is the override's index in the rules, -1 for the default
// rule. Fields are the conditions the rule itself writes that alone keep no endpoint, by their names in the rules
// (`classes`, `max_price.prompt`); empty when only the conditions together, or ones it inherits, keep none.
type Unmatched struct {
	Rule   int
	Fields []string
}

// CheckRules reads rules as a preset of p's layer and currency (ReadRules, so it refuses what a write refuses) and
// says what a list shows of them and which rules keep no endpoint in view of a's keys in the current snapshot, judged
// as KeyView judges a model list: hard conditions only, the price at the standard tier and the shortest prompt, nothing
// transient. The default rule is judged against every endpoint in view, an override against its author's or model's;
// one whose author or model has none in view is not judged, nor is any rule before the router has a snapshot. Nothing
// is stored: rules that keep nothing are still a preset a write takes, and fail every call it routes.
func (r *Router) CheckRules(p Preset, rules []byte, a Admission) (RulesSummary, []Unmatched, error) {
	if err := p.ReadRules(rules); err != nil {
		return RulesSummary{}, nil, err
	}
	var j rulesJSON
	if err := jsonx.UnmarshalKnown(rules, &j); err != nil {
		return RulesSummary{}, nil, err // ReadRules read the same bytes
	}
	snap := r.snap.Load()
	if snap == nil {
		return p.summary(), nil, nil
	}
	view := snap.inView(a)
	whole := &preset{Preset: p}
	var out []Unmatched
	judge := func(index int, j ruleJSON, rule Rule, in func(string) bool) {
		if kept, judged := view.keeps(whole, rule, in); kept || !judged {
			return
		}
		u := Unmatched{Rule: index, Fields: []string{}}
		for _, c := range alone(j, rule) {
			// The conditions alone are held without the platform's advanced settings, which a rule does not write.
			if kept, _ := view.keeps(&preset{}, c.rule, in); !kept {
				u.Fields = append(u.Fields, c.name)
			}
		}
		out = append(out, u)
	}
	judge(-1, j.Default, p.Rule, func(string) bool { return true })
	for i, o := range j.Rules {
		if o.Author != "" {
			judge(i, o, p.Authors[o.Author], func(slug string) bool { return authorOf(slug) == o.Author })
		} else {
			judge(i, o, p.Models[o.Model], func(slug string) bool { return slug == o.Model })
		}
	}
	return p.summary(), out, nil
}

// inView are the snapshot's endpoints a's keys can see and that have a price for its account and tier, with that price.
type inView []viewed

type viewed struct {
	c     *candidate
	price *candidatePrice
}

func (s *Snapshot) inView(a Admission) inView {
	var out inView
	for _, model := range s.byModel {
		for _, c := range model {
			if price := c.viewPrice(a); price != nil && c.Owner.visibleTo(a) {
				out = append(out, viewed{c, price})
			}
		}
	}
	return out
}

// keeps reports whether rule of p keeps an endpoint of a model in takes, and whether in takes any endpoint at all.
func (v inView) keeps(p *preset, rule Rule, in func(slug string) bool) (kept, some bool) {
	for _, e := range v {
		if !in(e.c.ModelSlug) {
			continue
		}
		if viewAllows(p, &rule, e.c, e.price) {
			return true, true
		}
		some = true
	}
	return false, some
}

// condition is one condition of a rule, as a rule that holds an endpoint to it and to nothing else.
type condition struct {
	name string
	rule Rule
}

// alone are the conditions j itself writes, with the values its compiled rule holds for them.
func alone(j ruleJSON, compiled Rule) []condition {
	var out []condition
	add := func(written bool, name string, set func(*Rule)) {
		if written {
			c := allowAll
			set(&c)
			out = append(out, condition{name, c})
		}
	}
	add(j.Classes != nil, "classes", func(c *Rule) { c.Classes = compiled.Classes })
	add(j.Policies != nil, "policies", func(c *Rule) { c.Policies = compiled.Policies })
	add(j.MinScore != nil, "min_score", func(c *Rule) { c.MinScore = compiled.MinScore })
	add(j.MinScoreConfidence != nil, "min_score_confidence", func(c *Rule) { c.MinScoreConfidence = compiled.MinScoreConfidence })
	add(j.VerifiedOnly != nil && *j.VerifiedOnly, "verified_only", func(c *Rule) { c.VerifiedOnly = true })
	if j.MaxPrice != nil {
		for _, it := range j.MaxPrice.items() {
			add(it.value != nil, "max_price."+it.name, func(c *Rule) {
				c.MaxPrice.Set.Add(it.item)
				c.MaxPrice.Max[it.item] = compiled.MaxPrice.Max[it.item]
			})
		}
	}
	return out
}
