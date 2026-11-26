package main

import (
	"context"
	"encoding/base64"
	"math/big"
	"slices"
	"sort"
	"strings"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// A supplier's names in piles and an endpoint's price lines (#948, internal/suppliers/README.md「管理接口」).

// supplierModelCursor is where a page of a supplier's names ended: its group and name, in the cursor's own form.
type supplierModelCursor struct {
	Group uuid.UUID `json:"g"`
	Name  string    `json:"n"`
}

// supplierModelAfter reads a page's cursor; the zero cursor, with no name, is the first page.
func supplierModelAfter(cursor *string) (supplierModelCursor, error) {
	var c supplierModelCursor
	if cursor == nil {
		return c, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil || jsonx.Unmarshal(decoded, &c) != nil || c.Name == "" {
		return c, invalidField("cursor", "not a cursor this API gave out")
	}
	return c, nil
}

// after reports whether the name comes after the cursor, in the list's order of group, then name.
func (c supplierModelCursor) after(m suppliers.SupplierModel) bool {
	if c.Name == "" {
		return true
	}
	if g := m.Group.Compare(c.Group); g != 0 {
		return g > 0
	}
	return m.UpstreamName > c.Name
}

// supplierModelFilter is what a page of a supplier's names is chosen by.
type supplierModelFilter struct {
	pile  *adminapi.SupplierModelPile
	group *uuid.UUID
	after supplierModelCursor
	size  int
}

func (f supplierModelFilter) takes(m suppliers.SupplierModel) bool {
	return (f.pile == nil || string(m.Pile) == string(*f.pile)) && (f.group == nil || m.Group == *f.group) && f.after.after(m)
}

// ListSupplierModels lists a page of a supplier's names, sorted into piles, by group and name. The piles are worked out
// for every name of the supplier at once (suppliers.Admin.SupplierModels) and the page is taken from them, so a filter
// by pile pages as the counts of getSupplier count.
func (a adminAPI) ListSupplierModels(ctx context.Context, request adminapi.ListSupplierModelsRequestObject) (adminapi.ListSupplierModelsResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	p := request.Params
	f := supplierModelFilter{pile: p.Pile, size: pageSize(p.Limit)}
	if p.GroupId != nil {
		g, err := idIn("group_id", *p.GroupId)
		if err != nil {
			return nil, err
		}
		f.group = &g
	}
	if f.after, err = supplierModelAfter(p.Cursor); err != nil {
		return nil, err
	}
	var models []suppliers.SupplierModel
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		models, err = a.suppliers.SupplierModels(ctx, tx, id, suppliers.EveryOwner)
		return err
	}); err != nil {
		return nil, err
	}
	list, err := supplierModelPage(models, f)
	if err != nil {
		return nil, err
	}
	return adminapi.ListSupplierModels200JSONResponse(list), nil
}

// supplierModelPage is the page of the names f takes, with the cursor of the next page when there is one.
func supplierModelPage(models []suppliers.SupplierModel, f supplierModelFilter) (adminapi.SupplierModelList, error) {
	list := adminapi.SupplierModelList{Data: []adminapi.SupplierModel{}}
	for _, m := range models {
		if !f.takes(m) {
			continue
		}
		if len(list.Data) < f.size {
			list.Data = append(list.Data, supplierModelBody(m))
			continue
		}
		last := list.Data[f.size-1]
		encoded, err := jsonx.MarshalCanonical(supplierModelCursor{Group: uuid.MustParse(last.GroupId), Name: last.UpstreamName})
		if err != nil {
			return list, err
		}
		next := base64.RawURLEncoding.EncodeToString(encoded)
		list.NextCursor = &next
		break
	}
	return list, nil
}

// supplierModelBody is a name as the contract gives it.
func supplierModelBody(m suppliers.SupplierModel) adminapi.SupplierModel {
	body := adminapi.SupplierModel{
		GroupId: m.Group.String(), UpstreamName: m.UpstreamName, Formats: modelFormats(m.Formats),
		Pile: adminapi.SupplierModelPile(m.Pile), FirstSeenAt: m.FirstSeen,
	}
	if m.Model.Model != (catalog.ModelID{}) {
		id, status := m.Model.Model.String(), adminapi.ModelStatus(m.Model.Status)
		body.ModelId, body.ModelSlug, body.ModelStatus, body.Family = &id, &m.Model.Slug, &status, familyBody(m.Model.Family)
	}
	if m.Endpoint != nil {
		body.EndpointId = text(m.Endpoint.String())
	}
	if m.Suggested.Model != (catalog.ModelID{}) {
		id := m.Suggested.Model.String()
		body.SuggestedModelId, body.SuggestedModelSlug = &id, &m.Suggested.Slug
	}
	return body
}

