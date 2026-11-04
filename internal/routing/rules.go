package routing

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// PresetScope is the layer a routing preset belongs to and a default is set at: the platform, a site, an account of a
// site, or a workspace of an account (docs/domains/routing.md「默认链与 key 落章」).
type PresetScope uint8

// The layers, from the widest.
const (
	ScopePlatform PresetScope = iota
	ScopeSite
	ScopeAccount
	ScopeWorkspace
)

var scopeNames = [...]string{"platform", "site", "account", "workspace"}

func (s PresetScope) String() string { return scopeNames[s] }

// ParseScope reads a layer as the tables write it.
func ParseScope(name string) (PresetScope, error) {
	for scope := ScopePlatform; scope <= ScopeWorkspace; scope++ {
		if scopeNames[scope] == name {
			return scope, nil
		}
	}
	return 0, fmt.Errorf("routing: %q is not a preset scope", name)
}

// Rule is what a preset holds the endpoints of a model to: its default rule, or one of its rules for a model author or
// a model. A compiled rule is whole: what a rule left out is already its parent's.
type Rule struct {
	Strategy Strategy
	Classes  ClassSet
	Policies PolicySet
	// MinScore is the product score (the fixed benchmark against the official direct run, 0 to 100) and
	// MinScoreConfidence its confidence (0 to 1) an endpoint needs. They are not the operations quality ranking reads:
	// an endpoint without a score has 0 and 0, so a preset that sets either keeps none of it (docs/domains/routing.md
	// 「产品质量与来源类别」).
	MinScore           float64
	MinScoreConfidence float64
	VerifiedOnly       bool
	MaxPrice           PriceLimit
}

// ErrRules is rules a preset cannot hold: not the shape of rules.go, a filter that widens what it inherits, a value out
// of range, or a setting its layer may not write.
var ErrRules = errors.New("routing: invalid preset rules")

// The bounds of the rules (docs/domains/routing.md「规则内容」). A version's rules are at most maxRulesBytes as
// PostgreSQL prints them, which the table checks too; the rest bound what a snapshot holds for one preset.
const (
	maxRulesBytes = 16 << 10
	maxRules      = 100
	maxOrderNames = 20
	// maxStickyTTL and maxAttemptWindow bound the platform's run-time settings, so a mistyped number cannot pin
	// sessions for years or hold a request in its route for hours.
	maxStickyTTL     = 24 * time.Hour
	maxAttemptWindow = 10 * time.Minute
)

