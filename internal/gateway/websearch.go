package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/websearch"
)

// SearchBackends are the node's enabled search engines, by name. The interface keeps tests independent of how the
// node loaded them; the node's own value is *websearch.Engines.
type SearchBackends interface {
	Find(name string) (*websearch.Backend, bool)
	Default() (*websearch.Backend, bool)
	List() []websearch.Engine
	Configured() bool
}

// checkWebSearch refuses a web plugin the node cannot serve, before anything is reserved. The query and the input its
// results may add are fixed later, only when resolveSearch chooses a backend. The refusal while no backend is
// configured is the documented one: the same sentence the plugin and :online gave before the backend existed.
func (h *Handler) checkWebSearch(req *protocol.Request) *Refusal {
	if req.Web == nil {
		return nil
	}
	if req.Format != protocol.Chat {
		if req.Web.Online {
			return invalid("The :online variant is only supported for Chat requests.")
		}
		return invalid(`plugin "web" is not supported for this format`)
	}
	// The name is checked here, where the configured engines are known; whether the default exists is only known
	// against the entry's model, so resolveSearch decides that after routing. The query and its bound belong to a
	// backend search only: a native search never reads or injects it, and a long last message must not be refused
	// before resolveSearch has chosen that path.
	if req.Web.Engine != "" && req.Web.Engine != "native" {
		if _, ok := findBackend(h.Search, req.Web.Engine); !ok {
			return invalid(fmt.Sprintf("plugin option %q is not an enabled search engine", req.Web.Engine))
		}
	}
	return nil
}

// findBackend is Find that also answers for a node with no search configured at all.
func findBackend(all SearchBackends, name string) (*websearch.Backend, bool) {
	if all == nil {
		return nil, false
	}
	return all.Find(name)
}

// defaultBackend is Default for a node with no search configured.
func defaultBackend(all SearchBackends) (*websearch.Backend, bool) {
	if all == nil {
		return nil, false
	}
	return all.Default()
}

// onlineWebPlugin gives a request whose deprecated :online variant asked for web search the plugin it stands for: one
// search, OpenRouter's default number of results.
func onlineWebPlugin(req *protocol.Request, online bool) {
	if online && req.Web == nil {
		req.Web = &protocol.WebPlugin{MaxResults: protocol.DefaultWebSearchResults, Online: true}
	}
}

// restoreWeb puts back what compression removed: the plugin is a routing setting, not a body one. The input a backend
// search may add was reserved at admission and is not restored here.
func restoreWeb(req *protocol.Request, web *protocol.WebPlugin) {
	req.Web = web
}

// restoreServer puts back the fields a re-Inspection of a rewritten body cannot see: the server tool was removed from
// the body before parsing or compression, so the new Request knows nothing of it, and the loop or native mapping would
// be lost (docs/domains/gateway.md「服务端工具」).
func restoreServer(req *protocol.Request, old protocol.Request) {
	req.ServerSearch = old.ServerSearch
	req.NativeWeb = old.NativeWeb
	req.MaxToolCalls, req.HasMaxToolCalls = old.MaxToolCalls, old.HasMaxToolCalls
	req.WebSearches = old.WebSearches
	if old.ServerSearch != nil {
		// The loop runs ordinary tools through an external backend; only native search needs the model's WebSearch
		// capability, which resolveServerTool adds when it chooses that path.
		req.Needs |= protocol.Tools
	}
}

