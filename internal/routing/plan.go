package routing

import (
	"errors"
	"slices"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// need is everything a candidate is held against, worked out once per request.
type need struct {
	in     *Input
	req    *protocol.Request
	preset *Preset
	over   *Override
	auto   bool                   // rank across every model: the request named openrouter/auto
	tokens int64                  // an estimate of the input, in tokens; at billing.InputBytesPerToken, the rate a charge's estimate takes
	demand billing.Demand         // what billing would quote the request for, which bounds its length for the price limits
	tiers  catalog.ServiceTierSet // the tiers it may be charged at, as billing takes them: its own and standard
	now    time.Time
}

// attempts is how many endpoints a plan may try: the preset's or the router's default, and one for a file reference,
// whose id is the supplier account's and can only reach the endpoint that holds it.
func (r *Router) attempts(n *need) int {
	if n.in.FileRef {
		return 1
	}
	return orDefault(n.preset.MaxAttempts, r.cfg.MaxAttempts)
}

// rankAll reports whether one ranking spans every model rather than each model's endpoints first: a global sort, or
// openrouter/auto, whose whole choice is a comparison between models.
func (n *need) rankAll() bool { return n.over.SortGlobal || n.auto }

// rankModels ranks the endpoints of every model the request may reach: the models it names, or every model in the
// snapshot for openrouter/auto, which lets the ranking choose the model. It records why a model was left out.
func (r *Router) rankModels(snap *Snapshot, n *need, rec *Record) []ranked {
	models := n.in.Models
	if n.in.Auto {
		models = snap.models
	}
	var order []ranked
	var seen []catalog.ModelID
	for _, m := range models {
		if slices.Contains(seen, m) { // a model named twice is one model: its endpoints are ranked once
			continue
		}
		seen = append(seen, m)
		if !n.in.Admission.allows(m) {
			rec.Excluded = append(rec.Excluded, Excluded{Reason: NotAllowed})
			continue
		}
		cands := snap.byModel[m]
		if len(cands) == 0 {
			rec.Excluded = append(rec.Excluded, Excluded{Reason: NoCandidates})
		}
		order = append(order, r.rank(cands, n, rec)...)
	}
	return order
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
		in: &in, req: in.Req, preset: &preset, over: &in.Override, auto: in.Auto, now: now,
		tokens: in.Req.TextBytes / billing.InputBytesPerToken, demand: DemandOf(in.Req), tiers: in.Req.ServiceTiers,
	}
	n.tiers.Add(catalog.ServiceTierStandard)
	var rec Record
	order := r.rankModels(snap, n, &rec)
	if n.rankAll() {
		r.sort(order, n.strategy())
	}
	attempts := r.attempts(n)
	order = healthOrder(order, n, &rec, min(attempts, MaxEntries))
	rec.Unmeasured = unmeasured(order, n.strategy())
	entries := r.entries(order, n, &rec)
	entries = entries[:min(len(entries), attempts)] // an entry past the most attempts could never be reached
	if len(entries) == 0 {
		wait, err := r.nothing(&rec)
		return Plan{Record: rec}, wait, err
	}
	for _, e := range entries {
		rec.Route = append(rec.Route, e.Endpoint)
		if e.BreakerFallback {
			rec.BreakerFallback = append(rec.BreakerFallback, e.Endpoint)
		}
	}
	return Plan{
		Entries: entries, MaxAttempts: attempts,
		StartBy: now.Add(orDefault(preset.AttemptWindow, r.cfg.AttemptWindow)), StickyTTL: preset.StickyTTL, Record: rec,
	}, nil, nil
}

// unmeasured reports whether a fastest-first order has nothing measured to rank by. It reads the order after the
// breakers: a measured endpoint they take out of the route says nothing about this route's speed.
func unmeasured(order []ranked, strategy Strategy) bool {
	return strategy == Fastest && len(order) > 0 && !slices.ContainsFunc(order, func(c ranked) bool { return c.Throughput > 0 })
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
	singleSupplier := n.over.NoFallbacks && len(n.over.OrderNames) == 0
	for _, c := range order {
		if len(entries) == MaxEntries {
			break
		}
		if slices.ContainsFunc(entries, func(e Entry) bool {
			return e.Endpoint == c.ID || singleSupplier && e.Model == c.Model
		}) {
			continue
		}
		if len(entries) > 0 && !r.budget.allow(c.Supplier, n.now) {
			rec.Excluded = append(rec.Excluded, Excluded{c.ID, RetryBudget})
			continue
		}
		entry := r.entry(c.candidate, n)
		entry.BreakerFallback = !c.opened.IsZero()
		entry.Price = c.price.PriceRef
		entries = append(entries, entry)
	}
	return entries
}