var (
	authorSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	modelSlug  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}(:free)?(@[a-z0-9][a-z0-9._-]{0,39})?$`)
	classSlug  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// The JSON of a version's rules. A member this node does not know is refused, so a rule a newer node wrote is never
// half applied here: the preset is unavailable on this node instead (docs/domains/routing.md「滚动发布、失败与性能」).
type (
	rulesJSON struct {
		Default  ruleJSON      `json:"default"`
		Rules    []ruleJSON    `json:"rules"`
		Advanced *advancedJSON `json:"advanced"`
	}
	ruleJSON struct {
		Author             string     `json:"author"`
		Model              string     `json:"model"`
		Sort               string     `json:"sort"`
		Classes            []string   `json:"classes"`
		Policies           []string   `json:"policies"`
		MinScore           *float64   `json:"min_score"`
		MinScoreConfidence *float64   `json:"min_score_confidence"`
		VerifiedOnly       *bool      `json:"verified_only"`
		MaxPrice           *priceJSON `json:"max_price"`
	}
	// priceJSON holds decimal strings, never JSON numbers, so no float is in the way of the digits.
	priceJSON struct {
		Prompt     *string `json:"prompt"`
		Completion *string `json:"completion"`
		Request    *string `json:"request"`
	}
	// advancedJSON is the platform's run-time settings; the two durations are seconds.
	advancedJSON struct {
		Order         []string `json:"order"`
		NoFallbacks   bool     `json:"no_fallbacks"`
		MaxAttempts   int      `json:"max_attempts"`
		StickyTTL     int64    `json:"sticky_ttl"`
		AttemptWindow int64    `json:"attempt_window"`
		MinQuality    float64  `json:"min_quality"`
		MinConfidence float64  `json:"min_confidence"`
	}
)

var sortNames = map[string]Strategy{
	"balanced": Balanced, "price": Cheapest, "throughput": Fastest, "latency": LowestLatency, "quality": BestQuality,
}

var policyNames = map[string]Policy{"no_retention": PolicyNoRetention, "retained": PolicyRetained, "may_train": PolicyMayTrain}

// allowAll is what the default rule narrows: every class and policy, no threshold and no limit.
var allowAll = Rule{Classes: AllClasses, Policies: AllPolicies}

// ReadRules fills p's rules and run-time settings from one version's rules, which it checks: p.Scope says whether the
// advanced settings may be there, and p.Currency must be set exactly when a platform preset limits a price, since a
// site's preset is priced in the site's currency and a platform one in the currency it names. It is the one check of
// rules, for writing a version and for building a snapshot, so the two never disagree.
func (p *Preset) ReadRules(rules []byte) error {
	if len(rules) > maxRulesBytes {
		return fmt.Errorf("%w: %d bytes, the most is %d", ErrRules, len(rules), maxRulesBytes)
	}
	var j rulesJSON
	if err := jsonx.UnmarshalKnown(rules, &j); err != nil {
		return fmt.Errorf("%w: %w", ErrRules, err)
	}
	if j.Default.Author != "" || j.Default.Model != "" {
		return fmt.Errorf("%w: the default rule names no author or model", ErrRules)
	}
	def, err := j.Default.narrow(allowAll, "default")
	if err != nil {
		return err
	}
	authors, models, err := overrides(j.Rules, def)
	if err != nil {
		return err
	}
	if err := p.checkLayer(anyPriced(def, authors, models), j.Advanced != nil); err != nil {
		return err
	}
	p.Rule, p.Authors, p.Models = def, authors, models
	return p.readAdvanced(j.Advanced)
}

// checkLayer holds the preset's currency and advanced settings to its layer.
func (p *Preset) checkLayer(priced, advanced bool) error {
	switch {
	case p.Scope == ScopePlatform && priced && p.Currency == (platform.Currency{}):
		return fmt.Errorf("%w: a platform preset that limits a price names its currency", ErrRules)
	case p.Scope != ScopePlatform && p.Currency != (platform.Currency{}):
		return fmt.Errorf("%w: a %s preset is in its site's currency and names none", ErrRules, p.Scope)
	case p.Scope != ScopePlatform && advanced:
		return fmt.Errorf("%w: only a platform preset has advanced settings", ErrRules)
	}
	return nil
}

// anyPriced reports whether any of the rules limits a price.
func anyPriced(def Rule, rules ...map[string]Rule) bool {
	priced := def.MaxPrice.Set != 0
	for _, m := range rules {
		for _, r := range m {
			priced = priced || r.MaxPrice.Set != 0
		}
	}
	return priced
}

// overrides compiles the rules by author, then those by model, each held to what it inherits: a model's author rule
// when there is one, the default otherwise.
func overrides(rules []ruleJSON, def Rule) (authors, models map[string]Rule, err error) {
	if len(rules) > maxRules {
		return nil, nil, fmt.Errorf("%w: %d rules, the most is %d", ErrRules, len(rules), maxRules)
	}
	for i, r := range rules {
		if err := r.checkKey(i); err != nil {
			return nil, nil, err
		}
	}
	authors, err = compileRules(rules, func(r ruleJSON) string { return r.Author }, func(string) Rule { return def })
	if err != nil {
		return nil, nil, err
	}
	models, err = compileRules(rules, func(r ruleJSON) string { return r.Model }, func(model string) Rule {
		if a, ok := authors[authorOf(model)]; ok {
			return a
		}
		return def
	})
	return authors, models, err
}

// checkKey: a rule names one catalog author or one catalog model.
func (j ruleJSON) checkKey(i int) error {
	switch {
	case (j.Author == "") == (j.Model == ""):
		return fmt.Errorf("%w: rules[%d] names an author or a model, not both", ErrRules, i)
	case j.Author != "" && !authorSlug.MatchString(j.Author), j.Model != "" && !modelSlug.MatchString(j.Model):
		return fmt.Errorf("%w: rules[%d] does not name a catalog author or model", ErrRules, i)
	}
	return nil
}

// compileRules compiles the rules that key names, each over the rule parent gives for its key; nil when there are none.
func compileRules(rules []ruleJSON, key func(ruleJSON) string, parent func(string) Rule) (map[string]Rule, error) {
	var out map[string]Rule
	for i, r := range rules {
		k := key(r)
		if k == "" {
			continue
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%w: rules[%d] repeats %q", ErrRules, i, k)
		}
		compiled, err := r.narrow(parent(k), fmt.Sprintf("rules[%d]", i))
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]Rule{}
		}
		out[k] = compiled
	}
	return out, nil
}

// authorOf is the author of a catalog model slug: the catalog requires a slug to start with its author's.
func authorOf(slug string) string {
	author, _, _ := strings.Cut(slug, "/")
	return author
}

// narrow is the rule j writes over parent. Each filter j sets must be at least as strict as parent's, and one it leaves
// out is parent's, so a rule can never widen what it inherits. The sort is an order, not a filter: one j sets
// replaces parent's.
func (j ruleJSON) narrow(parent Rule, where string) (Rule, error) {
	r := parent
	if j.Sort != "" {
		s, ok := sortNames[j.Sort]
		if !ok {
			return Rule{}, fmt.Errorf("%w: %s.sort %q is not a sort", ErrRules, where, j.Sort)
		}
		r.Strategy = s
	}
	if err := j.narrowSets(&r, parent, where); err != nil {
		return Rule{}, err
	}
	if err := j.narrowLimits(&r, parent, where); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// narrowSets narrows the classes and the data policies.
func (j ruleJSON) narrowSets(r *Rule, parent Rule, where string) (err error) {
	if j.Classes != nil {
		if r.Classes, err = narrowClasses(j.Classes, parent.Classes, where); err != nil {
			return err
		}
	}
	if j.Policies != nil {
		r.Policies, err = narrowPolicies(j.Policies, parent.Policies, where)
	}
	return err
}

// narrowLimits narrows the product score and its confidence, the verification and the price limits.
func (j ruleJSON) narrowLimits(r *Rule, parent Rule, where string) (err error) {
	if r.MinScore, err = atLeast(j.MinScore, parent.MinScore, 100, where+".min_score"); err != nil {
		return err
	}
	if r.MinScoreConfidence, err = atLeast(j.MinScoreConfidence, parent.MinScoreConfidence, 1, where+".min_score_confidence"); err != nil {
		return err
	}
	if j.VerifiedOnly != nil {
		if parent.VerifiedOnly && !*j.VerifiedOnly {
			return fmt.Errorf("%w: %s.verified_only cannot turn off what it inherits", ErrRules, where)
		}
		r.VerifiedOnly = *j.VerifiedOnly
	}
	if j.MaxPrice != nil {
		r.MaxPrice, err = j.MaxPrice.narrow(parent.MaxPrice, where+".max_price")
	}
	return err
}

// narrowClasses is a class list held to the inherited one: "*" alone, only where every class is inherited, or slugs
// all in the inherited list. An empty list would keep no endpoint, which is a mistake rather than a rule.
func narrowClasses(names []string, parent ClassSet, where string) (ClassSet, error) {
	all := slices.Contains(parent, "*")
	if len(names) == 1 && names[0] == "*" {
		if !all {
			return nil, fmt.Errorf("%w: %s.classes \"*\" is wider than what it inherits", ErrRules, where)
		}
		return AllClasses, nil
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: %s.classes allows no class", ErrRules, where)
	}
	out := make(ClassSet, 0, len(names))
	for _, n := range names {
		c := Class(n)
		switch {
		case !classSlug.MatchString(n):
			return nil, fmt.Errorf("%w: %s.classes %q is not a source class slug", ErrRules, where, n)
		case slices.Contains(out, c):
			return nil, fmt.Errorf("%w: %s.classes repeats %q", ErrRules, where, n)
		case !all && !slices.Contains(parent, c):
			return nil, fmt.Errorf("%w: %s.classes %q is not in what it inherits", ErrRules, where, n)
		}
		out = append(out, c)
	}
	return out, nil
}

func narrowPolicies(names []string, parent PolicySet, where string) (PolicySet, error) {
	if len(names) == 1 && names[0] == "*" {
		if parent != AllPolicies {
			return 0, fmt.Errorf("%w: %s.policies \"*\" is wider than what it inherits", ErrRules, where)
		}
		return AllPolicies, nil
	}
	var set PolicySet
	for _, n := range names {
		p, ok := policyNames[n]
		switch {
		case !ok:
			return 0, fmt.Errorf("%w: %s.policies %q is not a data policy", ErrRules, where, n)
		case set.Has(p):
			return 0, fmt.Errorf("%w: %s.policies repeats %q", ErrRules, where, n)
		case !parent.Has(p):
			return 0, fmt.Errorf("%w: %s.policies %q is not in what it inherits", ErrRules, where, n)
		}
		set |= 1 << p
	}
	if set == 0 {
		return 0, fmt.Errorf("%w: %s.policies allows no data policy", ErrRules, where)
	}
	return set, nil
}

// atLeast is a minimum held to the inherited one and to [0, most].
func atLeast(v *float64, parent, most float64, where string) (float64, error) {
	if v == nil {
		return parent, nil
	}
	switch {
	case math.IsNaN(*v) || *v < 0 || *v > most:
		return 0, fmt.Errorf("%w: %s must be from 0 to %v", ErrRules, where, most)
	case *v < parent:
		return 0, fmt.Errorf("%w: %s is below what it inherits", ErrRules, where)
	}
	return *v, nil
}

// narrow is the price limit j writes over parent: an item it names is at most parent's limit for it, an item it leaves
// out keeps parent's. The values have the request's max_price range and precision (PricePerMillion, PricePerRequest).
func (j priceJSON) narrow(parent PriceLimit, where string) (PriceLimit, error) {
	out := parent
	for _, it := range [...]struct {
		value *string
		item  catalog.Item
		name  string
		read  func([]byte) (platform.UnitPrice, bool)
	}{
		{j.Prompt, catalog.ItemInput, "prompt", PricePerMillion},
		{j.Completion, catalog.ItemOutput, "completion", PricePerMillion},
		{j.Request, catalog.ItemRequest, "request", PricePerRequest},
	} {
		if it.value == nil {
			continue
		}
		price, ok := it.read([]byte(*it.value))
		if !ok {
			return PriceLimit{}, fmt.Errorf("%w: %s.%s is not a price this limit can hold", ErrRules, where, it.name)
		}
		if parent.Set.Has(it.item) && price.Cmp(parent.Max[it.item]) > 0 {
			return PriceLimit{}, fmt.Errorf("%w: %s.%s is above what it inherits", ErrRules, where, it.name)
		}
		out.Set.Add(it.item)
		out.Max[it.item] = price
	}
	return out, nil
}

// readAdvanced fills the platform's run-time settings, which only a platform preset has.
func (p *Preset) readAdvanced(a *advancedJSON) error {
	if a == nil {
		return nil
	}
	if err := a.check(); err != nil {
		return err
	}
	p.OrderNames, p.NoFallbacks, p.MaxAttempts = a.Order, a.NoFallbacks, a.MaxAttempts
	p.StickyTTL, p.AttemptWindow = time.Duration(a.StickyTTL)*time.Second, time.Duration(a.AttemptWindow)*time.Second
	p.MinQuality, p.MinConfidence = a.MinQuality, a.MinConfidence
	return nil
}

// check holds the settings to their ranges.
func (a *advancedJSON) check() error {
	for _, c := range []struct {
		bad bool
		why string
	}{
		{len(a.Order) > maxOrderNames, fmt.Sprintf("order names %d groups, the most is %d", len(a.Order), maxOrderNames)},
		{slices.Contains(a.Order, "") || len(slices.Compact(slices.Sorted(slices.Values(a.Order)))) != len(a.Order), "order names empty or repeated groups"},
		{a.NoFallbacks && len(a.Order) == 0, "no_fallbacks needs an order"},
		{a.MaxAttempts < 0 || a.MaxAttempts > MaxEntries+1, fmt.Sprintf("max_attempts must be from 0 to %d", MaxEntries+1)},
		{a.StickyTTL < 0 || time.Duration(a.StickyTTL)*time.Second > maxStickyTTL, fmt.Sprintf("sticky_ttl must be from 0 to %d seconds", int64(maxStickyTTL/time.Second))},
		{a.AttemptWindow < 0 || time.Duration(a.AttemptWindow)*time.Second > maxAttemptWindow, fmt.Sprintf("attempt_window must be from 0 to %d seconds", int64(maxAttemptWindow/time.Second))},
		{!fraction(a.MinQuality) || !fraction(a.MinConfidence), "min_quality and min_confidence must be from 0 to 1"},
	} {
		if c.bad {
			return fmt.Errorf("%w: advanced.%s", ErrRules, c.why)
		}
	}
	return nil
}

func fraction(v float64) bool { return v >= 0 && v <= 1 } // false for NaN too