// webSearch runs the request's one search and injects its results before the first user turn. It must run after
// admission, so a call that cannot pay searches nothing, and before an attempt, so a failed search is an upstream
// failure rather than a charge. The query is client content and is never logged.
func (s *session) webSearch(ctx context.Context) *ended {
	web, backend := s.c.Request.Web, s.c.searchBackend
	if web == nil || backend == nil {
		return nil
	}
	if !s.reserveSearch(websearch.WorkingSet) {
		return &ended{err: protocol.Error{Type: protocol.Overloaded}}
	}
	searchCtx, cancel := context.WithTimeout(ctx, websearch.Timeout)
	defer cancel()
	results, err := backend.Search(searchCtx, websearch.Query{
		Text: s.c.search, MaxResults: web.MaxResults,
		IncludeDomains: web.IncludeDomains, ExcludeDomains: web.ExcludeDomains,
	})
	if err != nil {
		if errors.Is(err, websearch.ErrQueryTooLong) {
			return &ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: "The web plugin's search query is longer than this search engine accepts."}}
		}
		s.logger.WarnContext(ctx, "the web search backend failed", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	if !s.reserveSearch(3*int64(len(s.c.Body)) + protocol.WebSearchMaxBytes) {
		return &ended{err: protocol.Error{Type: protocol.Overloaded}}
	}
	body, err := protocol.InjectWebSearch(s.c.Body, webSearchMessage(web, results, s.start))
	if err != nil {
		s.logger.Error("the web search results could not be injected", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	req, err := protocol.Inspect(s.c.Format, body)
	if err != nil {
		s.logger.Error("the injected web search body is invalid", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	// :online is a routing setting, not a body member, so Inspect cannot see it; the plugin's own members remain in
	// the body and are read again, then removed by Prepare.
	if req.Web == nil {
		req.Web = web
	}
	req.SearchBytes = 0
	s.c.Body, s.c.Request = body, req
	s.searches = 1
	s.citations = make([]protocol.Citation, len(results))
	for i, r := range results {
		s.citations[i] = protocol.Citation{URL: r.URL, Title: r.Title, Content: r.Content}
	}
	return nil
}

// reserveSearch draws from the node's budget for a search's working set and keeps it until the call is over, when
// release gives it back. The results and the injected body are the call's to hold; the adapter's response buffer is
// already gone by then, but the same reservation keeps the whole operation within the node's budget.
func (s *session) reserveSearch(n int64) bool {
	if n <= 0 {
		return true
	}
	if !s.x.Memory.Charge(n) {
		return false
	}
	s.searchHeld += n
	return true
}

// webSearchMessage is the system message the results are injected in, worded as OpenRouter's web search announcement
// (OpenRouter《Introducing web search via the API》, read 2026-10-31). A configured search_prompt replaces the
// instruction; the results follow either way, one per line, so a model can cite the URL it used.
func webSearchMessage(web *protocol.WebPlugin, results []websearch.Result, at time.Time) string {
	prompt := web.SearchPrompt
	if prompt == "" {
		prompt = fmt.Sprintf("A web search was conducted on %s. Incorporate the following web search results into your response. IMPORTANT: Cite them using markdown links named using the domain of the source. Example: [nytimes.com](https://nytimes.com/some-page).", at.UTC().Format("2006-01-02"))
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n")
	if len(results) == 0 {
		b.WriteString("No results were returned.")
		return b.String()
	}
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return strings.TrimSpace(b.String())
}

// checkServerTool refuses an openrouter:web_search tool the node cannot serve, before anything is reserved. The
// engine name is checked here, where the configured engines are known; the default engine is only known against the
// routed entry, so resolveSearch decides that after routing.
//
//nolint:cyclop // one refusal per invalid combination
func (h *Handler) checkServerTool(req *protocol.Request, body []byte) *Refusal {
	t := req.ServerSearch
	if t == nil {
		return nil
	}
	if req.Web != nil {
		return invalid("openrouter:web_search cannot be combined with the web plugin: one request runs one search path.")
	}
	if req.NativeWeb {
		return invalid("openrouter:web_search cannot be combined with the provider's own web search tool.")
	}
	if req.Needs.Has(protocol.CodeExecution) {
		// Every sampling of the loop could run the provider's code again, while the quote counts its uses once and the
		// loop removes max_tool_calls, the one limit the upstream would hold them to.
		return invalid("openrouter:web_search cannot be combined with the provider's code execution tool.")
	}
	if !req.Format.Convertible() {
		return invalid("openrouter:web_search is only supported for the chat formats.")
	}
	if req.Choices > 1 {
		return invalid("openrouter:web_search does not support more than one choice.")
	}
	if req.HasMaxToolCalls && req.MaxToolCalls == 0 {
		return invalid("max_tool_calls is 0: the web search tool would have no step to run.")
	}
	if t.Engine != "" && t.Engine != "auto" && t.Engine != "native" {
		if _, ok := findBackend(h.Search, t.Engine); !ok {
			return invalid(fmt.Sprintf("tool option %q is not an enabled search engine", t.Engine))
		}
	}
	if protocol.ServerFunctionDeclared(req.Format, body) {
		return invalid(fmt.Sprintf("the request already declares a function named %q", protocol.ServerToolName))
	}
	return nil
}

// stripServerTool removes the tool's declaration from the body before admission and keeps what the gateway read of it.
// The body is what routing, the quote and Prepare see from here on, so the model-side loop's function declaration is
// added per endpoint later, after conversion, and no converter meets a tool it cannot express.
func (h *Handler) stripServerTool(c *Call, body []byte, req protocol.Request) ([]byte, protocol.Request, error) {
	stripped, err := protocol.RemoveServerTool(req.Format, body)
	if err != nil {
		return nil, req, invalid(err.Error())
	}
	if len(stripped) == len(body) {
		return body, req, nil // no declaration found: Inspect would not have set ServerSearch
	}
	held := int64(cap(stripped))
	if !h.Memory.Charge(held) {
		return nil, req, ErrOverloaded
	}
	next, err := protocol.Inspect(req.Format, stripped)
	if err != nil {
		h.Memory.Refund(held)
		return nil, req, invalid(err.Error())
	}
	c.hold(func() { h.Memory.Refund(held) })
	next.ServerSearch, next.NativeWeb = req.ServerSearch, req.NativeWeb
	next.MaxToolCalls, next.HasMaxToolCalls = req.MaxToolCalls, req.HasMaxToolCalls
	// The server tool is a tool declaration even after its own object is gone: the loop it stands for needs the
	// endpoint's tool-calling ability, and routing must not choose a model that lacks it.
	next.Needs |= protocol.Tools
	return stripped, next, nil
}
