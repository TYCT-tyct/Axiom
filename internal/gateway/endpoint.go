package gateway

import (
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// endpointPrice is the price routing holds for one endpoint before the call's audience is selected. The default layer
// may price fewer endpoints than an audience layer, and the reverse: the endpoint is served only if some layer has a
// price for it. The default PriceRef is then that layer's, though routing reads the audience's own version for the call.
func endpointPrice(sold billing.Sold, endpoint uuid.UUID) (routing.PriceRef, string) {
	price := routing.PriceRef{Version: sold.Version, Schedule: sold.Schedule}
	if schedule, ok := sold.PriceFor(endpoint); ok {
		price.Schedule = schedule
		return price, ""
	}
	if sold.Audiences == nil {
		return price, "no published sale price for this endpoint; check the price publication list and local costs"
	}
	for _, layer := range sold.Audiences.Layers() {
		if schedule, ok := layer.Sold.PriceFor(endpoint); ok {
			return routing.PriceRef{Version: layer.Sold.Version, Schedule: schedule}, ""
		}
	}
	return price, "no published sale price for this endpoint; check the price publication list and local costs"
}

// chatFormats are the formats that convert into each other: an endpoint speaking any of them serves all three.
var chatFormats = routing.Of(protocol.Messages, protocol.Responses, protocol.Chat)

// servingOf is the spec an endpoint must meet: all of Serving for a generation format, and only the context for an
// endpoint reached solely through embeddings or rerank, which generates nothing and needs no output limit.
func servingOf(model catalog.Model, formats []protocol.Format) (catalog.Serving, bool) {
	if serving, ok := model.Serving(); ok || !onlyDirectFormats(formats) {
		return serving, ok
	}
	return model.ServingDirect()
}

// onlyDirectFormats reports whether every format is a direct one, in which case the model generates nothing.
func onlyDirectFormats(formats []protocol.Format) bool {
	if len(formats) == 0 {
		return false
	}
	for _, f := range formats {
		if !f.Direct() {
			return false
		}
	}
	return true
}

// servedFormats are the formats an endpoint that speaks native serves. Every pair of the three chat formats converts
// (protocol README.md「跨格式转换」), so an endpoint that speaks one serves all three; Native keeps the one it is called
// in, which is what a request only its own format can express needs. The direct formats do not convert, so an endpoint
// serves one only where it lists it.
func servedFormats(native routing.FormatSet) routing.FormatSet {
	if native&chatFormats != 0 {
		return native | chatFormats
	}
	return native
}

// listedModelOf is the catalog model an endpoint may serve, or why it cannot: the catalog is the authority on what may
// be served, and suppliers may keep a mapping to a model retired since their last sync.
func listedModelOf(snap *catalog.Snapshot, id catalog.ModelID) (catalog.Model, string) {
	model, ok := snap.Model(id)
	switch {
	case !ok:
		return catalog.Model{}, "the catalog has no such model"
	case model.Status != catalog.StatusListed:
		return catalog.Model{}, "the catalog model is not listed"
	}
	return model, ""
}

// endpointOf is one supplier endpoint as routing holds it, or the reason it cannot be one. The rules are the assembly
// ones of docs/domains/routing.md「快照从哪来」: nothing is guessed — a model without a spec has no routing entry at all;
// an endpoint with no sale price for it has none, while another endpoint of the same model can still build.
func endpointOf(snap *catalog.Snapshot, prices map[catalog.ModelID]billing.Sold, blocked map[catalog.ModelID]struct{}, byokFee string, rates []platform.ExchangeRate, siteCurrency platform.Currency, r suppliers.Routable,
	measured quality.Measures,
) (routing.Endpoint, string) {
	model, why := listedModelOf(snap, catalog.ModelID(r.Model))
	if why != "" {
		return routing.Endpoint{}, why
	}
	serving, ok := servingOf(model, r.Formats)
	if !ok {
		return routing.Endpoint{}, "its spec declares no context length, output limit, streaming or abilities"
	}
	sold, why := saleOf(snap, prices, byokFee, rates, siteCurrency, model, r)
	if why != "" {
		return routing.Endpoint{}, why
	}
	reportable := model.Reportable()
	price, why := endpointPrice(sold, r.ID)
	if why != "" {
		return routing.Endpoint{}, why
	}
	if sold.Audiences == nil && billing.Unpriced(price.Schedule, reportable) != (catalog.Reportable{}) {
		// Only a reference price was checked against what the model reports when it was listed; a fixed price was not.
		return routing.Endpoint{}, "its sale price lacks a price for an item or a service tier the model reports"
	}
	tiers := reportable.ServiceTiers
	tiers.Add(catalog.ServiceTierStandard) // every endpoint is called at the standard tier unless it says otherwise
	native := routing.Of(r.Formats...)
	_, blockedItem := blocked[catalog.ModelID(r.Model)]
	// The model streams only if its spec says the vendor does and the supplier's own policy says it can pass a stream:
	// the gateway reads every answer as one, so an endpoint lacking either cannot serve a generation.
	streams := serving.Streams && r.Streams
	dialect := model.Dialect()
	baseCaps, baseParams := capabilities(serving.Abilities), parameterSupport(serving, dialect)
	specs := formatSpecs(r.Formats, serving, baseCaps, baseParams, measured)
	e := routing.Endpoint{
		ID: routing.EndpointID(r.ID), Supplier: routing.SupplierID(r.Supplier), Codename: r.Codename, Model: catalog.ModelID(r.Model), ModelSlug: model.Slug,
		// openrouter/free chooses only these: the catalog's :free entry, whose sale price the check above pins at zero.
		Free: model.Free(),
		// The platform's own: M1's suppliers are the platform's (suppliers' owner scope is not part of Routable), and
		// the node reaches every one of them from its own address.
		Owner: routing.Owner{Site: routing.SiteID(r.Site), Account: routing.AccountID(r.Account)},
		Byok:  r.Byok, ByokFallback: r.ByokFallback,
		Enabled: true, BalanceOK: r.Funds != nil, Funds: r.Funds, Cost: r.Schedule, Egresses: 1, PoolLow: r.PoolLow,
		// Where the endpoint's supply comes from is what quality's fingerprints decided from its real responses, the
		// fallback being relay until a rule matches (docs/domains/quality.md「来源类别与指纹」). Nothing records what a
		// supplier does with the data it is sent, so an endpoint is taken to be the worst case rather than the best: a
		// preset that requires no retention must not route to it on the strength of a value nobody set.
		Class: routing.Class(measured.Class), Policy: routing.PolicyMayTrain,
		// Health, the quality score, its confidence, the tool-call error rate, and the time to first token and speed are
		// what real traffic and the probes measured, as of this build: the snapshot is at most one rebuild behind the attempts
		// (docs/domains/quality.md「质量分、吞吐与延迟」). An endpoint nothing counted has the neutral quality 1 and the
		// confidence 0, which routing keeps apart from a measured bad one. Verified is the health loop's last
		// fingerprint check, which real traffic does not count for (docs/domains/quality.md「真伪检测」).
		Quality: measured.Quality, Confidence: measured.Confidence, Health: measured.Health, ToolErrorRate: measured.ToolErrorRate,
		Throughput: measured.Throughput, Latency: measured.Latency, Verified: measured.Verified,
		Score: measured.ProductScore, ScoreConfidence: measured.ProductScoreConfidence,
		LatencyPercentiles: measured.LatencyPercentiles, ThroughputPercentiles: measured.ThroughputPercentiles,
		Quantization: r.Quantization,
		ServiceTiers: tiers,
		Native:       native, Served: servedFormats(native), Specs: specs,
		Caps: baseCaps, Parameters: baseParams, Context: serving.Context, MaxOutput: serving.MaxOutput,
		// The spec records no image size and no page limit, so ImageTokens, PageTokens and FilePages stay 0: billing
		// takes an image or a file of unknown size to fill the whole window when it reserves, which must never be below
		// what a request spends, and charges an estimate when the upstream reports no input (billing.InputEstimate).
		Streams: streams,
		Cancels: r.Cancels,
		// The two model facts a reservation made in segments rests on, from the spec: the fastest output, which sizes
		// the overshoot allowance, and the formats whose stream shows reasoning, which metering must see. A spec that
		// does not say leaves them zero, and the entry is reserved whole.
		OutputSpeed:      model.OutputSpeed(),
		ReasoningVisible: reasoningVisible(model.ReasoningVisible()),
		Reports:          reportable.Items,
		ToolUses:         model.ToolUses(),
		UnknownItem:      blockedItem,
		AudiencePrices:   sold.Audiences,
		Price:            price,
		Target:           targetOf(r.Upstream, streams, dialect),
	}
	if r.Capacity != nil { // a nil *suppliers.Capacity in the interface would read as an endpoint with no capacity
		e.Capacity = r.Capacity
	}
	// The endpoint's own fields are what holds in every native format; Plan reads Specs for the format it calls.
	surface := e.Surface()
	e.Caps, e.Parameters, e.Context, e.MaxOutput = surface.Caps, surface.Parameters, surface.Context, surface.MaxOutput
	return e, ""
}

// saleOf is the sale price routing holds for the endpoint's model: the site's published one, or for an endpoint of the
// account's own key the site's BYOK fee on the model's reference price.
func saleOf(snap *catalog.Snapshot, prices map[catalog.ModelID]billing.Sold, byokFee string, rates []platform.ExchangeRate,
	siteCurrency platform.Currency, model catalog.Model, r suppliers.Routable,
) (billing.Sold, string) {
	sold, ok := prices[catalog.ModelID(r.Model)]
	if !r.Byok && (!ok || !sold.Available()) {
		return billing.Sold{}, "no published sale price is available for this node; check the price publication list and local costs"
	}
	if model.Free() && ok && !sold.Zero() {
		return billing.Sold{}, "a :free model has a non-zero sale price for some audience"
	}
	if !r.Byok {
		return sold, ""
	}
	currency := siteCurrency
	if ok && sold.Available() {
		currency = sold.Schedule.Currency()
	}
	return byokSale(snap, catalog.ModelID(r.Model), currency, byokFee, rates)
}

// byokSale is the price of an endpoint of the account's own key. The account pays its provider; Axiom charges the
// site's share of the model's reference price, the approved rule for a source the platform does not buy. The schedule
// is scaled here, once, so the reservation, settlement and refund all use the same amounts.
func byokSale(snap *catalog.Snapshot, model catalog.ModelID, currency platform.Currency, byokFee string,
	rates []platform.ExchangeRate,
) (billing.Sold, string) {
	ref := billing.ReferenceFor(snap, model, currency, "exchange")
	if ref.ID == uuid.Nil() {
		return billing.Sold{}, "no reference price is available for the BYOK fee"
	}
	multiplier := byokFee
	rule := billing.Rule{Enabled: true, Method: billing.Method{
		Kind: "reference_multiplier", Multiplier: &multiplier, CurrencyConversion: "exchange",
	}}
	scaled, err := billing.Sell(rule, ref, currency, rates...)
	if err != nil {
		return billing.Sold{}, "the site's BYOK fee cannot price the reference schedule"
	}
	// The endpoint is the key's own, so the site's Sold says nothing about it: a version priced from each endpoint's
	// cost has no price for an endpoint it did not publish, and endpointPrice would leave the key out
	// (billing.Sold.PriceFor). The scaled reference schedule is the whole of this endpoint's price.
	return billing.Sold{Version: ref.ID, Schedule: scaled}, ""
}

// formatSpecs is what the endpoint takes in each of its native formats. A measurement replaces the catalog's value for
// the format it was measured in: detection runs through the same protocol the gateway uses, so a capability or request
// parameter it found working is added and one it found lacking is removed; a measured context or output ceiling
// replaces the catalog's, higher or lower. A field nothing measured keeps the catalog's value, and another format's
// measurement says nothing about this one (docs/domains/quality.md「实测的能力与上限」).
func formatSpecs(formats []protocol.Format, serving catalog.Serving, caps protocol.Capabilities, params protocol.ParameterSet,
	measured quality.Measures,
) map[protocol.Format]routing.Spec {
	specs := make(map[protocol.Format]routing.Spec, len(formats))
	for _, f := range formats {
		facts := measured.Facts[f]
		spec := routing.Spec{
			Caps:       (caps &^ facts.Unsupported) | facts.Supported,
			Parameters: (params &^ facts.UnsupportedParameters) | facts.SupportedParameters,
			Context:    serving.Context, MaxOutput: serving.MaxOutput,
		}
		if facts.Context > 0 {
			spec.Context = facts.Context
		}
		if facts.MaxOutput > 0 {
			spec.MaxOutput = facts.MaxOutput
		}
		for _, c := range capabilityParameters {
			if !spec.Caps.Has(c.capability) {
				spec.Parameters &^= c.parameters
			}
		}
		specs[f] = spec
	}
	return specs
}

// capabilityParameters are the request parameters that express a capability. A parameter is meaningful only while its
// capability is there: a measurement that removes tools, structured output or reasoning also takes the parameters that
// express them out of the inventory.
var capabilityParameters = [...]struct {
	capability protocol.Capabilities
	parameters protocol.ParameterSet
}{
	{protocol.Tools, 1<<protocol.ParamTools | 1<<protocol.ParamToolChoice | 1<<protocol.ParamParallelToolCalls},
	{protocol.StructuredOutput, 1 << protocol.ParamResponseFormat},
	{protocol.Reasoning, 1<<protocol.ParamReasoning | 1<<protocol.ParamReasoningEffort},
}

// targetOf is how an attempt is written for the endpoint: its upstream name and whether it streams, and the spec's
// dialect as it is. An unset claim stays unset in the Target; protocol decides what a request that needs it is to do
// (internal/protocol/README.md「跨格式转换」), so no default for the model is made here.
func targetOf(upstream string, streams bool, d catalog.Dialect) protocol.Target {
	return protocol.Target{
		Model: upstream, Streams: streams, Thinking: d.Thinking, EncryptedReasoning: d.EncryptedReasoning, MidSystem: d.MidSystem,
		UntrustedSystemAfterTools: d.UntrustedSystemAfterTools, DefaultSamplingOnly: d.DefaultSamplingOnly,
		ForcedToolChoice: d.ForcedToolChoice, CacheBreakpoints: d.CacheBreakpoints,
	}
}

// abilityBits maps the catalog's abilities to protocol's capability bits. The two enumerations happen to be in the same
// order, but the mapping is written out so that a change in either of them is a missing line here and not a silent
// shift. It is also the public page's order.
var abilityBits = [...]struct {
	ability catalog.Ability
	bit     protocol.Capabilities
}{
	{catalog.AbilityTools, protocol.Tools},
	{catalog.AbilityImages, protocol.Images},
	{catalog.AbilityDocuments, protocol.Documents},
	{catalog.AbilityReasoning, protocol.Reasoning},
	{catalog.AbilityPromptCache, protocol.PromptCache},
	{catalog.AbilityStructuredOutput, protocol.StructuredOutput},
	{catalog.AbilityWebSearch, protocol.WebSearch},
	{catalog.AbilityCodeExecution, protocol.CodeExecution},
	{catalog.AbilityFileSearch, protocol.FileSearch},
}

// capabilities is a spec's abilities as protocol's bits.
func capabilities(abilities catalog.AbilitySet) (c protocol.Capabilities) {
	for _, a := range abilityBits {
		if abilities.Has(a.ability) {
			c |= a.bit
		}
	}
	return c
}

// abilityNames is capabilities' inverse for the public page.
func abilityNames(c protocol.Capabilities) []string {
	names := make([]string, 0, len(abilityBits))
	for _, a := range abilityBits {
		if c.Has(a.bit) {
			names = append(names, a.ability.String())
		}
	}
	return names
}

// reasoningVisible is a spec's formats whose stream shows reasoning as routing's bits. The two enumerations happen to be
// in the same order, but the mapping is written out so that a change in either of them is a missing line here and not a
// silent shift, as capabilities does for the abilities.
func reasoningVisible(formats catalog.FormatSet) (f routing.FormatSet) {
	for _, x := range [...]struct {
		format catalog.CallFormat
		bit    protocol.Format
	}{
		{catalog.FormatMessages, protocol.Messages},
		{catalog.FormatResponses, protocol.Responses},
		{catalog.FormatChat, protocol.Chat},
	} {
		if formats.Has(x.format) {
			f |= routing.Of(x.bit)
		}
	}
	return f
}
