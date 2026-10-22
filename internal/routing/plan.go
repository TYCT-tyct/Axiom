package routing

import (
	"slices"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// need is everything a candidate is held against, worked out once per request.
type need struct {
	in     *Input
	req    *protocol.Request
	preset *Preset
	over   *Override
	tokens int64                  // an estimate of the input, in tokens; at billing.InputBytesPerToken, the rate a charge's estimate takes
	demand billing.Demand         // what billing would quote the request for, which bounds its length for the price limits
	tiers  catalog.ServiceTierSet // the tiers it may be charged at, as billing takes them: its own and standard
	now    time.Time
}

// Plan makes the route of a request: the candidates of the first model that survive the filters, in order, then the
// alternates', cut to MaxEntries. It reads memory only, so it can run inside the admission transaction. When nothing
// is left it says whether to wait (every candidate that passed is full) or why nothing can serve the request.
func (r *Router) Plan(in Input) (Plan, *Wait, error) {
	now := r.cfg.Now()
	snap := r.snap.Load()
	if snap == nil || !r.fresh(snap, now) {
		return Plan{}, nil, ErrStale
	}
	preset, ok := snap.presets[in.Admission.Preset]
	if !ok {
		return Plan{}, nil, ErrUnknownPreset
	}
	n := &need{
		in: &in, req: in.Req, preset: &preset, over: &in.Override, now: now,
		tokens: in.Req.TextBytes / billing.InputBytesPerToken, demand: DemandOf(in.Req), tiers: in.Req.ServiceTiers,
	}
	n.tiers.Add(catalog.ServiceTierStandard)
	var rec Record
	var order []ranked
	var seen []catalog.ModelID
	for _, m := range in.Models {
		if slices.Contains(seen, m) { // a model named twice is one model: its endpoints are ranked once
			continue
		}
		seen = append(seen, m)
		if !in.Admission.allows(m) {
			rec.Excluded = append(rec.Excluded, Excluded{Reason: NotAllowed})
			continue
		}
		cands := snap.byModel[m]
		if len(cands) == 0 {
			rec.Excluded = append(rec.Excluded, Excluded{Reason: NoCandidates})
		}
		order = append(order, r.rank(cands, n, &rec)...)
	}
	attempts := orDefault(preset.MaxAttempts, r.cfg.MaxAttempts)
	entries := r.entries(order, n, &rec)
	entries = entries[:min(len(entries), attempts)] // an entry past the most attempts could never be reached
	if len(entries) == 0 {
		wait, err := r.nothing(&rec)
		return Plan{Record: rec}, wait, err
	}
	for _, e := range entries {
		rec.Route = append(rec.Route, e.Endpoint)
	}
	return Plan{
		Entries: entries, MaxAttempts: attempts,
		StartBy: now.Add(orDefault(preset.AttemptWindow, r.cfg.AttemptWindow)), StickyTTL: preset.StickyTTL, Record: rec,
	}, nil, nil
}

// DemandOf is the request as billing's quote reads it, so the length that bounds the price limits is billing's own
// (billing.InputBound), and the gateway reserves for the same demand it was routed by.
func DemandOf(req *protocol.Request) billing.Demand {
	d := billing.Demand{
		TextBytes: req.TextBytes, Images: req.Images, Documents: req.Documents, Choices: req.Choices,
		Compaction: req.Compacts, InputItems: req.InputItems, ServiceTiers: req.ServiceTiers,
	}
	if req.Needs.Has(protocol.WebSearch) {
		d.ServerTools = append(d.ServerTools, billing.ServerTool{Item: catalog.ItemWebSearch, MaxUses: req.WebSearches})
	}
	if req.Needs.Has(protocol.CodeExecution) {
		d.ServerTools = append(d.ServerTools, billing.ServerTool{Item: catalog.ItemCodeExecution, MaxUses: req.CodeExecutions})
	}
	return d
}

// nothing says why the plan is empty: a Wait when some endpoint would have served but is full, otherwise a NoEndpoint.
func (r *Router) nothing(rec *Record) (*Wait, error) {
	permanent := true
	for _, e := range rec.Excluded {
		switch {
		case e.Reason == Full:
			return &Wait{RetryAfter: r.cfg.RetryAfter}, nil
		case !e.Reason.Permanent():
			permanent = false
		}
	}
	if len(rec.Excluded) == 0 {
		rec.Excluded = append(rec.Excluded, Excluded{Reason: NoCandidates})
	}
	return nil, &NoEndpoint{Reasons: rec.Excluded, Permanent: permanent}
}

// entries takes the first MaxEntries ranked endpoints. An entry after the first is a retry: one at a supplier whose
// retry budget is used up is left out, as the budget is for.
func (r *Router) entries(order []ranked, n *need, rec *Record) []Entry {
	entries := make([]Entry, 0, MaxEntries)
	for _, c := range order {
		if len(entries) == MaxEntries {
			break
		}
		if slices.ContainsFunc(entries, func(e Entry) bool { return e.Endpoint == c.ID }) {
			continue
		}
		if len(entries) > 0 && !r.budget.allow(c.Supplier, n.now) {
			rec.Excluded = append(rec.Excluded, Excluded{c.ID, RetryBudget})
			continue
		}
		entries = append(entries, r.entry(c.candidate, n))
	}
	return entries
}

// entry works out what billing needs for one endpoint and this request: the output limit and whether the reservation can
// be made in segments.
func (r *Router) entry(c *candidate, n *need) Entry {
	out, _ := c.outputLimit(n.req, n.tokens)
	// Without a known output speed the overshoot allowance of a segmented reservation would be nothing, so an entry
	// whose speed is unknown is reserved in full; so is one whose call the gateway cannot meter (Input.NoSegments), and
	// every entry while no segment size is configured: a segment of 0 tokens would reserve no output at all.
	segmentable := !n.in.NoSegments && c.Streams && c.Cancels && c.ReasoningVisible.Has(n.req.Format) && !n.req.HidesReasoning &&
		c.OutputSpeed > 0 && r.cfg.FirstSegment > 0 && out > r.cfg.FirstSegment+r.cfg.Overshoot
	target := c.Target
	format, _ := c.callFormat(n)
	target.Format = format
	target.Streams = c.Streams // the request sent asks for a stream, which the gateway relays or aggregates
	if c.MaxOutput > 0 {       // a model whose limit is unknown has no default to send: the upstream's own applies
		target.MaxOutput = out
	}
	limits := c.model
	limits.OpenAIUsage = format != protocol.Messages // the call format decides whose usage semantics the reply has
	return Entry{
		Exchange: c.Exchange,
		Endpoint: c.ID, Supplier: c.Supplier, Target: target, Model: c.Model, Price: c.Price, Limits: limits, MaxOutput: out,
		Segmentable: segmentable,
	}
}

// callFormat is the format the endpoint is called in for the request, and whether it can serve it at all. A request of
// the endpoint's own format is called in it. Otherwise Messages/Responses use Chat;
// Chat uses Messages before Responses (docs/domains/gateway.md「转发规则」).
// NativeOnly says only the request's own format will do.
func (c *candidate) callFormat(n *need) (protocol.Format, bool) {
	f := n.req.Format
	if n.in.NativeOnly {
		return f, c.Native.Has(f)
	}
	if f != protocol.Chat {
		if c.Native.Has(f) {
			return f, true
		}
		if c.Served.Has(f) && c.Native.Has(protocol.Chat) {
			return protocol.Chat, true
		}
		return f, c.Served.Has(f)
	}
	for _, g := range [...]protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		if c.Native.Has(g) {
			return g, true
		}
	}
	return f, false
}

