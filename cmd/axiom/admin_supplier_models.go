package main

import (
	"context"
	"encoding/base64"
	"math/big"
	"slices"
	"strings"
	"uuid"

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

func supplierModelAfter(cursor *string) (*supplierModelCursor, error) {
	if cursor == nil {
		return nil, nil //nolint:nilnil // no cursor is the first page
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*cursor)
	var c supplierModelCursor
	if err != nil || jsonx.Unmarshal(decoded, &c) != nil || c.Name == "" {
		return nil, invalidField("cursor", "not a cursor this API gave out")
	}
	return &c, nil
}

// after reports whether the name comes after the cursor, in the list's order of group, then name.
func (c *supplierModelCursor) after(m suppliers.SupplierModel) bool {
	if c == nil {
		return true
	}
	if g := m.Group.Compare(c.Group); g != 0 {
		return g > 0
	}
	return m.UpstreamName > c.Name
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
	var group *uuid.UUID
	if p.GroupId != nil {
		g, err := idIn("group_id", *p.GroupId)
		if err != nil {
			return nil, err
		}
		group = &g
	}
	after, err := supplierModelAfter(p.Cursor)
	if err != nil {
		return nil, err
	}
	var models []suppliers.SupplierModel
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		models, err = a.suppliers.SupplierModels(ctx, tx, id, suppliers.EveryOwner)
		return err
	}); err != nil {
		return nil, err
	}
	size := pageSize(p.Limit)
	list := adminapi.SupplierModelList{Data: []adminapi.SupplierModel{}}
	for _, m := range models {
		if (p.Pile != nil && string(m.Pile) != string(*p.Pile)) || (group != nil && m.Group != *group) || !after.after(m) {
			continue
		}
		if len(list.Data) == size {
			last := list.Data[size-1]
			encoded, err := jsonx.MarshalCanonical(supplierModelCursor{Group: uuid.MustParse(last.GroupId), Name: last.UpstreamName})
			if err != nil {
				return nil, err
			}
			next := base64.RawURLEncoding.EncodeToString(encoded)
			list.NextCursor = &next
			break
		}
		list.Data = append(list.Data, supplierModelBody(m))
	}
	return adminapi.ListSupplierModels200JSONResponse(list), nil
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
// per request. A line's price is its native price times the unit value, the one factor the plan's conversion takes; how
// the supplier came to its native price is not stored with the plan, so it is not a factor here.
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
		if body.BreakEven, err = a.breakEven(ctx, tx, cost); err != nil {
			return err
		}
		return nil
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
		line := adminapi.CostLine{Item: adminapi.MeteringItem(l.Item.String()), Unit: meteringUnits[l.Item], Factors: []adminapi.CostFactor{}}
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

// breakEven is the break-even of an endpoint the upstream charges per request, and for no token, while the platform
// sells the model by the token: nil for any other, and while the request's price, the sale price or a rate between the
// two currencies is not known.
func (a adminAPI) breakEven(ctx context.Context, tx pg.Tx, c suppliers.EndpointCost) (*adminapi.CostBreakEven, error) {
	if c.Schedule == nil || c.Schedule.Priced(tokenItems) {
		return nil, nil //nolint:nilnil // no break-even is an answer of its own
	}
	i := slices.IndexFunc(c.Lines, func(l suppliers.CostLine) bool {
		return l.Item == catalog.ItemRequest && l.Base && l.ServiceTier == catalog.ServiceTierStandard && l.Price != nil
	})
	site, ok := a.accounts.MainSite()
	if i < 0 || !ok {
		return nil, nil //nolint:nilnil // no one request price, or no site that sells it
	}
	sold, err := billing.PublishedPrices(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return nil, err
	}
	sale, ok := sold[c.Model]
	if !ok {
		return nil, nil //nolint:nilnil // not on sale
	}
	schedule := sale.Schedule
	if p, ok := sale.Endpoints[c.Endpoint]; ok {
		schedule = p
	}
	rate, ok, err := settlementRate(ctx, tx, c.SettlementCurrency, site.Currency)
	if err != nil || !ok {
		return nil, err
	}
	traffic, err := a.suppliers.EndpointTraffic(ctx, tx, c.Endpoint)
	if err != nil {
		return nil, err
	}
	return breakEvenOf(*c.Lines[i].Price, rate, schedule, traffic), nil
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
// at rate: what the sale charges a request of its own, if anything, plus each token at the mean sale price of a token
// at the endpoint's mix of the last 24 hours, by each token item's base price. With no traffic the mix is all input, the
// side a sale prices lowest, so the break-even errs high. nil when no token of the mix has a sale price, which no number
// of tokens covers.
func breakEvenOf(request platform.UnitPrice, rate *big.Rat, sale catalog.Schedule, t suppliers.Traffic) *adminapi.CostBreakEven {
	units := t.Units
	var tokens int64
	for it, n := range units {
		if tokenItems.Has(it) {
			tokens += n
		}
	}
	if tokens == 0 {
		units = map[catalog.Item]int64{catalog.ItemInput: 1}
	}
	revenue, weight := new(big.Rat), int64(0)
	for it, n := range units {
		if p, ok := sale.BaseRate(it); ok && tokenItems.Has(it) {
			revenue.Add(revenue, new(big.Rat).Mul(p.Rat(), big.NewRat(n, 1)))
			weight += n
		}
	}
	if revenue.Sign() == 0 {
		return nil
	}
	perToken := revenue.Quo(revenue, big.NewRat(weight, 1))
	need := new(big.Rat).Mul(request.Rat(), rate)
	if p, ok := sale.BaseRate(catalog.ItemRequest); ok {
		need.Sub(need, p.Rat())
	}
	b := &adminapi.CostBreakEven{RequestPrice: request.String(), Requests24h: &t.Requests}
	if need.Sign() > 0 {
		q := need.Quo(need, perToken)
		n, rem := new(big.Int).QuoRem(q.Num(), q.Denom(), new(big.Int))
		if rem.Sign() > 0 {
			n.Add(n, big.NewInt(1))
		}
		if !n.IsInt64() {
			return nil // no request is that long
		}
		b.Tokens = n.Int64()
	}
	if t.Requests > 0 {
		mean := tokens / t.Requests
		b.MeanTokens24h = &mean
	}
	return b
}
