package gateway

import (
	"fmt"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
)

// resolveSearch decides the request's one search path, now that routing has chosen the entry whose model and format
// decide what the provider can do. The plugin is one search, never two (docs/domains/gateway.md「联网搜索」): a request
// that already declares the provider's own search tool, and a model that searches on every call, keep that path and
// drop the plugin; otherwise the plugin is either the provider's tool or one backend, and never both.
func (h *Handler) resolveSearch(c *Call, plan routing.Plan, replan func() (routing.Plan, error)) (routing.Plan, error) {
	if c.Request.ServerSearch != nil {
		return h.resolveServerTool(c, plan)
	}
	if c.Request.Web == nil || len(plan.Entries) == 0 {
		return plan, nil
	}
	entry := plan.Entries[0]
	choice := searchChoice{MaxUses: entry.Limits.MaxUses[catalog.ItemWebSearch]}
	if model, ok := c.snap.Model(entry.Model); ok {
		choice.Native = servesWebSearch(model) && protocol.NativeSearchSupported(entry.Target.Format)
		choice.Always = model.AlwaysSearches()
	}
	decision, err := chooseSearch(c.Request.Web, choice, c.Request.Needs.Has(protocol.WebSearch), h.Search)
	if err != nil {
		return plan, err
	}
	if decision.native > 0 {
		return nativeSearch(c, plan, decision)
	}
	if decision.backend != nil {
		return backendSearch(c, plan, decision, replan)
	}
	if choice.Always {
		// The first entry searches without a tool. A normal fallback has neither that tool nor injected results, so
		// keeping it would silently turn a failed automatic search into an answer with no search at all.
		plan = autoSearchRoute(c, plan)
		if len(plan.Entries) == 0 {
			return plan, invalid("no candidate endpoint searches automatically")
		}
	}
	c.Request.Web = nil
	c.Request.SearchBytes = 0
	return plan, nil
}

// resolveServerTool decides how the openrouter:web_search tool runs: the provider's own search when the routed entry
// can serve it and the engine is native, otherwise the model-side loop through the chosen backend. The engine choice
// and its default are the web plugin's (docs/domains/gateway.md「联网搜索」「服务端工具」).
func (h *Handler) resolveServerTool(c *Call, plan routing.Plan) (routing.Plan, error) {
	tool := c.Request.ServerSearch
	if len(plan.Entries) == 0 {
		return plan, nil
	}
	cap := serverToolCap(&c.Request)
	if cap <= 0 {
		return plan, invalid("max_tool_calls is 0: the web search tool would have no step to run")
	}
	entry := plan.Entries[0]
	choice := searchChoice{MaxUses: cap}
	if model, ok := c.snap.Model(entry.Model); ok {
		choice.Native = servesWebSearch(model) && protocol.NativeSearchSupported(entry.Target.Format)
		choice.Always = model.AlwaysSearches()
	}
	if choice.Always {
		// The model searches without a tool on every call; the tool's declaration is dropped and its searches are the
		// model's own, as the web plugin treats such a model. A fallback without the automatic search would answer
		// without one, so only models that search stay.
		tool.Loop = false
		c.Request.Needs |= protocol.WebSearch
		c.Request.WebSearches = min(cap, maxNativeSearches)
		c.Request.SearchBytes, c.Request.Web = 0, nil
		plan = autoSearchRoute(c, plan)
		if len(plan.Entries) == 0 {
			return plan, invalid("no candidate endpoint searches automatically")
		}
		return plan, nil
	}
	decision, err := chooseSearchFor(&protocol.WebPlugin{Engine: tool.Engine, MaxResults: tool.MaxResults}, choice, false, h.Search, "tool")
	if err != nil {
		return plan, err
	}
	if decision.native > 0 {
		tool.Loop = false
		return nativeSearch(c, plan, decision)
	}
	// The loop runs one search per step and samples the model again after each: its input re-reads and per-use fees
	// are what the reservation covers, at the cap above.
	tool.Loop = true
	c.serverLoop, c.searchBackend, c.searchPrice = true, decision.backend, decision.price
	c.Request.Needs |= protocol.Tools
	c.Request.WebSearches = cap
	c.Request.SearchBytes = 0
	c.Request.Web = nil
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the selected search engine")
	}
	return plan, nil
}

// nativeSearch turns on the model's own tool and keeps only entries that can run it.
func nativeSearch(c *Call, plan routing.Plan, decision searchDecision) (routing.Plan, error) {
	// A request that already carries Responses' call limit keeps the stricter of it and the model's bound; the same
	// number goes into the body below, so the reservation and the upstream limit cannot disagree. Inspect records
	// the cap for a tool the client declared; a plugin turns the tool on after Inspect, so read the cap here for that
	// path too.
	if c.Request.Format == protocol.Responses && c.Request.WebSearches == 0 {
		if n, err := protocol.ResponsesMaxToolCalls(c.Body); err == nil {
			c.Request.WebSearches = n
		}
	}
	if c.Request.WebSearches > 0 {
		decision.native = min(decision.native, int(c.Request.WebSearches))
	}
	c.nativeSearch = decision.native
	c.Request.Needs |= protocol.WebSearch
	c.Request.WebSearches = int64(decision.native)
	c.Request.SearchBytes = 0
	c.Request.Web = nil
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the model's native web search")
	}
	return plan, nil
}

