package billing

import (
	"slices"
	"uuid"

	"axiom/internal/accounts"
)

// Audience is the account and current tier read by admission. Zero IDs preview the default price.
type Audience struct {
	Account accounts.AccountID
	Tier    accounts.TierID
}

// AudiencePrice is a published audience layer. A matching target with no version stops sales instead of falling back.
type AudiencePrice struct {
	Audience Audience
	Target   Selector
	Sold     Sold
}

// AudiencePrices indexes sparse layers once, so a request examines at most account, tier and default.
// The slices and maps are immutable after compilation.
type AudiencePrices struct {
	layers   []AudiencePrice
	accounts map[accounts.AccountID]int
	tiers    map[accounts.TierID]int
	all      int
}

// CompilePrices freezes the audience lookup table outside the call path.
func CompilePrices(prices []AudiencePrice) *AudiencePrices {
	p := &AudiencePrices{layers: slices.Clone(prices), accounts: map[accounts.AccountID]int{}, tiers: map[accounts.TierID]int{}, all: -1}
	for i, price := range p.layers {
		switch {
		case price.Audience.Account != (accounts.AccountID{}):
			p.accounts[price.Audience.Account] = i
		case price.Audience.Tier != (accounts.TierID{}):
			p.tiers[price.Audience.Tier] = i
		default:
			p.all = i
		}
	}
	return p
}

// Layers are the immutable inputs used to precompute routing weights.
func (p *AudiencePrices) Layers() []AudiencePrice { return p.layers }

// Select chooses by target first, then account, tier and all, without allocating.
func (p *AudiencePrices) Select(a Audience) int {
	best, specificity := -1, len(targetKinds)
	consider := func(i int) {
		target := slices.Index(targetKinds, p.layers[i].Target.Kind)
		if target >= 0 && target < specificity {
			best, specificity = i, target
		}
	}
	if i, ok := p.accounts[a.Account]; ok {
		consider(i)
	}
	if i, ok := p.tiers[a.Tier]; ok {
		consider(i)
	}
	if p.all >= 0 {
		consider(p.all)
	}
	return best
}

func selectorAudience(s Selector) (Audience, error) {
	var a Audience
	var err error
	switch s.Kind {
	case "account":
		a.Account, err = accounts.ParseAccountID(s.ID)
	case "tier":
		a.Tier, err = accounts.ParseTierID(s.ID)
	}
	return a, err
}

// For selects the immutable serving price, also used by the current-price preview.
func (s Sold) For(a Audience) Sold {
	if s.Audiences == nil {
		return s
	}
	at := s.Audiences.Select(a)
	if at < 0 {
		return Sold{}
	}
	return s.Audiences.layers[at].Sold
}

// Available reports whether any audience can use this model's published table.
func (s Sold) Available() bool {
	if s.Audiences == nil {
		return s.Version != uuid.Nil()
	}
	return slices.ContainsFunc(s.Audiences.layers, func(p AudiencePrice) bool { return s.For(p.Audience).Version != uuid.Nil() })
}

// Zero reports whether every price this sale can serve is zero. A :free model must satisfy it for every audience, not
// only the default layer: a fixed or multiplied rule can otherwise give one audience a paid price behind the free name.
func (s Sold) Zero() bool {
	if s.Version != uuid.Nil() && !s.Schedule.IsZero() {
		return false
	}
	if s.Audiences != nil {
		for _, layer := range s.Audiences.Layers() {
			if layer.Sold.Version != uuid.Nil() && !layer.Sold.Schedule.IsZero() {
				return false
			}
		}
	}
	return true
}