// check holds an endpoint against the request, cheapest tests first, and returns why it is left out, or 0. An endpoint
// the account cannot see is not left out, it is not there: no reason is given, so no existence is revealed.
func check(c *candidate, n *need) (reason Reason, visible bool) {
	if !c.Owner.visibleTo(n.in.Admission) {
		return 0, false
	}
	if reason = c.unavailable(); reason != 0 {
		return reason, true
	}
	if reason = c.unfit(n); reason != 0 {
		return reason, true
	}
	switch {
	case !n.presetAllows(c):
		return PresetFilter, true
	case !n.overrideAllows(c):
		return OverrideFilter, true
	case c.Price.none():
		return NoPrice, true
	case c.UnknownItem:
		return UnknownItem, true
	case c.state.blocked.active(n.now) || c.supplierPause.active(n.now):
		return CoolingDown, true
	case c.full():
		return Full, true
	}
	return 0, true
}

// unavailable is the endpoint's own state: switched off, taken out, or its supplier unable to pay or reach.
func (c *candidate) unavailable() Reason {
	switch {
	case !c.Enabled:
		return Disabled
	case c.BreakerOpen:
		return BreakerOpen
	case !c.BalanceOK:
		return SupplierBalance
	case c.Egresses <= 0:
		return NoEgress
	}
	return 0
}