// entry works out what billing needs for one endpoint and this request: the output limit and whether the reservation can
// be made in segments.
func (r *Router) entry(c *candidate, n *need) Entry {
	out, _ := c.outputLimit(n.req, n.tokens)
	format, _ := c.callFormat(n)
	// Without a known output speed the overshoot allowance of a segmented reservation would be nothing, so an entry
	// whose speed is unknown is reserved in full; so is one whose call the gateway cannot meter (Input.NoSegments), and
	// every entry while no segment size is configured: a segment of 0 tokens would reserve no output at all.
	allowance, _ := r.cfg.Segments.Allowance(c.OutputSpeed)
	// Reasoning counts against a segment while it streams, so a model that reasons must show it in the stream the
	// gateway reads: the format the endpoint is called in, not the client's, since a converted call is metered where it
	// is made. A model without the ability has no reasoning to miss.
	reasoning := c.Caps.Has(protocol.Reasoning)
	segmentable := !n.in.NoSegments && c.Streams && c.Cancels && (!reasoning || c.ReasoningVisible.Has(format)) &&
		!n.req.HidesReasoning && c.OutputSpeed > 0 && r.cfg.Segments.Output > 0 && out > r.cfg.Segments.Output+allowance
	target := c.Target
	target.Format = format
	target.Streams = c.Streams // the request sent asks for a stream, which the gateway relays or aggregates
	if c.MaxOutput > 0 {       // a model whose limit is unknown has no default to send: the upstream's own applies
		target.MaxOutput = out
	}
	limits := c.model
	limits.OpenAIUsage = format != protocol.Messages // the call format decides whose usage semantics the reply has
	return Entry{
		Exchange: c.Exchange,
		Endpoint: c.ID, Supplier: c.Supplier, Target: target, Model: c.Model, ModelSlug: c.ModelSlug, Price: c.Price, Limits: limits, MaxOutput: out,
		Segmentable: segmentable,
	}
}

// callFormat is the format the endpoint is called in for the request, and whether it can serve it at all. A request of
// the endpoint's own format is called in it. Otherwise Messages/Responses use Chat, then the other of the two; Chat uses
// Messages before Responses (docs/domains/gateway.md「转发规则」). Every pair converts, so what is left is whether the
// endpoint has a format to convert to at all. NativeOnly says only the request's own format will do.
func (c *candidate) callFormat(n *need) (protocol.Format, bool) {
	f := n.req.Format
	if n.in.NativeOnly {
		return f, c.Native.Has(f)
	}
	if c.Native.Has(f) {
		return f, true
	}
	if f == protocol.Chat {
		for _, g := range [...]protocol.Format{protocol.Messages, protocol.Responses} {
			if c.Native.Has(g) {
				return g, true
			}
		}
		return f, false
	}
	if c.Native.Has(protocol.Chat) {
		return protocol.Chat, true
	}
	g := protocol.Responses // the other native format of a Messages client, and the other way round
	if f == protocol.Responses {
		g = protocol.Messages
	}
	return g, c.Native.Has(g)
}

// check holds an endpoint against the request, cheapest tests first, and returns why it is left out, or 0. An endpoint
// the account cannot see is not left out, it is not there: no reason is given, so no existence is revealed.
func check(c *candidate, n *need, price *candidatePrice) (reason Reason, visible bool) {
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
	case !n.presetAllows(c, price):
		return PresetFilter, true
	case !n.overrideAllows(c, price):
		return OverrideFilter, true
	case price.none():
		return NoPrice, true
	case c.UnknownItem:
		return UnknownItem, true
	case c.state.blocked.active(n.now) || c.supplierPause.active(n.now):
		return CoolingDown, true
	}
	if reason = c.funding(n); reason != 0 {
		return reason, true
	}
	if c.full() {
		return Full, true
	}
	return 0, true
}

