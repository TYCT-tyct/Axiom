package newapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// priceEntry is the part of a /api/pricing entry that is read. A ratio left out of the answer is left out here, which
// is not the same as a ratio of zero.
type priceEntry struct {
	Model           string   `json:"model_name"`
	QuotaType       int      `json:"quota_type"`
	BillingMode     string   `json:"billing_mode"`
	ModelRatio      number   `json:"model_ratio"`
	CompletionRatio *number  `json:"completion_ratio"`
	CacheRatio      *number  `json:"cache_ratio"`
	CreateCache     *number  `json:"create_cache_ratio"`
	ImageRatio      *number  `json:"image_ratio"`
	Endpoints       []string `json:"supported_endpoint_types"`
	EnableGroups    []string `json:"enable_groups,omitempty"`
}

// autoRatios are the ratios new-api writes for its "auto" group: "auto" from 1.0, "自动" before.
var autoRatios = map[number]bool{"auto": true, "自动": true}

// offerProgress is the position in the offer listing: the groups, their ratios and the prices read when the pass began,
// the group it reached, and the groups built so far. The pass reads the prices once, so its models are priced alike; a
// model's cost is worked out when the pass ends, so the position carries names, not schedules. A position whose groups
// are no longer the supplier's begins again: a group removed since would be read for ever, its model list refused.
type offerProgress struct {
	V      int                   `json:"v"`
	Names  []string              `json:"names"`
	Groups map[string]groupEntry `json:"groups"`
	Prices []priceEntry          `json:"prices"`
	Next   int                   `json:"next"`
	Built  []builtGroup          `json:"built"`
}

// builtGroup is a group whose models the pass read; the prices turn it into an offer group when the pass ends.
type builtGroup struct {
	Name, Desc, Ratio string
	RatioHidden       bool `json:",omitempty"`
	Models            []string
}

func decodeOffer(raw adapter.Listing) offerProgress {
	if len(raw) == 0 {
		return offerProgress{}
	}
	var p offerProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != 1 || p.Next < 0 || p.Next > len(p.Names) {
		return offerProgress{}
	}
	return p
}

// groupEntry is a group as /api/user/self/groups states it. Hidden marks a ratio the system does not show (one-api's),
// which is written as 1.
type groupEntry struct {
	Ratio  number `json:"ratio"`
	Desc   string `json:"desc"`
	Hidden bool   `json:"ratio_hidden,omitempty"`
}

// Offer implements adapter.Manager: the account's groups with their ratios, the models each serves, and for each model
// a cost schedule where new-api's pricing can be expressed as one. The pricing answer is not trusted to name the
// models a group serves; the per-group model list does. The listing is read from the position in from, as far as the
// global bucket allows; what it read is in the position it answers with.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	p, err := m.offerStart(ctx, from)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	return adapter.Drain(func() (adapter.Page[adapter.Group], bool, error) {
		return m.offerPage(ctx, &p)
	})
}

func (m *manager) offerStart(ctx context.Context, from adapter.Listing) (offerProgress, error) {
	p := decodeOffer(from)
	if p.V == 0 {
		return m.startOffer(ctx, nil)
	}
	groups, err := m.groupsNow(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	if !slices.Equal(slices.Sorted(maps.Keys(groups)), p.Names) {
		return m.startOffer(ctx, groups)
	}
	return p, nil
}

func (m *manager) offerPage(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	if p.Next == len(p.Names) {
		groups, err := p.offer(m.sys == newAPI013)
		return adapter.Page[adapter.Group]{Items: groups, Done: true}, false, err
	}
	name := p.Names[p.Next]
	if autoRatios[p.Groups[name].Ratio] { // picks among groups per request and has no ratio of its own
		p.Next++
		return page, true, nil
	}
	if _, ok := p.Groups[name].Ratio.rat(); !ok { // missing, null or not a number: another form than the one known, not a group to leave out
		return page, false, errMissing("ratio of group " + strconv.Quote(name))
	}
	var models struct {
		Data []string `json:"data"`
	}
	if err := m.api.get(ctx, m.modelsPath(name), global, &models); err != nil {
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			return adapter.Page[adapter.Group]{Next: adapter.ListingOf(*p), Until: blocked.Until}, false, nil
		}
		return page, false, err
	}
	if models.Data == nil {
		return page, false, errMissing("list of models")
	}
	g := p.Groups[name]
	p.Built = append(p.Built, builtGroup{Name: name, Desc: g.Desc, Ratio: string(g.Ratio), RatioHidden: g.Hidden, Models: models.Data})
	p.Next++
	return page, true, nil
}