// unfit is what the request needs and the endpoint lacks. The input is an estimate of its tokens from the body's bytes
// (billing.InputBytesPerToken), so an endpoint is left out only when the request surely does not fit; one that turns out too
// small answers with a context error, which falls back to the next entry.
func (c *candidate) unfit(n *need) Reason {
	switch {
	case !c.Caps.Has(n.req.Needs):
		return MissingCapability
	case !c.servesFormat(n):
		return FormatNotServed
	case !c.streamsFor(n):
		return FormatNotServed
	case n.req.ServiceTiers&c.ServiceTiers == 0:
		return ServiceTierNotServed
	}
	if _, fits := c.outputLimit(n.req, n.tokens); !fits {
		return ContextTooSmall
	}
	return 0
}

// streamsFor says the endpoint can answer as the gateway reads the answer: every call but the compact one is read as a
// stream, which an endpoint that does not stream cannot give it (docs/domains/gateway.md「转发规则」).
func (c *candidate) streamsFor(n *need) bool { return c.Streams || n.in.NoSegments }

// outputLimit is the one rule for how much output an entry at c may produce and whether the request fits c's context,
// used both to filter and to build the entry, so the two never disagree. The limit is the request's own, if it sets one,
// held to the endpoint's, or else the endpoint's default. The context holds the input and the output together: a limit
// the request set that does not fit leaves the endpoint out; a default is cut to what is left of the context.
func (c *candidate) outputLimit(req *protocol.Request, tokens int64) (limit int64, fits bool) {
	limit = c.MaxOutput
	if req.MaxOutput > 0 && (limit == 0 || req.MaxOutput < limit) {
		limit = req.MaxOutput
	}
	room := c.Context - tokens
	switch {
	case room <= 0:
		return 0, false
	case req.MaxOutput > 0 && limit > room:
		return 0, false
	case limit == 0 || limit > room:
		limit = room
	}
	return limit, true
}

func (c *candidate) servesFormat(n *need) bool {
	_, ok := c.callFormat(n)
	return ok
}

func (c *candidate) full() bool {
	return c.MaxConcurrent > 0 && c.state.inflight.Load() >= c.MaxConcurrent
}

// presetAllows applies the preset's filters, and the price limit that the request's override may lower.
func (n *need) presetAllows(c *candidate) bool {
	p := n.preset
	return p.Classes.Has(c.Class) && p.Policies.Has(c.Policy) && c.Quality >= p.MinQuality && c.Confidence >= p.MinConfidence &&
		(!p.VerifiedOnly || c.Verified) && n.withinLimit(p.MaxPrice, c) &&
		(!p.NoFallbacks || listed(p.Order, c.Supplier))
}

// overrideAllows applies the request's filters. They only narrow what the preset allows: they are checked after it,
// and an endpoint must pass both.
func (n *need) overrideAllows(c *candidate) bool {
	o := n.over
	return (len(o.Only) == 0 || listed(o.Only, c.Supplier)) && !listed(o.Exclude, c.Supplier) &&
		(o.Classes == 0 || o.Classes.Has(c.Class)) && (o.Policies == 0 || o.Policies.Has(c.Policy)) &&
		n.withinLimit(o.MaxPrice, c) && (!o.NoFallbacks || listed(o.Order, c.Supplier))
}

// withinLimit holds the endpoint's price to a limit against the longest prompt any sampling of the request can have.
func (n *need) withinLimit(limit PriceLimit, c *candidate) bool {
	if limit.Set == 0 {
		return true
	}
	_, longest := billing.InputBound(n.demand, c.model)
	return limit.within(c.Price.Schedule, n.tiers, longest)
}

func listed(ids []SupplierID, id SupplierID) bool { return slices.Contains(ids, id) }