// backendSearch reads the query once, keeps the plugin for DemandOf and webSearch, and keeps only entries that can
// follow that one external search.
func backendSearch(c *Call, plan routing.Plan, decision searchDecision, replan func() (routing.Plan, error)) (routing.Plan, error) {
	query, err := protocol.ChatSearchQuery(c.Body)
	if err != nil {
		return plan, invalid(err.Error())
	}
	if query == "" {
		return plan, invalid(protocol.ErrEmptySearchQuery.Error())
	}
	if len(query) > protocol.MaxWebSearchQueryBytes {
		return plan, invalid(fmt.Sprintf("The web plugin's search query is longer than %d bytes.", protocol.MaxWebSearchQueryBytes))
	}
	// The backend search runs before the attempt, so the request keeps its plugin (and the input bound the injected
	// results reserve) for DemandOf and webSearch; only its price changes.
	c.search, c.searchBackend, c.searchPrice = query, decision.backend, decision.price
	c.Request.SearchBytes = protocol.WebSearchMaxBytes
	// The first route ran with no injected input. Route again with the bound the backend will add; otherwise a small
	// context endpoint can win now and only fail after the search has already run and been charged.
	plan, err = replan()
	if err != nil {
		return plan, err
	}
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the selected search engine")
	}
	return plan, nil
}

// autoSearchRoute keeps only models that search without a tool. The first entry chose that path; a normal fallback
// would answer without the search the client asked for.
func autoSearchRoute(c *Call, plan routing.Plan) routing.Plan {
	kept := make([]int, 0, len(plan.Entries))
	for i := range plan.Entries {
		model, ok := c.snap.Model(plan.Entries[i].Model)
		if ok && model.AlwaysSearches() {
			kept = append(kept, i)
		}
	}
	return trim(plan, kept)
}

// searchRoute cuts the fallbacks to the entries that can follow the one search path the first entry chose. A native
// search on a model that cannot run it, or on one that searches on every call, would fail or search twice; a backend
// search on a model that always searches would double-search the same way. Trimming here, before Quote, means the
// reservation covers exactly the entries an attempt may use.
func searchRoute(c *Call, plan routing.Plan, decision searchDecision) routing.Plan {
	kept := make([]int, 0, len(plan.Entries))
	for i := range plan.Entries {
		entry := &plan.Entries[i]
		model, ok := c.snap.Model(entry.Model)
		if !ok || model.AlwaysSearches() {
			continue
		}
		if decision.native > 0 && (!servesWebSearch(model) || !protocol.NativeSearchSupported(entry.Target.Format)) {
			continue
		}
		kept = append(kept, i)
	}
	return trim(plan, kept)
}

// maxNativeSearches bounds what one plugin request may turn on at a provider; a model's spec may allow more, but one
// plugin search should not spend an unbounded amount of the account's money.
const maxNativeSearches = 10

// searchChoice is what the routed entry's model and format allow: its own web search, and the model's default most
// searches. Always is a model that searches without a tool, which the plugin must not search again.
type searchChoice struct {
	Native  bool
	Always  bool
	MaxUses int64
}

// searchDecision is the one path a plugin request takes: native searches at the provider, or one backend, never both.
type searchDecision struct {
	native  int
	backend *websearch.Backend
	price   platform.UnitPrice
}

// chooseSearch is resolveSearch's decision, apart from the catalog it reads: declared is a request that already turns
// the provider's own search on, and all are the node's enabled engines.
func chooseSearch(web *protocol.WebPlugin, choice searchChoice, declared bool, all SearchBackends) (searchDecision, error) {
	return chooseSearchFor(web, choice, declared, all, "plugin")
}

// chooseSearchFor is chooseSearch with the word the refusal uses for the request's member: a plugin or the tool.
func chooseSearchFor(web *protocol.WebPlugin, choice searchChoice, declared bool, all SearchBackends, noun string) (searchDecision, error) {
	if declared || choice.Always {
		return searchDecision{}, nil
	}
	switch {
	case web.Engine == "native" && !choice.Native:
		return searchDecision{}, invalid(fmt.Sprintf("%s option \"native\" is not supported: the selected model has no native web search on this endpoint", noun))
	case web.Engine == "native", choice.Native && (web.Engine == "" || web.Engine == "auto"):
		return searchDecision{native: int(min(max(choice.MaxUses, 1), maxNativeSearches))}, nil
	}
	b, err := searchBackendFor(web, all, noun)
	if err != nil {
		return searchDecision{}, err
	}
	price, err := b.Price(web.MaxResults)
	if err != nil {
		return searchDecision{}, invalid("the search engine's published price is out of range")
	}
	return searchDecision{backend: b, price: price}, nil
}

// searchBackendFor is the backend a search the provider does not run goes through: the engine the request names, or
// the node's default for auto.
func searchBackendFor(web *protocol.WebPlugin, all SearchBackends, noun string) (*websearch.Backend, error) {
	if web.Engine != "" && web.Engine != "auto" {
		b, ok := findBackend(all, web.Engine)
		if !ok {
			return nil, invalid(fmt.Sprintf("%s option %q is not an enabled search engine", noun, web.Engine))
		}
		return b, nil
	}
	b, ok := defaultBackend(all)
	switch {
	case ok:
		return b, nil
	case web.Online:
		return nil, invalid(errOnline)
	}
	return nil, invalid(fmt.Sprintf("%s %q is not supported: %s", noun, "web", protocol.NoWebSearch))
}

// servesWebSearch reports whether the model's spec declares the provider's web search ability.
func servesWebSearch(m catalog.Model) bool {
	serving, ok := m.Serving()
	return ok && serving.Abilities.Has(catalog.AbilityWebSearch)
}