// modelCounts is how many names each pile of the supplier has, for getSupplier.
func (a adminAPI) modelCounts(ctx context.Context, tx pg.Tx, id uuid.UUID, owners suppliers.Owners) (*adminapi.SupplierModelCounts, error) {
	models, err := a.suppliers.SupplierModels(ctx, tx, id, owners)
	if err != nil {
		return nil, err
	}
	c := suppliers.Count(models)
	return &adminapi.SupplierModelCounts{Review: c.Review, Listed: c.Listed, Known: c.Known, Outdated: c.Outdated}, nil
}

// costSources are the contract's CostSource of each basis a plan is stored with.
var costSources = map[string]adminapi.CostSource{"published": "read", "confirmed": "confirmed"}

// GetSupplierEndpointCost answers an endpoint's cost plan as price lines, and the break-even of one the upstream charges
// per request. A line's factors are what its native price is the product of, as the plan stored them (none for a plan
// stored without), then the unit value its price is converted with.
func (a adminAPI) GetSupplierEndpointCost(ctx context.Context, request adminapi.GetSupplierEndpointCostRequestObject) (adminapi.GetSupplierEndpointCostResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	endpoint, err := uuid.Parse(request.EndpointId)
	if err != nil || endpoint.String() != request.EndpointId {
		return nil, suppliers.ErrNoSupplierEndpoint
	}
	var body adminapi.EndpointCost
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		cost, err := a.suppliers.EndpointCost(ctx, tx, supplier, endpoint, suppliers.EveryOwner)
		if err != nil {
			return err
		}
		body = endpointCostBody(cost)
		return a.breakEven(ctx, tx, cost, &body)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.GetSupplierEndpointCost200JSONResponse(body), nil
}

// endpointCostBody is the plan as the contract gives it. A relay's unit, quota, is the supplier's own; one billed in
// money has no native price apart from its price.
func endpointCostBody(c suppliers.EndpointCost) adminapi.EndpointCost {
	body := adminapi.EndpointCost{
		EndpointId: c.Endpoint.String(), CostState: adminapi.EndpointCostCostState(c.CostState),
		SettlementCurrency: c.SettlementCurrency, Lines: []adminapi.CostLine{},
	}
	if c.UnitValue != nil {
		body.UnitValue = text(c.UnitValue.String())
	}
	if c.Plan != nil {
		plan, source := c.Plan.String(), costSources[c.Basis]
		body.PlanId, body.Source = &plan, &source
	}
	for _, l := range c.Lines {
		line := adminapi.CostLine{Item: adminapi.MeteringItem(l.Item.String()), Unit: meteringUnit(l.Item), Factors: []adminapi.CostFactor{}}
		for _, f := range l.Factors {
			line.Factors = append(line.Factors, adminapi.CostFactor{Kind: adminapi.CostFactorKind(f.Kind), Value: f.Value})
		}
		if l.ServiceTier != catalog.ServiceTierStandard {
			tier := adminapi.ServiceTier(l.ServiceTier.String())
			line.ServiceTier = &tier
		}
		if !l.Base {
			line.Above = &l.Above
		}
		if c.NativeUnit == "quota" {
			line.NativePrice = text(l.Native.String())
		}
		if l.Price != nil {
			line.Price = text(l.Price.String())
			line.Factors = append(line.Factors, adminapi.CostFactor{Kind: "unit_value", Value: c.UnitValue.String()})
		}
		body.Lines = append(body.Lines, line)
	}
	return body
}

// tokenItems are the items billed by the token.
var tokenItems = func() (items catalog.ItemSet) {
	for it := range catalog.NumItems {
		if it.Unit() == catalog.UnitToken {
			items.Add(it)
		}
	}
	return items
}()

// breakEven sets the break-even of an endpoint the upstream charges per request, and for no token, while the platform
// sells the model by the token. It sets none for any other, nor while the request's price, the sale price or a rate
// between the two currencies is not known.
func (a adminAPI) breakEven(ctx context.Context, tx pg.Tx, c suppliers.EndpointCost, body *adminapi.EndpointCost) error {
	request, perRequest := requestPrice(c)
	site, ok := a.accounts.MainSite()
	if !perRequest || !ok {
		return nil
	}
	sale, ok, err := endpointSale(ctx, tx, site, c)
	if err != nil || !ok {
		return err
	}
	rate, ok, err := settlementRate(ctx, tx, c.SettlementCurrency, site.Currency)
	if err != nil || !ok {
		return err
	}
	traffic, err := a.suppliers.EndpointTraffic(ctx, tx, c.Endpoint)
	if err != nil {
		return err
	}
	body.BreakEven = breakEvenOf(request, rate, sale, traffic)
	return nil
}

// requestPrice is the price of one request of a plan that prices requests and no token, in the settlement currency.
func requestPrice(c suppliers.EndpointCost) (platform.UnitPrice, bool) {
	if c.Schedule == nil || c.Schedule.Priced(tokenItems) {
		return platform.UnitPrice{}, false
	}
	i := slices.IndexFunc(c.Lines, func(l suppliers.CostLine) bool {
		return l.Item == catalog.ItemRequest && l.Base && l.ServiceTier == catalog.ServiceTierStandard && l.Price != nil
	})
	if i < 0 {
		return platform.UnitPrice{}, false
	}
	return *c.Lines[i].Price, true
}

