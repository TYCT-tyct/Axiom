package sub2api

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// The model plaza (GET /api/v1/model-plaza, off by default) is where sub2api publishes what it charges: for each group,
// each model's price per token in dollars at a multiplier of 1, worked out by its own billing function on the same
// resolution chain as a charge (group price card, then channel, then catalogue; service/model_plaza_service.go
// fillDisplayPricing, billing_context_schedule.go). A charge is the sum of tokens × price times the group's multiplier
// (service/billing_service.go computeTokenBreakdown: ActualCost = TotalCost × rateMultiplier), so a published price
// times the multiplier the offer reads is what a token costs.
//
// Invariant: a published price is a cost only as a whole schedule that prices every token the way sub2api does, at
// price × multiplier rounded up once to a unit price, so the estimate is never below the charge. A model whose price
// depends on more than its tokens has none, and its cost stays unknown until a person confirms a base price: a price
// by the request or the image, prices by context length, by time of day (time_pricing, and DeepSeek's default prices,
// which sub2api doubles at its peak hours in code without listing them: billing_service.go calculateTokenCost), or by
// reasoning effort. The /api/v1/channels/available list is not read: it shows the channels' own price cards, which a
// group's card overrides when the charge is worked out.

// plazaDTO is the part of the model plaza that prices a group's models.
type plazaDTO struct {
	Groups []struct {
		ID     number       `json:"id"`
		Models []plazaModel `json:"models"`
	} `json:"groups"`
}

type plazaModel struct {
	Name        string        `json:"name"`
	Pricing     *plazaPricing `json:"pricing"`
	TimePricing any           `json:"time_pricing"`
}

// plazaPricing is a model's published prices, dollars per token; an absent price is one the plaza does not state.
type plazaPricing struct {
	Mode          string            `json:"billing_mode"`
	Input         number            `json:"input_price"`
	Output        number            `json:"output_price"`
	CacheRead     number            `json:"cache_read_price"`
	CacheWrite    number            `json:"cache_write_price"`
	CacheWrite1h  number            `json:"cache_write_1h_price"`
	CacheWrite12h number            `json:"cache_write_12h_price"`
	ImageInput    number            `json:"image_input_price"`
	ImageOutput   number            `json:"image_output_price"`
	Effort        map[string]number `json:"reasoning_effort_multipliers"`
	Intervals     []any             `json:"intervals"`
}

// basePrice is a model's published price at a multiplier of 1, and the capabilities it does not cover.
type basePrice struct {
	schedule catalog.Schedule
	without  protocol.Capabilities
}

// plazaPrices reads the published prices by group id and model name; none when the plaza is off (404, its default, and
// the answer of releases without it).
func (m *manager) plazaPrices(ctx context.Context) (map[string]map[string]basePrice, error) {
	resp, err := m.get(ctx, "/api/v1/model-plaza", bucketPanel)
	if err != nil {
		return nil, err
	}
	prices := map[string]map[string]basePrice{}
	switch resp.Status {
	case http.StatusNotFound:
		return prices, nil
	case http.StatusOK:
	default:
		return nil, statusError("model plaza", resp)
	}
	plaza, err := open[plazaDTO](resp)
	if err != nil {
		return nil, err
	}
	for _, g := range plaza.Groups {
		if !g.ID.present() {
			return nil, fmt.Errorf("%w: a model plaza group without its id", adapter.ErrUnrecognised)
		}
		models := map[string]basePrice{}
		for _, pm := range g.Models {
			if p, ok := pm.base(); ok {
				models[pm.Name] = p
			}
		}
		prices[string(g.ID)] = models
	}
	return prices, nil
}

// base is the model's price as a schedule in sub2api's unit at a multiplier of 1, or false when its price is not
// a per-token one a schedule says the same (the invariant above).
func (pm plazaModel) base() (basePrice, bool) {
	p := pm.Pricing
	if !pm.perToken() {
		return basePrice{}, false
	}
	rates, ok := p.rates()
	if !ok {
		return basePrice{}, false
	}
	stored, err := jsonx.MarshalCanonical(map[string]any{"v": 1, "currency": "XXX", "rates": rates})
	if err != nil {
		return basePrice{}, false
	}
	s, err := catalog.ParseSchedule(stored)
	if err != nil {
		return basePrice{}, false
	}
	b := basePrice{schedule: s}
	if differs(p.ImageInput, p.Input) {
		b.without = protocol.Images // images priced apart from text, which the input price does not say
	}
	return b, true
}

// perToken reports whether the model's price depends on its tokens alone, as a schedule's does.
func (pm plazaModel) perToken() bool {
	p := pm.Pricing
	return p != nil && p.Mode == "token" && len(p.Intervals) == 0 && pm.TimePricing == nil && len(p.Effort) == 0 &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(pm.Name)), "deepseek-") && !differs(p.ImageOutput, p.Output)
}

// rates are the published prices by schedule item, rounded up to unit prices; false without an input and an output
// price, or with one that is not a number a unit price can hold.
func (p plazaPricing) rates() (map[string]string, bool) {
	rates := map[string]string{}
	for item, n := range map[string]number{
		"input": p.Input, "output": p.Output, "cache_read": p.CacheRead, "cache_write": p.CacheWrite, "cache_write_1h": p.CacheWrite1h,
	} {
		if !n.present() {
			if item == "input" || item == "output" {
				return nil, false
			}
			continue // not stated: the item stays unpriced, which an estimate reports rather than guesses
		}
		price, ok := unitPrice(n)
		if !ok {
			return nil, false
		}
		rates[item] = price
	}
	if differs(p.CacheWrite12h, p.CacheWrite) {
		delete(rates, "cache_write") // an OpenAI write held for 12 hours costs more, and a schedule has no item for it
	}
	return rates, true
}

// differs reports a price stated apart from the ordinary one and not equal to it.
func differs(apart, ordinary number) bool {
	if !apart.present() {
		return false
	}
	a, okA := new(big.Rat).SetString(string(apart))
	o, okO := new(big.Rat).SetString(string(ordinary))
	return !okA || !okO || a.Cmp(o) != 0
}

// oneUnit is the unit price 1, which rounds an exact price up to a unit price.
var oneUnit = func() platform.UnitPrice {
	p, err := platform.ParseUnitPrice("1")
	if err != nil {
		panic(err)
	}
	return p
}()

// unitPrice is n, a JSON number such as 3e-06, rounded up to a unit price: a published price written with more digits
// than a unit price holds costs a little more here, never less.
func unitPrice(n number) (string, bool) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || r.Sign() < 0 {
		return "", false
	}
	p, err := oneUnit.Scale(r)
	if err != nil {
		return "", false
	}
	return p.String(), true
}

// priced is the group's model with the published price times the group's multiplier, and the factors that gives,
// when the plaza prices it; the model as it was otherwise.
func priced(m adapter.Model, base basePrice, ok bool, ratio adapter.Amount) adapter.Model {
	if !ok {
		return m
	}
	scaled, err := adapter.ScaleSchedule(base.schedule, ratio)
	if err != nil { // above what a unit price may be: the cost stays unknown
		return m
	}
	m.Cost, m.Derivation, m.Without = &scaled, adapter.ScaledDerivation(base.schedule, ratio), base.without
	return m
}