// funding uses the same demand, output cap and call format as the reservation, but the supplier's native cost plan.
func (c *candidate) funding(n *need) Reason {
	if c.Funds == nil {
		return 0
	}
	if !c.Funds.CanPay(platform.Exact{}) {
		return SupplierBalance
	}
	out, _ := c.outputLimit(n.req, n.tokens)
	format, _ := c.callFormat(n)
	model := c.model
	model.OpenAIUsage = format != protocol.Messages
	cost, err := billing.ReservedCost(n.demand, billing.Quotable{Price: c.Cost, Model: model, MaxOutput: out})
	// An unbounded tool is a request error: admission's Quote must keep returning its actionable 400, rather than
	// disguising it as a supplier outage. The same bounds make Quote refuse it before any upstream call.
	if errors.Is(err, billing.ErrUnbounded) {
		return 0
	}
	if err != nil {
		return UnquotableCost
	}
	if !c.Funds.CanPay(cost) {
		return SupplierBalance
	}
	return 0
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

// full reports whether the endpoint can take no more calls now: its credentials are all at their concurrency or minute
// ceilings, or its own in-flight ceiling is reached. The credential view is read here, at the end of the filters, so a
// route is not frozen to an endpoint that would refuse every attempt (docs/domains/routing.md「容量感知」).
func (c *candidate) full() bool {
	if c.Capacity != nil && c.Capacity.Full() {
		return true
	}
	return c.MaxConcurrent > 0 && c.state.inflight.Load() >= c.MaxConcurrent
}

// presetAllows applies the preset's filters, and the price limit that the request's override may lower.
func (n *need) presetAllows(c *candidate, price *candidatePrice) bool {
	p := n.preset
	return p.Classes.Has(c.Class) && p.Policies.Has(c.Policy) && c.Quality >= p.MinQuality && c.Confidence >= p.MinConfidence &&
		(!p.VerifiedOnly || c.Verified) && n.withinLimit(p.MaxPrice, c, price) &&
		(!p.NoFallbacks || listed(p.Order, c.Supplier))
}

// overrideAllows applies the request's filters. They only narrow what the preset allows: they are checked after it,
// and an endpoint must pass both.
func (n *need) overrideAllows(c *candidate, price *candidatePrice) bool {
	o := n.over
	return o.allowsName(c.Codename) &&
		(o.Classes == 0 || o.Classes.Has(c.Class)) && (o.Policies == 0 || o.Policies.Has(c.Policy)) &&
		(!o.RequireParameters || c.Parameters.HasAll(o.RequiredParameters)) &&
		n.withinLimit(o.MaxPrice, c, price)
}

func (o *Override) allowsName(name string) bool {
	return (len(o.OnlyNames) == 0 || slices.Contains(o.OnlyNames, name)) && !slices.Contains(o.ExcludeNames, name) &&
		(len(o.OrderNames) == 0 || !o.NoFallbacks || slices.Contains(o.OrderNames, name))
}

// withinLimit holds the endpoint's price to a limit against the longest prompt any sampling of the request can have.
func (n *need) withinLimit(limit PriceLimit, c *candidate, price *candidatePrice) bool {
	if limit.Set == 0 {
		return true
	}
	_, longest := billing.InputBound(n.demand, c.model)
	return limit.within(price.Schedule, n.tiers, longest)
}

func listed(ids []SupplierID, id SupplierID) bool { return slices.Contains(ids, id) }

// serveable reports whether some endpoint can serve now. When only slow-full windows keep every other endpoint from
// serving, their marks are cleared first: a route that starts when a window recovers beats no route at all.
func serveable(order []ranked) bool {
	if slices.ContainsFunc(order, func(c ranked) bool { return c.opened.IsZero() && !c.slowFull }) {
		return true
	}
	if !slices.ContainsFunc(order, func(c ranked) bool { return c.opened.IsZero() }) {
		return false
	}
	for i := range order {
		order[i].slowFull = false
	}
	return true
}

// healthOrder applies breakers after every request-specific filter, across primary and alternate models.
func healthOrder(order []ranked, n *need, rec *Record, slots int) []ranked {
	if !serveable(order) {
		orderLastResorts(order, n.rankAll())
		return order
	}
	owned, hasOwned := ownedFallback(order, n.now)
	out := order[:0]
	for _, c := range order {
		reason := Reason(0)
		if !c.opened.IsZero() {
			reason = BreakerOpen
		} else if c.slowFull {
			reason = Full
		}
		if reason != 0 {
			rec.Excluded = append(rec.Excluded, Excluded{c.ID, reason})
		} else {
			out = append(out, c)
		}
	}
	if hasOwned {
		out = includeOwnedFallback(out, owned, slots, n.over.NoFallbacks && len(n.over.OrderNames) == 0)
	}
	return out
}

// Repeated suppliers of one model must not consume the slots reserved for alternate models before entries deduplicates.
func includeOwnedFallback(order []ranked, owned ranked, slots int, perModel bool) []ranked {
	if perModel {
		if slices.ContainsFunc(order, func(c ranked) bool { return c.Model == owned.Model }) {
			return order
		}
		kept := order[:0]
		for _, c := range order {
			if len(kept) == slots-1 {
				break
			}
			if !slices.ContainsFunc(kept, func(previous ranked) bool { return previous.Model == c.Model }) {
				kept = append(kept, c)
			}
		}
		order = kept
	}
	order = order[:min(len(order), slots-1)]
	return append(order, owned)
}

func ownedFallback(order []ranked, now time.Time) (owned ranked, found bool) {
	for _, c := range order {
		if c.Owner.Platform() || c.opened.IsZero() || now.Sub(c.opened) < 10*time.Minute {
			continue
		}
		if !found || c.opened.After(owned.opened) {
			owned = c
			found = true
		}
	}
	return
}

func orderLastResorts(order []ranked, global bool) {
	// rank already groups each model by supplier order and slow status.
	for start := 0; start < len(order); {
		end := start + 1
		for end < len(order) && (global || order[end].Model == order[start].Model) && order[end].order == order[start].order && order[end].slow == order[start].slow {
			end++
		}
		slices.SortStableFunc(order[start:end], func(a, b ranked) int { return b.opened.Compare(a.opened) })
		start = end
	}
}