// endpointSale is the price the site sells the endpoint's model at through the endpoint, as published; not ok when it is not
// on sale.
func endpointSale(ctx context.Context, tx pg.Tx, site accounts.Site, c suppliers.EndpointCost) (catalog.Schedule, bool, error) {
	sold, err := billing.PublishedPrices(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return catalog.Schedule{}, false, err
	}
	sale, ok := sold[c.Model]
	if !ok {
		return catalog.Schedule{}, false, nil
	}
	if p, ok := sale.Endpoints[c.Endpoint]; ok {
		return p, true, nil
	}
	return sale.Schedule, true, nil
}

// settlementRate is what one unit of the settlement currency is in the site's currency now: 1 for the same currency,
// else the current exchange rate between the two; not ok without one.
func settlementRate(ctx context.Context, tx pg.Tx, settlement string, site platform.Currency) (*big.Rat, bool, error) {
	if strings.EqualFold(settlement, site.String()) {
		return big.NewRat(1, 1), true, nil
	}
	from, err := platform.ParseCurrency(settlement)
	if err != nil {
		return nil, false, err
	}
	rates, err := billing.ExchangeRates(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	for _, r := range rates {
		if between, ok := r.Basis().Between(from, site); ok {
			return between, true, nil
		}
	}
	return nil, false, nil
}

// breakEvenOf is the fewest tokens a request needs for the sale price to cover the request price, in the site's currency
// at rate, at the endpoint's mix of the last 24 hours. nil when no request up to maxTokens long is covered: a sale with
// no token price, or one too low for any length.
func breakEvenOf(request platform.UnitPrice, rate *big.Rat, sale catalog.Schedule, t suppliers.Traffic) *adminapi.CostBreakEven {
	mix, tokens := tokenMix(t.Units)
	n, ok := fewestCovering(sale, mix, new(big.Rat).Mul(request.Rat(), rate))
	if !ok {
		return nil
	}
	b := &adminapi.CostBreakEven{RequestPrice: request.String(), Tokens: n, Requests24h: &t.Requests}
	if t.Requests > 0 {
		mean := tokens / t.Requests
		b.MeanTokens24h = &mean
	}
	return b
}

// tokenMix is the token items of the units and how many tokens they have. With no tokens the mix is all input, the
// side a sale prices lowest, so a break-even errs high.
func tokenMix(units map[catalog.Item]int64) (map[catalog.Item]int64, int64) {
	mix, tokens := map[catalog.Item]int64{}, int64(0)
	for it, n := range units {
		if tokenItems.Has(it) && n > 0 {
			mix[it], tokens = n, tokens+n
		}
	}
	if tokens == 0 {
		return map[catalog.Item]int64{catalog.ItemInput: 1}, 0
	}
	return mix, tokens
}

// maxTokens is the longest request a break-even is looked for in: catalog.NewUsage's bound on one count.
const maxTokens = 10_000_000_000

// fewestCovering is the fewest tokens n, split by the mix, for which billing's own price of one request of them
// (catalog's Schedule.Cost, with the sale's request price and the context tier the length falls in) is at least need.
// Each item's share is rounded down, so the request is never credited with more than its n tokens. Every share grows
// with n, so the context tier never falls as n grows and, within a tier, neither does the price: the search finds
// where each tier ends, and in the first tier whose longest request is covered, its shortest covered one. Prices need
// not rise with the tier, so a longer request than the answer may again fall short.
func fewestCovering(sale catalog.Schedule, mix map[catalog.Item]int64, need *big.Rat) (int64, bool) {
	var total int64
	for _, m := range mix {
		total += m
	}
	usage := func(n int64) catalog.Usage {
		counts := map[catalog.Item]int64{catalog.ItemRequest: 1}
		for it, m := range mix {
			share := new(big.Int).Mul(big.NewInt(n), big.NewInt(m))  // up to 10¹⁰ × a day's tokens: past int64
			counts[it] = share.Quo(share, big.NewInt(total)).Int64() // at most n, since m ≤ total
		}
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			panic(err) // every count is at most maxTokens
		}
		return u
	}
	tier := func(n int64) int {
		_, band, _ := sale.Rate(usage(n), catalog.ItemInput)
		return band
	}
	covers := func(n int64) bool {
		cost, _, err := sale.Cost(usage(n))
		return err == nil && cost.Rat().Cmp(need) >= 0
	}
	for lo := int64(0); lo <= maxTokens; {
		band := tier(lo)
		hi := lo + int64(sort.Search(int(maxTokens-lo+1), func(i int) bool { return tier(lo+int64(i)) != band })) - 1
		if covers(hi) {
			return lo + int64(sort.Search(int(hi-lo), func(i int) bool { return covers(lo + int64(i)) })), true
		}
		lo = hi + 1
	}
	return 0, false
}