func (m *manager) startOffer(ctx context.Context, groups map[string]groupEntry) (offerProgress, error) {
	var err error
	if groups == nil {
		if groups, err = m.groupsNow(ctx); err != nil {
			return offerProgress{}, err
		}
	}
	prices, err := m.prices(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	return offerProgress{V: 1, Groups: groups, Names: slices.Sorted(maps.Keys(groups)), Prices: prices}, nil
}

// modelsPath is where the models of a group are listed. new-api before 1.0 lists the models of all the account's
// groups together, whatever group is asked for (controller/user.go GetUserModels); one-api lists those of the
// account's one group.
func (m *manager) modelsPath(group string) string {
	switch m.sys {
	case newAPI013:
		return "/api/user/models"
	case oneAPI:
		return "/api/user/available_models"
	case newAPI: // the group's own list
	}
	return "/api/user/models?group=" + url.QueryEscape(group)
}

// prices reads the price list; none when the site hides it. A site whose pricing module is off answers a request that
// carries a login with 403 from a check that only its root passes (new-api middleware/header_nav.go:107-111), or, in
// releases before that check, with a 403 without a code. Either says the prices are not shown, not that the login is
// refused: the groups were just read with the same token, and the price list needs no scope. The models then have no
// cost, which keeps them out of routing until a person confirms a base price, as for sub2api. A 403 with one of the
// codes of a refused login is still that.
func (m *manager) prices(ctx context.Context) ([]priceEntry, error) {
	if m.sys == oneAPI { // has no price list
		return nil, nil
	}
	resp, err := m.api.send(ctx, http.MethodGet, "/api/pricing", global, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if resp.Status == http.StatusForbidden && jsonx.Unmarshal(resp.Body, &env) == nil && !env.Success && !authCodes[env.Code] {
		return nil, nil
	}
	var pricing struct {
		Data []priceEntry `json:"data"`
	}
	if err := read(resp, &pricing); err != nil {
		return nil, err
	}
	if pricing.Data == nil {
		return nil, errMissing("list of prices")
	}
	return pricing.Data, nil
}

// offer builds the groups; union says that each model list read is the union of the account's groups.
func (p offerProgress) offer(union bool) ([]adapter.Group, error) {
	prices := make(map[string]priceEntry, len(p.Prices))
	for _, e := range p.Prices {
		prices[e.Model] = e
	}
	groups := make([]adapter.Group, 0, len(p.Built))
	for _, b := range p.Built {
		ratio, ok := number(b.Ratio).rat()
		if !ok {
			return nil, errMissing("ratio of group " + strconv.Quote(b.Name))
		}
		g, err := buildGroup(b, ratio, union, prices)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (m *manager) groupsNow(ctx context.Context) (map[string]groupEntry, error) {
	if m.sys == oneAPI {
		return m.oneAPIGroups(ctx)
	}
	var groups struct {
		Data map[string]groupEntry `json:"data"`
	}
	if err := m.api.get(ctx, "/api/user/self/groups", global, &groups); err != nil {
		return nil, err
	}
	if groups.Data == nil {
		return nil, errMissing("list of groups")
	}
	return groups.Data, nil
}

// buildGroup is the offer group of b. When the model list is the union of the account's groups, a priced model is
// kept only in the groups its price names (enable_groups, which new-api builds from its channels, "all" for every
// one); a model without a price cannot be placed, so it stays in each group without a cost.
func buildGroup(b builtGroup, ratio *big.Rat, union bool, prices map[string]priceEntry) (adapter.Group, error) {
	slices.Sort(b.Models)
	g := adapter.Group{Ref: b.Name, Name: b.Name, Ratio: adapter.Amount(b.Ratio)}
	endpoints := map[string]bool{}
	for _, upstream := range slices.Compact(b.Models) {
		entry, priced := prices[upstream]
		if union && priced && !slices.Contains(entry.EnableGroups, b.Name) && !slices.Contains(entry.EnableGroups, "all") {
			continue
		}
		model := adapter.Model{UpstreamName: upstream, Formats: formatsOf(entry.Endpoints)}
		if priced {
			model.Cost, model.Derivation = schedule(entry, ratio)
			model.Without = unsupported(entry)
			for _, e := range entry.Endpoints {
				endpoints[e] = true
			}
		}
		g.Models = append(g.Models, model)
	}
	facts := map[string]any{"desc": b.Desc, "endpoint_types": slices.Sorted(maps.Keys(endpoints))}
	if b.RatioHidden {
		facts["ratio_shown"] = false
	}
	encoded, err := jsonx.MarshalCanonical(facts)
	if err != nil {
		return adapter.Group{}, fmt.Errorf("new-api: encode the facts of group %q: %w", b.Name, err)
	}
	g.Facts = encoded
	return g, nil
}

// formatsOf maps new-api's endpoint types to call formats; a model that names none is served as OpenAI chat.
func formatsOf(endpoints []string) []protocol.Format {
	var formats []protocol.Format
	for _, e := range endpoints {
		if f, ok := endpointFormats[e]; ok && !slices.Contains(formats, f) {
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		return []protocol.Format{protocol.Chat}
	}
	return formats
}

var endpointFormats = map[string]protocol.Format{
	"openai": protocol.Chat, "openai-response": protocol.Responses, "anthropic": protocol.Messages,
	"embeddings": protocol.Embeddings, "rerank": protocol.Rerank, "jina-rerank": protocol.Rerank,
}

// unsupported are the capabilities the price does not cover. An image ratio of its own other than 1 means images are
// priced apart from text, which a schedule's input price cannot say, so the endpoint does not claim image input.
func unsupported(e priceEntry) protocol.Capabilities {
	if e.ImageRatio != nil {
		if r, ok := e.ImageRatio.rat(); !ok || r.Cmp(big.NewRat(1, 1)) != 0 {
			return protocol.Images
		}
	}
	return 0
}

// schedule is the model's cost as a catalog schedule in new-api's own unit, quota per token, with the currency XXX
// ("no currency"): the amounts it yields are quota, not money, and the factors each price is the product of. It is nil,
// which makes the cost unknown, for what a schedule cannot express: a price per call, an expression, a ratio that is
// missing or not a number, or a price above what a unit price may be. Rounding goes up, so a price is never lower than
// new-api's.
//
// new-api charges (prompt + completion × completion_ratio) × model_ratio × group_ratio, a cache read at cache_ratio of
// the input price (1 when the pricing does not say), a cache write at create_cache_ratio of it (1.25 by default) and
// an hour-long cache write at 1.6 times that (constants in new-api's code). The ratio a price has to the input price is
// its factor beside the model and group ratios, the default when the pricing leaves it out; the hour-long write's is
// 1.6 × create_cache_ratio.
func schedule(e priceEntry, group *big.Rat) (*catalog.Schedule, adapter.Derivation) {
	if e.BillingMode == "tiered_expr" || e.QuotaType != 0 {
		return nil, nil
	}
	model, ok := e.ModelRatio.rat()
	if !ok {
		return nil, nil
	}
	cacheWrite := ratioOr(e.CreateCache, big.NewRat(5, 4))
	toInput := map[string]toInputRatio{
		"input":          {},
		"output":         {adapter.FactorCompletionRatio, ratioOr(e.CompletionRatio, big.NewRat(1, 1))},
		"cache_read":     {adapter.FactorCacheRatio, ratioOr(e.CacheRatio, big.NewRat(1, 1))},
		"cache_write":    {adapter.FactorCacheWriteRatio, cacheWrite},
		"cache_write_1h": {adapter.FactorCacheWriteRatio, new(big.Rat).Mul(cacheWrite, big.NewRat(8, 5))},
	}
	input := new(big.Rat).Mul(model, group)
	rates := make(map[string]string, len(toInput))
	for item, r := range toInput {
		p := input
		if r.ratio != nil {
			p = new(big.Rat).Mul(input, r.ratio)
		}
		text, ok := unitPrice(p)
		if !ok {
			return nil, nil
		}
		rates[item] = text
	}
	stored, err := jsonx.MarshalCanonical(map[string]any{"v": 1, "currency": "XXX", "rates": rates})
	if err != nil {
		return nil, nil
	}
	s, err := catalog.ParseSchedule(stored)
	if err != nil {
		return nil, nil
	}
	d := adapter.Derive(s, func(rate catalog.ScheduleRate) map[adapter.FactorKind]*big.Rat {
		f := map[adapter.FactorKind]*big.Rat{adapter.FactorModelRatio: model, adapter.FactorGroupRatio: group}
		if r := toInput[rate.Item.String()]; r.ratio != nil {
			f[r.kind] = r.ratio
		}
		return f
	}, factorOrder)
	return &s, d
}

// toInputRatio is the ratio a price has to the input price, and the factor it is; none for the input price itself.
type toInputRatio struct {
	kind  adapter.FactorKind
	ratio *big.Rat
}

// factorOrder is the order new-api multiplies a price's factors in.
var factorOrder = []adapter.FactorKind{
	adapter.FactorModelRatio, adapter.FactorGroupRatio, adapter.FactorCompletionRatio, adapter.FactorCacheRatio,
	adapter.FactorCacheWriteRatio,
}

// ratioOr is the ratio n holds, or fallback when the pricing leaves it out.
func ratioOr(n *number, fallback *big.Rat) *big.Rat {
	if n == nil {
		return fallback
	}
	if r, ok := n.rat(); ok {
		return r
	}
	return new(big.Rat).SetInt64(-1) // a ratio that is no number: unitPrice refuses the negative result
}

// unitPrice writes p rounded up to 15 decimals, the precision of a unit price, or reports that it is negative or
// above 1000, which a unit price cannot be.
func unitPrice(p *big.Rat) (string, bool) {
	if p.Sign() < 0 {
		return "", false
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil)
	scaled := new(big.Rat).Mul(p, new(big.Rat).SetInt(scale))
	n := new(big.Int).Div(scaled.Num(), scaled.Denom()) // floor, as the numerator is not negative
	if new(big.Rat).SetInt(n).Cmp(scaled) < 0 {
		n.Add(n, big.NewInt(1))
	}
	whole, frac := new(big.Int).DivMod(n, scale, new(big.Int))
	if whole.Cmp(big.NewInt(1000)) > 0 || whole.Cmp(big.NewInt(1000)) == 0 && frac.Sign() > 0 {
		return "", false
	}
	text := whole.String()
	if frac.Sign() > 0 {
		digits := fmt.Sprintf("%015s", frac.String())
		text += "." + strings.TrimRight(strings.ReplaceAll(digits, " ", "0"), "0")
	}
	return text, true
}
